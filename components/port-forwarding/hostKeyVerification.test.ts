import test from "node:test";
import assert from "node:assert/strict";

import {
  createKnownHostFromPortForwardHostKeyInfo,
  isPortForwardHostKeySessionId,
} from "./hostKeyVerification.ts";

test("isPortForwardHostKeySessionId only accepts port-forward tunnel sessions", () => {
  assert.equal(isPortForwardHostKeySessionId("pf-rule-1-123456"), true);
  assert.equal(isPortForwardHostKeySessionId("session-1"), false);
  assert.equal(isPortForwardHostKeySessionId(undefined), false);
});

test("createKnownHostFromPortForwardHostKeyInfo saves the verified host key", () => {
  assert.deepEqual(
    createKnownHostFromPortForwardHostKeyInfo(
      {
        hostname: "jump.internal",
        port: 2200,
        keyType: "ssh-ed25519",
        fingerprint: "abc123",
        publicKey: "ssh-ed25519 AAAA",
      },
      1000,
      "fixed",
    ),
    {
      id: "kh-1000-fixed",
      hostname: "jump.internal",
      port: 2200,
      keyType: "ssh-ed25519",
      publicKey: "ssh-ed25519 AAAA",
      fingerprint: "abc123",
      discoveredAt: 1000,
    },
  );
});
