import type { TransferTask } from "./models";

/** Statuses that still hold or may soon hold the destination path. */
export const SFTP_PATH_CONFLICT_ACTIVE_STATUSES: ReadonlySet<TransferTask["status"]> = new Set([
  "pending",
  "queued",
  "transferring",
  "pausing",
  "paused",
]);

type DestinationRef = Pick<
  TransferTask,
  "targetPath" | "targetConnectionId" | "targetHostId" | "targetConnectionKey" | "targetHostLabel"
>;

/**
 * Local filesystem destinations share one identity: Save As / downloadToLocal
 * use the `"local"` sentinel, while dual-pane transfers store an ephemeral pane
 * connection id with targetHostLabel "Local". Treat both as the same endpoint so
 * concurrent writers to one absolute path still conflict.
 */
export function isLocalTransferDestination(ref: DestinationRef): boolean {
  if (ref.targetConnectionId === "local" || ref.targetConnectionKey === "local") {
    return true;
  }
  // Dual-pane local rows: labeled Local, no remote host/key, pane connection id.
  return ref.targetHostLabel === "Local" && !ref.targetHostId && !ref.targetConnectionKey;
}

/**
 * Stable destination endpoint identity. Prefer connection key / host id so two
 * sessions to the same host still collide; fall back to connection id (covers
 * the local sentinel and single-session remotes). Local FS destinations are
 * normalized so pane id and `"local"` compare equal.
 */
export function sameTransferDestinationEndpoint(
  a: DestinationRef,
  b: DestinationRef,
): boolean {
  if (isLocalTransferDestination(a) && isLocalTransferDestination(b)) {
    return true;
  }
  if (isLocalTransferDestination(a) || isLocalTransferDestination(b)) {
    return false;
  }
  if (a.targetConnectionKey && b.targetConnectionKey) {
    return a.targetConnectionKey === b.targetConnectionKey;
  }
  if (a.targetHostId && b.targetHostId) {
    return a.targetHostId === b.targetHostId;
  }
  return a.targetConnectionId === b.targetConnectionId;
}

/**
 * Find another top-level transfer that writes the same destination path.
 * Concurrent writers (especially local .part + rename) race and corrupt output;
 * FileZilla-style clients refuse or queue the second job. Source path is ignored
 * so different sources targeting one file still conflict; endpoint identity avoids
 * treating identical path strings on different hosts as the same destination.
 */
export function findActivePathConflict(
  tasks: readonly TransferTask[],
  candidate: Pick<TransferTask, "id"> & DestinationRef,
): TransferTask | undefined {
  return tasks.find((task) => (
    !task.parentTaskId
    && task.id !== candidate.id
    && task.targetPath === candidate.targetPath
    && sameTransferDestinationEndpoint(task, candidate)
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
