import type { Dispatch, MutableRefObject, SetStateAction } from "react";
import type {
  SftpFilenameEncoding,
  TransferStatus,
  TransferTask,
} from "../../../domain/models";
import { createDirectoryEntryIdentity } from "../../../domain/sftpDirectoryCheckpoint";
import { isUnchangedTransferCandidate } from "../../../domain/sftpTransferSkip";
import { STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY } from "../../../infrastructure/config/storageKeys";
import { localStorageAdapter } from "../../../infrastructure/persistence/localStorageAdapter";
import { logger } from "../../../lib/logger";
import { isSessionError } from "./errors";
import type { DiscoveredTransferFile } from "./transferDirectoryDiscovery";
import { runSftpTransferWorkers } from "./transferConcurrency";
import { isTransferCancelledError } from "./transferRetry";

type TransferOneFile = (
  task: TransferTask,
  sourceSftpId: string | null,
  targetSftpId: string | null,
  sourceIsLocal: boolean,
  targetIsLocal: boolean,
  sourceEncoding: SftpFilenameEncoding,
  targetEncoding: SftpFilenameEncoding,
  rootTaskId: string,
  sameHost?: boolean,
) => Promise<void>;

export type TransferDiscoveredFilesParams = {
  rootTask: TransferTask;
  files: DiscoveredTransferFile[];
  sourceSftpId: string | null;
  targetSftpId: string | null;
  sourceIsLocal: boolean;
  targetIsLocal: boolean;
  sourceEncoding: SftpFilenameEncoding;
  targetEncoding: SftpFilenameEncoding;
  rootTaskId: string;
  sameHost?: boolean;
  skipUnchanged: boolean;
  startEntryIndex: number;
  cancelledTasksRef: MutableRefObject<Set<string>>;
  activeChildIdsRef: MutableRefObject<Map<string, Set<string>>>;
  transfersRef: MutableRefObject<TransferTask[]>;
  setTransfers: Dispatch<SetStateAction<TransferTask[]>>;
  waitWhileTransferPaused: (rootTaskId: string, taskId?: string) => Promise<void>;
  isPauseLatched: (rootTaskId: string, taskId?: string) => boolean;
  transferFile: TransferOneFile;
  tryStatTarget: (
    targetPath: string,
  ) => Promise<{ size: number; lastModified: number; type: string } | null>;
  /** Fresh source identity immediately before skip-unchanged (Codex P1). */
  tryStatSource: (
    sourcePath: string,
  ) => Promise<{ size: number; lastModified: number; type: string } | null>;
};

/**
 * Transfer a flat pre-scanned file list with the same child-task bookkeeping as
 * the interleaved directory walk.
 */
