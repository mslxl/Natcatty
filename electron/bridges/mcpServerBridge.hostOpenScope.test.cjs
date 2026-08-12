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
});
