"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const terminalBridge = require("./terminalBridge.cjs");

test("renderer terminal input reports activity for the matching session", () => {
  const writes = [];
  const activity = [];
  const sessions = new Map([
    ["session-1", {
      proc: { write: (data) => writes.push(data) },
      webContentsId: 1,
    }],
  ]);
  terminalBridge.init({
    sessions,
    electronModule: { webContents: { fromId: () => null } },
    reportOpenedSessionActivity: (event) => activity.push(event),
  });

  terminalBridge.writeToSession({ sender: {} }, {
    sessionId: "session-1",
    data: "pwd\r",
  });

  assert.deepEqual(writes, ["pwd\r"]);
  assert.deepEqual(activity, [
    { sessionId: "session-1", phase: "touch" },
  ]);
});

test("direct-mode close reports ownership cleanup after the backend already exited", () => {
  const activity = [];
  terminalBridge.init({
    sessions: new Map(),
    electronModule: {},
    reportOpenedSessionActivity: (event) => activity.push(event),
  });

  const result = terminalBridge.closeSession({ sender: {} }, {
    sessionId: "already-exited",
    bootEpoch: 3,
  });

  assert.deepEqual(result, { closed: false, reason: "missing" });
  assert.deepEqual(activity, [{ sessionId: "already-exited", phase: "closed" }]);
});

test("stale direct-mode close cannot clean up a newer same-id boot", () => {
  const activity = [];
  const { claimSessionSlot } = require("./sessionBootEpoch.cjs");
  const lifecycle = new Map();
  claimSessionSlot(lifecycle, "reconnected", {}, 4);
  lifecycle.delete("reconnected");
  terminalBridge.init({
    sessions: new Map(),
    electronModule: {},
    reportOpenedSessionActivity: (event) => activity.push(event),
  });

  const stale = terminalBridge.closeSession({ sender: {} }, {
    sessionId: "reconnected",
    bootEpoch: 3,
  });
  assert.deepEqual(stale, { skipped: true, reason: "boot-epoch-mismatch" });
  assert.deepEqual(activity, []);

  const current = terminalBridge.closeSession({ sender: {} }, {
    sessionId: "reconnected",
    bootEpoch: 4,
  });
  assert.deepEqual(current, { closed: false, reason: "missing" });
  assert.deepEqual(activity, [{ sessionId: "reconnected", phase: "closed" }]);
});
