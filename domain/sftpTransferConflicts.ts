import type { TransferTask } from "./models";

/** Statuses that still hold or may soon hold the destination path. */
export const SFTP_PATH_CONFLICT_ACTIVE_STATUSES: ReadonlySet<TransferTask["status"]> = new Set([
  "pending",
  "queued",
  "transferring",
  "pausing",
  "paused",
]);

/**
 * Find another top-level transfer that uses the same source and target paths.
 * Concurrent writers to one destination (especially local .part + rename) race
 * and corrupt output; FileZilla-style clients refuse or queue the second job.
 */
export function findActivePathConflict(
  tasks: readonly TransferTask[],
  candidate: Pick<TransferTask, "id" | "sourcePath" | "targetPath">,
): TransferTask | undefined {
  return tasks.find((task) => (
    !task.parentTaskId
    && task.id !== candidate.id
    && task.sourcePath === candidate.sourcePath
    && task.targetPath === candidate.targetPath
    && SFTP_PATH_CONFLICT_ACTIVE_STATUSES.has(task.status)
  ));
}

export function pathConflictMessage(existing: Pick<TransferTask, "fileName" | "status">): string {
  const label = existing.fileName || "file";
  if (existing.status === "paused") {
    return `Another transfer for "${label}" is paused. Resume or cancel it first.`;
  }
  return `Another transfer for "${label}" is already in progress.`;
}
