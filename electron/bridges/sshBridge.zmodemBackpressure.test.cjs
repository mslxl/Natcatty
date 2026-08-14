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
    /waitForTransportDrain\(drainOpts\)\s*{\s*return waitForWritableDrain\(stream, drainOpts\);\s*}/,
    "SSH ZMODEM must wait on the active shell stream after write() returns false",
  );
});
