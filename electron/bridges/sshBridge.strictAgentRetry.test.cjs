"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { _isStrictAgentAuthFailure } = require("./sshBridge.cjs");

test("strict agent failures skip encrypted default-key prompts", () => {
  const jumpHosts = [
    { hostname: "shared.example", useSshAgent: false },
    { hostname: "shared.example", useSshAgent: true, identitiesOnly: true },
  ];
  assert.equal(_isStrictAgentAuthFailure({ jumpHosts }, {
    isJumpHostAuthError: true,
    jumpHostIndex: 1,
    jumpHostHostname: "shared.example",
  }), true);
  assert.equal(_isStrictAgentAuthFailure({ jumpHosts }, {
    isJumpHostAuthError: true,
    jumpHostIndex: 0,
    jumpHostHostname: "shared.example",
  }), false);
  assert.equal(_isStrictAgentAuthFailure({ jumpHosts }, {
    isJumpHostAuthError: true,
    jumpHostHostname: "shared.example",
  }), false, "ambiguous legacy errors must not guess the failed hop");
});
