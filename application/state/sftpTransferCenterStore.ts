import { useSyncExternalStore } from "react";

import type { FileConflictAction, TransferTask } from "../../domain/models";
import {
  deserializeSftpTransferCenter,
  pruneSftpTransferHistory,
  serializeSftpTransferCenter,
} from "../../domain/sftpTransferCenter";
import {
  findActivePathConflict,
  pathConflictMessage,
} from "../../domain/sftpTransferConflicts";
import { STORAGE_KEY_SFTP_TRANSFER_CENTER } from "../../infrastructure/config/storageKeys";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";
import { notify } from "../notification";
import { globalSftpTransferScheduler } from "./sftp/globalTransferScheduler";
import {
  isTransferOrRootPauseLatched,
  isTransferPauseLatched,
  releaseTransferPauseTree,
} from "./sftp/transferPauseLatch";
import {
  bumpTransferControlEpoch,
  getTransferControlEpoch,
  isTransferControlEpochCurrent,
} from "./sftp/transferControlEpoch";
import { isTransferWalkInFlight } from "./sftp/transferWalkRegistry";
import {
  markTransferCancelledTree,
} from "./sftp/transferCancelLatch";
import {
  defaultTransferControlBridge,
  softPauseTransfer,
  softResumeTransfer,
  type TransferControlHost,
} from "./sftp/globalSftpTransferControl";

type Listener = () => void;

export interface SftpTransferOwnerControls {
  pause: (taskId: string) => void | Promise<void>;
  resume: (taskId: string) => void | Promise<void>;
  cancel: (taskId: string) => void | Promise<void>;
  retry: (taskId: string) => void | Promise<void>;
  prioritize: (taskId: string) => void | Promise<void>;
  dismiss: (taskId: string) => void;
  /**
   * True when the panel still lists this task locally.
   * Used only for adopt/retry UX after hard reconnect — never for soft
   * pause/resume (those are process-global via TransferRuntime / store).
   */
  ownsTask?: (taskId: string) => boolean;
  /** Pull lifecycle fields from the store after unified soft-control. */
  syncOwnedTasks?: () => void;
  canAdopt?: (task: TransferTask) => boolean;
  canPrepareAdoption?: boolean;
  adopt?: (task: TransferTask) => void | Promise<void>;
  resolveConflict?: (taskId: string, action: FileConflictAction, applyToAll?: boolean) => void | Promise<void>;
}

export interface SftpTransferCenterSnapshot {
  tasks: readonly TransferTask[];
  activeCount: number;
  queuedCount: number;
  attentionCount: number;
}

export type DedicatedTransferResumeHandler = (task: TransferTask) => Promise<{
  success: boolean;
  error?: string;
  needsAttention?: boolean;
  resetCheckpoint?: boolean;
}>;

export interface SftpTransferCenterStore {
  subscribe(listener: Listener): () => void;
  getSnapshot(): SftpTransferCenterSnapshot;
  getOwnerTasks(ownerId: string): TransferTask[];
  publishOwner(ownerId: string, tasks: readonly TransferTask[]): void;
  registerOwner(ownerId: string, controls: SftpTransferOwnerControls): () => void;
  setDedicatedResumeHandler(handler: DedicatedTransferResumeHandler | null): void;
  patchTask(taskId: string, updates: Partial<TransferTask>): void;
  /** Insert or merge tasks by id (used by dedicated directory resume for children). */
  upsertTasks(incoming: readonly TransferTask[]): void;
  canControl(taskId: string): boolean;
  pause(taskId: string): Promise<void>;
  resume(taskId: string): Promise<void>;
  cancel(taskId: string): Promise<void>;
  retry(taskId: string): Promise<void>;
  prioritize(taskId: string): Promise<void>;
  dismiss(taskId: string): void;
  clearTerminal(status?: TransferTask["status"]): void;
  markReconnectRequired(taskId: string, error?: string): void;
  reportResumePreparationFailure(taskId: string, error: string): void;
  ingestBackgroundEvent(event: {
    type: "queued" | "started" | "progress" | "pausing" | "paused" | "resumed" | "cancelled" | "completed" | "failed";
    transferId: string;
    direction?: TransferTask["direction"];
    fileName?: string;
    sourcePath?: string;
    targetPath?: string;
    startedAt?: number;
    endedAt?: number;
    error?: string;
    transferred?: number;
    totalBytes?: number;
    speed?: number;
    checkpointBytes?: number;
    resumeStage?: TransferTask["resumeStage"];
    downloadCheckpointBytes?: number;
    uploadCheckpointBytes?: number;
    sourceFingerprint?: string;
    sessionId?: string;
    sourceHostId?: string;
    targetHostId?: string;
    isDirectory?: boolean;
    controlKind?: TransferTask["controlKind"];
    phase?: TransferTask["phase"];
    lifecycleEpoch?: number;
    lifecycleState?: "queued" | "pausing" | "paused" | "transferring";
  }): void;
  resolveConflict(taskId: string, action: FileConflictAction, applyToAll?: boolean): Promise<void>;
}

interface StorePersistence {
  read(): string | null;
  write(value: string): void;
}

const EMPTY_SNAPSHOT: SftpTransferCenterSnapshot = {
  tasks: [],
  activeCount: 0,
  queuedCount: 0,
  attentionCount: 0,
};

function buildSnapshot(tasks: readonly TransferTask[]): SftpTransferCenterSnapshot {
  const topLevelTasks = tasks.filter((task) => !task.parentTaskId);
  return {
    tasks,
    activeCount: topLevelTasks.filter((task) => task.status === "transferring" || task.status === "pausing").length,
    queuedCount: topLevelTasks.filter((task) => task.status === "pending" || task.status === "queued").length,
    attentionCount: topLevelTasks.filter((task) => task.status === "attention" || task.status === "failed").length,
  };
}

const TERMINAL_OWNER_STATUSES = new Set<TransferTask["status"]>([
  "completed",
  "cancelled",
  "failed",
]);

/**
 * Panel snapshots and main-process progress IPC both write the same rows.
 * Never let a stale panel paint roll back bytes that background events already
 * advanced (tab-close / hide dual-writer race freezes the global center bar).
 * Also never resurrect completed/cancelled store rows from a late transferring
 * panel snapshot (ingest already guards terminal; publishOwner must too).
 */
