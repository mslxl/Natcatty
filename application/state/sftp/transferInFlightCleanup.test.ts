import assert from "node:assert/strict";
import test from "node:test";

import type { TransferTask } from "../../../domain/models";
import { sftpTransferCenterStore } from "../sftpTransferCenterStore";
import {
  isTransferWalkInFlight,
  registerTransferWalk,
  resetTransferWalkRegistryForTests,
  unregisterTransferWalk,
} from "./transferWalkRegistry";
import { finishTransferTask, runTrackedTransferAttempt } from "./useSftpTransfers.ts";

const makeTask = (status: TransferTask["status"] = "transferring"): TransferTask => ({
  id: "directory-1",
  fileName: "folder",
  sourcePath: "/source/folder",
  targetPath: "/target/folder",
  sourceConnectionId: "local",
  targetConnectionId: "remote",
  direction: "upload",
  status,
  totalBytes: 3,
  transferredBytes: 3,
  speed: 1,
  startTime: 1,
  isDirectory: true,
  progressMode: "files",
  resumable: true,
  retryable: true,
});

test("a throwing completion handler does not leave the transfer marked in flight", async () => {
  const inFlight = new Set<string>();
  const completionHandler = async () => {
    throw new Error("completion callback failed");
  };

  await assert.rejects(
    runTrackedTransferAttempt(inFlight, "transfer-1", async () => {
      await completionHandler();
      return "completed";
    }),
    /completion callback failed/,
  );
  assert.equal(inFlight.has("transfer-1"), false);

  let reruns = 0;
  const result = await runTrackedTransferAttempt(inFlight, "transfer-1", async () => {
    reruns += 1;
    return "completed";
  });
  assert.equal(result, "completed");
  assert.equal(reruns, 1);
  assert.equal(inFlight.has("transfer-1"), false);
});

test("directory completion reaches the global store while its walk is still active", (t) => {
  const ownerId = "completion-test";
  const task = { ...makeTask(), id: "directory-complete" };
  resetTransferWalkRegistryForTests();
  sftpTransferCenterStore.publishOwner(ownerId, [task]);
  registerTransferWalk(task.id);
  t.after(() => {
    unregisterTransferWalk(task.id);
    resetTransferWalkRegistryForTests();
    sftpTransferCenterStore.dismiss(task.id);
  });

  const status = finishTransferTask(
    task,
    { partialFailure: false, cancelled: false, endTime: Date.now() },
    (updates) => {
      sftpTransferCenterStore.publishOwner(ownerId, [{ ...task, ...updates }]);
    },
  );

  assert.equal(status, "completed");
  assert.equal(isTransferWalkInFlight(task.id), true);
  assert.equal(sftpTransferCenterStore.getTask(task.id)?.status, "completed");
  assert.equal(sftpTransferCenterStore.getSnapshot().activeCount, 0);
});

test("partial failure and late cancellation keep their existing terminal behavior", () => {
  const failedUpdates: Array<Partial<TransferTask>> = [];
  const failedStatus = finishTransferTask(
    { ...makeTask(), transferredBytes: 2 },
    { partialFailure: true, cancelled: false, endTime: 456 },
    (updates) => { failedUpdates.push(updates); },
  );
  assert.equal(failedStatus, "failed");
  assert.deepEqual(failedUpdates, [{
    status: "failed",
    error: "Some files failed to transfer",
    retryable: false,
    endTime: 456,
    transferredBytes: 2,
    speed: 0,
  }]);

  let cancelledUpdates: Partial<TransferTask> | undefined;
  const cancelledStatus = finishTransferTask(
    makeTask("cancelled"),
    { partialFailure: false, cancelled: false, endTime: 789 },
    (updates) => { cancelledUpdates = updates; },
  );
  assert.equal(cancelledStatus, "cancelled");
  assert.deepEqual(cancelledUpdates, {
    status: "cancelled",
    error: undefined,
    endTime: 789,
    speed: 0,
  });
});
