import type { TransferTask } from "../../../domain/models";

export interface DirectDownloadTransferTaskInput {
  id: string;
  fileName: string;
  sourcePath: string;
  targetPath: string;
  sourceConnectionId: string;
  sourceHostId: string;
  sourceHostLabel: string;
  totalBytes: number;
  isDirectory: boolean;
}

export function createDirectDownloadTransferTask(
  input: DirectDownloadTransferTaskInput,
): TransferTask {
  return {
    id: input.id,
    fileName: input.fileName,
    originalFileName: input.fileName,
    sourcePath: input.sourcePath,
    targetPath: input.targetPath,
    sourceConnectionId: input.sourceConnectionId,
    targetConnectionId: "local",
    sourceHostId: input.sourceHostId,
    sourceHostLabel: input.sourceHostLabel,
    targetHostLabel: "Local",
    direction: "download",
    status: "queued",
    totalBytes: input.totalBytes,
    transferredBytes: 0,
    speed: 0,
    startTime: Date.now(),
    isDirectory: input.isDirectory,
    progressMode: input.isDirectory ? "files" : "bytes",
    retryable: true,
    origin: "manual",
    resumable: true,
  };
}
