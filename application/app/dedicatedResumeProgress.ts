import type { TransferStatus, TransferTask } from "../../domain/models";

export const DEDICATED_RESUME_LARGE_HISTORY_THRESHOLD = 4_096;
export const DEDICATED_RESUME_CHILD_UPDATE_BATCH_SIZE = 512;

export interface DedicatedResumeChildUpdateBatcher {
  push(task: TransferTask): void;
  flush(): void;
}

/**
 * A restarted directory can retain tens of thousands of exception rows. The
 * store intentionally performs full history compaction on each upsert, so
 * feeding it one child transition at a time becomes quadratic. Keep only the
 * latest state for each retained child and compact in fixed-size batches.
 */
export function createDedicatedResumeChildUpdateBatcher(deps: {
  getTaskCount: () => number;
  hasTask: (taskId: string) => boolean;
  upsertTasks: (tasks: readonly TransferTask[]) => void;
}): DedicatedResumeChildUpdateBatcher {
  const pending = new Map<string, TransferTask>();
  const flush = () => {
    if (pending.size === 0) return;
    const batch = [...pending.values()];
    pending.clear();
    deps.upsertTasks(batch);
  };
  return {
    push(task) {
      const shouldBatch = !!task.parentTaskId
        && deps.getTaskCount() >= DEDICATED_RESUME_LARGE_HISTORY_THRESHOLD
        && deps.hasTask(task.id);
      if (!shouldBatch) {
        deps.upsertTasks([task]);
        return;
      }
      pending.set(task.id, task);
      if (pending.size >= DEDICATED_RESUME_CHILD_UPDATE_BATCH_SIZE) flush();
    },
    flush,
  };
}

/** Only rows still owned by an active resume may accept a deferred rAF sample. */
export function canApplyDedicatedResumeProgress(status: TransferStatus): boolean {
  return status === "pending" || status === "queued" || status === "transferring";
}
