"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const Module = require("node:module");
const path = require("node:path");

/**
 * Drive the shipped broadcastGlobalTransferEvent entry point with a stubbed
 * electron BrowserWindow — proves fan-out does not require a panel sender.
 */
test("broadcastGlobalTransferEvent fans progress to all live BrowserWindows", () => {
  const sent = [];
  const originalLoad = Module._load;
  Module._load = function load(request, parent, isMain) {
    if (request === "electron") {
      return {
        BrowserWindow: {
          getAllWindows() {
            return [
              {
                isDestroyed: () => false,
                webContents: {
                  isDestroyed: () => false,
                  send(channel, payload) {
                    sent.push({ channel, payload });
                  },
                },
              },
              {
                isDestroyed: () => true,
                webContents: { isDestroyed: () => false, send() { throw new Error("dead"); } },
              },
            ];
          },
        },
      };
    }
    return originalLoad(request, parent, isMain);
  };

  try {
    // Fresh load so the stub is used.
    const bridgePath = path.join(__dirname, "transferBridge.cjs");
    delete require.cache[require.resolve(bridgePath)];
    const bridge = require(bridgePath);
    assert.equal(typeof bridge.broadcastGlobalTransferEvent, "function");

    bridge.broadcastGlobalTransferEvent({
      type: "progress",
      transferId: "t-fanout",
      transferred: 50,
      totalBytes: 100,
      speed: 10,
    });

    assert.equal(sent.length, 1);
    assert.equal(sent[0].channel, "netcatty:sftp:global-transfer");
    assert.equal(sent[0].payload.transferId, "t-fanout");
    assert.equal(sent[0].payload.type, "progress");
    assert.equal(sent[0].payload.transferred, 50);
  } finally {
    Module._load = originalLoad;
    try {
      delete require.cache[require.resolve(path.join(__dirname, "transferBridge.cjs"))];
    } catch { /* ignore */ }
  }
});

test("broadcastGlobalTransferEvent no-ops without transferId", () => {
  const bridge = require("./transferBridge.cjs");
  assert.doesNotThrow(() => bridge.broadcastGlobalTransferEvent({ type: "progress" }));
  assert.doesNotThrow(() => bridge.broadcastGlobalTransferEvent(null));
});

test("worker-backed pause and resume fan authoritative lifecycle to every window", async () => {
  const sent = [];
  const originalLoad = Module._load;
  Module._load = function load(request, parent, isMain) {
    if (request === "electron") {
      return {
        BrowserWindow: {
          getAllWindows: () => [{
            isDestroyed: () => false,
            webContents: {
              isDestroyed: () => false,
              send(channel, payload) { sent.push({ channel, payload }); },
            },
          }],
        },
      };
    }
    return originalLoad(request, parent, isMain);
  };

  const bridgePath = path.join(__dirname, "transferBridge.cjs");
  try {
    delete require.cache[require.resolve(bridgePath)];
    const bridge = require(bridgePath);
    const handlers = new Map();
    bridge.registerHandlers({
      handle(channel, handler) { handlers.set(channel, handler); },
    }, {
      terminalWorkerManager: {
        async request(channel) {
          if (channel === "netcatty:transfer:pause") {
            return { success: true, checkpointBytes: 50, resumeStage: "upload" };
          }
          return { success: true };
        },
      },
    });

    await handlers.get("netcatty:transfer:pause")(
      { sender: { id: 1 } },
      { transferId: "worker-transfer" },
    );
    await handlers.get("netcatty:transfer:resume")(
      { sender: { id: 1 } },
      { transferId: "worker-transfer" },
    );

    const lifecycle = sent
      .filter((entry) => entry.channel === "netcatty:sftp:global-transfer")
      .map((entry) => entry.payload);
    assert.deepEqual(lifecycle, [
      {
        type: "pausing",
        transferId: "worker-transfer",
        lifecycleEpoch: 1,
        lifecycleState: "pausing",
      },
      {
        type: "paused",
        transferId: "worker-transfer",
        checkpointBytes: 50,
        resumeStage: "upload",
        downloadCheckpointBytes: undefined,
        uploadCheckpointBytes: undefined,
        sourceFingerprint: undefined,
        lifecycleEpoch: 1,
        lifecycleState: "paused",
      },
      {
        type: "resumed",
        transferId: "worker-transfer",
        lifecycleEpoch: 2,
        lifecycleState: "transferring",
      },
    ]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[require.resolve(bridgePath)];
  }
});