export function mergeOwnerPublishedTask(
  existing: TransferTask,
  incoming: TransferTask,
  ownerId: string,
  parentStatus?: TransferTask["status"],
): TransferTask {
  // Explicit restart / re-queue may reset progress to 0 — allow that.
  const incomingReset = (
    (incoming.status === "pending" || incoming.status === "queued")
    && (incoming.transferredBytes ?? 0) === 0
    && (incoming.checkpointBytes ?? 0) === 0
  );
  const incomingTerminal = TERMINAL_OWNER_STATUSES.has(incoming.status);
  // completed/cancelled stick on the store. A late panel "transferring" paint
  // must not un-complete after ingestBackgroundEvent(completed|cancelled).
  // failed is intentionally not sticky: same-id checkpoint resume re-opens it.
  if (
    (existing.status === "completed" || existing.status === "cancelled")
    && !incomingTerminal
    && !incomingReset
  ) {
    return {
      ...existing,
      ownerId,
      updatedAt: existing.updatedAt ?? Date.now(),
    };
  }
  // Prefer cancelled over a late completed when both sides are terminal.
  if (
    existing.status === "cancelled"
    && incoming.status === "completed"
  ) {
    return {
      ...existing,
      ownerId,
      updatedAt: existing.updatedAt ?? Date.now(),
    };
  }

  const merged: TransferTask = {
    ...incoming,
    ownerId,
    updatedAt: incoming.updatedAt ?? Date.now(),
  };

  if (incomingReset || incomingTerminal) {
    return merged;
  }

  // Keep higher water marks while either side still treats the row as live work.
  const existingLive = !TERMINAL_OWNER_STATUSES.has(existing.status);
  const incomingLive = !TERMINAL_OWNER_STATUSES.has(incoming.status);
  if (existingLive && incomingLive) {
    const existingBytes = existing.transferredBytes ?? 0;
    const incomingBytes = incoming.transferredBytes ?? 0;
    if (existingBytes > incomingBytes) {
      merged.transferredBytes = existingBytes;
      // Prefer the fresher non-zero speed from either side when bytes stall.
      if (!(Number.isFinite(merged.speed) && merged.speed > 0) && existing.speed > 0) {
        merged.speed = existing.speed;
      }
    }
    const existingCheckpoint = existing.checkpointBytes ?? 0;
    const incomingCheckpoint = incoming.checkpointBytes ?? 0;
    if (existingCheckpoint > incomingCheckpoint) {
      merged.checkpointBytes = existingCheckpoint;
    }
    if ((existing.totalBytes ?? 0) > (incoming.totalBytes ?? 0)) {
      merged.totalBytes = existing.totalBytes;
    }
    const existingLifecycleEpoch = Number.isFinite(existing.lifecycleEpoch)
      ? (existing.lifecycleEpoch as number)
      : -1;
    const incomingLifecycleEpoch = Number.isFinite(incoming.lifecycleEpoch)
      ? (incoming.lifecycleEpoch as number)
      : -1;
    if (existingLifecycleEpoch > incomingLifecycleEpoch) {
      merged.lifecycleEpoch = existing.lifecycleEpoch;
      merged.status = existing.status;
      if (existing.status === "paused" || existing.status === "pausing") {
        merged.transferredBytes = existing.transferredBytes;
        merged.checkpointBytes = existing.checkpointBytes;
        merged.speed = 0;
      }
    }
    // Background progress re-opens transferring; don't let a frozen panel
    // snapshot paint a non-terminal lower status over a live transferring row
    // when bytes moved forward on the store.
    if (
      existing.status === "transferring"
      && (incoming.status === "pending" || incoming.status === "queued")
      && (merged.transferredBytes ?? 0) > 0
    ) {
      merged.status = "transferring";
    }
    // Intentional pause from the panel must always win over soft-drain water
    // marks. Freezing transferredBytes on pause used to leave store bytes ahead
    // of the panel snapshot; the old "higher bytes keep transferring" rule then
    // rejected pause and the global transfer center looked stuck on 传输中.
    // Only a strictly newer lifecycle epoch may re-open transferring (resume).
    const incomingPaused = incoming.status === "paused" || incoming.status === "pausing";
    const existingPaused = existing.status === "paused" || existing.status === "pausing";
    if (incomingPaused && incomingLifecycleEpoch >= existingLifecycleEpoch) {
      merged.status = incoming.status;
      merged.speed = 0;
      // Pin the visible bar while paused/latched. Soft-drain must not keep
      // raising transferredBytes after the user paused (checkpoint may still rise).
      const latched = isTransferOrRootPauseLatched(
        existing.parentTaskId ?? existing.id,
        existing.id,
      ) || isTransferPauseLatched(existing.id);
      if (latched || existingPaused) {
        merged.transferredBytes = existingBytes;
      } else {
        merged.transferredBytes = Math.max(existingBytes, incomingBytes);
      }
      if (existingCheckpoint > 0 || incomingCheckpoint > 0) {
        merged.checkpointBytes = Math.max(existingCheckpoint, incomingCheckpoint);
      }
    } else if (
      existingPaused
      && (incoming.status === "transferring" || incoming.status === "pending" || incoming.status === "queued")
      // Only a strictly older epoch is a stale snapshot. Equal/missing epochs must
      // allow intentional Resume (publishOwner after unlatch) to re-open the row.
      && incomingLifecycleEpoch < existingLifecycleEpoch
    ) {
      // Soft-drain / late progress publish must not un-pause without resume epoch.
      merged.status = existing.status;
      merged.speed = 0;
      merged.transferredBytes = existing.transferredBytes;
      merged.checkpointBytes = existing.checkpointBytes ?? merged.checkpointBytes;
    }
    // Folder parent already paused/pausing: never re-open a child as live from a
    // late panel paint. That made the collapsed "current file" row blink in/out.
    const parentHoldsPause = parentStatus === "paused" || parentStatus === "pausing";
    if (
      parentHoldsPause
      && !["completed", "cancelled", "failed"].includes(merged.status)
      && (merged.status === "transferring" || merged.status === "pending" || merged.status === "queued")
    ) {
      merged.status = parentStatus === "pausing" ? "pausing" : "paused";
      merged.speed = 0;
    }
  }

  return merged;
}

