const test = require("node:test");
const assert = require("node:assert/strict");

const {
  collectSshDeepLinkUrls,
  isSshDeepLinkUrl,
} = require("./deepLink.cjs");

test("isSshDeepLinkUrl accepts only ssh URLs", () => {
  assert.equal(isSshDeepLinkUrl("ssh://alice@example.com:2200"), true);
  assert.equal(isSshDeepLinkUrl("SSH://alice@example.com"), true);
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
