import { useCallback, useEffect, useMemo, useRef, useState, type SetStateAction } from "react";
import {
  FileConflict,
  FileConflictAction,
  SftpFilenameEncoding,
  TransferDirection,
  TransferStatus,
  TransferTask,
} from "../../../domain/models";
import {
  canReplaceSftpConflict,
  describeSftpExistingKind,
  describeSftpIncomingKind,
  getSftpConflictTypeKey,
} from "../../../domain/sftpConflict";
import {
  findActivePathConflict,
  pathConflictMessage,
} from "../../../domain/sftpTransferConflicts";
import { useI18n } from "../../i18n/I18nProvider";
import { notify } from "../../notification";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { logger } from "../../../lib/logger";
import { sftpTransferCenterStore } from "../sftpTransferCenterStore";
import { SftpPane } from "./types";
import { useSftpDirectoryTransferOps } from "./transferDirectoryOps";
import { useSftpTransferConflictOps } from "./transferConflictOps";
import { useSftpTransferTaskOps } from "./transferTaskOps";
import { globalSftpTransferScheduler } from "./globalTransferScheduler";
import {
  createDirectDownloadTransferTask,
  resolveDirectDirectoryDownloadFinalStatus,
} from "./downloadTransferTask";
import {
  releaseTransferPauseTree,
  waitUntilTransferPauseReleased,
} from "./transferPauseLatch";
import {
  bumpTransferControlEpoch,
} from "./transferControlEpoch";
import { transferRuntime } from "./transferRuntime";
import {
  clearTransferCancelled,
  clearTransferCancelledTree,
  isTransferCancelledFlag,
  markTransferCancelled,
  markTransferCancelledTree,
} from "./transferCancelLatch";
import type { TransferResult, UseSftpTransfersParams, UseSftpTransfersResult } from "./useSftpTransfers.types";
import type { TransferConnectionLease } from "./transferConnectionPool";
import { getParentPath, joinPath } from "./utils";

/** Keep the MutableRefObject mirror in sync with the process-global latch set. */
function syncPausedTasksRef(ref: { current: Set<string> }, taskId: string, latched: boolean) {
  if (latched) ref.current.add(taskId);
  else ref.current.delete(taskId);
}

/** Set whose has() also observes process-global cancel flags (tab-close path). */
class PanelCancelSet extends Set<string> {
  override add(id: string): this {
    super.add(id);
    markTransferCancelled(id);
    return this;
  }
  override delete(id: string): boolean {
    const had = super.delete(id);
    clearTransferCancelled(id);
    return had;
  }
  override has(id: string): boolean {
    return super.has(id) || isTransferCancelledFlag(id);
  }
}

function createPanelCancelSet(): Set<string> {
  return new PanelCancelSet();
}

/** User-facing path exclusivity notice (toast + logs). */
function notifyPathConflict(
  existing: Pick<TransferTask, "fileName" | "status">,
  t: (key: string, values?: Record<string, string | number>) => string,
): void {
  const name = existing.fileName || "file";
  const message = existing.status === "paused"
    ? t("sftp.transfers.pathConflict.paused", { name })
    : t("sftp.transfers.pathConflict.inProgress", { name });
  logger.warn("[SFTP] path conflict", pathConflictMessage(existing));
  notify.warning(message, t("sftp.transfers.pathConflict.title"));
}

