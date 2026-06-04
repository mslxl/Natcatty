"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");
const os = require("node:os");

const { detectCodebuddyAuthPresence, getCodebuddyConfigDir } = require("./codebuddyAuth.cjs");

test("getCodebuddyConfigDir: defaults to ~/.codebuddy", () => {
  assert.equal(getCodebuddyConfigDir(), path.join(os.homedir(), ".codebuddy"));
});

test("detectCodebuddyAuthPresence: CODEBUDDY_AUTH_TOKEN in env => 'auth-token'", () => {
  assert.equal(detectCodebuddyAuthPresence({ CODEBUDDY_AUTH_TOKEN: "tok-x" }, () => false), "auth-token");
});

test("detectCodebuddyAuthPresence: CODEBUDDY_AUTH_TOKEN takes precedence over settings file", () => {
  assert.equal(
    detectCodebuddyAuthPresence({ CODEBUDDY_AUTH_TOKEN: "tok" }, () => true),
    "auth-token",
  );
});

test("detectCodebuddyAuthPresence: blank CODEBUDDY_AUTH_TOKEN is ignored", () => {
  assert.equal(detectCodebuddyAuthPresence({ CODEBUDDY_AUTH_TOKEN: "   " }, () => false), "none");
});

test("detectCodebuddyAuthPresence: CODEBUDDY_API_KEY is ignored (not a valid auth method)", () => {
  assert.equal(detectCodebuddyAuthPresence({ CODEBUDDY_API_KEY: "sk-x" }, () => false), "none");
});

test("detectCodebuddyAuthPresence: settings.json exists => 'settings-file'", () => {
  const settingsPath = path.join(os.homedir(), ".codebuddy", "settings.json");
  const result = detectCodebuddyAuthPresence(
    {},
    (p) => p === settingsPath,
  );
  assert.equal(result, "settings-file");
});

test("detectCodebuddyAuthPresence: nothing => 'none'", () => {
  assert.equal(detectCodebuddyAuthPresence({}, () => false), "none");
});