export function createSftpTransferCenterStore(persistence?: StorePersistence): SftpTransferCenterStore {
  const restored = deserializeSftpTransferCenter(persistence?.read() ?? null);
  let tasks = pruneSftpTransferHistory(restored.tasks);
  let snapshot = tasks.length > 0 ? buildSnapshot(tasks) : EMPTY_SNAPSHOT;
  const listeners = new Set<Listener>();
  const controllers = new Map<string, SftpTransferOwnerControls>();
  const resumeInvocations = new Map<string, Promise<void>>();
  const resumePreparationFailures = new Map<string, string>();
  let dedicatedResumeHandler: DedicatedTransferResumeHandler | null = null;
  const dedicatedResumeWaiters = new Set<{
    taskId: string;
    resolve: () => void;
    timer: ReturnType<typeof setTimeout>;
  }>();

  const notifyDedicatedResumeWaiters = () => {
    for (const waiter of dedicatedResumeWaiters) {
      const current = tasks.find((task) => task.id === waiter.taskId);
      if (
        !dedicatedResumeHandler
        && current
        && current.status !== "cancelled"
        && current.status !== "completed"
      ) continue;
      clearTimeout(waiter.timer);
      dedicatedResumeWaiters.delete(waiter);
      waiter.resolve();
    }
  };

  const waitForDedicatedResumeHandler = (taskId: string) => new Promise<void>((resolve) => {
    if (dedicatedResumeHandler) {
      resolve();
      return;
    }
    const waiter = {
      taskId,
      resolve,
      // Vault unlock + first host load can exceed a few seconds after force-quit.
      timer: setTimeout(() => {
        dedicatedResumeWaiters.delete(waiter);
        resolve();
      }, 15000),
    };
    dedicatedResumeWaiters.add(waiter);
  });

  const persist = () => {
    persistence?.write(serializeSftpTransferCenter(tasks));
  };
  const emit = () => {
    const beforePrune = tasks;
    tasks = pruneSftpTransferHistory(tasks);
    const retainedIds = new Set(tasks.map((task) => task.id));
    for (const removed of beforePrune) {
      if (retainedIds.has(removed.id)) continue;
      controllers.get(removed.ownerId ?? "")?.dismiss(removed.id);
    }
    snapshot = buildSnapshot(tasks);
    persist();
    notifyDedicatedResumeWaiters();
    for (const listener of listeners) listener();
  };
  const findOwner = (taskId: string) => tasks.find((task) => task.id === taskId)?.ownerId;
  const findAdopter = (task: TransferTask) => [...controllers.entries()].find(([, controls]) => (
    controls.adopt && controls.canAdopt?.(task)
  ));
  const prepareAdopter = async (task: TransferTask) => {
    let adopter = findAdopter(task);
    let preparationError: string | undefined;
    let cancelled = false;
    if (!adopter && typeof globalThis.window !== "undefined") {
      // Open the SFTP panel on the active terminal tab first so a preparer can
      // register, then ask it to reconnect the required hosts.
      globalThis.window.dispatchEvent(new CustomEvent("netcatty:open-sftp-transfer-target", {
        detail: { task, forResume: true },
      }));
      // ~45s is enough for MFA/password prompts; longer felt like a hang.
      const maxAttempts = 90;
      let prepareDispatched = false;
      for (let attempt = 0; attempt < maxAttempts && !adopter && !preparationError; attempt += 1) {
        const currentTask = tasks.find((candidate) => candidate.id === task.id);
        if (!currentTask || ["cancelled", "completed"].includes(currentTask.status)) {
          cancelled = true;
          break;
        }
        preparationError = resumePreparationFailures.get(task.id);
        if (preparationError) break;
        const preparer = [...controllers.entries()].find(([, controls]) => controls.canPrepareAdoption);
        if (preparer && !prepareDispatched) {
          prepareDispatched = true;
          globalThis.window.dispatchEvent(new CustomEvent("netcatty:prepare-sftp-transfer-resume", {
            detail: {
              task,
              targetOwnerId: preparer[0],
              reportFailure: (error: string) => {
                preparationError = error;
                resumePreparationFailures.set(task.id, error);
              },
            },
          }));
        } else if (!preparer && attempt === 10) {
          // Re-request panel open if nothing registered after a few seconds.
          globalThis.window.dispatchEvent(new CustomEvent("netcatty:open-sftp-transfer-target", {
            detail: { task, forResume: true },
          }));
          prepareDispatched = false;
        }
        await new Promise((resolve) => setTimeout(resolve, 500));
        adopter = findAdopter(task);
      }
      if (!adopter && !preparationError && !cancelled) {
        preparationError = resumePreparationFailures.get(task.id)
          ?? "Could not reconnect in time. Open an SFTP panel and try again.";
      }
    }
    resumePreparationFailures.delete(task.id);
    return { adopter, error: preparationError, cancelled };
  };
  const resolveLiveController = (taskId: string) => {
    const ownerId = findOwner(taskId);
    const candidate = ownerId ? controllers.get(ownerId) : undefined;
    if (!candidate) return undefined;
    // Stale owner after tab close: registered but no longer tracking the task.
    if (typeof candidate.ownsTask === "function" && !candidate.ownsTask(taskId)) {
      return undefined;
    }
    return candidate;
  };

  const invoke = async (taskId: string, requestedAction: "pause" | "resume" | "cancel" | "retry" | "prioritize") => {
    let action = requestedAction;
    let controller = resolveLiveController(taskId);
    let task = tasks.find((candidate) => candidate.id === taskId);
    // Intentional resume/retry must clear a pre-start cancel latch left by an
    // earlier cancel that never hit startTransferNow (same transferId).
    if (action === "resume" || action === "retry") {
      try {
        await netcattyBridge.get()?.clearPendingTransferCancel?.(taskId);
      } catch {
        // best-effort
      }
    }
    // Compressed soft-control is process-global: do not gate on a live panel owner.
    const liveCompressedJob = task?.controlKind === "compressed-upload"
      && task.reconnectRequired !== true
      && task.status !== "interrupted"
      && task.status !== "attention";
    if (liveCompressedJob && (action === "pause" || action === "resume" || action === "cancel")) {
      const bridge = netcattyBridge.get();
      if (action === "pause") {
        // Control epoch supersedes races; bridge lifecycleEpoch is stamped on success.
        const pauseControlEpoch = bumpTransferControlEpoch(taskId);
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: "pausing" as const,
          pauseUnavailableReason: undefined,
          speed: 0,
        } : candidate);
        emit();
        const result = await (bridge?.pauseCompressedUpload?.(taskId)
          ?? { success: false, deferred: false, reason: "Pause unavailable" });
        const latest = tasks.find((candidate) => candidate.id === taskId);
        if (!latest || latest.status === "cancelled") return;
        // Drop stale paint if a newer control resume already superseded this pause.
        if (!isTransferControlEpochCurrent(taskId, pauseControlEpoch)
          && (latest.status === "transferring" || latest.status === "paused")) {
          return;
        }
        const bridgeEpoch = Number.isFinite(result.lifecycleEpoch)
          ? (result.lifecycleEpoch as number)
          : undefined;
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: result.success
            ? (result.deferred ? "pausing" as const : "paused" as const)
            : "transferring" as const,
          pauseUnavailableReason: result.success ? undefined : result.reason,
          speed: result.success ? 0 : candidate.speed,
          ...(bridgeEpoch !== undefined ? { lifecycleEpoch: bridgeEpoch } : null),
        } : candidate);
        emit();
        if (!result.success && result.reason) notify.warning(result.reason, "SFTP");
        return;
      }
      if (action === "resume") {
        bumpTransferControlEpoch(taskId);
        const result = await (bridge?.resumeCompressedUpload?.(taskId)
          ?? { success: false, reason: "Resume unavailable" });
        const latest = tasks.find((candidate) => candidate.id === taskId);
        if (!latest || latest.status === "cancelled") return;
        const bridgeEpoch = Number.isFinite(result.lifecycleEpoch)
          ? (result.lifecycleEpoch as number)
          : undefined;
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: result.success ? "transferring" as const : candidate.status,
          error: result.success ? undefined : (result.reason ?? candidate.error),
          pauseUnavailableReason: result.success ? undefined : candidate.pauseUnavailableReason,
          speed: 0,
          // Bridge epoch or clear so fanout progress is not stale-dropped.
          lifecycleEpoch: result.success ? bridgeEpoch : candidate.lifecycleEpoch,
        } : candidate);
        emit();
        if (!result.success && result.reason) notify.warning(result.reason, "SFTP");
        return;
      }
      const result = await (bridge?.cancelCompressedUpload?.(taskId)
        ?? { success: false });
      if (!result.success) {
        const reason = "Could not cancel the compressed upload.";
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: "attention" as const,
          error: reason,
          speed: 0,
        } : candidate);
        emit();
        notify.warning(reason, "SFTP");
        return;
      }
      markTransferCancelledTree(taskId);
      releaseTransferPauseTree(taskId);
      tasks = tasks.map((candidate) => candidate.id === taskId ? {
        ...candidate,
        status: "cancelled" as const,
        error: undefined,
        endTime: Date.now(),
        speed: 0,
        phase: undefined,
      } : candidate);
      emit();
      return;
    }
    // After app restart (or any reconnectRequired task), a retained panel owner
    // often cannot resume (missing panes / dead sftpId). Prefer a dedicated
    // transfer session instead of failing with "Reconnect the source and target".
    // Never dedicated-resume rows waiting on conflict resolution — that would
    // stream with replace semantics and skip Replace/Skip/Duplicate UI.
    const needsDedicatedReconnect = action === "resume" && !!task && !task.conflict && (
      task.reconnectRequired === true
      || task.status === "interrupted"
      || (task.status === "attention" && !task.conflict)
      || task.ownerId === "background-agent"
    );
    if (needsDedicatedReconnect && controller && !controller.canAdopt?.(task)) {
      controller = undefined;
    }
    // Prefer the live owner controller for pause/resume of still-active work.
    // Do NOT drop it just because canAdopt is false when the transfer is still
    // live in the backend — downloads often have only a remote pane open.
    //
    // Only paint "Reconnecting…" when we truly need a dedicated/session reopen.
    // Soft-paused live streams after tab close must soft-resume without this
    // flash (otherwise Resume feels laggy / "broken").
    if (action === "resume" && task && needsDedicatedReconnect) {
      tasks = tasks.map((candidate) => candidate.id === taskId ? {
        ...candidate,
        status: "pending",
        error: undefined,
        speed: 0,
        phase: undefined,
        reconnectRequired: true,
        lifecycleEpoch: undefined,
      } : candidate);
      emit();
    }
    // The transfer center can render before vault startup has installed the
    // dedicated reconnect handler. Only wait when nothing else can take the
    // job yet (no owner controller and no panel adopter).
    if (
      needsDedicatedReconnect
      && !dedicatedResumeHandler
      && !controller
      && task
      && !findAdopter(task)
    ) {
      await waitForDedicatedResumeHandler(taskId);
      task = tasks.find((candidate) => candidate.id === taskId);
      if (!task || task.status === "cancelled" || task.status === "completed") return;
      // A panel may have registered while we waited for vault startup.
      controller = resolveLiveController(taskId);
    }
    // Unified soft pause/resume — process-global, not tied to panel/terminal
    // lifecycle. Controllers only sync local React lists after the store paints.
    if (action === "pause" || action === "resume") {
      const controlHost: TransferControlHost = {
        getTasks: () => tasks,
        setTasks: (next) => {
          tasks = next;
          emit();
        },
        getBridge: defaultTransferControlBridge,
      };
      const notifyOwners = () => {
        for (const controls of controllers.values()) {
          try { controls.syncOwnedTasks?.(); } catch { /* best-effort */ }
        }
      };

      if (action === "pause") {
        await softPauseTransfer(controlHost, taskId);
        notifyOwners();
        return;
      }

      // Soft-resume whenever a walk may still be alive, even if reconnectRequired
      // was set spuriously. Dedicated only when soft-resume cannot rejoin.
      const preferSoft = !needsDedicatedReconnect || isTransferWalkInFlight(taskId);
      if (preferSoft) {
        const handled = await softResumeTransfer(controlHost, taskId);
        if (handled) {
          notifyOwners();
          return;
        }
        // Soft could not rejoin (dead walk + bridge miss). Never silent-return:
        // demote so the hard reconnect / adopt path below can run even when a
        // stale panel owner is still registered.
        if (action === "resume") {
          tasks = tasks.map((candidate) => candidate.id === taskId ? {
            ...candidate,
            status: "interrupted" as const,
            reconnectRequired: true,
            speed: 0,
            phase: undefined,
            error: candidate.error
              ?? "Transfer session is no longer active. Resume will reconnect.",
          } : candidate);
          emit();
          task = tasks.find((candidate) => candidate.id === taskId);
          controller = undefined;
          notifyOwners();
        }
      }
    }

    // Preferred path after app restart / closed server: open a dedicated SFTP
    // session from vault credentials and continue from the checkpoint. Does not
    // require any UI panel. Single files resume the stream; directories re-walk
    // the tree, skip completed children, and resume partial files.
    const softFailedNeedsHard = action === "resume"
      && !!task
      && task.reconnectRequired === true
      && task.status === "interrupted";
    if (
      (!controller || needsDedicatedReconnect || softFailedNeedsHard)
      && action === "resume"
      && task
      && !task.conflict
      && dedicatedResumeHandler
    ) {
      // Same source+target already writing elsewhere — do not open a second
      // stream into the same destination (.part / rename race).
      const pathConflict = findActivePathConflict(tasks, task);
      if (pathConflict) {
        const conflictError = pathConflictMessage(pathConflict);
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: "attention" as const,
          error: conflictError,
          reconnectRequired: true,
          speed: 0,
          phase: undefined,
        } : candidate);
        emit();
        // Surface exclusivity failures immediately — silent attention rows look
        // like a dead Resume click after path races.
        notify.warning(conflictError, "SFTP");
        return;
      }
      const previousOwnerId = task.ownerId;
      // Detach parent + directory children so publishOwner cannot clobber
      // in-flight dedicated progress with a stale interrupted/paused snapshot.
      tasks = tasks.map((candidate) => {
        if (candidate.id !== taskId && candidate.parentTaskId !== taskId) return candidate;
        if (candidate.id === taskId) {
          return {
            ...candidate,
            ownerId: "dedicated-resume",
            status: "pending" as const,
            error: undefined,
            reconnectRequired: true,
            speed: 0,
            phase: undefined,
            updatedAt: Date.now(),
            lifecycleEpoch: undefined,
          };
        }
        // Re-home children (keep completed status for skip-on-resume).
        return {
          ...candidate,
          ownerId: "dedicated-resume",
          updatedAt: Date.now(),
          lifecycleEpoch: undefined,
        };
      });
      emit();
      const latest = tasks.find((candidate) => candidate.id === taskId) ?? task;
      const result = await dedicatedResumeHandler({
        ...latest,
        ownerId: "dedicated-resume",
        reconnectRequired: true,
      });
      // Cancel/pause may finish while dedicated resume was still reconnecting.
      const afterDedicated = tasks.find((candidate) => candidate.id === taskId);
      if (!afterDedicated || afterDedicated.status === "cancelled") {
        const childIds = tasks
          .filter((candidate) => candidate.parentTaskId === taskId)
          .map((candidate) => candidate.id);
        try { await netcattyBridge.get()?.cancelTransfer?.(taskId); } catch { /* best-effort */ }
        for (const childId of childIds) {
          try { await netcattyBridge.get()?.cancelTransfer?.(childId); } catch { /* best-effort */ }
        }
        const cancelIds = new Set([taskId, ...childIds]);
        tasks = tasks.map((candidate) => cancelIds.has(candidate.id) && candidate.status !== "completed" ? {
          ...candidate,
          status: "cancelled",
          error: undefined,
          endTime: candidate.endTime ?? Date.now(),
          speed: 0,
        } : candidate);
        emit();
        return;
      }
      if (result.success) {
        // Stream / directory finished successfully. Even if the user hit pause
        // during reconnect (status demoted to interrupted), work is done —
        // promote to completed rather than leaving a false interrupted row.
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          ownerId: "dedicated-resume",
          status: "completed",
          transferredBytes: Math.max(
            candidate.transferredBytes,
            candidate.totalBytes || candidate.transferredBytes,
          ),
          speed: 0,
          endTime: Date.now(),
          error: undefined,
          reconnectRequired: false,
          phase: undefined,
        } : candidate);
        emit();
        return;
      }
      if (afterDedicated.status === "paused" || afterDedicated.status === "interrupted") {
        // Keep interrupted/paused without calling cancelTransfer — that would
        // poison pendingCancelTransferIds and break a later same-id resume.
        // Finalize any children still marked transferring after abort wind-down.
        const childIds = tasks
          .filter((candidate) => candidate.parentTaskId === taskId
            && ["transferring", "pausing", "pending", "queued"].includes(candidate.status))
          .map((candidate) => candidate.id);
        if (childIds.length > 0) {
          for (const childId of childIds) {
            try { await netcattyBridge.get()?.cancelTransfer?.(childId); } catch { /* best-effort */ }
          }
          tasks = tasks.map((candidate) => childIds.includes(candidate.id) ? {
            ...candidate,
            status: "interrupted",
            speed: 0,
            reconnectRequired: true,
            phase: undefined,
          } : candidate);
          emit();
        }
        return;
      }
      // Abort throws "Transfer cancelled" when shouldAbort (pause/interrupt).
      // Do not force-cancel a row the user already re-activated (transferring/
      // pending after a quick Resume during wind-down).
      const cancelLike = /cancelled|canceled/i.test(result.error || "");
      if (cancelLike) {
        const liveAfter = tasks.find((candidate) => candidate.id === taskId);
        // Soft-unpause may have painted transferring while the held dedicated
        // walk was already dying. If no invocation remains, demote so Resume works.
        if (
          liveAfter
          && ["transferring", "pending", "queued", "paused", "pausing"].includes(liveAfter.status)
        ) {
          if (!resumeInvocations.has(taskId) && liveAfter.status === "transferring") {
            tasks = tasks.map((candidate) => candidate.id === taskId || candidate.parentTaskId === taskId
              ? {
                ...candidate,
                status: candidate.status === "completed" || candidate.status === "cancelled"
                  ? candidate.status
                  : "interrupted" as const,
                speed: 0,
                reconnectRequired: true,
                phase: undefined,
              }
              : candidate);
            emit();
          }
          return;
        }
        const cancelIds = new Set([
          taskId,
          ...tasks.filter((candidate) => candidate.parentTaskId === taskId).map((c) => c.id),
        ]);
        tasks = tasks.map((candidate) => cancelIds.has(candidate.id) && candidate.status !== "completed" ? {
          ...candidate,
          status: "cancelled",
          error: undefined,
          endTime: candidate.endTime ?? Date.now(),
          speed: 0,
          reconnectRequired: false,
          phase: undefined,
        } : candidate);
        emit();
        return;
      }
      // Source changed / partial directory attention — keep progress, show retry UI.
      if (result.needsAttention) {
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          ownerId: "dedicated-resume",
          status: "attention",
          error: result.error,
          reconnectRequired: false,
          speed: 0,
          phase: undefined,
          retryable: true,
          ...(result.resetCheckpoint
            ? { checkpointBytes: 0, transferredBytes: 0 }
            : null),
        } : candidate);
        emit();
        return;
      }
      // Soft failure for server-to-server (needs panel) — restore prior owner
      // so a live controller can still resume, or fall through to adoption.
      if (result.error && /SFTP panel|both hosts/i.test(result.error)) {
        const restoreOwner = previousOwnerId && previousOwnerId !== "dedicated-resume"
          ? previousOwnerId
          : ownerId;
        const restoredTask = {
          ...(tasks.find((candidate) => candidate.id === taskId) ?? task),
          ownerId: restoreOwner || "dedicated-resume",
          status: "attention" as const,
          error: result.error,
          reconnectRequired: true,
          speed: 0,
          phase: undefined,
        };
        if (restoreOwner) {
          tasks = tasks.map((candidate) => candidate.id === taskId ? restoredTask : candidate);
          emit();
          controller = controllers.get(restoreOwner);
          // Re-home via adopt (not resume) — the panel dropped the row while
          // ownership was dedicated-resume, so resume would no-op.
          if (controller?.canAdopt?.(restoredTask) && controller.adopt) {
            await controller.adopt({ ...restoredTask, ownerId: restoreOwner, reconnectRequired: true });
            return;
          }
          // Live owner cannot adopt (missing host panes) — clear so prepareAdopter runs.
          controller = undefined;
        }
      } else if (result.error) {
        // Hard dedicated failure.
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: "attention",
          error: result.error,
          reconnectRequired: true,
          speed: 0,
          phase: undefined,
        } : candidate);
        emit();
        return;
      }
    }
    if (!controller && action === "resume") {
      const prepared = task ? await prepareAdopter(task) : undefined;
      const adopter = prepared?.adopter;
      const currentTask = tasks.find((candidate) => candidate.id === taskId);
      if (prepared?.cancelled || !currentTask || ["cancelled", "completed"].includes(currentTask.status)) return;
      if (task && adopter) {
        const [adopterId, adopterControls] = adopter;
        // Rehome parent + directory children together so completed child
        // checkpoints survive publishOwner / foreign-owner stripping.
        tasks = tasks.map((candidate) => (
          candidate.id === taskId || candidate.parentTaskId === taskId
            ? { ...candidate, ownerId: adopterId }
            : candidate
        ));
        emit();
        await adopterControls.adopt?.({ ...task, ownerId: adopterId, reconnectRequired: true });
        return;
      }
      if (task) {
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: "attention",
          error: prepared?.error ?? "Could not reconnect. Check the host credentials and try again.",
          reconnectRequired: true,
        } : candidate);
        emit();
      }
    }
    if (!controller && action === "cancel" && task && ["paused", "interrupted", "attention", "pending", "queued", "transferring", "pausing"].includes(task.status)) {
      const childIds = tasks
        .filter((candidate) => candidate.parentTaskId === taskId)
        .map((candidate) => candidate.id);
      // Stop surviving processTransfer walks immediately (panel-local
      // cancelledTasksRef is gone after unmount).
      markTransferCancelledTree(taskId, childIds);
      releaseTransferPauseTree(taskId, childIds);
      try {
        globalSftpTransferScheduler.cancel(taskId);
        for (const childId of childIds) globalSftpTransferScheduler.cancel(childId);
      } catch {
        // best-effort
      }
      try {
        await netcattyBridge.get()?.cancelTransfer?.(taskId);
        for (const childId of childIds) {
          try { await netcattyBridge.get()?.cancelTransfer?.(childId); } catch { /* best-effort */ }
        }
      } catch {
        // Best-effort backend cancel when the owning panel is gone / no window.
      }
      try {
        await netcattyBridge.get()?.cleanupTransferArtifacts?.({
          transferId: taskId,
          sourcePath: task.sourcePath,
          targetPath: task.targetPath,
          stagedTargetPath: task.stagedTargetPath,
        });
      } catch {
        // best-effort temp/.part cleanup
      }
      const cancelIds = new Set([taskId, ...childIds]);
      tasks = tasks.map((candidate) => cancelIds.has(candidate.id) ? {
        ...candidate,
        status: "cancelled",
        error: undefined,
        endTime: Date.now(),
        speed: 0,
        conflict: undefined,
      } : candidate);
      emit();
      return;
    }
    // pause/resume always handled above (unified soft-control or dedicated).
    // Controllers remain for cancel/retry/prioritize when a live owner exists.
    if (action === "pause" || action === "resume") return;
    controller = resolveLiveController(taskId);
    if (!controller) return;
    await controller[action](taskId);
  };

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getSnapshot: () => snapshot,
    getOwnerTasks(ownerId) {
      return tasks.filter((task) => task.ownerId === ownerId).map((task) => ({ ...task }));
    },
    publishOwner(ownerId, ownerTasks) {
      const incoming = new Map(ownerTasks.map((task) => [task.id, task]));
      const existingIds = new Set(tasks.map((task) => task.id));
      // Resolve parent status from the store first when runtime owns the walk
      // (panel is view-only for live lifecycle), else prefer the panel snapshot.
      const resolveParentStatus = (parentTaskId: string | undefined): TransferTask["status"] | undefined => {
        if (!parentTaskId) return undefined;
        const storeParent = tasks.find((candidate) => candidate.id === parentTaskId);
        if (
          storeParent
          && (isTransferWalkInFlight(parentTaskId)
            || isTransferPauseLatched(parentTaskId)
            || storeParent.status === "paused"
            || storeParent.status === "pausing")
        ) {
          return storeParent.status;
        }
        return incoming.get(parentTaskId)?.status ?? storeParent?.status;
      };
      tasks = tasks.flatMap((task) => {
        // Tasks reassigned to dedicated-resume (or other owners) are not
        // clobbered by this panel's local snapshot.
        if (task.ownerId !== ownerId) return [task];
        const replacement = incoming.get(task.id);
        if (!replacement) {
          // Runtime-owned live walks keep their store row even when the panel
          // unmounts and stops publishing (no orphan drop of in-flight work).
          if (
            isTransferWalkInFlight(task.id)
            || (task.parentTaskId && isTransferWalkInFlight(task.parentTaskId))
            || isTransferPauseLatched(task.id)
            || (task.parentTaskId && isTransferPauseLatched(task.parentTaskId))
            || task.status === "transferring"
            || task.status === "pausing"
            || task.status === "paused"
            || task.status === "queued"
            || task.status === "pending"
          ) {
            return [task];
          }
          return [];
        }
        let merged = mergeOwnerPublishedTask(
          task,
          replacement,
          ownerId,
          resolveParentStatus(replacement.parentTaskId ?? task.parentTaskId),
        );
        // Runtime writer is authority for live walks: panel cannot roll lifecycle
        // back past the process-global control epoch (soft pause/resume).
        const runtimeOwned = isTransferWalkInFlight(task.id)
          || (task.parentTaskId ? isTransferWalkInFlight(task.parentTaskId) : false)
          || isTransferOrRootPauseLatched(task.parentTaskId ?? task.id, task.id);
        if (runtimeOwned) {
          const storeEpoch = Number.isFinite(task.lifecycleEpoch)
            ? (task.lifecycleEpoch as number)
            : getTransferControlEpoch(task.parentTaskId ?? task.id);
          const panelEpoch = Number.isFinite(replacement.lifecycleEpoch)
            ? (replacement.lifecycleEpoch as number)
            : -1;
          if (storeEpoch > panelEpoch) {
            merged = {
              ...merged,
              status: task.status,
              lifecycleEpoch: task.lifecycleEpoch ?? merged.lifecycleEpoch,
              speed: (task.status === "paused" || task.status === "pausing") ? 0 : merged.speed,
              transferredBytes: Math.max(task.transferredBytes ?? 0, merged.transferredBytes ?? 0),
              checkpointBytes: Math.max(task.checkpointBytes ?? 0, merged.checkpointBytes ?? 0),
            };
          }
        }
        return [merged];
      });
      for (const task of ownerTasks) {
        // Never re-introduce a panel row that already exists under another owner
        // (e.g. completed via dedicated resume while the panel still holds interrupted).
        if (!existingIds.has(task.id)) {
          const parentStatus = resolveParentStatus(task.parentTaskId);
          const parentHoldsPause = parentStatus === "paused" || parentStatus === "pausing";
          const seeded = { ...task, ownerId, updatedAt: task.updatedAt ?? Date.now() };
          if (
            parentHoldsPause
            && !["completed", "cancelled", "failed", "paused", "pausing"].includes(seeded.status)
          ) {
            seeded.status = parentStatus === "pausing" ? "pausing" : "paused";
            seeded.speed = 0;
          }
          tasks.push(seeded);
        }
      }
      emit();
    },
    registerOwner(ownerId, controls) {
      controllers.set(ownerId, controls);
      return () => {
        if (controllers.get(ownerId) === controls) controllers.delete(ownerId);
      };
    },
    setDedicatedResumeHandler(handler) {
      dedicatedResumeHandler = handler;
      notifyDedicatedResumeWaiters();
    },
    patchTask(taskId, updates) {
      let changed = false;
      tasks = tasks.map((task) => {
        if (task.id !== taskId) return task;
        // Never resurrect a user-stopped row via dedicated-resume progress.
        if (task.status === "cancelled") return task;
        // Dedicated-owned rows may move pending → transferring while a panel
        // still holds a local interrupted copy that is no longer authoritative.
        // Force-quit continue rehomes to dedicated-resume first; allow that owner
        // (and explicit ownerId updates in the patch) to leave interrupted.
        const nextOwner = updates.ownerId ?? task.ownerId;
        const currentEpoch = Number.isFinite(task.lifecycleEpoch)
          ? (task.lifecycleEpoch as number)
          : -1;
        const incomingEpoch = Number.isFinite(updates.lifecycleEpoch)
          ? (updates.lifecycleEpoch as number)
          : -1;
        const explicitResume = updates.status === "transferring"
          && (incomingEpoch > currentEpoch
            || (incomingEpoch >= currentEpoch && updates.lifecycleEpoch !== undefined)
            || nextOwner === "dedicated-resume"
            || task.ownerId === "dedicated-resume");
        if (
          nextOwner !== "dedicated-resume"
          && task.ownerId !== "dedicated-resume"
          && (task.status === "paused" || task.status === "interrupted")
          && (updates.status === "transferring" || updates.status === "pending" || updates.status === "completed")
          && !explicitResume
        ) {
          return task;
        }
        changed = true;
        const merged = { ...task, ...updates, updatedAt: Date.now() };
        // Soft-drain / walk progress must not move the visible bar after Pause.
        // Latch or paused/pausing freezes transferredBytes/speed unless this patch
        // is an explicit resume (transferring + epoch, or dedicated-resume).
        const pauseHeld = isTransferOrRootPauseLatched(task.parentTaskId ?? task.id, task.id)
          || isTransferPauseLatched(task.id)
          || task.status === "paused"
          || task.status === "pausing"
          || (!!task.parentTaskId && (
            isTransferPauseLatched(task.parentTaskId)
            || (() => {
              const parent = tasks.find((candidate) => candidate.id === task.parentTaskId);
              return parent?.status === "paused" || parent?.status === "pausing";
            })()
          ));
        if (pauseHeld && !explicitResume) {
          // Keep lifecycle at paused/pausing; allow checkpoint metadata only.
          if (task.status === "pausing" || task.status === "paused") {
            merged.status = task.status;
          } else {
            merged.status = "paused";
          }
          merged.transferredBytes = task.transferredBytes;
          merged.speed = 0;
          if (updates.checkpointBytes !== undefined) {
            merged.checkpointBytes = Math.max(
              task.checkpointBytes ?? 0,
              Number(updates.checkpointBytes) || 0,
            );
          } else {
            merged.checkpointBytes = task.checkpointBytes;
          }
          if (updates.resumeStage !== undefined) merged.resumeStage = updates.resumeStage;
          if (updates.downloadCheckpointBytes !== undefined) {
            merged.downloadCheckpointBytes = updates.downloadCheckpointBytes;
          }
          if (updates.uploadCheckpointBytes !== undefined) {
            merged.uploadCheckpointBytes = updates.uploadCheckpointBytes;
          }
          if (updates.sourceFingerprint !== undefined) {
            merged.sourceFingerprint = updates.sourceFingerprint;
          }
          return merged;
        }
        // Dedicated-resume / dual-writer progress must never roll the bar back
        // while the row is still live (force-quit continue freezes at checkpoint
        // if a late lower sample wins).
        if (
          updates.transferredBytes !== undefined
          && !["completed", "cancelled", "failed"].includes(merged.status)
          && (updates.status === "transferring" || merged.status === "transferring" || merged.status === "pausing")
        ) {
          merged.transferredBytes = Math.max(task.transferredBytes ?? 0, Number(updates.transferredBytes) || 0);
        }
        if (
          updates.checkpointBytes !== undefined
          && !["completed", "cancelled", "failed"].includes(merged.status)
        ) {
          merged.checkpointBytes = Math.max(task.checkpointBytes ?? 0, Number(updates.checkpointBytes) || 0);
        }
        return merged;
      });
      if (changed) emit();
    },
    upsertTasks(incoming) {
      if (incoming.length === 0) return;
      const byId = new Map(incoming.map((task) => [task.id, task]));
      const seen = new Set<string>();
      const parentTerminal = new Map<string, TransferTask["status"]>();
      for (const task of tasks) {
        if (!task.parentTaskId) parentTerminal.set(task.id, task.status);
      }
      tasks = tasks.map((task) => {
        const replacement = byId.get(task.id);
        if (!replacement) return task;
        seen.add(task.id);
        if (task.status === "cancelled" && replacement.status !== "cancelled") return task;
        return { ...task, ...replacement, updatedAt: Date.now() };
      });
      for (const task of incoming) {
        if (seen.has(task.id)) continue;
        // Do not resurrect work under a cancelled/completed directory parent.
        if (task.parentTaskId) {
          const parentStatus = parentTerminal.get(task.parentTaskId)
            ?? tasks.find((candidate) => candidate.id === task.parentTaskId)?.status;
          if (parentStatus === "cancelled" || parentStatus === "completed") continue;
        }
        tasks.push({ ...task, updatedAt: task.updatedAt ?? Date.now() });
      }
      emit();
    },
    canControl(taskId) {
      const ownerId = findOwner(taskId);
      const task = tasks.find((candidate) => candidate.id === taskId);
      if (!task) return false;
      const hasLiveOwner = !!ownerId && controllers.has(ownerId);
      if (hasLiveOwner) return true;
      // After restart (or panel unmount) unfinished tasks stay in the store with
      // no owner controller. The global center must still be able to resume,
      // cancel, or dismiss them — otherwise they become dead rows.
      const terminal = task.status === "completed" || task.status === "cancelled";
      // Failed rows stay controllable so orphan Retry/Resume can still run after restart.
      if (!terminal && task.status !== "failed") return true;
      if (task.status === "failed") return true;
      return !!(task && [...controllers.values()].some((controls) => (
        controls.adopt && controls.canAdopt?.(task)
      )));
    },
    pause: (taskId) => invoke(taskId, "pause"),
    async resume(taskId) {
      const startFresh = () => {
        const running = invoke(taskId, "resume").finally(() => {
          if (resumeInvocations.get(taskId) === running) resumeInvocations.delete(taskId);
        });
        resumeInvocations.set(taskId, running);
        return running;
      };

      const existing = resumeInvocations.get(taskId);
      if (existing) {
        // Dedicated resume holds the invocation for the full stream lifetime.
        const task = tasks.find((candidate) => candidate.id === taskId);
        if (task?.status === "cancelled") return existing;

        // Soft-unpause live backend streams when still paused under a held run.
        // Dedicated *directory* walks treat status===paused as shouldAbort between
        // files, so soft-rejoin is unsafe (dying promise + false transferring).
        // Wind down soft-paused children then startFresh from checkpoints.
        // Single-file dedicated (and non-directory live streams) soft-unpause.
        if (task && (task.status === "paused" || task.status === "pausing")) {
          const childIds = tasks
            .filter((candidate) => candidate.parentTaskId === taskId
              && (candidate.status === "paused"
                || candidate.status === "pausing"
                || candidate.status === "transferring"))
            .map((candidate) => candidate.id);

          // Always clear process-global latches + control epoch so walks wake and
          // late soft-drain / pauseWatch cannot re-pause streams. Do not stamp
          // control epoch as task.lifecycleEpoch (bridge-aligned only).
          bumpTransferControlEpoch(taskId);
          releaseTransferPauseTree(taskId, childIds);

          if (task.ownerId === "dedicated-resume" && task.isDirectory) {
            const bridge = netcattyBridge.get();
            // Cancel soft-paused child streams so the held walk settles. When a
            // child is not in activeTransfers, cancelTransfer leaves a sticky
            // pendingCancel latch — clear it before startFresh reuses the same
            // child transferIds (otherwise startStreamTransfer aborts immediately).
            for (const id of childIds) {
              try { await bridge?.cancelTransfer?.(id); } catch { /* best-effort wind-down */ }
              try { await bridge?.clearPendingTransferCancel?.(id); } catch { /* best-effort */ }
            }
            try { await bridge?.clearPendingTransferCancel?.(taskId); } catch { /* best-effort */ }
            try {
              await existing;
            } catch { /* previous aborted / cancelled */ }
            // Clear again after wind-down in case cancel raced during await.
            for (const id of childIds) {
              try { await bridge?.clearPendingTransferCancel?.(id); } catch { /* best-effort */ }
            }
            try { await bridge?.clearPendingTransferCancel?.(taskId); } catch { /* best-effort */ }
            return resumeInvocations.get(taskId) ?? startFresh();
          }

          try {
            const resumeIds = [taskId, ...childIds.filter((id) => id !== taskId)];
            const results = await Promise.all(resumeIds.map(async (id) =>
              netcattyBridge.get()?.resumeTransfer?.(id) ?? { success: false },
            ));
            const after = tasks.find((candidate) => candidate.id === taskId);
            if (after?.status === "cancelled") return existing;
            // Only rejoin when at least one backend stream actually resumed.
            // Empty/all-fail must not paint transferring over a dead held run.
            const successIds = resumeIds.filter((_, index) => results[index]?.success);
            if (successIds.length > 0) {
              const resumed = new Set(successIds);
              // Align with softResumeTransfer: prefer bridge lifecycleEpoch; clear
              // if omitted so main-process progress is not stale-dropped.
              let bridgeEpoch: number | undefined;
              for (let index = 0; index < results.length; index += 1) {
                if (!results[index]?.success) continue;
                const epoch = (results[index] as { lifecycleEpoch?: number } | undefined)?.lifecycleEpoch;
                if (!Number.isFinite(epoch)) continue;
                bridgeEpoch = bridgeEpoch === undefined
                  ? (epoch as number)
                  : Math.max(bridgeEpoch, epoch as number);
              }
              tasks = tasks.map((candidate) => {
                if (candidate.id === taskId || resumed.has(candidate.id)) {
                  return {
                    ...candidate,
                    status: "transferring" as const,
                    error: undefined,
                    reconnectRequired: false,
                    pauseUnavailableReason: undefined,
                    phase: undefined,
                    lifecycleEpoch: bridgeEpoch,
                  };
                }
                return candidate;
              });
              emit();
              return existing;
            }
          } catch {
            // Fall through to await + restart.
          }
          try {
            await existing;
          } catch { /* previous aborted */ }
          return resumeInvocations.get(taskId) ?? startFresh();
        }

        // After demotion to interrupted/attention/failed while work unwinds:
        // wait then re-invoke (do not rejoin a dying canceling promise).
        if (task && (task.status === "interrupted" || task.status === "attention" || task.status === "failed")) {
          try {
            await existing;
          } catch { /* previous aborted */ }
          return resumeInvocations.get(taskId) ?? startFresh();
        }
        return existing;
      }
      return startFresh();
    },
    cancel: (taskId) => invoke(taskId, "cancel"),
    async retry(taskId) {
      const ownerId = findOwner(taskId);
      const controller = controllers.get(ownerId ?? "");
      if (controller) {
        await controller.retry(taskId);
        return;
      }
      // Orphaned after restart: clear checkpoint so Retry truly restarts, then
      // resume (dedicated or adopt) from byte 0.
      const task = tasks.find((candidate) => candidate.id === taskId);
      if (task) {
        tasks = tasks.map((candidate) => candidate.id === taskId ? {
          ...candidate,
          status: "interrupted",
          error: undefined,
          checkpointBytes: 0,
          transferredBytes: 0,
          downloadCheckpointBytes: undefined,
          uploadCheckpointBytes: undefined,
          resumeStage: undefined,
          sourceFingerprint: undefined,
          reconnectRequired: true,
          speed: 0,
          endTime: undefined,
          lifecycleEpoch: undefined,
        } : candidate);
        emit();
        try {
          await netcattyBridge.get()?.cleanupTransferArtifacts?.({
            transferId: taskId,
            sourcePath: task.sourcePath,
            targetPath: task.targetPath,
            stagedTargetPath: task.stagedTargetPath,
          });
        } catch {
          // best-effort
        }
      }
      await this.resume(taskId);
    },
    async prioritize(taskId) {
      const ownerId = findOwner(taskId);
      const controller = controllers.get(ownerId ?? "");
      if (controller) {
        void controller.prioritize(taskId);
        return;
      }
      // Orphan: still bump store priority and ask backend / renderer scheduler.
      tasks = tasks.map((candidate) => candidate.id === taskId
        ? { ...candidate, priority: Date.now(), updatedAt: Date.now() }
        : candidate);
      emit();
      try {
        void netcattyBridge.get()?.prioritizeTransfer?.(taskId);
      } catch {
        // best-effort
      }
      try {
        globalSftpTransferScheduler.prioritize(taskId);
      } catch {
        // Scheduler may be empty in pure node tests.
      }
    },
    async resolveConflict(taskId, action, applyToAll) {
      let ownerId = findOwner(taskId);
      let controller = controllers.get(ownerId ?? "");
      const task = tasks.find((candidate) => candidate.id === taskId);
      if (!controller && task) {
        const prepared = await prepareAdopter(task);
        if (prepared.cancelled) return;
        const adopter = prepared.adopter;
        if (!adopter) {
          tasks = tasks.map((candidate) => candidate.id === taskId ? {
            ...candidate,
            status: "attention",
            error: prepared.error ?? "Could not open an SFTP panel to resolve this conflict.",
            reconnectRequired: true,
          } : candidate);
          emit();
          return;
        }
        const [adopterId, adopterControls] = adopter;
        const current = tasks.find((candidate) => candidate.id === taskId);
        if (!current || current.status === "cancelled" || current.status === "completed") return;
        ownerId = adopterId;
        controller = adopterControls;
        tasks = tasks.map((candidate) => candidate.id === taskId ? { ...candidate, ownerId: adopterId } : candidate);
        emit();
        await adopterControls.adopt?.({ ...current, ownerId: adopterId });
        const afterAdopt = tasks.find((candidate) => candidate.id === taskId);
        if (!afterAdopt || afterAdopt.status === "cancelled") return;
      }
      await controller?.resolveConflict?.(taskId, action, applyToAll);
    },
    dismiss(taskId) {
      const ownerId = findOwner(taskId);
      const controller = ownerId ? controllers.get(ownerId) : undefined;
      if (controller) {
        controller.dismiss(taskId);
      }
      tasks = tasks.filter((task) => task.id !== taskId && task.parentTaskId !== taskId);
      emit();
    },
    clearTerminal(status) {
      const terminal = new Set<TransferTask["status"]>(["completed", "failed", "cancelled"]);
      const unfinishedParents = new Set(
        tasks.filter((task) => !terminal.has(task.status)).map((task) => task.id),
      );
      // Do not wipe completed children of an unfinished directory parent —
      // those rows are resume checkpoints, not disposable history.
      const removing = tasks.filter((task) =>
        terminal.has(task.status)
        && (status === undefined || task.status === status)
        && !(task.parentTaskId && unfinishedParents.has(task.parentTaskId)),
      );
      for (const task of removing) {
        controllers.get(task.ownerId ?? "")?.dismiss(task.id);
      }
      const removingIds = new Set(removing.map((task) => task.id));
      tasks = tasks.filter((task) => !removingIds.has(task.id) && !removingIds.has(task.parentTaskId ?? ""));
      emit();
    },
    markReconnectRequired(taskId, error) {
      tasks = tasks.map((task) => task.id === taskId ? {
        ...task,
        status: "attention",
        error: error ?? "The original server connection is unavailable",
        reconnectRequired: true,
        speed: 0,
        lifecycleEpoch: undefined,
      } : task);
      emit();
    },
    reportResumePreparationFailure(taskId, error) {
      resumePreparationFailures.set(taskId, error);
    },
    ingestBackgroundEvent(event) {
      const existing = tasks.find((task) => task.id === event.transferId);
      const terminal = existing
        && (existing.status === "cancelled" || existing.status === "completed" || existing.status === "failed");
      // Never resurrect a finished/cancelled agent row with late queued/progress.
      if (terminal && event.type !== "cancelled") {
        // Allow a late explicit cancel to stick; ignore everything else.
        if (event.type === "completed" || event.type === "failed") {
          // Keep existing terminal state (prefer cancelled over late completed).
          return;
        }
        if (event.type === "queued" || event.type === "started" || event.type === "progress" || event.type === "pausing" || event.type === "resumed" || event.type === "paused") {
          return;
        }
      }
      if ((event.type === "queued" || event.type === "started" || event.type === "progress") && !existing) {
        const sourcePath = event.sourcePath ?? "";
        const targetPath = event.targetPath ?? "";
        tasks.push({
          id: event.transferId,
          ownerId: event.controlKind === "compressed-upload" ? "background-transfer" : "background-agent",
          fileName: event.fileName ?? (
            targetPath.split(/[\\/]/).pop()
            || sourcePath.split(/[\\/]/).pop()
            || event.transferId
          ),
          sourcePath,
          targetPath,
          sourceConnectionId: event.direction === "upload" ? "local" : (event.sessionId ?? "agent"),
          targetConnectionId: event.direction === "download" ? "local" : (event.sessionId ?? "agent"),
          sourceHostId: event.sourceHostId,
          targetHostId: event.targetHostId,
          direction: event.direction ?? "upload",
          status: event.type === "queued" ? "queued" : "transferring",
          totalBytes: event.totalBytes ?? 0,
          transferredBytes: event.transferred ?? 0,
          speed: event.speed ?? 0,
          startTime: event.startedAt ?? Date.now(),
          isDirectory: event.isDirectory ?? false,
          origin: "agent",
          background: true,
          resumable: true,
          controlKind: event.controlKind,
          phase: event.phase,
          lifecycleEpoch: event.lifecycleEpoch,
        });
      } else if (existing && (event.type === "queued" || event.type === "started")) {
        tasks = tasks.map((task) => {
          if (task.id !== event.transferId) return task;
          const currentEpoch = Number.isFinite(task.lifecycleEpoch) ? (task.lifecycleEpoch as number) : -1;
          const incomingEpoch = Number.isFinite(event.lifecycleEpoch) ? (event.lifecycleEpoch as number) : -1;
          const acceptsLifecycle = task.lifecycleEpoch === undefined
            ? true
            : event.lifecycleEpoch !== undefined && incomingEpoch >= currentEpoch;
          return {
            ...task,
            status: acceptsLifecycle
              ? (event.type === "queued" ? "queued" : "transferring")
              : task.status,
            error: acceptsLifecycle ? undefined : task.error,
            endTime: acceptsLifecycle ? undefined : task.endTime,
            fileName: event.fileName ?? task.fileName,
            totalBytes: event.totalBytes ?? task.totalBytes,
            isDirectory: event.isDirectory ?? task.isDirectory,
            controlKind: event.controlKind ?? task.controlKind,
            phase: event.phase ?? task.phase,
            lifecycleEpoch: acceptsLifecycle && event.lifecycleEpoch !== undefined
              ? event.lifecycleEpoch
              : task.lifecycleEpoch,
          };
        });
      } else if (existing && event.type === "progress") {
        tasks = tasks.map((task) => {
          if (task.id !== event.transferId) return task;
          // Main-process progress must re-open a live bar even if the panel is
          // hidden and no longer painting "transferring" via React callbacks.
          const nextTransferred = event.transferred ?? task.transferredBytes;
          const currentEpoch = Number.isFinite(task.lifecycleEpoch) ? (task.lifecycleEpoch as number) : -1;
          const incomingEpoch = Number.isFinite(event.lifecycleEpoch) ? (event.lifecycleEpoch as number) : -1;
          const staleLifecycle = task.lifecycleEpoch !== undefined
            && (event.lifecycleEpoch === undefined || incomingEpoch < currentEpoch);
          if (staleLifecycle) return task;
          const acceptsLifecycle = incomingEpoch >= currentEpoch;
          const lifecycleStatus = event.lifecycleState === "paused"
            ? "paused"
            : event.lifecycleState === "pausing"
              ? "pausing"
              : event.lifecycleState === "queued"
                ? "queued"
                : "transferring";
          // Soft-drain continues writing after Pause, but the bar must freeze as
          // soon as the user asked to pause. Only an explicit resume (higher
          // epoch + transferring) may move bytes again.
          // Also freeze children when the folder parent is paused/latched — otherwise
          // soft-drain progress re-opens them as "transferring" and the collapsed
          // current-file row blinks in and out under an already-paused parent.
          const parentRow = task.parentTaskId
            ? tasks.find((candidate) => candidate.id === task.parentTaskId)
            : undefined;
          const parentHoldsPause = !!parentRow && (
            parentRow.status === "paused"
            || parentRow.status === "pausing"
            || isTransferPauseLatched(parentRow.id)
          );
          const selfLatched = isTransferOrRootPauseLatched(
            task.parentTaskId ?? task.id,
            task.id,
          ) || isTransferPauseLatched(task.id);
          const uiPauseLatched = task.status === "paused"
            || task.status === "pausing"
            || parentHoldsPause
            || selfLatched;
          const explicitResume = lifecycleStatus === "transferring"
            && event.lifecycleEpoch !== undefined
            && incomingEpoch > currentEpoch;
          if (uiPauseLatched && !explicitResume) {
            const nextCheckpoint = Math.max(
              task.checkpointBytes ?? 0,
              Number(event.checkpointBytes) || 0,
            );
            const holdStatus = event.lifecycleState === "paused" && acceptsLifecycle
              ? "paused" as const
              : (task.status === "paused" || task.status === "pausing"
                ? task.status
                : parentHoldsPause
                  ? (parentRow!.status === "pausing" ? "pausing" as const : "paused" as const)
                  : "pausing" as const);
            return {
              ...task,
              // Keep pausing until backend confirms paused; never flip back to
              // transferring from soft-drain progress.
              status: holdStatus,
              speed: 0,
              checkpointBytes: nextCheckpoint || task.checkpointBytes,
              resumeStage: event.resumeStage ?? task.resumeStage,
              downloadCheckpointBytes: event.downloadCheckpointBytes ?? task.downloadCheckpointBytes,
              uploadCheckpointBytes: event.uploadCheckpointBytes ?? task.uploadCheckpointBytes,
              sourceFingerprint: event.sourceFingerprint ?? task.sourceFingerprint,
              updatedAt: Date.now(),
              lifecycleEpoch: acceptsLifecycle && event.lifecycleEpoch !== undefined
                ? event.lifecycleEpoch
                : task.lifecycleEpoch,
            };
          }
          const nextStatus = event.lifecycleEpoch !== undefined && acceptsLifecycle
            ? lifecycleStatus
            : task.lifecycleEpoch === undefined
              ? ((task.status === "paused" || task.status === "pausing") ? task.status : "transferring")
              : task.status;
          const keepPaused = nextStatus === "paused" || nextStatus === "pausing";
          return {
            ...task,
            status: nextStatus,
            transferredBytes: keepPaused ? task.transferredBytes : nextTransferred,
            totalBytes: event.totalBytes ?? task.totalBytes,
            speed: keepPaused ? 0 : (event.speed ?? task.speed),
            checkpointBytes: event.checkpointBytes ?? task.checkpointBytes,
            resumeStage: event.resumeStage ?? task.resumeStage,
            downloadCheckpointBytes: event.downloadCheckpointBytes ?? task.downloadCheckpointBytes,
            uploadCheckpointBytes: event.uploadCheckpointBytes ?? task.uploadCheckpointBytes,
            sourceFingerprint: event.sourceFingerprint ?? task.sourceFingerprint,
            phase: event.phase ?? task.phase,
            error: keepPaused ? task.error : undefined,
            endTime: undefined,
            updatedAt: Date.now(),
            lifecycleEpoch: acceptsLifecycle && event.lifecycleEpoch !== undefined
              ? event.lifecycleEpoch
              : task.lifecycleEpoch,
          };
        });
      } else if (existing && (event.type === "pausing" || event.type === "paused" || event.type === "resumed")) {
        tasks = tasks.map((task) => {
          if (task.id !== event.transferId) return task;
          const currentEpoch = Number.isFinite(task.lifecycleEpoch) ? (task.lifecycleEpoch as number) : -1;
          const incomingEpoch = Number.isFinite(event.lifecycleEpoch) ? (event.lifecycleEpoch as number) : -1;
          const acceptsLifecycle = task.lifecycleEpoch === undefined
            ? true
            : event.lifecycleEpoch !== undefined && incomingEpoch >= currentEpoch;
          return {
            ...task,
            status: acceptsLifecycle
              ? (event.type === "pausing" ? "pausing" : event.type === "paused" ? "paused" : "transferring")
              : task.status,
            speed: acceptsLifecycle && event.type !== "resumed" ? 0 : task.speed,
            checkpointBytes: event.checkpointBytes ?? task.checkpointBytes,
            resumeStage: event.resumeStage ?? task.resumeStage,
            downloadCheckpointBytes: event.downloadCheckpointBytes ?? task.downloadCheckpointBytes,
            uploadCheckpointBytes: event.uploadCheckpointBytes ?? task.uploadCheckpointBytes,
            sourceFingerprint: event.sourceFingerprint ?? task.sourceFingerprint,
            phase: event.phase ?? task.phase,
            lifecycleEpoch: acceptsLifecycle && event.lifecycleEpoch !== undefined
              ? event.lifecycleEpoch
              : task.lifecycleEpoch,
          };
        });
      } else if (existing && event.type !== "started") {
        tasks = tasks.map((task) => task.id === event.transferId ? {
          ...task,
          status: event.type === "completed" ? "completed" : event.type === "cancelled" ? "cancelled" : "failed",
          error: event.error,
          endTime: event.endedAt ?? Date.now(),
          speed: 0,
        } : task);
      }
      emit();
    },
  };
}

const browserPersistence: StorePersistence | undefined = typeof globalThis.localStorage === "undefined"
  ? undefined
  : {
      read: () => globalThis.localStorage.getItem(STORAGE_KEY_SFTP_TRANSFER_CENTER),
      write: (value) => globalThis.localStorage.setItem(STORAGE_KEY_SFTP_TRANSFER_CENTER, value),
    };

export const sftpTransferCenterStore = createSftpTransferCenterStore(browserPersistence);

export function useSftpTransferCenter(): SftpTransferCenterSnapshot {
  return useSyncExternalStore(
    sftpTransferCenterStore.subscribe,
    sftpTransferCenterStore.getSnapshot,
    sftpTransferCenterStore.getSnapshot,
  );
}
