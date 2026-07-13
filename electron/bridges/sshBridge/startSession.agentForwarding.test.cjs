"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  resolveUnlockedEncryptedKeysForAuth,
  shouldOfferAgentForLogin,
  shouldPrepareSystemAgentForLogin,
  shouldPromoteCachedAuthMethod,
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

test("direct SSH allows only a restricted selected agent-backed key", () => {
  const selectedAgentKey = {
    authMethod: "key",
    useSshAgent: true,
    identitiesOnly: true,
    agentPublicKeys: ["ssh-ed25519 AAAASELECTED"],
  };
  assert.equal(shouldPrepareSystemAgentForLogin(selectedAgentKey), true);
  assert.equal(shouldOfferAgentForLogin(selectedAgentKey, { agent: {} }), true);

  const selectedReferencedKey = {
    ...selectedAgentKey,
    agentPublicKeys: [],
    identityFilePaths: ["~/.ssh/id_work"],
  };
  assert.equal(shouldPrepareSystemAgentForLogin(selectedReferencedKey), true);
  assert.equal(shouldOfferAgentForLogin(selectedReferencedKey, { agent: {} }), true);

  assert.equal(shouldPrepareSystemAgentForLogin({
    ...selectedAgentKey,
    agentPublicKeys: [],
    identityFilePaths: [],
  }), false);
  assert.equal(shouldOfferAgentForLogin({
    ...selectedAgentKey,
    identitiesOnly: false,
  }, { agent: {} }), false);
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

test("explicit auth modes exclude unlocked unrelated default keys", () => {
  const unlocked = [{ keyName: "id_other", privateKey: "PRIVATE KEY" }];
  for (const authMethod of ["password", "key", "certificate"]) {
    assert.deepEqual(resolveUnlockedEncryptedKeysForAuth({
      authMethod,
      _unlockedEncryptedKeys: unlocked,
    }, false), []);
  }
  assert.equal(resolveUnlockedEncryptedKeysForAuth({
    authMethod: "auto",
    _unlockedEncryptedKeys: unlocked,
  }, false), unlocked);
});

test("cached methods cannot override explicit authentication ordering", () => {
  for (const authMethod of ["password", "key", "certificate"]) {
    assert.equal(shouldPromoteCachedAuthMethod(authMethod, "password"), false);
    assert.equal(shouldPromoteCachedAuthMethod(authMethod, "keyboard-interactive"), false);
  }
  assert.equal(shouldPromoteCachedAuthMethod("auto", "password"), false);
  assert.equal(shouldPromoteCachedAuthMethod("auto", "keyboard-interactive"), false);
  assert.equal(shouldPromoteCachedAuthMethod("auto", "agent"), true);
  assert.equal(shouldPromoteCachedAuthMethod("auto", "publickey-default-id_work"), true);
  assert.equal(shouldPromoteCachedAuthMethod(undefined, "password"), true);
});
