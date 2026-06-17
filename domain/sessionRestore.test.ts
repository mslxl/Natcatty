import test from "node:test";
import assert from "node:assert/strict";

import {
  buildSessionRestorePayload,
  isRestoredDisconnectedSession,
  resolveRestoredActiveTabId,
  sanitizeSessionRestorePayload,
} from "./sessionRestore.ts";
import type { TerminalSession, Workspace } from "./models.ts";

const session = (id: string, workspaceId?: string): TerminalSession => ({
  id,
  hostId: `host-${id}`,
  hostLabel: `Host ${id}`,
  hostname: `${id}.example.test`,
  username: "root",
  status: "connected",
  protocol: "ssh",
  workspaceId,
});

test("buildSessionRestorePayload stores restored sessions as disconnected and drops reuse pointers", () => {
  const payload = buildSessionRestorePayload({
    sessions: [{ ...session("s1"), reuseConnectionFromSessionId: "source" }],
    workspaces: [],
    tabOrder: ["s1"],
    activeTabId: "s1",
    now: 123,
  });

  assert.equal(payload.version, 1);
  assert.equal(payload.savedAt, 123);
  assert.equal(payload.sessions[0].status, "disconnected");
  assert.equal(payload.sessions[0].reuseConnectionFromSessionId, undefined);
  assert.equal(payload.sessions[0].restoreState, "restored-disconnected");
});

test("buildSessionRestorePayload only stores allowlisted session fields", () => {
  const payload = buildSessionRestorePayload({
    sessions: [{
      ...session("s1"),
      terminalData: "do-not-store",
      reuseConnectionFromSessionId: "source",
    } as TerminalSession & { terminalData: string }],
    workspaces: [],
    tabOrder: ["s1"],
    activeTabId: "s1",
    now: 123,
  });

  assert.deepEqual(Object.keys(payload.sessions[0]).sort(), [
    "hostId",
    "hostLabel",
    "hostname",
    "id",
    "protocol",
    "restoreState",
    "status",
    "username",
  ].sort());
});

test("buildSessionRestorePayload drops startup commands from restored sessions", () => {
  const payload = buildSessionRestorePayload({
    sessions: [{
      ...session("s1"),
      startupCommand: "rm -rf /tmp/example",
      noAutoRun: false,
    }],
    workspaces: [],
    tabOrder: ["s1"],
    activeTabId: "s1",
    now: 123,
  });

  assert.equal(payload.sessions[0].startupCommand, undefined);
  assert.equal(payload.sessions[0].noAutoRun, undefined);
});

test("sanitizeSessionRestorePayload prunes invalid workspace panes and drops empty workspaces", () => {
  const workspace: Workspace = {
    id: "ws-1",
    title: "Workspace",
    root: {
      id: "split-1",
      type: "split",
      direction: "vertical",
      children: [
        { id: "pane-1", type: "pane", sessionId: "s1" },
        { id: "pane-2", type: "pane", sessionId: "missing" },
      ],
      sizes: [0.25, 0.75],
    },
    focusedSessionId: "missing",
    focusSessionOrder: ["missing", "s1"],
  };

  const sanitized = sanitizeSessionRestorePayload({
    version: 1,
    savedAt: 1,
    activeTabId: "missing",
    tabOrder: ["missing", "ws-1", "s1"],
    sessions: [session("s1", "ws-1")],
    workspaces: [workspace],
  });

  assert.equal(sanitized.sessions.length, 1);
  assert.equal(sanitized.workspaces.length, 1);
  assert.equal(sanitized.workspaces[0].root.type, "pane");
  assert.equal(sanitized.workspaces[0].focusedSessionId, "s1");
  assert.deepEqual(sanitized.workspaces[0].focusSessionOrder, ["s1"]);
  assert.deepEqual(sanitized.tabOrder, ["ws-1"]);
  assert.equal(sanitized.activeTabId, "ws-1");
});

test("sanitizeSessionRestorePayload drops malformed session and workspace records", () => {
  const sanitized = sanitizeSessionRestorePayload({
    version: 1,
    savedAt: 1,
    activeTabId: "ws-1",
    tabOrder: ["ws-1", "s1"],
    sessions: [null, session("s1")],
    workspaces: [{ id: "ws-1" }],
  } as unknown);

  assert.deepEqual(sanitized.sessions.map((item) => item.id), ["s1"]);
  assert.deepEqual(sanitized.workspaces, []);
  assert.deepEqual(sanitized.tabOrder, ["s1"]);
  assert.equal(sanitized.activeTabId, "s1");
});

test("sanitizeSessionRestorePayload enforces workspace session ownership", () => {
  const workspace: Workspace = {
    id: "ws-1",
    title: "Workspace",
    root: {
      id: "split-1",
      type: "split",
      direction: "horizontal",
      children: [
        { id: "pane-1", type: "pane", sessionId: "owned" },
        { id: "pane-2", type: "pane", sessionId: "standalone" },
        { id: "pane-3", type: "pane", sessionId: "other-workspace" },
      ],
      sizes: [0.2, 0.3, 0.5],
    },
    focusedSessionId: "standalone",
    focusSessionOrder: ["standalone", "owned", "other-workspace"],
  };

  const sanitized = sanitizeSessionRestorePayload({
    version: 1,
    savedAt: 1,
    activeTabId: "ws-1",
    tabOrder: ["ws-1", "standalone", "ws-2"],
    sessions: [
      session("owned", "ws-1"),
      session("standalone"),
      session("other-workspace", "ws-2"),
    ],
    workspaces: [
      workspace,
      {
        id: "ws-2",
        title: "Other",
        root: { id: "pane-4", type: "pane", sessionId: "other-workspace" },
      },
    ],
  });

  assert.equal(sanitized.workspaces[0].root.type, "pane");
  assert.equal(sanitized.workspaces[0].root.sessionId, "owned");
  assert.equal(sanitized.workspaces[0].focusedSessionId, "owned");
  assert.deepEqual(sanitized.workspaces[0].focusSessionOrder, ["owned"]);
  assert.deepEqual(sanitized.tabOrder, ["ws-1", "standalone", "ws-2"]);
});

test("resolveRestoredActiveTabId falls back to vault when no restored tab is valid", () => {
  assert.equal(resolveRestoredActiveTabId("missing", [], [], []), "vault");
});

test("isRestoredDisconnectedSession detects only explicit restored sessions", () => {
  assert.equal(
    isRestoredDisconnectedSession({
      ...session("s1"),
      status: "disconnected",
      restoreState: "restored-disconnected",
    }),
    true,
  );
  assert.equal(isRestoredDisconnectedSession({ ...session("s1"), status: "disconnected" }), false);
});
