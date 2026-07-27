"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const compressUploadBridge = require("./compressUploadBridge.cjs");

test("worker-backed compressed pause and resume publish ordered lifecycle", async () => {
  const handlers = new Map();
  const events = [];
  compressUploadBridge.init({
    sftpClients: new Map(),
    transferBridge: {
      broadcastGlobalTransferEvent(event) { events.push(event); },
    },
  });
  compressUploadBridge.registerHandlers({
    handle(channel, handler) { handlers.set(channel, handler); },
  }, {
    terminalWorkerManager: {
      async request(channel) {
        if (channel === "netcatty:compress:pause") {
          return { success: true, deferred: false };
        }
        return { success: true };
      },
    },
  });

  await handlers.get("netcatty:compress:pause")(
    { sender: { id: 1 } },
    { compressionId: "compressed-worker" },
  );
  await handlers.get("netcatty:compress:resume")(
    { sender: { id: 1 } },
    { compressionId: "compressed-worker" },
  );

  assert.deepEqual(events, [
    {
      type: "pausing",
      transferId: "compressed-worker",
      lifecycleEpoch: 1,
      lifecycleState: "pausing",
    },
    {
      type: "paused",
      transferId: "compressed-worker",
      lifecycleEpoch: 1,
      lifecycleState: "paused",
    },
    {
      type: "resumed",
      transferId: "compressed-worker",
      lifecycleEpoch: 2,
      lifecycleState: "transferring",
    },
  ]);
});