export const useSftpTransfers = ({
  ownerId,
  canPrepareAdoption,
  getActivePane,
  getPaneByConnectionId,
  getTabByConnectionId,
  updateTab,
  refresh,
  clearCacheForConnection,
  sftpSessionsRef,
  connectionCacheKeyMapRef,
  listLocalFiles,
  listRemoteFiles,
  handleSessionError,
  acquireTransferSession,
}: UseSftpTransfersParams): UseSftpTransfersResult => {
  const { t } = useI18n();
  const tRef = useRef(t);
  tRef.current = t;
  const [transfers, setTransfersState] = useState<TransferTask[]>(() => sftpTransferCenterStore.getOwnerTasks(ownerId));
  const [conflicts, setConflicts] = useState<FileConflict[]>(() => sftpTransferCenterStore
    .getOwnerTasks(ownerId)
    .map((task) => task.conflict)
    .filter((conflict): conflict is FileConflict => !!conflict));

  // Track cancelled task IDs. has() also sees process-global cancel flags set by
  // the transfer center after the React owner unmounts.
  const cancelledTasksRef = useRef<Set<string>>(createPanelCancelSet());
  // Process-global pause latches (survive panel unmount / tab close). Local ref
  // mirrors the global set so transferDirectoryOps can keep using a MutableRefObject.
  const pausedTasksRef = useRef<Set<string>>(new Set());
  const pauseWaitersRef = useRef<Map<string, Set<() => void>>>(new Map());
  // Track active child transfer IDs per parent (outside React state for immediate visibility)
  const activeChildIdsRef = useRef<Map<string, Set<string>>>(new Map());
  // Live processTransfer walks (esp. directory parents). Resume must unlatch
  // these instead of starting a second full re-walk of the tree.
  const inFlightTransferIdsRef = useRef<Set<string>>(new Set());
  /** While folder is latched, keep hammering pauseTransfer on live children. */
  const folderPauseWatchdogsRef = useRef<Map<string, ReturnType<typeof setInterval>>>(new Map());
  // Sole writer after mount is setTransfers (ref-first). Do not clobber with
  // React state on every render — a parent re-render between progress IPC ticks
  // would roll the ref back and freeze the global center bar.
  const transfersRef = useRef(transfers);
  const conflictsRef = useRef(conflicts);
  conflictsRef.current = conflicts;

  // Keep transfersRef + global center in lockstep with every update so progress
  // survives panel hide/unmount. Preload transfer:progress callbacks outlive
  // React — they still call this setTransfers after the owner unmounts.
  //
  // CRITICAL: publishOwner must NOT live only inside setState's updater. After
  // unmount React discards setState and never runs the updater, which froze the
  // global transfer center bar while the stream kept running. Apply against
  // transfersRef first so progress IPC keeps publishing even when React is gone.
  //
  // Do not pre-assign transfersRef before calling setTransfers with the same
  // array reference — identity equality skips publish (avoids store↔listener loops).
  const setTransfers = useCallback((update: SetStateAction<TransferTask[]>) => {
    const prev = transfersRef.current;
    const next = typeof update === "function"
      ? (update as (value: TransferTask[]) => TransferTask[])(prev)
      : update;
    if (next === prev) return;
    transfersRef.current = next;
    sftpTransferCenterStore.publishOwner(ownerId, next);
    setTransfersState(next);
  }, [ownerId]);
  const completionHandlersRef = useRef<Map<string, (result: TransferResult) => void | Promise<void>>>(new Map());
  const conflictDefaultsRef = useRef<Map<string, FileConflictAction>>(new Map());

  const clearCancelledTask = useCallback((taskId: string) => {
    cancelledTasksRef.current.delete(taskId);
    clearTransferCancelled(taskId);
  }, []);

  const waitUntilTransferResumed = useCallback(async (taskId: string) => {
    // Process-global latch: survives panel unmount so the transfer center can
    // release a walk that outlived the React owner.
    await waitUntilTransferPauseReleased(taskId);
    syncPausedTasksRef(pausedTasksRef, taskId, false);
  }, []);

  const stopFolderPauseWatchdog = useCallback((taskId: string) => {
    const timer = folderPauseWatchdogsRef.current.get(taskId);
    if (timer) {
      clearInterval(timer);
      folderPauseWatchdogsRef.current.delete(taskId);
    }
  }, []);

  /** Every unfinished child id under a folder parent (state + live workers). */
  const collectPauseTreeChildIds = useCallback((rootTaskId: string): string[] => {
    const childIdSet = new Set(activeChildIdsRef.current.get(rootTaskId) ?? []);
    for (const candidate of transfersRef.current) {
      if (
        candidate.parentTaskId === rootTaskId
        && !["completed", "cancelled", "failed"].includes(candidate.status)
      ) {
        childIdSet.add(candidate.id);
      }
    }
    return [...childIdSet];
  }, []);

  /**
   * Resume/cancel must clear the whole latch tree. Store pause latches parent
   * + children; releasing only the parent left child latches stuck and the
   * directory walk blocked forever (UI "transferring", 0 bytes moving).
   */
  const releasePausedTransfer = useCallback((taskId: string, childIds?: readonly string[]) => {
    // Invalidate any in-flight soft-drain / watchdog pause IPC for this tree.
    bumpTransferControlEpoch(taskId);
    const kids = childIds ?? collectPauseTreeChildIds(taskId);
    releaseTransferPauseTree(taskId, kids);
    syncPausedTasksRef(pausedTasksRef, taskId, false);
    for (const id of kids) syncPausedTasksRef(pausedTasksRef, id, false);
    stopFolderPauseWatchdog(taskId);
    const wake = (id: string) => {
      const waiters = pauseWaitersRef.current.get(id);
      pauseWaitersRef.current.delete(id);
      for (const resolve of waiters ?? []) resolve();
    };
    wake(taskId);
    for (const id of kids) wake(id);
  }, [collectPauseTreeChildIds, stopFolderPauseWatchdog]);

  /** Latch the tree and return the pause control epoch for soft-drain gating. */
  const resolveTaskEndpoints = useCallback((task: TransferTask) => {
    const sourceTab = getTabByConnectionId(task.sourceConnectionId);
    const targetTab = task.targetConnectionId === "local"
      ? null
      : getTabByConnectionId(task.targetConnectionId);
    const sourceByHost = !sourceTab && task.sourceHostId
      ? (["left", "right"] as const).map((side) => {
          const pane = getActivePane(side);
          return pane?.connection?.hostId === task.sourceHostId ? { side, pane } : null;
        }).find(Boolean)
      : null;
    const targetByHost = !targetTab && task.targetHostId
      ? (["left", "right"] as const).map((side) => {
          const pane = getActivePane(side);
          return pane?.connection?.hostId === task.targetHostId ? { side, pane } : null;
        }).find(Boolean)
      : null;
    const localTab = (["left", "right"] as const).map((side) => {
      const pane = getActivePane(side);
      return pane?.connection?.isLocal ? { side, pane } : null;
    }).find(Boolean);

    const source = sourceTab
      ? { side: sourceTab.side, pane: sourceTab.pane }
      : sourceByHost
        ?? (!task.sourceHostId ? localTab : null);
    const target = targetTab
      ? { side: targetTab.side, pane: targetTab.pane }
      : targetByHost
        ?? (!task.targetHostId || task.targetConnectionId === "local" ? localTab : null);

    if (!source?.pane.connection || !target?.pane.connection) {
      return null;
    }

    return {
      sourceSide: source.side,
      targetSide: target.side,
      sourcePane: source.pane,
      targetPane: target.pane,
    };
  }, [getActivePane, getTabByConnectionId]);

  const cleanupTaskArtifacts = useCallback(async (task: TransferTask) => {
    const endpoints = resolveTaskEndpoints(task);
    const targetPane = endpoints?.targetPane;
    const targetSftpId = targetPane?.connection && !targetPane.connection.isLocal
      ? sftpSessionsRef.current.get(targetPane.connection.id)
      : undefined;
    await netcattyBridge.get()?.cleanupTransferArtifacts?.({
      transferId: task.id,
      sourcePath: task.sourcePath,
      targetPath: task.targetPath,
      targetSftpId,
      targetEncoding: targetPane?.filenameEncoding,
      stagedTargetPath: task.stagedTargetPath,
    });
  }, [resolveTaskEndpoints, sftpSessionsRef]);

  const isTransferCancelledError = useCallback(
    (error: unknown): boolean =>
      error instanceof Error && error.message === "Transfer cancelled",
    [],
  );

  const conflictDefaultKey = useCallback(
    (batchId: string | undefined, isDirectory: boolean, existingType?: "file" | "directory" | "symlink") =>
      `${batchId ?? "global"}:${getSftpConflictTypeKey(isDirectory, existingType)}`,
    [],
  );

  const buildReplaceTypeMismatchError = useCallback(
    (isDirectory: boolean, existingType: "file" | "directory" | "symlink" | undefined, targetPath: string) =>
      `Cannot replace existing ${describeSftpExistingKind(existingType)} with ${describeSftpIncomingKind(isDirectory)}: ${targetPath}`,
    [],
  );

  const { completeCancelledTask, cancelBackendTransfers, markBatchStopped } = useSftpTransferTaskOps({
    cancelledTasksRef,
    activeChildIdsRef,
    transfersRef,
    completionHandlersRef,
    setConflicts,
    setTransfers,
    releasePausedTransfer,
    cleanupTaskArtifacts,
  });

  const { statTargetPath, getDuplicateTarget } = useSftpTransferConflictOps();

  const { estimateDirectoryBytes, transferFile, countDirectoryFiles, transferDirectory } = useSftpDirectoryTransferOps({
    ownerId,
    cancelledTasksRef,
    pausedTasksRef,
    waitUntilTransferResumed,
    activeChildIdsRef,
    transfersRef,
    setTransfers,
    listLocalFiles,
    listRemoteFiles,
    acquireTransferSession,
  });

  const processTransfer = async (
    task: TransferTask,
    sourcePane: SftpPane,
    targetPane: SftpPane,
    targetSide: "left" | "right",
  ): Promise<TransferStatus> => {
    if (cancelledTasksRef.current.has(task.id)) {
      return "cancelled";
    }
    // Guard against concurrent processTransfer on the same id (resume used to
    // re-enter while a paused directory walk was still alive). Process-global
    // registry also covers walks after the SFTP panel / terminal tab unmounts.
    if (inFlightTransferIdsRef.current.has(task.id) || transferRuntime.isWalkInFlight(task.id)) {
      logger.warn("[SFTP] processTransfer already in flight; skipping re-entry", task.id);
      return transfersRef.current.find((candidate) => candidate.id === task.id)?.status
        ?? transferRuntime.getTask(task.id)?.status
        ?? "transferring";
    }
    inFlightTransferIdsRef.current.add(task.id);

    // Runtime is the authority writer for live lifecycle. Also mirror into the
    // panel list when mounted (view only — soft control does not depend on it).
    const updateTask = (updates: Partial<TransferTask>) => {
      transferRuntime.patchTask(task.id, updates);
      setTransfers((prev) =>
        prev.map((t) => (t.id === task.id ? { ...t, ...updates } : t)),
      );
    };

    let walkStatus: TransferStatus = "transferring";
    await transferRuntime.runWalk(task.id, async () => {
      walkStatus = await processTransferBody(task, sourcePane, targetPane, targetSide, updateTask);
    });
    inFlightTransferIdsRef.current.delete(task.id);
    return walkStatus;
  };

  const processTransferBody = async (
    task: TransferTask,
    sourcePane: SftpPane,
    targetPane: SftpPane,
    targetSide: "left" | "right",
    updateTask: (updates: Partial<TransferTask>) => void,
  ): Promise<TransferStatus> => {

    // Initialize encoding early to avoid temporal dead zone issues
    const sourceEncoding: SftpFilenameEncoding = sourcePane.connection?.isLocal
      ? "auto"
      : sourcePane.filenameEncoding || "auto";
    const targetEncoding: SftpFilenameEncoding = targetPane.connection?.isLocal
      ? "auto"
      : targetPane.filenameEncoding || "auto";

    // Browse panel sessions may be soft-closed while transfers continue on the
    // dedicated pool. Prefer live browse ids, else open pool sessions for work.
    let sourceSftpId: string | null = sourcePane.connection?.isLocal
      ? null
      : (sftpSessionsRef.current.get(sourcePane.connection!.id) ?? null);
    let targetSftpId: string | null = targetPane.connection?.isLocal
      ? null
      : (sftpSessionsRef.current.get(targetPane.connection!.id) ?? null);

    let sourceWorkLease: TransferConnectionLease | null = null;
    let targetWorkLease: TransferConnectionLease | null = null;

    const releaseWorkLeases = (mode: "release" | "discard" = "release") => {
      if (mode === "discard") {
        sourceWorkLease?.discard();
        targetWorkLease?.discard();
      } else {
        sourceWorkLease?.release();
        targetWorkLease?.release();
      }
      sourceWorkLease = null;
      targetWorkLease = null;
    };

    // Detect same-host: both sides connected to the same remote endpoint.
    // Use per-connection cache keys (hostname+port+protocol+sudo+username) instead of
    // just hostId, because the same hostId can have different session-time overrides.
    const sourceCacheKey = sourcePane.connection?.id
      ? connectionCacheKeyMapRef.current.get(sourcePane.connection.id)
      : undefined;
    const targetCacheKey = targetPane.connection?.id
      ? connectionCacheKeyMapRef.current.get(targetPane.connection.id)
      : undefined;
    // sameHost remote-cp needs a live sftpId; pool-only transfers fall back to
    // recursive file copy via transferDirectory.
    const sameHostEndpoints = !!(
      !sourcePane.connection?.isLocal && !targetPane.connection?.isLocal
      && sourceCacheKey && targetCacheKey
      && sourceCacheKey === targetCacheKey
    );

    try {
    // Prefer dedicated work leases for remote ends so folder walks/listings
    // survive browse park and tab hide (pool holders, not panel map ids).
    if (!sourcePane.connection?.isLocal) {
      const sourceHostId = task.sourceHostId || sourcePane.connection?.hostId;
      if (acquireTransferSession && sourceHostId) {
        sourceWorkLease = await acquireTransferSession(sourceHostId, `${task.id}:work-source`);
        sourceSftpId = sourceWorkLease.sftpId;
      }
      if (!sourceSftpId) {
        const sourceSide = targetSide === "left" ? "right" : "left";
        handleSessionError(sourceSide, new Error("Source SFTP session lost"));
        throw new Error("Source SFTP session not found");
      }
    }

    if (!targetPane.connection?.isLocal) {
      const targetHostId = task.targetHostId || targetPane.connection?.hostId;
      if (acquireTransferSession && targetHostId) {
        targetWorkLease = await acquireTransferSession(targetHostId, `${task.id}:work-target`);
        targetSftpId = targetWorkLease.sftpId;
      }
      if (!targetSftpId) {
        handleSessionError(targetSide, new Error("Target SFTP session lost"));
        throw new Error("Target SFTP session not found");
      }
    }

    const sameHost = sameHostEndpoints && !!sourceSftpId && !!targetSftpId;

    const discoverTransferSize = async () => {
      try {
        if (task.isDirectory) {
          const discoveredSize = await estimateDirectoryBytes(
            task.sourcePath,
            sourceSftpId,
            sourcePane.connection!.isLocal,
            sourceEncoding,
            task.id,
          );
          if (cancelledTasksRef.current.has(task.id)) return;
          updateTask({
            totalBytes: Math.max(discoveredSize, 0),
          });
          return;
        }

        if (task.totalBytes > 0 || !!task.sourceLastModified) return;

        if (sourcePane.connection?.isLocal) {
          const stat = await netcattyBridge.get()?.statLocal?.(task.sourcePath);
          if (stat) {
            if (!task.sourceLastModified && stat.lastModified) {
              task.sourceLastModified = stat.lastModified;
            }
            if (!cancelledTasksRef.current.has(task.id)) {
              updateTask({
                totalBytes: stat.size,
              });
            }
          }
          return;
        }

        if (sourceSftpId) {
          const stat = await netcattyBridge.get()?.statSftp?.(
            sourceSftpId,
            task.sourcePath,
            sourceEncoding,
          );
          if (stat) {
            if (!task.sourceLastModified && stat.lastModified) {
              task.sourceLastModified = stat.lastModified;
            }
            if (!cancelledTasksRef.current.has(task.id)) {
              updateTask({
                totalBytes: stat.size,
              });
            }
          }
        }
      } catch (err) {
        if (!isTransferCancelledError(err)) {
          logger.debug?.("[SFTP] Deferred transfer size discovery failed", err);
        }
      }
    };

    try {
      const t0 = performance.now();
      logger.debug(`[SFTP:perf] processTransfer START — file=${task.fileName} isDir=${task.isDirectory}`);

      // Directory parents use file counts in transferredBytes/totalBytes. Never
      // seed from byte checkpointBytes (pause clears those, which would show 0/N
      // while completed children are skipped without re-counting).
      const directoryCompletedCount = task.isDirectory
        ? transfersRef.current.filter(
          (candidate) => candidate.parentTaskId === task.id && candidate.status === "completed",
        ).length
        : 0;
      updateTask({
        status: "transferring",
        totalBytes: Math.max(task.totalBytes, 0),
        transferredBytes: task.isDirectory
          ? Math.max(task.transferredBytes, directoryCompletedCount)
          : (task.checkpointBytes ?? task.transferredBytes ?? 0),
        startTime: Date.now(),
        phase: "transferring",
        resumable: task.resumable !== false,
        reconnectRequired: false,
        error: undefined,
      });

      // Run size discovery and conflict check in parallel
      const conflictCheckPromise = (async (): Promise<FileConflict | null> => {
        if (task.skipConflictCheck || !targetPane.connection) return null;

        const sourceStat: { size: number; mtime: number } | null =
          (task.totalBytes > 0 || task.sourceLastModified)
            ? { size: task.totalBytes, mtime: task.sourceLastModified || Date.now() }
            : null;

        try {
          const existingStat = await statTargetPath(targetPane, targetSftpId, task.targetPath, targetEncoding);

          if (existingStat) {
            const applyToAllCount = task.batchId
              ? await (async () => {
                  const candidates = transfersRef.current.filter((candidate) =>
                    candidate.batchId === task.batchId &&
                    candidate.isDirectory === task.isDirectory &&
                    !candidate.parentTaskId &&
                    candidate.status !== "completed" &&
                    candidate.status !== "cancelled",
                  );
                  const matches = await Promise.all(candidates.map(async (candidate) => {
                    if (candidate.id === task.id) return true;
                    try {
                      const candidateStat = await statTargetPath(
                        targetPane,
                        targetSftpId,
                        candidate.targetPath,
                        targetEncoding,
                      );
                      return candidateStat?.type === existingStat.type;
                    } catch {
                      return false;
                    }
                  }));
                  return Math.max(1, matches.filter(Boolean).length);
                })()
              : 1;

            return {
              transferId: task.id,
              batchId: task.batchId,
              fileName: task.fileName,
              sourcePath: task.sourcePath,
              targetPath: task.targetPath,
              isDirectory: task.isDirectory,
              existingType: existingStat.type,
              applyToAllCount,
              existingSize: existingStat.size,
              newSize: sourceStat?.size || task.totalBytes || 0,
              existingModified: existingStat.mtime,
              newModified: sourceStat?.mtime || Date.now(),
            };
          }
        } catch {
          // ignore
        }
        return null;
      })();

      // For single files: fire-and-forget size discovery
      if (!task.isDirectory) {
        void discoverTransferSize();
      }

      // Only await conflict check (fast single stat call)
      const conflict = await conflictCheckPromise;
      // Cancel/Stop may have won while conflict stats were in flight.
      if (cancelledTasksRef.current.has(task.id)) return "cancelled";

      if (conflict) {
        const defaultAction = conflictDefaultsRef.current.get(
          conflictDefaultKey(task.batchId, task.isDirectory, conflict.existingType),
        );
        if (defaultAction) {
          if (defaultAction === "stop") {
            await markBatchStopped(task);
            return "cancelled";
          }

          if (defaultAction === "skip") {
            cancelledTasksRef.current.add(task.id);
            updateTask({ status: "cancelled", endTime: Date.now() });
            await completeCancelledTask(task);
            return "cancelled";
          }

          if (defaultAction === "replace" && !canReplaceSftpConflict(task.isDirectory, conflict.existingType)) {
            updateTask({
              status: "failed",
              endTime: Date.now(),
              error: buildReplaceTypeMismatchError(task.isDirectory, conflict.existingType, task.targetPath),
              retryable: false,
            });
            return "failed";
          }

          const duplicateTarget = defaultAction === "duplicate"
            ? await getDuplicateTarget(task, targetPane, targetSftpId, targetEncoding)
            : null;
          if (cancelledTasksRef.current.has(task.id)) return "cancelled";
          const updatedTask: TransferTask = {
            ...task,
            ...(duplicateTarget
              ? {
                  fileName: duplicateTarget.fileName,
                  targetPath: duplicateTarget.targetPath,
                }
              : null),
            skipConflictCheck: true,
            replaceExistingTarget: defaultAction === "replace",
          };
          setTransfers((prev) =>
            prev.map((t) =>
              t.id === task.id
                ? { ...updatedTask, status: "pending" as TransferStatus }
                : t,
            ),
          );
          return processTransfer(updatedTask, sourcePane, targetPane, targetSide);
        }

        if (cancelledTasksRef.current.has(task.id)) return "cancelled";
        setConflicts((prev) => [...prev, conflict]);
        updateTask({
          status: "attention",
          totalBytes: conflict.newSize || task.totalBytes || 0,
          conflict,
        });
        return "attention";
      }

      logger.debug(`[SFTP:perf] starting actual transfer — file=${task.fileName} isDir=${task.isDirectory} — ${(performance.now() - t0).toFixed(0)}ms since start`);
      await waitUntilTransferResumed(task.id);
      if (cancelledTasksRef.current.has(task.id)) return "cancelled";

      let dirPartialFailure = false;

      // Same-host exec-based paths are only safe for UTF-8 compatible encodings.
      // "auto" is allowed here — the backend resolves it to the actual encoding
      // and skips exec if it resolved to non-UTF-8 (e.g. gb18030).
      const encodingSafeForExec =
        (!sourceEncoding || sourceEncoding === "utf-8" || sourceEncoding === "auto") &&
        (!targetEncoding || targetEncoding === "utf-8" || targetEncoding === "auto");

      // Try same-host directory optimization first; falls back to recursive transfer
      // if remote cp is unavailable (e.g. Windows SSH servers).
      let dirHandledBySameHost = false;
      if (task.isDirectory && task.resumable === false && sameHost && encodingSafeForExec && sourceSftpId) {
        if (cancelledTasksRef.current.has(task.id)) {
          throw new Error("Transfer cancelled");
        }
        const result = await netcattyBridge.require().sameHostCopyDirectory!(
          sourceSftpId,
          task.sourcePath,
          task.targetPath,
          sourceEncoding,
          task.id,
        );
        if (cancelledTasksRef.current.has(task.id)) {
          throw new Error("Transfer cancelled");
        }
        dirHandledBySameHost = result.success;
      }

      if (task.isDirectory && !dirHandledBySameHost) {
        // For directory transfers, parent task uses:
        //   totalBytes = total file count (discovered async)
        //   transferredBytes = completed file count (incremented by child completions)
        // Child file tasks are registered in transfers array with their own byte progress.

        // Fire-and-forget: count total files for parent progress display
        void countDirectoryFiles(
          task.sourcePath,
          sourceSftpId,
          sourcePane.connection!.isLocal,
          sourceEncoding,
          task.id,
        ).then((fileCount) => {
          if (!cancelledTasksRef.current.has(task.id)) {
            updateTask({ totalBytes: fileCount });
          }
        }).catch(() => {});

        const stagedTargetPath = task.replaceExistingTarget
          ? `${task.targetPath}.netcatty-${task.id.replace(/[^A-Za-z0-9_-]/g, "_")}.part`
          : undefined;
        const directoryTask = stagedTargetPath ? { ...task, targetPath: stagedTargetPath } : task;
        if (stagedTargetPath) updateTask({ stagedTargetPath });

        const dirErrors = await transferDirectory(
          directoryTask,
          sourceSftpId,
          targetSftpId,
          sourcePane.connection!.isLocal,
          targetPane.connection!.isLocal,
          sourceEncoding,
          targetEncoding,
          task.id, // rootTaskId - this is the top-level task
          sameHost,
        );

        if (dirErrors > 0) {
          dirPartialFailure = true;
        } else if (stagedTargetPath) {
          const bridge = netcattyBridge.require();
          const backupPath = `${task.targetPath}.netcatty-${task.id.replace(/[^A-Za-z0-9_-]/g, "_")}.backup`;
          let backedUp = false;
          try {
            if (targetPane.connection!.isLocal) {
              if (!bridge.renameLocalFile || !bridge.deleteLocalFile) throw new Error("Local directory replacement is unavailable");
              try {
                await bridge.renameLocalFile(task.targetPath, backupPath);
                backedUp = true;
              } catch { /* target may not exist */ }
              await bridge.renameLocalFile(stagedTargetPath, task.targetPath);
              if (backedUp) await bridge.deleteLocalFile(backupPath);
            } else if (targetSftpId) {
              if (!bridge.renameSftp || !bridge.deleteSftp) throw new Error("Remote directory replacement is unavailable");
              try {
                await bridge.renameSftp(targetSftpId, task.targetPath, backupPath, targetEncoding);
                backedUp = true;
              } catch { /* target may not exist */ }
              try {
                await bridge.renameSftp(targetSftpId, stagedTargetPath, task.targetPath, targetEncoding);
              } catch (error) {
                if (backedUp) await bridge.renameSftp(targetSftpId, backupPath, task.targetPath, targetEncoding).catch(() => {});
                throw error;
              }
              if (backedUp) await bridge.deleteSftp(targetSftpId, backupPath, targetEncoding);
            }
            updateTask({ stagedTargetPath: undefined });
          } catch (error) {
            if (backedUp && targetPane.connection!.isLocal) {
              await bridge.renameLocalFile?.(backupPath, task.targetPath).catch(() => {});
            }
            throw error;
          }
        }
      } else if (!task.isDirectory) {
        await transferFile(
          task,
          sourceSftpId,
          targetSftpId,
          sourcePane.connection!.isLocal,
          targetPane.connection!.isLocal,
          sourceEncoding,
          targetEncoding,
          task.id, // rootTaskId - this is the top-level task
          sameHost,
        );
      }

      if (cancelledTasksRef.current.has(task.id)) {
        throw new Error("Transfer cancelled");
      }

      const finalStatus: TransferStatus = dirPartialFailure ? "failed" : "completed";
      setTransfers((prev) => {
        return prev.map((t) => {
          if (t.id !== task.id) return t;
          // Late cancel must not be overwritten by completed/failed.
          if (t.status === "cancelled" || cancelledTasksRef.current.has(task.id)) {
            return {
              ...t,
              status: "cancelled" as TransferStatus,
              error: undefined,
              endTime: Date.now(),
              speed: 0,
            };
          }
          return {
            ...t,
            status: finalStatus,
            error: dirPartialFailure ? "Some files failed to transfer" : undefined,
            // Disable retry for partial failures — retrying replays the entire
            // directory without conflict checks, overwriting already-copied files
            retryable: dirPartialFailure ? false : t.retryable,
            endTime: Date.now(),
            transferredBytes: dirPartialFailure ? t.transferredBytes : t.totalBytes,
            speed: 0,
          };
        });
      });

      // Target contents may have been cached before this transfer started,
      // especially when dropping into a subdirectory like "/tmp" from its parent.
      // Clear the target connection cache so the next navigation reloads fresh data.
      clearCacheForConnection(task.targetConnectionId);

      const targetTab = getTabByConnectionId(task.targetConnectionId);
      if (targetTab) {
        updateTab(targetTab.side, targetTab.tabId, (prev) => ({
          ...prev,
          transferMutationToken: prev.transferMutationToken + 1,
        }));
      }

      // Refresh the specific target tab, not whichever tab happens to be
      // active now — focus may have switched during the transfer.
      if (getParentPath(task.targetPath) === targetPane.connection!.currentPath) {
        await refresh(targetSide, { tabId: targetPane.id });
      }
      // Clean up tracked child IDs for this transfer
      activeChildIdsRef.current.delete(task.id);

      const completionHandler = completionHandlersRef.current.get(task.id);
      if (completionHandler) {
        try {
          await completionHandler({
            id: task.id,
            fileName: task.fileName,
            originalFileName: task.originalFileName ?? task.fileName,
            status: finalStatus,
          });
        } finally {
          completionHandlersRef.current.delete(task.id);
        }
      }
      return finalStatus;
    } catch (err) {
      activeChildIdsRef.current.delete(task.id);
      // Check if this was a cancellation
      const isCancelled = cancelledTasksRef.current.has(task.id) ||
        (err instanceof Error && err.message === "Transfer cancelled");

      if (isCancelled) {
        // Don't update status - cancelTransfer already set it to cancelled
        const completionHandler = completionHandlersRef.current.get(task.id);
        if (completionHandler) {
          try {
            await completionHandler({
              id: task.id,
              fileName: task.fileName,
              originalFileName: task.originalFileName ?? task.fileName,
              status: "cancelled",
            });
          } finally {
            completionHandlersRef.current.delete(task.id);
          }
        }
        clearCancelledTask(task.id);
        return "cancelled";
      }

      updateTask({
        status: "failed",
        error: err instanceof Error ? err.message : "Transfer failed",
        endTime: Date.now(),
        speed: 0,
      });
      const completionHandler = completionHandlersRef.current.get(task.id);
      if (completionHandler) {
        try {
          await completionHandler({
            id: task.id,
            fileName: task.fileName,
            originalFileName: task.originalFileName ?? task.fileName,
            status: "failed",
          });
        } finally {
          completionHandlersRef.current.delete(task.id);
        }
      }
      return "failed";
    }
    } finally {
      // Walk registry cleanup is owned by transferRuntime.runWalk.
      // Drop ephemeral pool holds opened when browse was soft-closed.
      // Per-file transferDirectory leases are released inside transferDirectory.
      releaseWorkLeases();
    }
  };

  const startTransfer = useCallback(
    async (
      sourceFiles: { name: string; isDirectory: boolean }[],
      sourceSide: "left" | "right",
      targetSide: "left" | "right",
      options?: {
        sourcePane?: SftpPane;
        sourcePath?: string;
        sourceConnectionId?: string;
        targetPath?: string;
        onTransferComplete?: (result: TransferResult) => void | Promise<void>;
      },
    ) => {
      const sourcePane = options?.sourcePane
        ?? (options?.sourceConnectionId ? getPaneByConnectionId(options.sourceConnectionId) : null)
        ?? getActivePane(sourceSide);
      const targetPane = getActivePane(targetSide);

      if (!sourcePane?.connection || !targetPane?.connection) return [];

      const sourcePath = options?.sourcePath ?? sourcePane.connection.currentPath;
      const targetPath = options?.targetPath ?? targetPane.connection.currentPath;
      const sourceConnectionId = options?.sourceConnectionId ?? sourcePane.connection.id;
      const batchId = crypto.randomUUID();
      const usesLegacyScp = sourcePane.connection.fileProtocol === "scp" || targetPane.connection.fileProtocol === "scp";

      const newTasks: TransferTask[] = [];
      const skippedResults: TransferResult[] = [];

      const canReusePaneMetadata = sourcePath === sourcePane.connection.currentPath;
      const fileEntryMap = canReusePaneMetadata
        ? new Map(sourcePane.files.map(f => [f.name, f]))
        : null;

      for (const file of sourceFiles) {
        const direction: TransferDirection =
          sourcePane.connection!.isLocal && !targetPane.connection!.isLocal
            ? "upload"
            : !sourcePane.connection!.isLocal && targetPane.connection!.isLocal
              ? "download"
              : "remote-to-remote";

        // Use cached metadata from the source pane's file list to avoid
        // redundant stat calls over the network, but only when the transfer
        // source matches the pane's currently listed directory.
        const fileEntry = fileEntryMap?.get(file.name);
        const fileSize = file.isDirectory ? 0 : (fileEntry?.size ?? 0);
        const sourceLastModified = fileEntry?.lastModified ?? 0;

        const nextSourcePath = joinPath(sourcePath, file.name);
        const nextTargetPath = joinPath(targetPath, file.name);
        const targetIsLocal = targetPane.connection!.isLocal;
        const pathTaken = findActivePathConflict(
          [...sftpTransferCenterStore.getSnapshot().tasks, ...transfersRef.current, ...newTasks],
          {
            id: "",
            targetPath: nextTargetPath,
            // Normalize local pane ids to the same sentinel downloadToLocal uses.
            targetConnectionId: targetIsLocal ? "local" : targetPane.connection!.id,
            targetHostId: targetIsLocal ? undefined : targetPane.connection!.hostId,
            targetHostLabel: targetIsLocal ? "Local" : targetPane.connection!.hostLabel,
            isDirectory: file.isDirectory,
          },
        );
        if (pathTaken) {
          notifyPathConflict(pathTaken, tRef.current);
          const message = pathConflictMessage(pathTaken);
          const attentionTask: TransferTask = {
            id: crypto.randomUUID(),
            batchId,
            fileName: file.name,
            originalFileName: file.name,
            sourcePath: nextSourcePath,
            targetPath: nextTargetPath,
            sourceConnectionId,
            targetConnectionId: targetPane.connection!.id,
            sourceHostId: sourcePane.connection!.isLocal ? undefined : sourcePane.connection!.hostId,
            sourceHostLabel: sourcePane.connection!.isLocal ? "Local" : sourcePane.connection!.hostLabel,
            targetHostId: targetIsLocal ? undefined : targetPane.connection!.hostId,
            targetHostLabel: targetIsLocal ? "Local" : targetPane.connection!.hostLabel,
            direction,
            status: "attention",
            totalBytes: fileSize,
            transferredBytes: 0,
            speed: 0,
            startTime: Date.now(),
            endTime: Date.now(),
            isDirectory: file.isDirectory,
            progressMode: file.isDirectory ? "files" : "bytes",
            sourceLastModified,
            origin: "manual",
            error: message,
            resumable: false,
          };
          newTasks.push(attentionTask);
          const skipped: TransferResult = {
            id: attentionTask.id,
            fileName: attentionTask.fileName,
            originalFileName: attentionTask.originalFileName ?? attentionTask.fileName,
            status: "attention",
          };
          skippedResults.push(skipped);
          if (options?.onTransferComplete) {
            await options.onTransferComplete(skipped);
          }
          continue;
        }
        newTasks.push({
          id: crypto.randomUUID(),
          batchId,
          fileName: file.name,
          originalFileName: file.name,
          sourcePath: nextSourcePath,
          targetPath: nextTargetPath,
          sourceConnectionId,
          targetConnectionId: targetPane.connection!.id,
          sourceHostId: sourcePane.connection!.isLocal ? undefined : sourcePane.connection!.hostId,
          sourceHostLabel: sourcePane.connection!.isLocal ? "Local" : sourcePane.connection!.hostLabel,
          targetHostId: targetPane.connection!.isLocal ? undefined : targetPane.connection!.hostId,
          targetHostLabel: targetPane.connection!.isLocal ? "Local" : targetPane.connection!.hostLabel,
          direction,
          status: "pending" as TransferStatus,
          totalBytes: fileSize,
          transferredBytes: 0,
          speed: 0,
          startTime: Date.now(),
          isDirectory: file.isDirectory,
          progressMode: file.isDirectory ? "files" : "bytes",
          sourceLastModified,
          origin: "manual",
          resumable: !usesLegacyScp,
          pauseUnavailableReason: usesLegacyScp ? "This server uses legacy SCP; cancel and retry from the beginning instead" : undefined,
          phase: file.isDirectory ? "scanning" : "transferring",
        });
      }

      setTransfers((prev) => [...prev, ...newTasks]);

      // Only enqueue runnable tasks — attention path-conflict rows stay visible
      // and must not re-enter processTransfer.
      const runnableTasks = newTasks.filter((task) => task.status !== "attention");

      if (options?.onTransferComplete) {
        for (const task of runnableTasks) {
          completionHandlersRef.current.set(task.id, options.onTransferComplete);
        }
      }

      const results = await Promise.all(runnableTasks.map(async (task): Promise<TransferResult> => {
        const status = await processTransfer(task, sourcePane, targetPane, targetSide);
        return {
          id: task.id,
          fileName: task.fileName,
          originalFileName: task.originalFileName ?? task.fileName,
          status,
        };
      }));

      return [...skippedResults, ...results];
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [getActivePane, getPaneByConnectionId, getTabByConnectionId, sftpSessionsRef, updateTab],
  );

  const cancelTransfer = useCallback(
    async (transferId: string) => {
      const taskToCancel = transfersRef.current.find((task) => task.id === transferId);
      // Add to cancelled set so async operations can check (local + process-global
      // so walks survive panel unmount and still honor center Cancel).
      cancelledTasksRef.current.add(transferId);
      markTransferCancelled(transferId);
      releasePausedTransfer(transferId);
      globalSftpTransferScheduler.cancel(transferId);

      // Cancel parent + remove child tasks
      const childIdsToCancel = new Set<string>();
      const childrenToCleanup: TransferTask[] = [];
      for (const t of transfersRef.current) {
        if (t.parentTaskId === transferId && !["completed", "cancelled", "failed"].includes(t.status)) {
          childIdsToCancel.add(t.id);
          childrenToCleanup.push(t);
        }
      }
      for (const cid of activeChildIdsRef.current.get(transferId) ?? []) {
        if (!childIdsToCancel.has(cid)) {
          childIdsToCancel.add(cid);
          if (taskToCancel) {
            childrenToCleanup.push({
              ...taskToCancel,
              id: cid,
              parentTaskId: transferId,
            });
          }
        }
      }
      for (const cid of childIdsToCancel) {
        cancelledTasksRef.current.add(cid);
        markTransferCancelled(cid);
        globalSftpTransferScheduler.cancel(cid);
      }
      markTransferCancelledTree(transferId, [...childIdsToCancel]);
      // Keep refs in sync immediately so same-turn resolveConflict/resume sees cancel.
      const nextTransfers = transfersRef.current
        .filter((t) => t.parentTaskId !== transferId)
        .map((t) =>
          t.id === transferId
            ? { ...t, status: "cancelled" as TransferStatus, endTime: Date.now(), conflict: undefined }
            : t,
        );
      setTransfers(nextTransfers);
      conflictsRef.current = conflictsRef.current.filter((c) => c.transferId !== transferId && !childIdsToCancel.has(c.transferId));
      setConflicts(conflictsRef.current);

      await cancelBackendTransfers([transferId, ...childIdsToCancel]);
      if (taskToCancel) await cleanupTaskArtifacts(taskToCancel);
      // Child stages are keyed by per-file transferId — clean each known child.
      for (const child of childrenToCleanup) {
        try {
          await cleanupTaskArtifacts(child);
        } catch {
          // best-effort
        }
      }

    },
    [cancelBackendTransfers, cleanupTaskArtifacts, releasePausedTransfer, setTransfers],
  );

  // Soft pause/resume: single TransferRuntime entry (store soft-control +
  // dedicated hard reconnect). No panel-local soft-control dual path.
  const resumeTransfer = useCallback(async (transferId: string) => {
    // Clear sticky child cancel latches so re-walk can retry same child ids.
    clearCancelledTask(transferId);
    for (const child of transfersRef.current) {
      if (child.parentTaskId === transferId) clearCancelledTask(child.id);
    }
    for (const childId of activeChildIdsRef.current.get(transferId) ?? []) {
      clearCancelledTask(childId);
    }
    clearTransferCancelledTree(
      transferId,
      [...(activeChildIdsRef.current.get(transferId) ?? [])],
    );
    await transferRuntime.resume(transferId);
  }, [clearCancelledTask]);

  const prioritizeTransfer = useCallback((transferId: string) => {
    globalSftpTransferScheduler.prioritize(transferId);
    void netcattyBridge.get()?.prioritizeTransfer?.(transferId);
    setTransfers((prev) => {
      const nextPriority = prev.reduce((max, task) => Math.max(max, task.priority ?? 0), 0) + 1;
      return prev.map((task) => task.id === transferId ? { ...task, priority: nextPriority } : task);
    });
  }, [setTransfers]);

  const retryTransfer = useCallback(
    async (transferId: string) => {
      const task = transfersRef.current.find((t) => t.id === transferId);
      if (!task || task.retryable === false) return;
      await cleanupTaskArtifacts(task);

      const retriedTask: TransferTask = {
        ...task,
        id: crypto.randomUUID(),
        status: "pending" as TransferStatus,
        error: undefined,
        transferredBytes: 0,
        checkpointBytes: 0,
        resumeStage: undefined,
        downloadCheckpointBytes: undefined,
        uploadCheckpointBytes: undefined,
        speed: 0,
        startTime: Date.now(),
        endTime: undefined,
        lifecycleEpoch: undefined,
      };

      const endpoints = resolveTaskEndpoints(task);
      if (!endpoints) return;
      const { targetSide, sourcePane, targetPane } = endpoints;

      const completionHandler = completionHandlersRef.current.get(transferId);
      if (completionHandler) {
        completionHandlersRef.current.set(retriedTask.id, completionHandler);
        completionHandlersRef.current.delete(transferId);
      }

      setTransfers((prev) =>
        prev.map((t) =>
          t.id === transferId
            ? retriedTask
            : t,
        ),
      );
      await processTransfer(retriedTask, sourcePane, targetPane, targetSide);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- processTransfer is defined inline
    [cleanupTaskArtifacts, resolveTaskEndpoints, setTransfers],
  );

  const clearCompletedTransfers = useCallback(() => {
    setTransfers((prev) => {
      const unfinishedParents = new Set(
        prev.filter((t) => !["completed", "cancelled", "failed"].includes(t.status)).map((t) => t.id),
      );
      // Keep completed/cancelled children of unfinished directory parents —
      // they are resume checkpoints, not disposable history.
      return prev.filter((t) => {
        if (t.parentTaskId && unfinishedParents.has(t.parentTaskId)) return true;
        return t.status !== "completed" && t.status !== "cancelled";
      });
    });
  }, [setTransfers]);

  const dismissTransfer = useCallback((transferId: string) => {
    const task = transfersRef.current.find((candidate) => candidate.id === transferId);
    if (task) void cleanupTaskArtifacts(task);
    setTransfers((prev) => prev.filter((t) => t.id !== transferId && t.parentTaskId !== transferId));
  }, [cleanupTaskArtifacts, setTransfers]);

  const dismissTransfers = useCallback((transferIds: readonly string[]) => {
    if (transferIds.length === 0) return;
    const removing = new Set(transferIds);
    const artifactTasks = transfersRef.current.filter((task) => (
      task.status !== "completed"
      && (removing.has(task.id) || removing.has(task.parentTaskId ?? ""))
    ));
    // This callback is used only for automatic history eviction. Completed
    // streams already cleaned their staging files; sending one cleanup IPC per
    // pruned child recreates a main-process event storm at folder completion.
    setTransfers((prev) => prev.filter((task) => (
      !removing.has(task.id) && !removing.has(task.parentTaskId ?? "")
    )));
    // Failed/cancelled transfers can retain resumable staging files. Clean them
    // sequentially in the background so a large eviction never floods IPC.
    if (artifactTasks.length > 0) {
      void (async () => {
        for (const task of artifactTasks) {
          try {
            await cleanupTaskArtifacts(task);
          } catch {
            // best-effort history cleanup
          }
        }
      })();
    }
  }, [cleanupTaskArtifacts, setTransfers]);

  const isTransferCancelled = useCallback((transferId: string) => {
    return cancelledTasksRef.current.has(transferId) || isTransferCancelledFlag(transferId);
  }, []);

  const addExternalUpload = useCallback((task: TransferTask) => {
    // Filter out any pending scanning tasks before adding the new task.
    // This ensures that even if dismissExternalUpload's state update hasn't been applied yet
    // (due to React state batching), the scanning placeholder will still be removed.
    if (task.parentTaskId) {
      const childIds = activeChildIdsRef.current.get(task.parentTaskId) ?? new Set<string>();
      childIds.add(task.id);
      activeChildIdsRef.current.set(task.parentTaskId, childIds);
    }
    setTransfers((prev) => [
      ...prev.filter(t => !(t.status === "pending" && t.fileName === "Scanning files...")),
      task
    ]);
  }, [setTransfers]);

  const updateExternalUpload = useCallback((taskId: string, updates: Partial<TransferTask>) => {
    const currentTask = transfersRef.current.find((task) => task.id === taskId);
    if (currentTask?.parentTaskId && updates.status && ["completed", "failed", "cancelled"].includes(updates.status)) {
      activeChildIdsRef.current.get(currentTask.parentTaskId)?.delete(taskId);
    }
    setTransfers((prev) =>
      prev.map((t) => {
        if (
          currentTask?.parentTaskId
          && t.id === currentTask.parentTaskId
          && updates.resumable === false
        ) {
          return {
            ...t,
            resumable: false,
            pauseUnavailableReason: updates.pauseUnavailableReason,
          };
        }
        if (t.id !== taskId) return t;

        const merged: TransferTask = { ...t, ...updates };
        const effectiveStatus = merged.status ?? t.status;
        const isPausedUi = effectiveStatus === "paused" || effectiveStatus === "pausing";

        // Keep progress monotonic and bounded for stable progress UI.
        // While paused, freeze visible transferred/speed so soft-drain tail
        // progress does not make Pause look ignored.
        if (isPausedUi) {
          merged.transferredBytes = t.transferredBytes;
          merged.speed = 0;
          if (Number.isFinite(Number(updates.checkpointBytes))) {
            merged.checkpointBytes = Math.max(
              t.checkpointBytes ?? 0,
              Math.trunc(Number(updates.checkpointBytes)),
            );
          }
        } else if (typeof merged.totalBytes === "number" && merged.totalBytes > 0) {
          merged.transferredBytes = Math.max(
            t.transferredBytes,
            Math.min(merged.transferredBytes, merged.totalBytes),
          );
        } else {
          merged.transferredBytes = Math.max(t.transferredBytes, merged.transferredBytes);
        }

        if (!Number.isFinite(merged.speed) || merged.speed < 0) {
          merged.speed = 0;
        }

        return merged;
      }),
    );
  }, [setTransfers]);

  const resolveConflict = useCallback(
    async (conflictId: string, action: FileConflictAction, applyToAll = false) => {
      if (cancelledTasksRef.current.has(conflictId)) return;
      const conflict = conflictsRef.current.find((c) => c.transferId === conflictId);
      if (!conflict) return;

      const task = transfersRef.current.find((t) => t.id === conflictId);
      if (!task) {
        conflictsRef.current = conflictsRef.current.filter((c) => c.transferId !== conflictId);
        setConflicts(conflictsRef.current);
        return;
      }

      const selectedConflictKey = conflictDefaultKey(conflict.batchId, conflict.isDirectory, conflict.existingType);
      const affectedConflicts = applyToAll
        ? conflictsRef.current.filter((candidate) =>
            conflictDefaultKey(candidate.batchId, candidate.isDirectory, candidate.existingType) === selectedConflictKey,
          )
        : [conflict];
      const affectedConflictIds = new Set(affectedConflicts.map((candidate) => candidate.transferId));
      const affectedTasks = affectedConflicts
        .map((candidate) => transfersRef.current.find((transfer) => transfer.id === candidate.transferId))
        .filter((candidate): candidate is TransferTask => Boolean(candidate));
      const affectedConflictById = new Map<string, FileConflict>(
        affectedConflicts.map((candidate): [string, FileConflict] => [candidate.transferId, candidate]),
      );

      if (applyToAll) {
        conflictDefaultsRef.current.set(selectedConflictKey, action);
      }

      // Eagerly clear refs so a double-click cannot schedule two processTransfer calls.
      conflictsRef.current = conflictsRef.current.filter((c) => !affectedConflictIds.has(c.transferId));
      setConflicts(conflictsRef.current);

      if (affectedTasks.length === 0) {
        return;
      }

      if (action === "stop") {
        await markBatchStopped(task);
        return;
      }

      if (action === "skip") {
        for (const affectedTask of affectedTasks) {
          cancelledTasksRef.current.add(affectedTask.id);
        }
        setTransfers((prev) =>
          prev.map((t) => affectedConflictIds.has(t.id)
              ? { ...t, status: "cancelled" as TransferStatus, endTime: Date.now() }
              : t,
          ),
        );
        for (const affectedTask of affectedTasks) {
          await completeCancelledTask(affectedTask);
        }
        return;
      }

      const updatedTasks: TransferTask[] = [];
      const blockedReplaceTasks: Array<{ task: TransferTask; conflict: FileConflict }> = [];

      for (const affectedTask of affectedTasks) {
        if (cancelledTasksRef.current.has(affectedTask.id)) continue;
        let updatedTask = { ...affectedTask };
        const affectedConflict = affectedConflictById.get(affectedTask.id);

        if (action === "duplicate") {
          const endpoints = resolveTaskEndpoints(affectedTask);
          if (!endpoints) continue;
          const targetSftpId = endpoints.targetPane.connection?.isLocal
            ? null
            : sftpSessionsRef.current.get(endpoints.targetPane.connection!.id) ?? null;
          const targetEncoding = endpoints.targetPane.connection?.isLocal
            ? "auto"
            : endpoints.targetPane.filenameEncoding || "auto";
          const duplicateTarget = await getDuplicateTarget(affectedTask, endpoints.targetPane, targetSftpId, targetEncoding);
          if (cancelledTasksRef.current.has(affectedTask.id)) continue;
          updatedTask = {
            ...affectedTask,
            fileName: duplicateTarget.fileName,
            targetPath: duplicateTarget.targetPath,
            skipConflictCheck: true,
          };
        } else if (action === "replace") {
          if (
            affectedConflict &&
            !canReplaceSftpConflict(affectedTask.isDirectory, affectedConflict.existingType)
          ) {
            blockedReplaceTasks.push({ task: affectedTask, conflict: affectedConflict });
            continue;
          }
          updatedTask = {
            ...affectedTask,
            skipConflictCheck: true,
            replaceExistingTarget: true,
          };
        } else if (action === "merge") {
          updatedTask = {
            ...affectedTask,
            skipConflictCheck: true,
            replaceExistingTarget: false,
          };
        }

        updatedTasks.push(updatedTask);
      }

      if (blockedReplaceTasks.length > 0) {
        const blockedTaskIds = new Set(blockedReplaceTasks.map(({ task }) => task.id));
        const blockedErrors = new Map(
          blockedReplaceTasks.map(({ task, conflict }) => [
            task.id,
            buildReplaceTypeMismatchError(task.isDirectory, conflict.existingType, task.targetPath),
          ]),
        );
        setTransfers((prev) =>
          prev.map((t) => blockedTaskIds.has(t.id)
            ? {
                ...t,
                status: "failed" as TransferStatus,
                endTime: Date.now(),
                error: blockedErrors.get(t.id),
                retryable: false,
                conflict: undefined,
              }
            : t,
          ),
        );
      }

      const liveUpdatedTasks = updatedTasks.filter((candidate) => !cancelledTasksRef.current.has(candidate.id));
      const updatedTaskMap = new Map(liveUpdatedTasks.map((updatedTask) => [updatedTask.id, updatedTask]));
      setTransfers((prev) =>
        prev.map((t) => {
          if (t.status === "cancelled" || cancelledTasksRef.current.has(t.id)) return t;
          const updatedTask = updatedTaskMap.get(t.id);
          return updatedTask
            ? { ...updatedTask, status: "pending" as TransferStatus, conflict: undefined }
            : t;
        }),
      );

      for (const updatedTask of liveUpdatedTasks) {
        setTimeout(async () => {
          if (cancelledTasksRef.current.has(updatedTask.id)) return;
          const endpoints = resolveTaskEndpoints(updatedTask);
          if (!endpoints) return;
          await processTransfer(updatedTask, endpoints.sourcePane, endpoints.targetPane, endpoints.targetSide);
        }, 100);
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- processTransfer is defined inline; transfers/conflicts accessed via refs
    [
      completeCancelledTask,
      conflictDefaultKey,
      getDuplicateTarget,
      markBatchStopped,
      resolveTaskEndpoints,
      sftpSessionsRef,
    ],
  );

  // Only work that is still moving or queued — paused rows must not keep the
  // header "N active" lit or pause feels like it did nothing.
  const activeTransfersCount = useMemo(() => transfers.filter(
    (t) => !t.parentTaskId
      && ["pending", "queued", "transferring", "pausing"].includes(t.status),
  ).length, [transfers]);

  const downloadToLocal = useCallback(
    async (params: {
      fileName: string;
      sourcePath: string;
      targetPath: string;
      sftpId: string;
      connectionId: string;
      sourceHostId: string;
      sourceHostLabel: string;
      sourceEncoding?: SftpFilenameEncoding;
      isDirectory: boolean;
      totalBytes?: number;
    }): Promise<TransferStatus> => {
      // Same destination concurrent writers corrupt .part / final files.
      // Must pass the local endpoint identity — createDirectDownloadTransferTask
      // stores targetConnectionId:"local"; a bare { targetPath } never matches
      // isLocalTransferDestination and silently skips dedupe (double download).
      const downloadDest = {
        targetPath: params.targetPath,
        targetConnectionId: "local" as const,
        targetHostLabel: "Local",
        isDirectory: params.isDirectory,
      };
      const conflict = findActivePathConflict(
        [...sftpTransferCenterStore.getSnapshot().tasks, ...transfersRef.current],
        { id: "", ...downloadDest },
      );
      if (conflict) {
        notifyPathConflict(conflict, tRef.current);
        return conflict.status === "completed" ? "completed" : "attention";
      }

      const task = createDirectDownloadTransferTask({
        id: crypto.randomUUID(),
        fileName: params.fileName,
        sourcePath: params.sourcePath,
        targetPath: params.targetPath,
        sourceConnectionId: params.connectionId,
        sourceHostId: params.sourceHostId,
        sourceHostLabel: params.sourceHostLabel,
        totalBytes: params.totalBytes ?? 0,
        isDirectory: params.isDirectory,
      });

      // Reserve the path in transfersRef before any await so concurrent
      // downloadToLocal calls (double-click) see each other.
      setTransfers((prev) => {
        const raced = findActivePathConflict(
          [...sftpTransferCenterStore.getSnapshot().tasks, ...prev],
          { id: task.id, ...downloadDest },
        );
        if (raced) return prev;
        return [...prev, task];
      });
      if (!transfersRef.current.some((row) => row.id === task.id)) {
        notifyPathConflict(
          findActivePathConflict(
            [...sftpTransferCenterStore.getSnapshot().tasks, ...transfersRef.current],
            { id: "", ...downloadDest },
          ) ?? task,
          tRef.current,
        );
        return "attention";
      }

      const sourceEncoding = params.sourceEncoding ?? "auto";
      // Mutable counter to track child failures outside React state,
      // so the final status check doesn't depend on render timing.
      let childFailureCount = 0;

      // Dedicated pool only when host id is known — never fall back to browse
      // (tab close would kill the download and freeze the global center).
      let sourceWorkLease: TransferConnectionLease | null = null;
      let workingSourceSftpId = params.sftpId;
      if (acquireTransferSession && params.sourceHostId) {
        sourceWorkLease = await acquireTransferSession(
          params.sourceHostId,
          `${task.id}:work-source`,
        );
        workingSourceSftpId = sourceWorkLease.sftpId;
      }

      try {
        if (params.isDirectory) {
          // Count files for progress display
          void countDirectoryFiles(
            params.sourcePath,
            workingSourceSftpId,
            false,
            sourceEncoding,
            task.id,
            0,     // symlinkDepth
            true,  // followSymlinks
          ).then((fileCount) => {
            if (!cancelledTasksRef.current.has(task.id)) {
              setTransfers((prev) =>
                prev.map((t) => (t.id === task.id ? { ...t, totalBytes: fileCount } : t)),
              );
            }
          }).catch(() => {});

          childFailureCount = await transferDirectory(
            task,
            workingSourceSftpId,
            null,       // targetSftpId = null (local)
            false,       // sourceIsLocal = false
            true,        // targetIsLocal = true
            sourceEncoding,
            "auto",      // targetEncoding
            task.id,
            false,       // sameHost
            0,           // symlinkDepth
            true,        // followSymlinks — download should expand symlink dirs
          );
        } else {
          await transferFile(
            task,
            workingSourceSftpId,
            null,
            false,
            true,
            sourceEncoding,
            "auto",
            task.id,
          );
        }

        // Use childFailureCount (tracked outside React state) to determine
        // final status reliably, regardless of render timing.
        // Cancel must win: transferDirectory counts cancelled children as errors,
        // but cancelTransfer already marked the parent cancelled — do not demote
        // it to failed with "Some files failed to transfer".
        // Re-read cancel inside the state update so a cancel that lands after
        // transferDirectory returns cannot be overwritten by completed/failed.
        let appliedStatus: TransferStatus = "completed";
        setTransfers((prev) => {
          const completedCount = prev.filter(
            (t) => t.parentTaskId === task.id && t.status === "completed",
          ).length;
          return prev.map((t) => {
            if (t.id !== task.id) return t;
            const parentCancelled = t.status === "cancelled"
              || cancelledTasksRef.current.has(task.id);
            const resolved = resolveDirectDirectoryDownloadFinalStatus({
              parentCancelled,
              childFailureCount,
            });
            appliedStatus = resolved.status;
            if (resolved.status === "cancelled") {
              cancelledTasksRef.current.delete(task.id);
              return {
                ...t,
                status: "cancelled" as TransferStatus,
                error: undefined,
                endTime: Date.now(),
                // Keep partial progress — do not look 100% complete when cancelled.
                speed: 0,
              };
            }
            const finalTotal = t.totalBytes > 0 ? t.totalBytes : completedCount;
            const hasFailures = resolved.status === "failed";
            return {
              ...t,
              status: resolved.status,
              error: resolved.error,
              endTime: Date.now(),
              totalBytes: finalTotal,
              transferredBytes: hasFailures ? completedCount : finalTotal,
              speed: 0,
            };
          });
        });
        activeChildIdsRef.current.delete(task.id);
        return appliedStatus;
      } catch (err) {
        activeChildIdsRef.current.delete(task.id);
        const isCancelled = cancelledTasksRef.current.has(task.id);
        // Clean up cancelled task tracking to prevent memory leak
        if (isCancelled) cancelledTasksRef.current.delete(task.id);
        const errMsg = err instanceof Error ? err.message : String(err);
        setTransfers((prev) =>
          prev.map((t) =>
            t.id === task.id
              ? {
                  ...t,
                  status: isCancelled ? ("cancelled" as TransferStatus) : ("failed" as TransferStatus),
                  error: isCancelled ? undefined : errMsg,
                  endTime: Date.now(),
                }
              : t,
          ),
        );
        return isCancelled ? "cancelled" : "failed";
      } finally {
        sourceWorkLease?.release();
        sourceWorkLease = null;
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sftpSessionsRef, acquireTransferSession],
  );

  useEffect(() => {
    sftpTransferCenterStore.publishOwner(ownerId, transfers);
  }, [ownerId, transfers]);

  // Drop local rows the global store reassigned to dedicated-resume (or another
  // owner) so the panel cannot start a second stream with the same transferId.
  useEffect(() => {
    return sftpTransferCenterStore.subscribe(() => {
      const foreignIds = new Set(
        sftpTransferCenterStore.getSnapshot().tasks
          .filter((task) => task.ownerId !== ownerId)
          .map((task) => task.id),
      );
      if (foreignIds.size === 0) return;
      setTransfers((prev) => {
        const next = prev.filter((task) => !foreignIds.has(task.id));
        return next.length === prev.length ? prev : next;
      });
    });
  }, [ownerId, setTransfers]);

  const resolveAdoptionPanes = useCallback((task: TransferTask) => {
    const panes = [getActivePane("left"), getActivePane("right")].filter((pane): pane is SftpPane => !!pane?.connection);
    const sourcePane = panes.find((pane) => task.sourceHostId
      ? pane.connection?.hostId === task.sourceHostId
      : pane.connection?.isLocal);
    const targetPane = panes.find((pane) => task.targetHostId
      ? pane.connection?.hostId === task.targetHostId
      : pane.connection?.isLocal);
    // Downloads to local only need the remote source; local pane is optional for
    // matching but preferred so processTransfer can run dual-endpoint checks.
    if (sourcePane && targetPane) return { sourcePane, targetPane };
    if (sourcePane && task.sourceHostId && !task.targetHostId) {
      const localPane = panes.find((pane) => pane.connection?.isLocal);
      if (localPane) return { sourcePane, targetPane: localPane };
    }
    if (targetPane && task.targetHostId && !task.sourceHostId) {
      const localPane = panes.find((pane) => pane.connection?.isLocal);
      if (localPane) return { sourcePane: localPane, targetPane };
    }
    return null;
  }, [getActivePane]);

  const adoptInterruptedTransfer = useCallback(async (task: TransferTask) => {
    const panes = resolveAdoptionPanes(task);
    if (!panes?.sourcePane.connection || !panes.targetPane.connection) return;
    // Keep checkpoint / fingerprint fields so resume restarts from the saved byte offset.
    const hasConflict = !!task.conflict;
    const isDirectoryResume = !!task.isDirectory && !hasConflict;
    const adoptedTask: TransferTask = {
      ...task,
      ownerId,
      sourceConnectionId: panes.sourcePane.connection.id,
      targetConnectionId: panes.targetPane.connection.isLocal
        ? (task.targetConnectionId === "local" ? "local" : panes.targetPane.connection.id)
        : panes.targetPane.connection.id,
      // Conflict rows stay in attention until resolveConflict applies the action.
      // Other reconnects stay "pending" + reconnectRequired for the spinner.
      // Directory resume: skip re-prompting conflict when the target already
      // exists from the first attempt (merge/continue semantics).
      status: hasConflict ? "attention" : "pending",
      reconnectRequired: !hasConflict,
      error: hasConflict ? task.error : undefined,
      speed: 0,
      // Preserve replace/staging flags so resume keeps the original conflict
      // choice; only force skipConflict when continuing a directory resume.
      skipConflictCheck: isDirectoryResume ? true : task.skipConflictCheck,
      replaceExistingTarget: isDirectoryResume
        ? !!task.replaceExistingTarget
        : task.replaceExistingTarget,
      stagedTargetPath: task.stagedTargetPath,
    };
    // Re-home orphaned children under this parent so directory resume can skip
    // already-completed files using persisted checkpoints (same ownerId).
    const storeChildren = sftpTransferCenterStore.getSnapshot().tasks.filter(
      (candidate) => candidate.parentTaskId === task.id,
    );
    const rehomedChildren = storeChildren.map((child) => ({
      ...child,
      ownerId,
      status: (child.status === "completed" || child.status === "cancelled" || child.status === "failed")
        ? child.status
        : "interrupted" as TransferStatus,
      reconnectRequired: child.status !== "completed" && child.status !== "cancelled" && child.status !== "failed",
    }));
    const nextTransfers = [
      ...transfersRef.current.filter(
        (candidate) => candidate.id !== task.id && candidate.parentTaskId !== task.id,
      ),
      adoptedTask,
      ...rehomedChildren,
    ];
    setTransfers(nextTransfers);
    if (task.conflict) {
      // Keep conflictsRef in sync immediately — store.resolveConflict may call
      // controller.resolveConflict in the same turn before React re-renders.
      const nextConflicts = [
        ...conflictsRef.current.filter((item) => item.transferId !== task.id),
        task.conflict,
      ];
      conflictsRef.current = nextConflicts;
      setConflicts(nextConflicts);
      // Do not auto-resume — caller will apply the chosen conflict action.
      return;
    }
    // Cancel may have won while we were rehoming ownership.
    if (cancelledTasksRef.current.has(task.id)) return;
    // Force resumeTransfer to accept pending (reconnect path) statuses.
    await resumeTransfer(task.id);
  }, [ownerId, resolveAdoptionPanes, resumeTransfer, setTransfers]);

  const syncOwnedTasksFromStore = useCallback(() => {
    const snap = sftpTransferCenterStore.getSnapshot().tasks;
    setTransfers((prev) => {
      let changed = false;
      const next = prev.map((row) => {
        const storeRow = snap.find((candidate) => candidate.id === row.id);
        if (!storeRow) return row;
        if (
          storeRow.status === row.status
          && storeRow.speed === row.speed
          && (storeRow.checkpointBytes ?? 0) === (row.checkpointBytes ?? 0)
          && (storeRow.transferredBytes ?? 0) === (row.transferredBytes ?? 0)
          && storeRow.pauseUnavailableReason === row.pauseUnavailableReason
          && storeRow.reconnectRequired === row.reconnectRequired
          && storeRow.error === row.error
          && storeRow.lifecycleEpoch === row.lifecycleEpoch
        ) {
          return row;
        }
        changed = true;
        return {
          ...row,
          status: storeRow.status,
          speed: storeRow.speed,
          checkpointBytes: storeRow.checkpointBytes,
          transferredBytes: storeRow.transferredBytes,
          pauseUnavailableReason: storeRow.pauseUnavailableReason,
          reconnectRequired: storeRow.reconnectRequired,
          error: storeRow.error,
          phase: storeRow.phase,
          resumeStage: storeRow.resumeStage,
          downloadCheckpointBytes: storeRow.downloadCheckpointBytes,
          uploadCheckpointBytes: storeRow.uploadCheckpointBytes,
          sourceFingerprint: storeRow.sourceFingerprint,
          lifecycleEpoch: storeRow.lifecycleEpoch,
        };
      });
      return changed ? next : prev;
    });
  }, [setTransfers]);

  useEffect(() => sftpTransferCenterStore.registerOwner(ownerId, {
    // Soft pause/resume: TransferRuntime (process-global). Owner registration is
    // view/sync + cancel/retry/adopt only — not control authority for live walks.
    pause: async (taskId: string) => {
      await transferRuntime.pause(taskId);
    },
    resume: async (taskId: string) => {
      // Use resumeTransfer so cancel latches are cleared before soft/hard resume.
      await resumeTransfer(taskId);
    },
    cancel: cancelTransfer,
    retry: retryTransfer,
    prioritize: prioritizeTransfer,
    dismiss: dismissTransfer,
    dismissMany: dismissTransfers,
    resolveConflict,
    // Origin/local listing only — soft pause/resume must not key on this.
    ownsTask: (taskId: string) => transfersRef.current.some(
      (row) => row.id === taskId || row.parentTaskId === taskId,
    ),
    syncOwnedTasks: syncOwnedTasksFromStore,
    canAdopt: (task) => resolveAdoptionPanes(task) !== null,
    canPrepareAdoption,
    adopt: adoptInterruptedTransfer,
  }), [adoptInterruptedTransfer, canPrepareAdoption, cancelTransfer, dismissTransfer, dismissTransfers, ownerId, prioritizeTransfer, resolveAdoptionPanes, resolveConflict, resumeTransfer, retryTransfer, syncOwnedTasksFromStore]);

  return {
    transfers,
    conflicts,
    activeTransfersCount,
    startTransfer,
    downloadToLocal,
    addExternalUpload,
    updateExternalUpload,
    cancelTransfer,
    // Single process-level control surface (TransferRuntime).
    pauseTransfer: async (transferId: string) => {
      await transferRuntime.pause(transferId);
    },
    resumeTransfer,
    prioritizeTransfer,
    isTransferCancelled,
    retryTransfer,
    clearCompletedTransfers,
    dismissTransfer,
    resolveConflict,
  };
};
