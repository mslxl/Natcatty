import assert from "node:assert/strict";
import test from "node:test";

import {
  enqueuePendingSftpUpload,
  removePendingSftpUpload,
} from "./pendingSftpUploadQueue";

test("repeated terminal drops remain queued in arrival order", () => {
  const first = { requestId: "drop-1", entries: ["one.txt"] };
  const second = { requestId: "drop-2", entries: ["two.txt"] };

  const queued = enqueuePendingSftpUpload(
    enqueuePendingSftpUpload([], first),
    second,
  );

  assert.deepEqual(queued, [first, second]);
  assert.deepEqual(removePendingSftpUpload(queued, "drop-1"), [second]);
});

test("handling a stale request leaves the pending queue unchanged", () => {
  const queued = [{ requestId: "drop-2", entries: ["two.txt"] }];
  assert.equal(removePendingSftpUpload(queued, "drop-1"), queued);
});
