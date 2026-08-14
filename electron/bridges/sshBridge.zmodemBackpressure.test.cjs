"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");

test("SSH ZMODEM uploads wire stream backpressure to a real drain wait", () => {
  const bridgeSource = fs.readFileSync(require.resolve("./sshBridge.cjs"), "utf8");
  const startSessionSource = fs.readFileSync(
    require.resolve("./sshBridge/startSession.cjs"),
    "utf8",
  );

  assert.match(
    bridgeSource,
    /createZmodemSentry,\s*waitForWritableDrain/,
    "sshBridge must pass the shared drain helper into the SSH session factory",
  );
  assert.match(
    startSessionSource,
    /waitForTransportDrain\(drainOpts = {}\)[\s\S]*?waitForWritableDrain\(stream, {[\s\S]*?progressIntervalMs: 1000/,
    "SSH ZMODEM must bound stalls while allowing healthy slow-link progress",
  );
});
