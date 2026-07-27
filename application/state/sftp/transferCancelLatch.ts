/**
 * Process-wide cancel flags for SFTP transfers.
 *
 * Directory walks and stream arms check these so Cancel from the global
 * transfer center still stops work after the React owner unmounts.
 */

const cancelledIds = new Set<string>();

export function markTransferCancelled(taskId: string): void {
  cancelledIds.add(taskId);
}

export function markTransferCancelledTree(rootTaskId: string, childIds: readonly string[] = []): void {
  cancelledIds.add(rootTaskId);
  for (const id of childIds) cancelledIds.add(id);
}

export function clearTransferCancelled(taskId: string): void {
  cancelledIds.delete(taskId);
}

export function clearTransferCancelledTree(rootTaskId: string, childIds: readonly string[] = []): void {
  cancelledIds.delete(rootTaskId);
  for (const id of childIds) cancelledIds.delete(id);
}

export function isTransferCancelledFlag(taskId: string): boolean {
  return cancelledIds.has(taskId);
}

export function isTransferOrRootCancelled(rootTaskId: string, taskId?: string): boolean {
  return cancelledIds.has(rootTaskId) || (!!taskId && cancelledIds.has(taskId));
}

/** Test helper. */
export function resetTransferCancelLatchesForTests(): void {
  cancelledIds.clear();
}
