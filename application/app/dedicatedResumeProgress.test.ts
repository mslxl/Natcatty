import assert from "node:assert/strict";
import test from "node:test";
import type { TransferTask } from "../../domain/models";
import {
  canApplyDedicatedResumeProgress,
  createDedicatedResumeChildUpdateBatcher,
  DEDICATED_RESUME_CHILD_UPDATE_BATCH_SIZE,
} from "./dedicatedResumeProgress";

test("deferred dedicated-resume progress cannot reopen a settled row", () => {
  for (const status of ["completed", "failed", "cancelled", "attention", "interrupted"] as const) {
    assert.equal(canApplyDedicatedResumeProgress(status), false, status);
  }
  for (const status of ["pending", "queued", "transferring"] as const) {
    assert.equal(canApplyDedicatedResumeProgress(status), true, status);
  }
});

test("50,000 retained child updates use a hard-bounded number of store scans", () => {
  const retained = new Set(Array.from({ length: 50_000 }, (_, index) => `child-${index}`));
  const batches: TransferTask[][] = [];
  const batcher = createDedicatedResumeChildUpdateBatcher({
    getTaskCount: () => 50_001,
    hasTask: (taskId) => retained.has(taskId),
    upsertTasks: (tasks) => batches.push([...tasks]),
  });

  for (let index = 0; index < 50_000; index += 1) {
    const child = {
      id: `child-${index}`,
      status: "transferring",
      parentTaskId: "parent",
    } as TransferTask;
    batcher.push(child);
    batcher.push({ ...child, status: "completed" });
  }
  batcher.flush();

  assert.ok(
    batches.length <= Math.ceil(50_000 / DEDICATED_RESUME_CHILD_UPDATE_BATCH_SIZE),
    `expected bounded store scans, got ${batches.length}`,
  );
  const finalById = new Map<string, TransferTask>();
  for (const task of batches.flat()) finalById.set(task.id, task);
  assert.equal(finalById.size, 50_000);
  assert.ok([...finalById.values()].every((task) => task.status === "completed"));
  assert.ok(
    batches.flat().length <= 50_000 + batches.length,
    "a batch-boundary transition may repeat at most one child per store scan",
  );
});
