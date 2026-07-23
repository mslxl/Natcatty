import assert from "node:assert/strict";
import test from "node:test";

import { createDirectDownloadTransferTask } from "./downloadTransferTask";

test("download tasks retain the remote host needed for reconnecting after the original SSH session closes", () => {
  const task = createDirectDownloadTransferTask({
    id: "download-1",
    fileName: "archive.bin",
    sourcePath: "/remote/archive.bin",
    targetPath: "/local/archive.bin",
    sourceConnectionId: "connection-1",
    sourceHostId: "host-1",
    sourceHostLabel: "Production",
    totalBytes: 128,
    isDirectory: false,
  });

  assert.equal(task.sourceHostId, "host-1");
  assert.equal(task.sourceHostLabel, "Production");
  assert.equal(task.targetConnectionId, "local");
  assert.equal(task.resumable, true);
  assert.equal(task.status, "queued");
  assert.equal(task.phase, undefined);
});
