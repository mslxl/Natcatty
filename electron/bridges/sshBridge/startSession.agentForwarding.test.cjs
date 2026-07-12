"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  resolveUnlockedEncryptedKeysForAuth,
  shouldOfferAgentForLogin,
} = require("./startSession.cjs");

test("agent forwarding does not enable agent login after an explicit opt-out", () => {
  assert.equal(shouldOfferAgentForLogin(
    { useSshAgent: false, agentForwarding: true },
    { agent: "/tmp/agent.sock", agentForward: true },
  ), false);
});

test("agent login remains available when it is not explicitly disabled", () => {
  assert.equal(shouldOfferAgentForLogin(
    { agentForwarding: true },
    { agent: "/tmp/agent.sock", agentForward: true },
  ), true);
});

test("strict agent selection excludes unlocked default keys", () => {
  const unlocked = [{ keyName: "id_other", privateKey: "PRIVATE KEY" }];
  assert.deepEqual(resolveUnlockedEncryptedKeysForAuth({
    _unlockedEncryptedKeys: unlocked,
  }, true), []);
  assert.equal(resolveUnlockedEncryptedKeysForAuth({
    _unlockedEncryptedKeys: unlocked,
  }, false), unlocked);
});
