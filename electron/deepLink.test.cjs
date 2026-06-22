const test = require("node:test");
const assert = require("node:assert/strict");

const {
  collectSshDeepLinkUrls,
  isSshDeepLinkUrl,
  applySshProtocolClientPreference,
  readSshDeepLinkEnabledPreference,
  writeSshDeepLinkEnabledPreference,
} = require("./deepLink.cjs");

test("isSshDeepLinkUrl accepts only ssh URLs", () => {
  assert.equal(isSshDeepLinkUrl("ssh://alice@example.com:2200"), true);
  assert.equal(isSshDeepLinkUrl("SSH://alice@example.com"), true);
  assert.equal(isSshDeepLinkUrl("ssh://example.com:99999"), true);
  assert.equal(isSshDeepLinkUrl("https://example.com"), false);
  assert.equal(isSshDeepLinkUrl("--flag"), false);
});

test("collectSshDeepLinkUrls extracts ssh URLs from process arguments", () => {
  assert.deepEqual(
    collectSshDeepLinkUrls([
      "/Applications/Netcatty.app/Contents/MacOS/Netcatty",
      "--flag",
      "ssh://alice@example.com",
      "file:///tmp/example",
      "ssh://bob@example.net:2222",
    ]),
    ["ssh://alice@example.com", "ssh://bob@example.net:2222"],
  );
});

test("applySshProtocolClientPreference registers or removes the ssh handler", () => {
  const calls = [];
  const app = {
    setAsDefaultProtocolClient: (...args) => {
      calls.push(["set", ...args]);
      return true;
    },
    removeAsDefaultProtocolClient: (...args) => {
      calls.push(["remove", ...args]);
      return true;
    },
  };

  assert.equal(applySshProtocolClientPreference({ app, enabled: true, isDev: false }), true);
  assert.equal(applySshProtocolClientPreference({ app, enabled: false, isDev: false }), true);
  assert.deepEqual(calls, [
    ["set", "ssh"],
    ["remove", "ssh"],
  ]);
});

test("ssh deep link enabled preference persists outside renderer localStorage", () => {
  const fs = require("node:fs");
  const os = require("node:os");
  const path = require("node:path");
  const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-deeplink-"));
  const app = { getPath: () => userDataDir };

  assert.equal(readSshDeepLinkEnabledPreference({ app }), true);
  assert.equal(writeSshDeepLinkEnabledPreference({ app, enabled: false }), true);
  assert.equal(readSshDeepLinkEnabledPreference({ app }), false);
  assert.equal(writeSshDeepLinkEnabledPreference({ app, enabled: true }), true);
  assert.equal(readSshDeepLinkEnabledPreference({ app }), true);
});
