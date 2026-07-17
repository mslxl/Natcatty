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
