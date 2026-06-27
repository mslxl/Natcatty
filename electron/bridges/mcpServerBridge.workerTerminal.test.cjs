"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

function loadFreshBridge() {
  const bridgePath = require.resolve("./mcpServerBridge.cjs");
  delete require.cache[bridgePath];
  return require("./mcpServerBridge.cjs");
}

test("MCP/Catty capability context uses scoped metadata when terminal sessions live in worker", async () => {
  const bridge = loadFreshBridge();
  bridge.init({
    sessions: new Map(),
    electronModule: null,
    terminalWorkerManager: {
      request() {
        throw new Error("getContext should not need a worker round trip");
      },
    },
  });
  bridge.setPermissionMode("auto");
  bridge.updateSessionMetadata([
    {
      sessionId: "ssh-1",
      hostname: "host.example",
      label: "Prod",
      username: "root",
      protocol: "ssh",
      shellType: "bash",
      connected: true,
    },
  ], "chat-1");

  const result = await bridge.dispatchBuiltinRpc("netcatty/getContext", {
    chatSessionId: "chat-1",
  });

  assert.equal(result.hostCount, 1);
  assert.deepEqual(result.hosts[0], {
    sessionId: "ssh-1",
    hostname: "host.example",
    label: "Prod",
    os: "",
    username: "root",
    protocol: "ssh",
    shellType: "bash",
    deviceType: "",
    connected: true,
    hostId: "",
    hostChain: [],
    activePortForwards: [],
  });
});

test("MCP/Catty terminal_execute proxies to worker when terminal sessions live in worker", async () => {
  const requests = [];
  const bridge = loadFreshBridge();
  bridge.init({
    sessions: new Map(),
    electronModule: null,
    terminalWorkerManager: {
      request(channel, payload, options) {
        requests.push({ channel, payload, options });
        return Promise.resolve({ ok: true, stdout: "ok\n", stderr: "", exitCode: 0 });
      },
    },
  });
  bridge.setPermissionMode("auto");
  bridge.setCommandBlocklist([]);
  bridge.setCommandTimeout(23);
  bridge.updateSessionMetadata([
    {
      sessionId: "ssh-1",
      hostname: "host.example",
      protocol: "ssh",
      connected: true,
    },
  ], "chat-1");

  const result = await bridge.dispatchBuiltinRpc("netcatty/exec", {
    sessionId: "ssh-1",
    command: "pwd",
    chatSessionId: "chat-1",
  });

  assert.deepEqual(result, { ok: true, stdout: "ok\n", stderr: "", exitCode: 0 });
  assert.deepEqual(requests, [
    {
      channel: "netcatty:ai:exec",
      payload: {
        sessionId: "ssh-1",
        command: "pwd",
        chatSessionId: "chat-1",
        commandTimeoutMs: 23000,
        sessionMeta: {
          hostname: "host.example",
          label: "",
          os: "",
          username: "",
          protocol: "ssh",
          shellType: "",
          deviceType: "",
          connected: true,
          hostId: "",
          hostChain: [],
          activePortForwards: [],
        },
        enforceWallTimeout: true,
      },
      options: {},
    },
  ]);
});
