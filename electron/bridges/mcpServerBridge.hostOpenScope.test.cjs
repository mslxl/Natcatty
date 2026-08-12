"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

function loadFreshBridge() {
  const bridgePath = require.resolve("./mcpServerBridge.cjs");
  delete require.cache[bridgePath];
  // Also clear retain/ownership deps so a fresh bridge gets fresh module state.
  for (const key of Object.keys(require.cache)) {
    if (key.includes(`${require("node:path").sep}mcpServerBridge${require("node:path").sep}`)) {
      delete require.cache[key];
    }
  }
  return require("./mcpServerBridge.cjs");
}

test("sidebar scope replace keeps host_open-owned sessions in the chat scope", async (t) => {
  const bridge = loadFreshBridge();
  t.after(() => bridge.cleanup());
  bridge.init({
    sessions: new Map(),
    electronModule: null,
  });
  bridge.setPermissionMode("auto");
  bridge.setVaultAgentInvoker(async (op) => {
    if (op === "host.open") {
      return { ok: true, sessionId: "sess-opened", hostId: "host-b", status: "connecting" };
    }
    return { ok: false, error: `unexpected op ${op}` };
  });

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: false,
      hostId: "host-b",
      protocol: "ssh",
    },
  ], "chat-1");

  const opened = await bridge.dispatchBuiltinRpc("public/vault/hosts/open", {
    chatSessionId: "chat-1",
    hostId: "host-b",
  });
  assert.equal(opened.ok, true);
  assert.equal(opened.sessionId, "sess-opened");

  // AIChatSidePanel-style full replace of only the focused tab.
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");

  assert.deepEqual(
    bridge.getScopedSessionIds("chat-1").sort(),
    ["sess-opened", "sess-original"],
  );
});

test("authoritative empty scope replace still clears host_open-owned sessions", async (t) => {
  const bridge = loadFreshBridge();
  t.after(() => bridge.cleanup());
  bridge.init({
    sessions: new Map(),
    electronModule: null,
  });
  bridge.setPermissionMode("auto");
  bridge.setVaultAgentInvoker(async () => ({
    ok: true,
    sessionId: "sess-opened",
    hostId: "host-b",
    status: "connecting",
  }));

  bridge.updateSessionMetadata([
    { sessionId: "sess-opened", hostname: "10.0.0.2", label: "server-b", connected: false },
  ], "chat-1");
  await bridge.dispatchBuiltinRpc("public/vault/hosts/open", {
    chatSessionId: "chat-1",
    hostId: "host-b",
  });

  bridge.updateSessionMetadata([], "chat-1");
  assert.deepEqual(bridge.getScopedSessionIds("chat-1"), []);

  // A later non-empty sync must not resurrect the cleared owned session via
  // cross-scope fallback / stale ownership.
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: true,
      hostId: "host-b",
    },
  ], "__external_mcp__");
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");
  assert.deepEqual(bridge.getScopedSessionIds("chat-1"), ["sess-original"]);
  assert.equal(bridge.getSessionMeta("sess-opened", "chat-1"), null);
});

test("retained host_open metadata refreshes connected from another scope", async (t) => {
  const bridge = loadFreshBridge();
  t.after(() => bridge.cleanup());
  bridge.init({
    sessions: new Map(),
    electronModule: null,
  });
  bridge.setPermissionMode("auto");
  bridge.setVaultAgentInvoker(async () => ({
    ok: true,
    sessionId: "sess-opened",
    hostId: "host-b",
    status: "connecting",
  }));

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: false,
      hostId: "host-b",
      protocol: "ssh",
    },
  ], "chat-1");
  await bridge.dispatchBuiltinRpc("public/vault/hosts/open", {
    chatSessionId: "chat-1",
    hostId: "host-b",
  });

  // Another surface (External MCP / opened tab) learns the session connected.
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: true,
      hostId: "host-b",
      protocol: "ssh",
      username: "root",
    },
  ], "__external_mcp__");

  // Original chat sidebar still pushes only the focused tab.
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");

  assert.equal(bridge.getSessionMeta("sess-opened", "chat-1")?.connected, true);
  assert.equal(bridge.getSessionMeta("sess-opened", "chat-1")?.username, "root");
});