export async function transferDiscoveredFiles(
  params: TransferDiscoveredFilesParams,
): Promise<number> {
  const {
    rootTask,
    files,
    sourceSftpId,
    targetSftpId,
    sourceIsLocal,
    targetIsLocal,
    sourceEncoding,
    targetEncoding,
    rootTaskId,
    sameHost,
    skipUnchanged,
    startEntryIndex,
    cancelledTasksRef,
    activeChildIdsRef,
    transfersRef,
    setTransfers,
    waitWhileTransferPaused,
    isPauseLatched,
    transferFile,
    tryStatTarget,
    tryStatSource,
  } = params;

  if (files.length === 0) return 0;

  setTransfers((prev) => prev.map((candidate) => candidate.id === rootTaskId
    ? { ...candidate, phase: "transferring" }
    : candidate));

  const errors: Error[] = [];
  let sessionLostError: Error | null = null;

  await runSftpTransferWorkers(
    files,
    () => localStorageAdapter.readNumber(STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY),
    async (file, fileIndex) => {
      if (sessionLostError) throw sessionLostError;
      if (cancelledTasksRef.current.has(rootTask.id) || cancelledTasksRef.current.has(rootTaskId)) {
        throw new Error("Transfer cancelled");
      }

      const fileSize = file.size;
      const sourcePath = file.sourcePath;
      const targetPath = file.targetPath;
      let skipSourceSize = fileSize;
      let skipSourceLastModified = file.lastModified;
      const directoryEntryIndex = startEntryIndex + fileIndex;
      let directoryEntryIdentity = createDirectoryEntryIdentity({
        sourcePath,
        targetPath,
        size: fileSize,
        lastModified: file.lastModified,
      });
      const persistedChild = transfersRef.current.find((candidate) => (
        candidate.parentTaskId === rootTaskId
        && candidate.sourcePath === sourcePath
        && candidate.targetPath === targetPath
      ));
      if (persistedChild?.status === "completed") return;

      await waitWhileTransferPaused(rootTaskId);
      if (cancelledTasksRef.current.has(rootTask.id) || cancelledTasksRef.current.has(rootTaskId)) {
        throw new Error("Transfer cancelled");
      }
      if (isPauseLatched(rootTaskId)) {
        await waitWhileTransferPaused(rootTaskId);
      }

      // Symlink listing attrs are the link node; transfer follows target bytes.
      // Re-stat regular files so skip and transfer both use current size/mtime
      // (Codex P1: pre-scan listing attrs can go stale before transfer starts).
      let freshSourceOk = false;
      if (!file.isSymlink) {
        const freshSource = await tryStatSource(sourcePath);
        if (freshSource && freshSource.type !== "directory") {
          skipSourceSize = freshSource.size;
          skipSourceLastModified = freshSource.lastModified;
          directoryEntryIdentity = createDirectoryEntryIdentity({
            sourcePath,
            targetPath,
            size: skipSourceSize,
            lastModified: skipSourceLastModified,
          });
          freshSourceOk = true;
        } else {
          // Do not pass stale pre-scan size as an explicit snapshot (Codex P2).
          // Child totalBytes 0 is omitted at startStreamTransfer via `|| undefined`,
          // so the dedicated transfer session re-stats the source itself.
          skipSourceSize = 0;
          skipSourceLastModified = 0;
        }
      }
      if (skipUnchanged && !file.isSymlink && freshSourceOk) {
        const existing = await tryStatTarget(targetPath);
        if (
          existing
          && existing.type !== "directory"
          && isUnchangedTransferCandidate(
            { size: skipSourceSize, lastModified: skipSourceLastModified, mtimeUnit: "ms" },
            { size: existing.size, lastModified: existing.lastModified, mtimeUnit: "ms" },
          )
        ) {
          const skippedId = persistedChild?.id ?? crypto.randomUUID();
          setTransfers((prev) => {
            const hasChild = prev.some((row) => row.id === skippedId);
            const next = hasChild
              ? prev.map((row) => row.id === skippedId
                ? {
                    ...row,
                    status: "completed" as TransferStatus,
                    transferredBytes: skipSourceSize,
                    totalBytes: skipSourceSize,
                    endTime: Date.now(),
                    speed: 0,
                    error: undefined,
                  }
                : row)
              : [...prev, {
                  ...rootTask,
                  id: skippedId,
                  fileName: file.name,
                  originalFileName: file.name,
                  sourcePath,
                  targetPath,
                  isDirectory: false,
                  progressMode: "bytes" as const,
                  parentTaskId: rootTaskId,
                  totalBytes: skipSourceSize,
                  transferredBytes: skipSourceSize,
                  sourceLastModified: skipSourceLastModified,
                  directoryEntryIndex,
                  directoryEntryIdentity,
                  status: "completed" as TransferStatus,
                  speed: 0,
                  startTime: Date.now(),
                  endTime: Date.now(),
                }];
            return next.map((row) => {
              if (row.id !== rootTaskId) return row;
              if (row.status === "paused" || row.status === "pausing" || isPauseLatched(rootTaskId)) {
                return { ...row, speed: 0 };
              }
              return { ...row, transferredBytes: row.transferredBytes + 1 };
            });
          });
          return;
        }
      }

      const fileId = persistedChild?.id ?? crypto.randomUUID();
      if (!activeChildIdsRef.current.has(rootTaskId)) {
        activeChildIdsRef.current.set(rootTaskId, new Set());
      }
      activeChildIdsRef.current.get(rootTaskId)!.add(fileId);

      const childTask: TransferTask = {
        ...rootTask,
        ...persistedChild,
        id: fileId,
        fileName: file.name,
        originalFileName: file.name,
        sourcePath,
        targetPath,
        isDirectory: false,
        progressMode: "bytes",
        parentTaskId: rootTaskId,
        totalBytes: skipSourceSize,
        sourceLastModified: skipSourceLastModified || undefined,
        directoryEntryIndex,
        directoryEntryIdentity,
        retryable: rootTask.retryable,
        lifecycleEpoch: undefined,
        phase: undefined,
        pauseUnavailableReason: undefined,
      };

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
        setTransfers((prev) => {
          const parentRow = prev.find((row) => row.id === rootTaskId);
          const parentFrozen = !!parentRow && (
            parentRow.status === "paused"
            || parentRow.status === "pausing"
            || isPauseLatched(rootTaskId)
          );
          return prev.map((t) => {
            if (t.id === fileId) {
              return {
                ...t,
                status: "completed" as TransferStatus,
                endTime: Date.now(),
                transferredBytes: t.totalBytes,
              };
            }
            if (t.id === rootTaskId) {
              if (parentFrozen) return { ...t, speed: 0 };
              return { ...t, transferredBytes: t.transferredBytes + 1, speed: t.speed };
            }
            return t;
          });
        });
        await waitWhileTransferPaused(rootTaskId);
      } catch (err) {
        activeChildIdsRef.current.get(rootTaskId)?.delete(fileId);
        const message = err instanceof Error ? err.message : String(err);
        if (isTransferCancelledError(err)) {
          setTransfers((prev) => prev.map((t) => (
            t.id === fileId
              ? { ...t, status: "cancelled" as TransferStatus, error: undefined, endTime: Date.now() }
              : t
          )));
          errors.push(err instanceof Error ? err : new Error(message));
          return;
        }
        setTransfers((prev) => prev.map((t) => (
          t.id === fileId
            ? { ...t, status: "failed" as TransferStatus, error: message }
            : t
        )));
        if (isSessionError(err) && !sessionLostError) {
          sessionLostError = err instanceof Error ? err : new Error(message);
          setTransfers((prev) => prev.map((t) => (
            t.parentTaskId === rootTaskId
            && (t.status === "queued" || t.status === "pending")
              ? {
                  ...t,
                  status: "failed" as TransferStatus,
                  error: "SFTP session lost - reconnect and resume remaining files",
                  speed: 0,
                  endTime: Date.now(),
                }
              : t
          )));
        }
        errors.push(err instanceof Error ? err : new Error(message));
        if (sessionLostError) throw sessionLostError;
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
    if (sessionLostError || isTransferCancelledError(err)) return;
    throw err;
  });

  if (sessionLostError) {
    logger.warn("[SFTP] Directory transfer stopped early: session lost", sessionLostError.message);
  } else if (errors.length > 0) {
    logger.debug?.("[SFTP] Some files in directory transfer failed", errors);
  }
  return errors.length;
}
