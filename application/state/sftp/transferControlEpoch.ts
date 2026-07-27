/**
 * Monotonic control epoch per transfer id.
 *
 * Pause soft-drain / watchdogs capture an epoch when they start. Resume (or a
 * newer pause) bumps the epoch so every in-flight soft-drain becomes a no-op
 * instead of racing the live stream. Process-global so it survives panel unmount.
 */

const epochs = new Map<string, number>();

export function getTransferControlEpoch(taskId: string): number {
  return epochs.get(taskId) ?? 0;
}

/** Bump and return the new epoch. Call on intentional Pause and Resume. */
export function bumpTransferControlEpoch(taskId: string): number {
  const next = getTransferControlEpoch(taskId) + 1;
  epochs.set(taskId, next);
  return next;
}

export function isTransferControlEpochCurrent(taskId: string, epoch: number): boolean {
  return getTransferControlEpoch(taskId) === epoch;
}

/** Test helper. */
export function resetTransferControlEpochsForTests(): void {
  epochs.clear();
}
