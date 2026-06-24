"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  writeMirror,
  serializeMirror,
  disposeMirror,
} = require("./terminalHeadlessMirror.cjs");

test("terminalHeadlessMirror tracks output and serializes snapshot", async () => {
  const sessionId = "mirror-test-session";
  disposeMirror(sessionId);
  writeMirror(sessionId, "hello\r\n", 80, 24);
  await new Promise((resolve) => setTimeout(resolve, 0));
  const { snapshot, alternateScreen } = serializeMirror(sessionId);
  assert.equal(alternateScreen, false);
  assert.match(snapshot, /hello/);
  disposeMirror(sessionId);
});
