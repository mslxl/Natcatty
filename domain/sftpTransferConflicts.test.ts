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

test("findActivePathConflict matches same destination among active rows", () => {
  const tasks = [
    base({ id: "live", status: "transferring" }),
    base({ id: "done", status: "completed", transferredBytes: 100 }),
    base({ id: "other-path", targetPath: "/tmp/other" }),
  ];
  assert.equal(
    findActivePathConflict(tasks, {
      id: "new",
      targetPath: "/Users/me/Desktop/sing-box",
      targetConnectionId: "local",
    })?.id,
    "live",
  );
  assert.equal(
    findActivePathConflict(tasks, {
      id: "live",
      targetPath: "/Users/me/Desktop/sing-box",
      targetConnectionId: "local",
    }),
    undefined,
  );
});

test("findActivePathConflict ignores identical paths on different endpoints", () => {
  const tasks = [
    base({
      id: "host-a",
      direction: "upload",
      sourcePath: "/local/file",
      targetPath: "/remote/file",
      sourceConnectionId: "local",
      targetConnectionId: "conn-a",
      targetHostId: "host-a",
    }),
  ];
  assert.equal(
    findActivePathConflict(tasks, {
      id: "host-b",
      targetPath: "/remote/file",
      targetConnectionId: "conn-b",
      targetHostId: "host-b",
    }),
    undefined,
  );
});

test("findActivePathConflict collides different sources writing one destination", () => {
  const tasks = [
    base({
      id: "from-a",
      sourcePath: "/root/a",
      targetPath: "/Users/me/Desktop/out.bin",
    }),
  ];
  assert.equal(
    findActivePathConflict(tasks, {
      id: "from-b",
      targetPath: "/Users/me/Desktop/out.bin",
      targetConnectionId: "local",
    })?.id,
    "from-a",
  );
});

test("interrupted is not an active path conflict (resume may claim the path)", () => {
  const tasks = [base({ id: "dead", status: "interrupted" })];
  assert.equal(
    findActivePathConflict(tasks, {
      id: "resume",
      targetPath: "/Users/me/Desktop/sing-box",
      targetConnectionId: "local",
    }),
    undefined,
  );
});

test("pathConflictMessage distinguishes paused vs running", () => {
  assert.match(pathConflictMessage({ fileName: "x", status: "paused" }), /paused/i);
  assert.match(pathConflictMessage({ fileName: "x", status: "transferring" }), /in progress/i);
});
