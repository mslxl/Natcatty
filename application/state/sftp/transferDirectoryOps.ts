import { useCallback, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import type { Host, SftpFileEntry, SftpFilenameEncoding, TransferStatus, TransferTask } from "../../../domain/models";
import { STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY } from "../../../infrastructure/config/storageKeys";
import { localStorageAdapter } from "../../../infrastructure/persistence/localStorageAdapter";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { logger } from "../../../lib/logger";
import { runSftpTransferWorkers } from "./transferConcurrency";
import { getSftpTransferResourceKeys, globalSftpTransferScheduler } from "./globalTransferScheduler";
import { resolveDedicatedStreamEndpointIds } from "../../../domain/sftpDedicatedStreamPolicy";
import { isSessionError } from "./errors";
import { isTransferCancelledError, runWithTransferRetry } from "./transferRetry";
import type { TransferConnectionLease } from "./transferConnectionPool";
import {
  hasNewSourceFingerprint,
  resolveDurableCheckpointBytes,
  shouldApplyTransferProgress,
} from "./transferProgressMetadata";
import {
  isTransferOrRootPauseLatched,
  isTransferPauseLatched,
  waitWhileTransferOrRootPaused,
} from "./transferPauseLatch";
import {
  getTransferControlEpoch,
  isTransferControlEpochCurrent,
} from "./transferControlEpoch";
import { isTransferOrRootCancelled } from "./transferCancelLatch";
import { joinPath } from "./utils";

function isCancelledLocalOrGlobal(
  cancelledTasksRef: { current: Set<string> },
  rootTaskId: string,
  taskId?: string,
): boolean {
  if (cancelledTasksRef.current.has(rootTaskId) || isTransferOrRootCancelled(rootTaskId, taskId)) {
    return true;
  }
  if (taskId && (cancelledTasksRef.current.has(taskId) || isTransferOrRootCancelled(taskId))) {
    return true;
  }
  return false;
}

export type AcquireTransferSessionFn = (
  hostId: string,
  transferId: string,
  /** Connect-time host (session hostname/port/user overrides). Prefer over vault. */
  connectHost?: Host,
) => Promise<TransferConnectionLease>;

interface UseSftpDirectoryTransferOpsParams {
  ownerId: string;
  cancelledTasksRef: MutableRefObject<Set<string>>;
  pausedTasksRef: MutableRefObject<Set<string>>;
  waitUntilTransferResumed: (taskId: string) => Promise<void>;
  activeChildIdsRef: MutableRefObject<Map<string, Set<string>>>;
  transfersRef: MutableRefObject<TransferTask[]>;
  setTransfers: Dispatch<SetStateAction<TransferTask[]>>;
  listLocalFiles: (path: string) => Promise<SftpFileEntry[]>;
  listRemoteFiles: (sftpId: string, path: string, encoding?: SftpFilenameEncoding) => Promise<SftpFileEntry[]>;
  /** FileZilla-style dedicated transfer connections (optional). */
  acquireTransferSession?: AcquireTransferSessionFn;
}

export function useSftpDirectoryTransferOps({
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
}: UseSftpDirectoryTransferOpsParams) {
  const getEntrySize = useCallback((entry: SftpFileEntry): number => {
    if (typeof entry.size === "string") {
      const parsed = parseInt(entry.size, 10);
      return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
    }
    return typeof entry.size === "number" && entry.size > 0 ? entry.size : 0;
  }, []);

  // Prefer process-global latches so the transfer center can pause/resume after
  // the React owner unmounts (tab close). Mirror the panel ref for same-owner races.
  const isPauseLatched = useCallback((rootTaskId: string, taskId?: string) => (
    isTransferOrRootPauseLatched(rootTaskId, taskId)
    || pausedTasksRef.current.has(rootTaskId)
    || (!!taskId && pausedTasksRef.current.has(taskId))
  ), [pausedTasksRef]);

  /**
   * Block until the folder (or single-file) pause latch is cleared.
   * Must be called before opening any new stream — otherwise a worker that
   * already passed a pre-check can start the next file after the user pauses.
   */
  const waitWhileTransferPaused = useCallback(async (rootTaskId: string, taskId?: string) => {
    // Global wait first (tab-close / center pause), then cancel checks.
    while (isPauseLatched(rootTaskId, taskId)) {
      await waitWhileTransferOrRootPaused(rootTaskId, taskId);
      // Also drain legacy local-ref-only latches if any remain out of sync.
      if (
        pausedTasksRef.current.has(rootTaskId)
        || (!!taskId && pausedTasksRef.current.has(taskId))
      ) {
        const latchId = pausedTasksRef.current.has(rootTaskId)
          ? rootTaskId
          : (taskId as string);
        await waitUntilTransferResumed(latchId);
      }
      if (isCancelledLocalOrGlobal(cancelledTasksRef, rootTaskId, taskId)) {
        throw new Error("Transfer cancelled");
      }
    }
  }, [cancelledTasksRef, isPauseLatched, pausedTasksRef, waitUntilTransferResumed]);

  /** Wait out the latch, then mark transferring only if still unlatched (no race). */
  const markTransferringIfNotPaused = useCallback(async (
    rootTaskId: string,
    taskId: string,
  ): Promise<boolean> => {
    for (;;) {
      await waitWhileTransferPaused(rootTaskId, taskId);
      if (isPauseLatched(rootTaskId, taskId)) continue;
      // Sync claim: if pause wins between the check and this update, the updater
      // re-reads the latch and keeps the row paused.
      let claimed = false;
      setTransfers((prev) => {
        if (isPauseLatched(rootTaskId, taskId)) {
          claimed = false;
          return prev.map((candidate) => (
            candidate.id === taskId
              && !["completed", "cancelled", "failed"].includes(candidate.status)
              ? { ...candidate, status: "paused" as TransferStatus, speed: 0 }
              : candidate
          ));
        }
        claimed = true;
        return prev.map((candidate) => (
          candidate.id === taskId
            ? {
                ...candidate,
                status: "transferring" as TransferStatus,
                pauseUnavailableReason: undefined,
                reconnectRequired: false,
              }
            : candidate
        ));
      });
      // Re-check after scheduling state — pause may have latched in the same tick.
      if (isPauseLatched(rootTaskId, taskId)) {
        setTransfers((prev) => prev.map((candidate) => (
          candidate.id === taskId
            && !["completed", "cancelled", "failed"].includes(candidate.status)
            ? { ...candidate, status: "paused" as TransferStatus, speed: 0 }
            : candidate
        )));
        continue;
      }
      return claimed;
    }
  }, [isPauseLatched, setTransfers, waitWhileTransferPaused]);

  const MAX_SYMLINK_DEPTH = 32;

  const estimateDirectoryBytes = useCallback(
    async (
      sourcePath: string,
      sourceSftpId: string | null,
      sourceIsLocal: boolean,
      sourceEncoding: SftpFilenameEncoding,
      rootTaskId: string,
      symlinkDepth = 0,
      followSymlinks = false,
    ): Promise<number> => {
      const estT0 = performance.now();
      if (cancelledTasksRef.current.has(rootTaskId)) {
        throw new Error("Transfer cancelled");
      }

      const files = sourceIsLocal
        ? await listLocalFiles(sourcePath)
        : sourceSftpId
          ? await listRemoteFiles(sourceSftpId, sourcePath, sourceEncoding)
          : null;

      if (!files) {
        throw new Error("No source connection");
      }

      let totalBytes = 0;
      const subdirs: { entry: SftpFileEntry; nextDepth: number }[] = [];

      for (const file of files) {
        if (file.name === ".." || file.name === ".") continue;

        if (file.type === "directory") {
          subdirs.push({ entry: file, nextDepth: symlinkDepth });
        } else if (followSymlinks && file.type === "symlink" && file.linkTarget === "directory") {
          if (symlinkDepth < MAX_SYMLINK_DEPTH) {
            subdirs.push({ entry: file, nextDepth: symlinkDepth + 1 });
          }
          // Skip at max depth — consistent with transferDirectory
        } else {
          totalBytes += getEntrySize(file);
        }
      }

      if (subdirs.length > 0) {
        if (cancelledTasksRef.current.has(rootTaskId)) {
          throw new Error("Transfer cancelled");
        }

        const subResults = await Promise.all(
          subdirs.map(({ entry: subdir, nextDepth }) =>
            estimateDirectoryBytes(
              joinPath(sourcePath, subdir.name),
              sourceSftpId,
              sourceIsLocal,
              sourceEncoding,
              rootTaskId,
              nextDepth,
              followSymlinks,
            ),
          ),
        );
        totalBytes += subResults.reduce((sum, size) => sum + size, 0);
      }

      logger.debug(`[SFTP:perf] estimateDirectoryBytes ${sourcePath} = ${totalBytes} — ${(performance.now() - estT0).toFixed(0)}ms`);
      return totalBytes;
    },
    [cancelledTasksRef, getEntrySize, listLocalFiles, listRemoteFiles],
  );

  const transferFile = async (
    task: TransferTask,
    sourceSftpId: string | null,
    targetSftpId: string | null,
    sourceIsLocal: boolean,
    targetIsLocal: boolean,
    sourceEncoding: SftpFilenameEncoding,
    targetEncoding: SftpFilenameEncoding,
    rootTaskId: string, // The original top-level task ID for cancellation checking
    sameHost?: boolean,
    onStreamProgress?: (transferred: number, total: number, speed: number) => void,
  ): Promise<void> => {
    // Check if task or root task was cancelled before starting
    if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
      throw new Error("Transfer cancelled");
    }
    // Do not admit a new scheduler job while the folder is paused — otherwise
    // soft-resume only unparks streams and the next file still starts under pause.
    await waitWhileTransferPaused(rootTaskId, task.id);

    setTransfers((prev) => prev.map((candidate) => candidate.id === task.id
      ? { ...candidate, status: "queued" as TransferStatus }
      : candidate));
    return globalSftpTransferScheduler.run(
      ownerId,
      task.id,
      getSftpTransferResourceKeys({
        sourceHostId: task.sourceHostId,
        targetHostId: task.targetHostId,
        sourceSftpId: sourceSftpId ?? undefined,
        targetSftpId: targetSftpId ?? undefined,
      }),
      () => localStorageAdapter.readNumber(STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY),
      async () => {
        // Cancel may have won while this job was only queued on the scheduler.
        if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
          throw new Error("Transfer cancelled");
        }
        // Job may have been queued before pause — block here before any I/O.
        // Must not paint "transferring" if pause wins the race after wait returns.
        await markTransferringIfNotPaused(rootTaskId, task.id);

        // FileZilla-style: prefer dedicated transfer sessions so the browse
        // panel connection is not blocked by bulk file I/O.
        let sourceLease: TransferConnectionLease | null = null;
        let targetLease: TransferConnectionLease | null = null;

        const acquireLeases = async () => {
          if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
            throw new Error("Transfer cancelled");
          }
          await waitWhileTransferPaused(rootTaskId, task.id);
          if (isPauseLatched(rootTaskId, task.id)) {
            await markTransferringIfNotPaused(rootTaskId, task.id);
          }
          // Dedicated pool only for remote ends — panel/browse sessions die when
          // the SFTP tab is closed, which freezes global transfer center rows.
          if (acquireTransferSession && !sourceIsLocal && task.sourceHostId) {
            sourceLease = await acquireTransferSession(task.sourceHostId, task.id);
          }
          if (acquireTransferSession && !targetIsLocal && task.targetHostId) {
            targetLease = await acquireTransferSession(task.targetHostId, task.id);
          }
        };

        const releaseLeases = (mode: "release" | "discard" = "release") => {
          if (mode === "discard") {
            sourceLease?.discard();
            targetLease?.discard();
          } else {
            sourceLease?.release();
            targetLease?.release();
          }
          sourceLease = null;
          targetLease = null;
        };

        let lastError: unknown = null;
        try {
          await acquireLeases();

          // One automatic retry for transient session/network blips (WinSCP-style).
          await runWithTransferRetry(async (attempt) => {
            if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
              throw new Error("Transfer cancelled");
            }
            // Final gate before startStreamTransfer — covers pause between lease
            // open and stream arming. Re-claim transferring only when unlatched.
            await markTransferringIfNotPaused(rootTaskId, task.id);
            // On retry after a dead pool session, open fresh dedicated connections.
            if (attempt > 0) {
              releaseLeases("discard");
              await acquireLeases();
              await markTransferringIfNotPaused(rootTaskId, task.id);
            }
            // Last sync check — if pause latched, do not open a new stream.
            if (isPauseLatched(rootTaskId, task.id)) {
              await markTransferringIfNotPaused(rootTaskId, task.id);
            }

            const resolved = resolveDedicatedStreamEndpointIds({
              sourceIsLocal,
              targetIsLocal,
              sourceHostId: task.sourceHostId,
              targetHostId: task.targetHostId,
              sourcePoolSftpId: sourceLease?.sftpId,
              targetPoolSftpId: targetLease?.sftpId,
              panelSourceSftpId: sourceSftpId,
              panelTargetSftpId: targetSftpId,
              poolAvailable: !!acquireTransferSession,
            });
            if (resolved.error) {
              throw new Error(resolved.error);
            }
            const effectiveSourceSftpId = resolved.sourceSftpId;
            const effectiveTargetSftpId = resolved.targetSftpId;

            // On retry, prefer latest checkpoint so we do not restart from zero.
            const latest = transfersRef.current.find((candidate) => candidate.id === task.id) ?? task;
            const options = {
              transferId: task.id,
              sourcePath: task.sourcePath,
              targetPath: task.targetPath,
              sourceType: sourceIsLocal ? ("local" as const) : ("sftp" as const),
              targetType: targetIsLocal ? ("local" as const) : ("sftp" as const),
              sourceSftpId: effectiveSourceSftpId || undefined,
              targetSftpId: effectiveTargetSftpId || undefined,
              sourceHostId: task.sourceHostId,
              targetHostId: task.targetHostId,
              totalBytes: task.totalBytes || undefined,
              sourceEncoding: sourceIsLocal ? undefined : sourceEncoding,
              targetEncoding: targetIsLocal ? undefined : targetEncoding,
              sameHost: sameHost || undefined,
              resumable: task.resumable !== false,
              checkpointBytes: latest.checkpointBytes ?? latest.transferredBytes ?? task.checkpointBytes,
              resumeStage: latest.resumeStage ?? task.resumeStage,
              downloadCheckpointBytes: latest.downloadCheckpointBytes ?? task.downloadCheckpointBytes,
              uploadCheckpointBytes: latest.uploadCheckpointBytes ?? task.uploadCheckpointBytes,
              sourceFingerprint: latest.sourceFingerprint ?? task.sourceFingerprint,
              // Renderer already admitted this file via globalSftpTransferScheduler
              // (unlimited host slots). Folder concurrency is only in runSftpTransferWorkers.
              skipAdmission: true,
            };

            let lastProgressUpdate = 0;
            let lastSourceFingerprint = options.sourceFingerprint;
            const onProgress = (
              transferred: number,
              total: number,
              speed: number,
              checkpoint?: {
                phase?: TransferTask["phase"];
                resumeStage?: TransferTask["resumeStage"];
                checkpointBytes?: number;
                downloadCheckpointBytes?: number;
                uploadCheckpointBytes?: number;
                sourceFingerprint?: string;
                resumable?: boolean;
                pauseUnavailableReason?: string;
              },
            ) => {
              onStreamProgress?.(transferred, total, speed);
              const now = Date.now();
              const sourceFingerprintChanged = hasNewSourceFingerprint(
                lastSourceFingerprint,
                checkpoint?.sourceFingerprint,
              );
              // Always apply progress while the folder is latched so we can
              // force status back to paused even when the throttle would skip.
              const parentLatched = !!task.parentTaskId
                && (isTransferPauseLatched(task.parentTaskId) || pausedTasksRef.current.has(task.parentTaskId));
              const selfLatched = isTransferOrRootPauseLatched(rootTaskId, task.id)
                || pausedTasksRef.current.has(task.id)
                || pausedTasksRef.current.has(rootTaskId);
              const forcePaused = parentLatched || selfLatched;
              if (!forcePaused && !shouldApplyTransferProgress({
                elapsedMs: now - lastProgressUpdate,
                transferred,
                total,
                currentSourceFingerprint: lastSourceFingerprint,
                incomingSourceFingerprint: checkpoint?.sourceFingerprint,
              })) return;
              lastProgressUpdate = now;
              if (sourceFingerprintChanged) lastSourceFingerprint = checkpoint?.sourceFingerprint;
              setTransfers((prev) =>
                prev.map((t) => {
                  if (t.id !== task.id) return t;
                  if (t.status === "cancelled") return t;
                  // Re-read latch inside updater — pause may have just won.
                  const latchedNow = isTransferOrRootPauseLatched(rootTaskId, t.id)
                    || pausedTasksRef.current.has(rootTaskId)
                    || (!!t.parentTaskId && (
                      isTransferPauseLatched(t.parentTaskId)
                      || pausedTasksRef.current.has(t.parentTaskId)
                    ))
                    || pausedTasksRef.current.has(t.id);
                  // Latch-primary only. Sticky local status "paused"/"pausing" after
                  // intentional Resume must not re-freeze the bar (soft-drain race).
                  const pauseRequested = latchedNow;
                  const normalizedTotal = total > 0 ? total : t.totalBytes;
                  const normalizedTransferred = Math.max(
                    t.transferredBytes,
                    Math.min(transferred, normalizedTotal > 0 ? normalizedTotal : transferred),
                  );
                  // Prefer bridge contiguous checkpoint — soft-drain can report
                  // high-water transferred past sparse holes. Keep updating the
                  // durable checkpoint under the hood, but freeze the visible
                  // bar as soon as pause is requested (soft-drain still writes).
                  const durableCheckpoint = resolveDurableCheckpointBytes({
                    transferred: normalizedTransferred,
                    previousCheckpoint: t.checkpointBytes,
                    incomingCheckpoint: checkpoint?.checkpointBytes,
                    status: pauseRequested ? "paused" : t.status,
                  });
                  return {
                    ...t,
                    // Latched folder pause always wins over mid-flight "transferring"
                    // paints from scheduler re-entry races.
                    status: pauseRequested
                      && !["completed", "cancelled", "failed"].includes(t.status)
                      ? (t.status === "pausing" ? "pausing" as TransferStatus : "paused" as TransferStatus)
                      : t.status,
                    transferredBytes: pauseRequested ? t.transferredBytes : normalizedTransferred,
                    checkpointBytes: durableCheckpoint,
                    resumeStage: checkpoint?.resumeStage ?? t.resumeStage,
                    downloadCheckpointBytes: checkpoint?.downloadCheckpointBytes ?? t.downloadCheckpointBytes,
                    uploadCheckpointBytes: checkpoint?.uploadCheckpointBytes ?? t.uploadCheckpointBytes,
                    sourceFingerprint: checkpoint?.sourceFingerprint ?? t.sourceFingerprint,
                    phase: checkpoint?.phase ?? t.phase,
                    // Keep pause affordance while paused even if a progress event
                    // briefly reports pauseSupported=false mid soft-drain.
                    resumable: pauseRequested ? true : (checkpoint?.resumable ?? t.resumable),
                    pauseUnavailableReason: pauseRequested
                      ? undefined
                      : (checkpoint && "pauseUnavailableReason" in checkpoint
                        ? checkpoint.pauseUnavailableReason
                        : t.pauseUnavailableReason),
                    totalBytes: normalizedTotal,
                    speed: pauseRequested ? 0 : (Number.isFinite(speed) && speed > 0 ? speed : 0),
                  };
                }),
              );
            };

            try {
              // Loop: never open a stream while the folder is latched. Soft-drain
              // completing a sibling used to free a worker that then started the
              // next file under a "paused" parent (51.7KB green → new yellow).
              for (;;) {
                if (isPauseLatched(rootTaskId, task.id)) {
                  await waitWhileTransferPaused(rootTaskId, task.id);
                  await markTransferringIfNotPaused(rootTaskId, task.id);
                  continue;
                }
                // Await the invoke result — cancel resolves with { error } and may
                // not fire onComplete/onError after preload clears listeners.
                const transferPromise = netcattyBridge.require().startStreamTransfer!(
                  options,
                  onProgress,
                  undefined,
                  undefined,
                );
                // Streams that arm after the parent pause round never receive the
                // initial pauseTransfer. Keep pausing while the folder is latched.
                // Capture epoch per attempt so Resume (epoch bump) undoes a late pause.
                let watchPaused = true;
                const pauseWatch = (async () => {
                  while (
                    watchPaused
                    && (
                      isTransferOrRootPauseLatched(rootTaskId, task.id)
                      || pausedTasksRef.current.has(rootTaskId)
                      || pausedTasksRef.current.has(task.id)
                    )
                  ) {
                    const epochAtAttempt = getTransferControlEpoch(rootTaskId);
                    try {
                      const result = await netcattyBridge.get()?.pauseTransfer?.(task.id);
                      // Resume won while we were awaiting pause — undo.
                      if (
                        result?.success
                        && !isTransferControlEpochCurrent(rootTaskId, epochAtAttempt)
                      ) {
                        try {
                          await netcattyBridge.get()?.resumeTransfer?.(task.id);
                        } catch { /* best-effort */ }
                        break;
                      }
                    } catch { /* best-effort */ }
                    await new Promise((resolve) => setTimeout(resolve, 80));
                  }
                })();
                let result: { error?: string; cancelled?: boolean } | undefined;
                try {
                  result = await transferPromise;
                } finally {
                  watchPaused = false;
                  await pauseWatch.catch(() => {});
                }
                if (result?.error || result?.cancelled) {
                  throw new Error(result.error || "Transfer cancelled");
                }
                // Soft-drain can complete this file while folder is still latched.
                // Park before the worker loop claims another index.
                if (isPauseLatched(rootTaskId, task.id)) {
                  await waitWhileTransferPaused(rootTaskId, task.id);
                }
                break;
              }
            } catch (error) {
              lastError = error;
              throw error;
            }
            lastError = null;
            if (attempt > 0) {
              logger.info(`[SFTP] Transfer ${task.fileName} succeeded after retry #${attempt}`);
            }
          }, {
            retries: 1,
            delayMs: 500,
            onRetry: (err, attempt) => {
              logger.warn(
                `[SFTP] Transient failure for ${task.fileName}; retrying (${attempt})`,
                err instanceof Error ? err.message : err,
              );
              setTransfers((prev) => prev.map((candidate) => candidate.id === task.id
                ? {
                    ...candidate,
                    status: "queued" as TransferStatus,
                    error: undefined,
                    speed: 0,
                  }
                : candidate));
            },
          });
        } finally {
          // Final session death must discard, not release, or the next file
          // reuses a corpse pool connection for up to the idle TTL.
          releaseLeases(isSessionError(lastError) ? "discard" : "release");
        }
      },
    );
  };

  /** Recursively count all files under a directory (for progress display). */
  const countDirectoryFiles = async (
    sourcePath: string,
    sourceSftpId: string | null,
    sourceIsLocal: boolean,
    sourceEncoding: SftpFilenameEncoding,
    rootTaskId: string,
    symlinkDepth = 0,
    followSymlinks = false,
  ): Promise<number> => {
    if (cancelledTasksRef.current.has(rootTaskId)) return 0;

    const files = sourceIsLocal
      ? await listLocalFiles(sourcePath)
      : sourceSftpId
        ? await listRemoteFiles(sourceSftpId, sourcePath, sourceEncoding)
        : null;
    if (!files) return 0;

    let count = 0;
    const subdirPromises: Promise<number>[] = [];
    for (const file of files) {
      if (file.name === ".." || file.name === ".") continue;
      if (file.type === "directory") {
        subdirPromises.push(
          countDirectoryFiles(joinPath(sourcePath, file.name), sourceSftpId, sourceIsLocal, sourceEncoding, rootTaskId, symlinkDepth, followSymlinks),
        );
      } else if (followSymlinks && file.type === "symlink" && file.linkTarget === "directory") {
        // Only recurse if within depth limit; skip entirely at max depth
        // (consistent with transferDirectory which also skips these)
        if (symlinkDepth < MAX_SYMLINK_DEPTH) {
          subdirPromises.push(
            countDirectoryFiles(joinPath(sourcePath, file.name), sourceSftpId, sourceIsLocal, sourceEncoding, rootTaskId, symlinkDepth + 1, followSymlinks),
          );
        }
      } else {
        count++;
      }
    }
    if (subdirPromises.length > 0) {
      const subCounts = await Promise.all(subdirPromises);
      count += subCounts.reduce((a, b) => a + b, 0);
    }
    return count;
  };

  /** Returns number of failed child file transfers */
  const transferDirectory = async (
    task: TransferTask,
    sourceSftpId: string | null,
    targetSftpId: string | null,
    sourceIsLocal: boolean,
    targetIsLocal: boolean,
    sourceEncoding: SftpFilenameEncoding,
    targetEncoding: SftpFilenameEncoding,
    rootTaskId: string, // The original top-level task ID for cancellation checking
    sameHost?: boolean,
    symlinkDepth = 0,
    followSymlinks = false, // Only true for downloadToLocal — uploads/copies treat symlinks as files
  ) => {
    // Check if task or root task was cancelled before starting
    if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
      throw new Error("Transfer cancelled");
    }

    let totalErrors = 0;

    if (targetIsLocal) {
      try {
        await netcattyBridge.get()?.mkdirLocal?.(task.targetPath);
      } catch (mkdirErr: unknown) {
        const isEEXIST = mkdirErr instanceof Error && mkdirErr.message.includes("EEXIST");
        if (!isEEXIST) throw mkdirErr;
        // EEXIST: verify the existing path is actually a directory, not a file
        const stat = await netcattyBridge.get()?.statLocal?.(task.targetPath);
        if (stat && stat.type !== 'directory') {
          throw new Error(`Target path exists as a file: ${task.targetPath}`);
        }
      }
    } else if (targetSftpId) {
      await netcattyBridge.get()?.mkdirSftp(targetSftpId, task.targetPath, targetEncoding);
    }

    let files: SftpFileEntry[];
    if (sourceIsLocal) {
      files = await listLocalFiles(task.sourcePath);
    } else if (sourceSftpId) {
      files = await listRemoteFiles(sourceSftpId, task.sourcePath, sourceEncoding);
    } else {
      throw new Error("No source connection");
    }

    // Filter both "." and ".." — some SFTP servers include "." in readdir
    const filtered = files.filter((f) => f.name !== ".." && f.name !== ".");
    // Separate directories from files.
    // Symlink directories are only followed when followSymlinks is true
    // (downloadToLocal). Uploads/copies treat symlinks as regular entries
    // to preserve existing behavior and avoid expanding symlinked trees.
    const dirs: SftpFileEntry[] = [];
    const regularFiles: SftpFileEntry[] = [];
    for (const f of filtered) {
      if (f.type === "directory") {
        dirs.push(f);
      } else if (followSymlinks && f.type === "symlink" && f.linkTarget === "directory") {
        if (symlinkDepth < MAX_SYMLINK_DEPTH) {
          dirs.push(f);
        } else {
          // Count as an error so the parent task is marked failed
          totalErrors++;
          logger.warn(`[SFTP] Skipping symlink directory at max depth: ${joinPath(task.sourcePath, f.name)}`);
        }
      } else {
        regularFiles.push(f);
      }
    }

    // Process subdirectories sequentially to avoid unbounded concurrent SFTP
    // requests from nested Promise.all + worker pools across the tree.
    // File-level concurrency within each directory is still governed by the
    // shared SFTP transfer worker scheduler below.
    for (const dir of dirs) {
      if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
        throw new Error("Transfer cancelled");
      }
      // Pause between subfolders — otherwise the walk enters the next tree while
      // the user thinks the whole folder transfer is paused.
      await waitWhileTransferPaused(rootTaskId);

      const childTask: TransferTask = {
        ...task,
        id: crypto.randomUUID(),
        fileName: dir.name,
        originalFileName: dir.name,
        sourcePath: joinPath(task.sourcePath, dir.name),
        targetPath: joinPath(task.targetPath, dir.name),
        isDirectory: true,
        progressMode: "files",
        parentTaskId: task.id,
      };

      const isSymlink = dir.type === "symlink";
      const subdirErrors = await transferDirectory(
        childTask,
        sourceSftpId,
        targetSftpId,
        sourceIsLocal,
        targetIsLocal,
        sourceEncoding,
        targetEncoding,
        rootTaskId,
        sameHost,
        isSymlink ? symlinkDepth + 1 : symlinkDepth,
        followSymlinks,
      );
      totalErrors += subdirErrors;
    }

    // Transfer files in parallel with concurrency limit
    if (regularFiles.length > 0) {
      const errors: Error[] = [];
      // If the SFTP session dies mid-directory, stop queueing more files
      // (remaining workers will still finish their current item).
      let sessionLostError: Error | null = null;

      await runSftpTransferWorkers(
        regularFiles,
        () => localStorageAdapter.readNumber(STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY),
        async (file) => {
          if (sessionLostError) throw sessionLostError;
          if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
            throw new Error("Transfer cancelled");
          }

          const fileSize = getEntrySize(file);
          const sourcePath = joinPath(task.sourcePath, file.name);
          const targetPath = joinPath(task.targetPath, file.name);
          const persistedChild = transfersRef.current.find((candidate) => (
            candidate.parentTaskId === rootTaskId
            && candidate.sourcePath === sourcePath
            && candidate.targetPath === targetPath
          ));
          // Skip completed children without re-transferring, but ensure parent
          // file-count already includes them (seeded at processTransfer start).
          if (persistedChild?.status === "completed") return;
          // Re-check after metadata — pause can land during path join/lookup.
          // beforeClaim already waited, but soft-drain of the previous file can
          // finish between claim and here; refuse to register a new child while
          // latched.
          await waitWhileTransferPaused(rootTaskId);
          if (cancelledTasksRef.current.has(task.id) || cancelledTasksRef.current.has(rootTaskId)) {
            throw new Error("Transfer cancelled");
          }
          if (isPauseLatched(rootTaskId)) {
            await waitWhileTransferPaused(rootTaskId);
          }
          const fileId = persistedChild?.id ?? crypto.randomUUID();

          // Track child ID outside React state for immediate cancellation visibility
          if (!activeChildIdsRef.current.has(rootTaskId)) {
            activeChildIdsRef.current.set(rootTaskId, new Set());
          }
          activeChildIdsRef.current.get(rootTaskId)!.add(fileId);

          const childTask: TransferTask = {
            ...task,
            ...persistedChild,
            id: fileId,
            fileName: file.name,
            originalFileName: file.name,
            sourcePath,
            targetPath,
            isDirectory: false,
            progressMode: "bytes",
            parentTaskId: rootTaskId,
            totalBytes: fileSize,
            // Inherit retryable from parent — downloadToLocal sets retryable: false
            // because "local" targetConnectionId can't be resolved by retryTransfer
            retryable: task.retryable,
            // New/restarted child streams arm at bridge lifecycleEpoch 0. Never
            // inherit the parent's soft-resume epoch or progress is stale-dropped.
            lifecycleEpoch: undefined,
            phase: undefined,
            pauseUnavailableReason: undefined,
          };

          // Register child in transfers array so UI can render it
          setTransfers((prev) => persistedChild
            ? prev.map((candidate) => candidate.id === fileId ? {
                ...childTask,
                status: "queued" as TransferStatus,
                speed: 0,
                error: undefined,
                endTime: undefined,
                lifecycleEpoch: undefined,
              } : candidate)
            : [...prev, {
                ...childTask,
                status: "queued" as TransferStatus,
                transferredBytes: 0,
                speed: 0,
                startTime: Date.now(),
                lifecycleEpoch: undefined,
              }]);

          try {
            await transferFile(
              childTask,
              sourceSftpId,
              targetSftpId,
              sourceIsLocal,
              targetIsLocal,
              sourceEncoding,
              targetEncoding,
              rootTaskId,
              sameHost,
            );

            activeChildIdsRef.current.get(rootTaskId)?.delete(fileId);
            // Mark child as completed & update parent file count.
            // Soft-drain may finish a child after the parent was paused — still
            // mark the child completed (resume skips it), but freeze the parent
            // file-count bar while pausing/paused so the UI does not twitch.
            setTransfers((prev) => {
              const parentRow = prev.find((row) => row.id === rootTaskId);
              const parentFrozen = !!parentRow && (
                parentRow.status === "paused"
                || parentRow.status === "pausing"
                || isPauseLatched(rootTaskId)
              );
              const updated = prev.map((t) => {
                if (t.id === fileId) {
                  return { ...t, status: "completed" as TransferStatus, endTime: Date.now(), transferredBytes: t.totalBytes };
                }
                if (t.id === rootTaskId) {
                  if (parentFrozen) {
                    return { ...t, speed: 0 };
                  }
                  return {
                    ...t,
                    transferredBytes: t.transferredBytes + 1,
                    speed: t.speed,
                  };
                }
                return t;
              });
              return updated;
            });
            // Soft-drain can complete the current file while the folder is still
            // latched. Park this worker before the queue claims another index.
            await waitWhileTransferPaused(rootTaskId);
          } catch (err) {
            activeChildIdsRef.current.get(rootTaskId)?.delete(fileId);
            const message = err instanceof Error ? err.message : String(err);
            if (isTransferCancelledError(err)) {
              // Keep cancelled status; do not rethrow — other workers must finish
              // and the parent should not become a clean completed tree.
              setTransfers((prev) =>
                prev.map((t) =>
                  t.id === fileId
                    ? { ...t, status: "cancelled" as TransferStatus, error: undefined, endTime: Date.now() }
                    : t,
                ),
              );
              errors.push(err instanceof Error ? err : new Error(message));
              return;
            }
            // Mark child as failed
            setTransfers((prev) =>
              prev.map((t) =>
                t.id === fileId
                  ? { ...t, status: "failed" as TransferStatus, error: message }
                  : t,
              ),
            );
            if (isSessionError(err) && !sessionLostError) {
              sessionLostError = err instanceof Error ? err : new Error(message);
              // Fail remaining queued siblings quickly with a clear cause.
              setTransfers((prev) => prev.map((t) => (
                t.parentTaskId === rootTaskId
                && (t.status === "queued" || t.status === "pending")
                  ? {
                      ...t,
                      status: "failed" as TransferStatus,
                      error: "SFTP session lost — reconnect and resume remaining files",
                      speed: 0,
                      endTime: Date.now(),
                    }
                  : t
              )));
            }
            errors.push(err instanceof Error ? err : new Error(message));
            if (sessionLostError) throw sessionLostError;
            // Stay parked after a failed attempt if the folder is paused so we
            // do not immediately claim the next file under a latched parent.
            if (isPauseLatched(rootTaskId)) {
              await waitWhileTransferPaused(rootTaskId);
            }
          }
        },
        {
          beforeClaim: async () => {
            if (sessionLostError) return;
            await waitWhileTransferPaused(rootTaskId);
          },
        },
      ).catch((err) => {
        if (sessionLostError || isTransferCancelledError(err)) {
          // Expected control-flow throws after cancel / session loss.
          return;
        }
        throw err;
      });

      totalErrors += errors.length;
      if (sessionLostError) {
        logger.warn("[SFTP] Directory transfer stopped early: session lost", sessionLostError.message);
      } else if (errors.length > 0) {
        logger.debug?.("[SFTP] Some files in directory transfer failed", errors);
      }
    }

    return totalErrors;
  };


  return { estimateDirectoryBytes, transferFile, countDirectoryFiles, transferDirectory };
}