test("ordinary tab close forgets host_open ownership so sidebar sync cannot revive ghosts", async (t) => {
  const bridge = loadFreshBridge();
  t.after(() => bridge.cleanup());
  const closedListeners = new Set();
  bridge.init({
    sessions: new Map(),
    electronModule: null,
    terminalWorkerManager: {
      onSessionClosed(listener) {
        closedListeners.add(listener);
        return {
          dispose: () => closedListeners.delete(listener),
        };
      },
    },
  });
  bridge.setPermissionMode("auto");
  bridge.setVaultAgentInvoker(async () => ({
    ok: true,
    sessionId: "sess-opened",
    hostId: "host-b",
    status: "connecting",
  }));

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: true,
      hostId: "host-b",
    },
  ], "chat-1");
  await bridge.dispatchBuiltinRpc("public/vault/hosts/open", {
    chatSessionId: "chat-1",
    hostId: "host-b",
  });

  // User closes the host_open tab through the normal UI / worker path.
  for (const listener of closedListeners) {
    listener({ sessionId: "sess-opened", reason: "closed", explicit: true });
  }

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");

  assert.deepEqual(bridge.getScopedSessionIds("chat-1"), ["sess-original"]);
  assert.equal(bridge.getSessionMeta("sess-opened", "chat-1"), null);
});

test("recoverable worker exits keep host_open ownership for reconnect", async (t) => {
  const bridge = loadFreshBridge();
  t.after(() => bridge.cleanup());
  const closedListeners = new Set();
  bridge.init({
    sessions: new Map(),
    electronModule: null,
    terminalWorkerManager: {
      onSessionClosed(listener) {
        closedListeners.add(listener);
        return {
          dispose: () => closedListeners.delete(listener),
        };
      },
    },
  });
  bridge.setPermissionMode("auto");
  bridge.setVaultAgentInvoker(async () => ({
    ok: true,
    sessionId: "sess-opened",
    hostId: "host-b",
    status: "connecting",
  }));

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: true,
      hostId: "host-b",
    },
  ], "chat-1");
  await bridge.dispatchBuiltinRpc("public/vault/hosts/open", {
    chatSessionId: "chat-1",
    hostId: "host-b",
  });

  for (const event of [
    { reason: "error" },
    { reason: "worker-exit" },
    { reason: "superseded" },
    { reason: "closed" },
    // Shell exits that leave the tab for reconnect (missing/nonzero exitCode).
    { reason: "exited" },
    { reason: "exited", exitCode: 1 },
  ]) {
    for (const listener of closedListeners) {
      listener({ sessionId: "sess-opened", ...event });
    }
  }

  // After reconnect, sidebar may push only the focused tab; ownership must
  // still retain the host_open session.
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");

  assert.deepEqual(
    bridge.getScopedSessionIds("chat-1").sort(),
    ["sess-opened", "sess-original"],
  );
});

test("clean shell exit keeps ownership until the tab is explicitly closed", async (t) => {
  const bridge = loadFreshBridge();
  t.after(() => bridge.cleanup());
  const closedListeners = new Set();
  bridge.init({
    sessions: new Map(),
    electronModule: null,
    terminalWorkerManager: {
      onSessionClosed(listener) {
        closedListeners.add(listener);
        return {
          dispose: () => closedListeners.delete(listener),
        };
      },
    },
  });
  bridge.setPermissionMode("auto");
  bridge.setVaultAgentInvoker(async () => ({
    ok: true,
    sessionId: "sess-opened",
    hostId: "host-b",
    status: "connecting",
  }));

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-opened",
      hostname: "10.0.0.2",
      label: "server-b",
      connected: true,
      hostId: "host-b",
    },
  ], "chat-1");
  await bridge.dispatchBuiltinRpc("public/vault/hosts/open", {
    chatSessionId: "chat-1",
    hostId: "host-b",
  });

  // Worker reports a clean exit; auto-close may still leave the tab briefly,
  // and disabled auto-close keeps it for reconnect — ownership stays.
  for (const listener of closedListeners) {
    listener({ sessionId: "sess-opened", reason: "exited", exitCode: 0 });
  }

  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");
  assert.deepEqual(
    bridge.getScopedSessionIds("chat-1").sort(),
    ["sess-opened", "sess-original"],
  );

  // Renderer tab close is the authoritative ownership drop.
  for (const listener of closedListeners) {
    listener({ sessionId: "sess-opened", reason: "closed", explicit: true });
  }
  bridge.updateSessionMetadata([
    {
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    },
  ], "chat-1");
  assert.deepEqual(bridge.getScopedSessionIds("chat-1"), ["sess-original"]);
});
