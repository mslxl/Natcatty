import test from "node:test";
import assert from "node:assert/strict";
import { shouldFollowTerminalCwdNavigate } from "./sftpFollowTerminalCwd";

const base = {
  followEnabled: true,
  isVisible: true,
  terminalCwd: "/home/user/project",
  currentPath: "/home/user",
  hasActiveWork: false,
  isConnected: true,
};

test("shouldFollowTerminalCwdNavigate returns true when follow is on and paths differ", () => {
  assert.equal(shouldFollowTerminalCwdNavigate(base), true);
});

test("shouldFollowTerminalCwdNavigate returns false when paths already match", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({ ...base, currentPath: "/home/user/project" }),
    false,
  );
});

test("shouldFollowTerminalCwdNavigate returns false when follow is disabled", () => {
  assert.equal(shouldFollowTerminalCwdNavigate({ ...base, followEnabled: false }), false);
});

test("shouldFollowTerminalCwdNavigate returns false while interactive work is active", () => {
  assert.equal(shouldFollowTerminalCwdNavigate({ ...base, hasActiveWork: true }), false);
});

test("shouldFollowTerminalCwdNavigate returns false without a known terminal cwd", () => {
  assert.equal(shouldFollowTerminalCwdNavigate({ ...base, terminalCwd: null }), false);
});
