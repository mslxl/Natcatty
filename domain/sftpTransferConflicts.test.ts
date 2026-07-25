import assert from "node:assert/strict";
import test from "node:test";

import type { TransferTask } from "./models";
import {
  findActivePathConflict,
  pathConflictMessage,
} from "./sftpTransferConflicts";

const base = (overrides: Partial<TransferTask> = {}): TransferTask => ({
  id: "a",
  fileName: "sing-box",
  sourcePath: "/root/sing-box",
  targetPath: "/Users/me/Desktop/sing-box",
  sourceConnectionId: "remote",
  targetConnectionId: "local",
  direction: "download",
  status: "transferring",
  totalBytes: 100,
  transferredBytes: 10,
  speed: 1,
  startTime: 1,
  isDirectory: false,
  ...overrides,
});

test("findActivePathConflict matches same source and target among active rows", () => {
  const tasks = [
    base({ id: "live", status: "transferring" }),
    base({ id: "done", status: "completed", transferredBytes: 100 }),
    base({ id: "other-path", targetPath: "/tmp/other" }),
  ];
  assert.equal(
    findActivePathConflict(tasks, {
      id: "new",
      sourcePath: "/root/sing-box",
      targetPath: "/Users/me/Desktop/sing-box",
    })?.id,
    "live",
  );
  assert.equal(
    findActivePathConflict(tasks, {
      id: "live",
      sourcePath: "/root/sing-box",
      targetPath: "/Users/me/Desktop/sing-box",
    }),
    undefined,
  );
});

test("interrupted is not an active path conflict (resume may claim the path)", () => {
  const tasks = [base({ id: "dead", status: "interrupted" })];
  assert.equal(
    findActivePathConflict(tasks, {
      id: "resume",
      sourcePath: "/root/sing-box",
      targetPath: "/Users/me/Desktop/sing-box",
    }),
    undefined,
  );
});

test("pathConflictMessage distinguishes paused vs running", () => {
  assert.match(pathConflictMessage({ fileName: "x", status: "paused" }), /paused/i);
  assert.match(pathConflictMessage({ fileName: "x", status: "transferring" }), /in progress/i);
});
