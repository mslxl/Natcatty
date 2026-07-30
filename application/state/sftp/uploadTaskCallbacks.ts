import type { TransferTask, TransferStatus } from "../../../domain/models";
import type { UploadCallbacks, UploadTaskInfo } from "../../../lib/uploadService";
import { sftpTransferCenterStore } from "../sftpTransferCenterStore";
import { joinPath } from "./utils";

type UploadTransferStore = Pick<
  typeof sftpTransferCenterStore,
  "upsertTasks" | "patchTask" | "dismiss"
>;

interface UploadTaskCallbacksParams {
  ownerId: string;
  connectionId: string;
  targetPath: string;
  targetHostId?: string;
  targetHostLabel?: string;
  targetConnectionKey?: string;
  store?: UploadTransferStore;
}

export const createUploadTaskCallbacks = ({
  ownerId,
  connectionId,
  targetPath,
  targetHostId,
  targetHostLabel,
  targetConnectionKey,
  store = sftpTransferCenterStore,
}: UploadTaskCallbacksParams): UploadCallbacks => ({
  onScanningStart: (taskId: string) => {
    store.upsertTasks([{
      id: taskId,
      ownerId,
      fileName: "Scanning files...",
      sourcePath: "local",
      targetPath,
      sourceConnectionId: "external",
      targetConnectionId: connectionId,
      targetHostId,
      targetHostLabel,
      sourceHostLabel: "Local",
      targetConnectionKey,
      direction: "upload",
      status: "pending" as TransferStatus,
      totalBytes: 0,
      transferredBytes: 0,
      speed: 0,
      startTime: Date.now(),
      isDirectory: true,
      progressMode: "bytes",
      origin: "drag-drop",
      background: false,
      resumable: true,
      phase: "scanning",
    }]);
  },
  onScanningEnd: (taskId: string) => {
    store.dismiss(taskId);
  },
  onTaskCreated: (task: UploadTaskInfo) => {
    store.upsertTasks([{
      id: task.id,
      ownerId,
      fileName: task.displayName,
      sourcePath: task.sourcePath ?? "local",
      targetPath: joinPath(targetPath, task.fileName),
      sourceConnectionId: "external",
      targetConnectionId: connectionId,
      targetHostId,
      targetHostLabel,
      sourceHostLabel: "Local",
      targetConnectionKey,
      direction: "upload",
      status: "transferring" as TransferStatus,
      totalBytes: task.totalBytes,
      transferredBytes: 0,
      speed: 0,
      startTime: Date.now(),
      isDirectory: task.isDirectory,
      progressMode: task.progressMode ?? "bytes",
      parentTaskId: task.parentTaskId,
      origin: "drag-drop",
      background: false,
      resumable: true,
      phase: "transferring",
      controlKind: task.controlKind,
    }]);
  },
  onTaskProgress: (taskId: string, progress) => {
    const durableCheckpoint = Number.isFinite(Number(progress.checkpointBytes))
      ? Math.max(0, Math.trunc(Number(progress.checkpointBytes)))
      : progress.transferred;
    // Only patch fingerprint/checkpoint while paused — do not keep animating
    // high-water transferred after the user hit Pause.
    store.patchTask(taskId, {
      transferredBytes: progress.transferred,
      totalBytes: progress.total,
      // Soft-drain high-water transferred must not become the resume offset.
      checkpointBytes: durableCheckpoint,
      speed: progress.speed,
      phase: progress.phase,
      resumable: progress.resumable,
      pauseUnavailableReason: progress.pauseUnavailableReason,
      // Durable pause identity may arrive on a forced progress event while
      // status is already paused — keep it for restart/resume safety.
      ...("sourceFingerprint" in progress && progress.sourceFingerprint
        ? { sourceFingerprint: progress.sourceFingerprint as string }
        : null),
    });
  },
  onTaskNameUpdate: (taskId: string, value: string) => {
    const separator = value.lastIndexOf("|");
    const phase = separator >= 0 ? value.slice(separator + 1) : "transferring";
    store.patchTask(taskId, {
      phase: phase === "compressed" ? "transferring" : phase as TransferTask["phase"],
    });
  },
  onTaskCompleted: (taskId: string, totalBytes: number) => {
    store.patchTask(taskId, {
      status: "completed" as TransferStatus,
      endTime: Date.now(),
      transferredBytes: totalBytes,
      speed: 0,
    });
  },
  onTaskFailed: (taskId: string, error: string) => {
    store.patchTask(taskId, {
      status: "failed" as TransferStatus,
      endTime: Date.now(),
      error,
      speed: 0,
    });
  },
  onTaskCancelled: (taskId: string) => {
    store.patchTask(taskId, {
      status: "cancelled" as TransferStatus,
      endTime: Date.now(),
      speed: 0,
    });
  },
});
