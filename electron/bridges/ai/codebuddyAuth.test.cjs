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
  assert.equal(detectCodebuddyAuthPresence({ CODEBUDDY_AUTH_TOKEN: "tok-x" }, () => null), "auth-token");
});

test("detectCodebuddyAuthPresence: CODEBUDDY_AUTH_TOKEN takes precedence over settings file", () => {
  assert.equal(
    detectCodebuddyAuthPresence({ CODEBUDDY_AUTH_TOKEN: "tok" }, () => '{"authToken":"file-tok"}'),
    "auth-token",
  );
});

test("detectCodebuddyAuthPresence: blank CODEBUDDY_AUTH_TOKEN is ignored", () => {
  assert.equal(detectCodebuddyAuthPresence({ CODEBUDDY_AUTH_TOKEN: "   " }, () => null), "none");
});

test("detectCodebuddyAuthPresence: CODEBUDDY_API_KEY is ignored (not a valid auth method)", () => {
  assert.equal(detectCodebuddyAuthPresence({ CODEBUDDY_API_KEY: "sk-x" }, () => null), "none");
});

test("detectCodebuddyAuthPresence: settings.json with authToken => 'settings-file'", () => {
  assert.equal(
    detectCodebuddyAuthPresence({}, () => '{"authToken":"real-token"}'),
    "settings-file",
  );
});

test("detectCodebuddyAuthPresence: settings.json with apiKeyHelper => 'settings-file'", () => {
  assert.equal(
    detectCodebuddyAuthPresence({}, () => '{"apiKeyHelper":"helper-cmd"}'),
    "settings-file",
  );
});

test("detectCodebuddyAuthPresence: empty settings.json => 'none'", () => {
  assert.equal(
    detectCodebuddyAuthPresence({}, () => ""),
    "none",
  );
});

test("detectCodebuddyAuthPresence: malformed JSON in settings.json => 'none'", () => {
  assert.equal(
    detectCodebuddyAuthPresence({}, () => "{not valid json"),
    "none",
  );
});

test("detectCodebuddyAuthPresence: settings.json without auth fields => 'none'", () => {
  assert.equal(
    detectCodebuddyAuthPresence({}, () => '{"theme":"dark","language":"en"}'),
    "none",
  );
});

test("detectCodebuddyAuthPresence: settings.json with blank authToken => 'none'", () => {
  assert.equal(
    detectCodebuddyAuthPresence({}, () => '{"authToken":"   "}'),
    "none",
  );
});

test("detectCodebuddyAuthPresence: no file => 'none'", () => {
  assert.equal(detectCodebuddyAuthPresence({}, () => null), "none");
});
