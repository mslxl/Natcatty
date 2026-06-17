import test from "node:test";
import assert from "node:assert/strict";

import {
  createInitialRestoredSessionState,
  mergeSessionRestoreCwd,
  updateRestoredSessionStatusState,
  updateSessionRestoreCwdState,
  shouldPersistSessionRestoreState,
} from "./sessionRestoreState.ts";
import type { SessionRestorePayload } from "../../domain/sessionRestore.ts";

const payload: SessionRestorePayload = {
  version: 1,
  savedAt: 1,
  activeTabId: "ws-1",
  tabOrder: ["ws-1"],
  sessions: [{
    id: "s1",
    hostId: "host-s1",
    hostLabel: "Host s1",
    hostname: "s1.example.test",
    username: "root",
    status: "disconnected",
    workspaceId: "ws-1",
    restoreState: "restored-disconnected",
  }],
  workspaces: [{
    id: "ws-1",
    title: "Workspace",
    root: { id: "pane-1", type: "pane", sessionId: "s1" },
  }],
};

test("restored session state hydrates sessions, workspaces, tab order, and active tab", () => {
  const restored = createInitialRestoredSessionState({
    restoreEnabled: true,
    payload,
  });

  assert.equal(restored.sessions[0].status, "disconnected");
  assert.equal(restored.sessions[0].restoreState, "restored-disconnected");
  assert.equal(restored.workspaces[0].id, "ws-1");
  assert.deepEqual(restored.tabOrder, ["ws-1"]);
  assert.equal(restored.activeTabId, "ws-1");
});

test("restored session state is empty when restore is disabled", () => {
  const restored = createInitialRestoredSessionState({ restoreEnabled: false, payload });
  assert.deepEqual(restored.sessions, []);
  assert.deepEqual(restored.workspaces, []);
  assert.deepEqual(restored.tabOrder, []);
  assert.equal(restored.activeTabId, "vault");
});

test("mergeSessionRestoreCwd records latest cwd metadata without terminal data", () => {
  const next = mergeSessionRestoreCwd(payload, "s1", "/usr/local/src");
  assert.equal(next.sessions[0].lastCwd, "/usr/local/src");
  assert.equal("terminalData" in next.sessions[0], false);
});

test("mergeSessionRestoreCwd removes cwd when terminal reports null", () => {
  const next = mergeSessionRestoreCwd({
    ...payload,
    sessions: [{ ...payload.sessions[0], lastCwd: "/tmp" }],
  }, "s1", null);
  assert.equal(next.sessions[0].lastCwd, undefined);
});

test("updateSessionRestoreCwdState records cwd in live session state", () => {
  const next = updateSessionRestoreCwdState(payload.sessions, "s1", "/opt/project");
  assert.equal(next[0].lastCwd, "/opt/project");
  assert.notEqual(next, payload.sessions);
});

test("updateSessionRestoreCwdState removes cwd from live session state", () => {
  const next = updateSessionRestoreCwdState([
    { ...payload.sessions[0], lastCwd: "/tmp" },
  ], "s1", null);
  assert.equal(next[0].lastCwd, undefined);
});

test("updateSessionRestoreCwdState preserves reference when session is missing", () => {
  const next = updateSessionRestoreCwdState(payload.sessions, "missing", "/opt/project");
  assert.equal(next, payload.sessions);
});

test("updateRestoredSessionStatusState clears restore marker after reconnect starts", () => {
  const next = updateRestoredSessionStatusState(payload.sessions, "s1", "connecting");

  assert.equal(next[0].status, "connecting");
  assert.equal(next[0].restoreState, undefined);
});

test("updateRestoredSessionStatusState keeps restore marker for disconnected placeholders", () => {
  const next = updateRestoredSessionStatusState(payload.sessions, "s1", "disconnected");

  assert.equal(next[0].status, "disconnected");
  assert.equal(next[0].restoreState, "restored-disconnected");
});

test("shouldPersistSessionRestoreState skips transient empty startup state", () => {
  assert.equal(shouldPersistSessionRestoreState([], [], []), false);
  assert.equal(shouldPersistSessionRestoreState(payload.sessions, payload.workspaces, payload.tabOrder), true);
});
