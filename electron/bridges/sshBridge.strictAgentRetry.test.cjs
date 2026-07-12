"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const source = fs.readFileSync(path.join(__dirname, "sshBridge.cjs"), "utf8");

test("strict agent failures skip encrypted default-key prompts", () => {
  assert.match(source, /hasStrictTargetAgent/);
  assert.match(source, /!isStrictAgentAuthFailure\(options, err\)/);
  assert.match(source, /failedJump\?\.useSshAgent === true && failedJump\.identitiesOnly === true/);
});
