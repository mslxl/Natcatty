import assert from "node:assert/strict";
import test from "node:test";

import type { TransferTask } from "../../../domain/models";
import { createUploadTaskCallbacks } from "./uploadTaskCallbacks";

test("upload task callbacks write through the transfer store without page callbacks", () => {
  const upserts: TransferTask[][] = [];
  const patches: Array<{ taskId: string; updates: Partial<TransferTask> }> = [];
  const dismissed: string[] = [];
  const store = {
    upsertTasks: (tasks: readonly TransferTask[]) => upserts.push([...tasks]),
    patchTask: (taskId: string, updates: Partial<TransferTask>) => patches.push({ taskId, updates }),
    dismiss: (taskId: string) => dismissed.push(taskId),
  };
  const callbacks = createUploadTaskCallbacks({
    ownerId: "owner-1",
    connectionId: "connection-1",
    targetPath: "/remote",
    targetHostId: "host-1",
    store,
  });

  callbacks.onTaskCreated?.({
    id: "upload-1",
    fileName: "file.bin",
    displayName: "file.bin",
    sourcePath: "/local/file.bin",
    totalBytes: 100,
    isDirectory: false,
  });
  callbacks.onTaskProgress?.("upload-1", {
    transferred: 40,
    total: 100,
    speed: 20,
    percent: 40,
    checkpointBytes: 32,
    phase: "transferring",
  });
  callbacks.onTaskCompleted?.("upload-1", 100);

  assert.equal(upserts.length, 1);
  assert.equal(upserts[0][0].ownerId, "owner-1");
  assert.equal(upserts[0][0].id, "upload-1");
  assert.deepEqual(patches[0], {
    taskId: "upload-1",
    updates: {
      transferredBytes: 40,
      totalBytes: 100,
      checkpointBytes: 32,
      speed: 20,
      phase: "transferring",
      resumable: undefined,
      pauseUnavailableReason: undefined,
    },
  });
  assert.equal(patches[1].taskId, "upload-1");
  assert.equal(patches[1].updates.status, "completed");
  assert.equal(patches[1].updates.transferredBytes, 100);
  assert.deepEqual(dismissed, []);
});
