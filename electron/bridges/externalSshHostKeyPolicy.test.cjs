const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");

const {
  buildAuthoritativeKnownHostsContent,
  buildExternalHostKeyConfigLines,
  buildExternalHostKeySshOptions,
  buildVaultKnownHostsContent,
  filterKnownHostsContentExcludingVaultHosts,
  formatVaultKnownHostLine,
  getDefaultGlobalKnownHostsPaths,
  quoteOpenSshOptionValue,
  resolveExternalStrictHostKeyChecking,
} = require("./externalSshHostKeyPolicy.cjs");

test("formatVaultKnownHostLine builds OpenSSH known_hosts lines", () => {
  assert.equal(
    formatVaultKnownHostLine({
      hostname: "host.example",
      port: 22,
      keyType: "ssh-ed25519",
      publicKey: "ssh-ed25519 AAAABASE64",
    }),
    "host.example ssh-ed25519 AAAABASE64",
  );
  assert.equal(
    formatVaultKnownHostLine({
      hostname: "host.example",
      port: 2222,
      keyType: "ssh-ed25519",
      publicKey: "AAAABASE64",
    }),
    "[host.example]:2222 ssh-ed25519 AAAABASE64",
  );
});

test("formatVaultKnownHostLine skips fingerprint-only vault entries", () => {
  assert.equal(
    formatVaultKnownHostLine({
      hostname: "host.example",
      keyType: "ssh-ed25519",
      publicKey: "SHA256:abcdef",
      fingerprint: "abcdef",
    }),
    null,
  );
});

test("buildVaultKnownHostsContent joins usable vault entries", () => {
  const content = buildVaultKnownHostsContent([
    {
      hostname: "a.example",
      keyType: "ssh-ed25519",
      publicKey: "ssh-ed25519 AAAAa",
    },
    { hostname: "skip.example", fingerprint: "only" },
    {
      hostname: "b.example",
      port: 2200,
      keyType: "ssh-rsa",
      publicKey: "ssh-rsa AAAAb",
    },
  ]);
  assert.equal(
    content,
    "a.example ssh-ed25519 AAAAa\n[b.example]:2200 ssh-rsa AAAAb\n",
  );
});

test("getDefaultGlobalKnownHostsPaths covers Unix and Windows defaults", () => {
  assert.deepEqual(getDefaultGlobalKnownHostsPaths({ platform: "linux" }), [
    "/etc/ssh/ssh_known_hosts",
    "/etc/ssh/ssh_known_hosts2",
  ]);
  assert.deepEqual(
    getDefaultGlobalKnownHostsPaths({ platform: "win32", programData: "C:\\ProgramData" }),
    [
      path.join("C:\\ProgramData", "ssh", "ssh_known_hosts"),
      path.join("C:\\ProgramData", "ssh", "ssh_known_hosts2"),
    ],
  );
});

test("filterKnownHostsContentExcludingVaultHosts drops conflicting system pins", () => {
  const content = [
    "host.example ssh-ed25519 AAASYSTEM",
    "other.example ssh-rsa AAAOTHER",
    "[host.example]:2222 ssh-ed25519 AAAPORT",
    "# comment kept",
  ].join("\n");
  const filtered = filterKnownHostsContentExcludingVaultHosts(content, [
    { hostname: "host.example", port: 22 },
  ]);
  assert.doesNotMatch(filtered, /AAASYSTEM/);
  assert.match(filtered, /other\.example ssh-rsa AAAOTHER/);
  assert.match(filtered, /\[host\.example\]:2222/);
  assert.match(filtered, /# comment kept/);
});

test("filterKnownHostsContentExcludingVaultHosts matches hashed host entries", () => {
  const hostname = "hashed.example";
  const salt = crypto.randomBytes(20);
  const digest = crypto.createHmac("sha1", salt).update(hostname).digest("base64");
  const hostField = `|1|${salt.toString("base64")}|${digest}`;
  const content = `${hostField} ssh-ed25519 AAAHASHED\nother.example ssh-rsa AAAOTHER\n`;
  const filtered = filterKnownHostsContentExcludingVaultHosts(content, [
    { hostname, port: 22 },
  ]);
  assert.doesNotMatch(filtered, /AAAHASHED/);
  assert.match(filtered, /other\.example/);
});

test("buildAuthoritativeKnownHostsContent makes vault authoritative over system pins", () => {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-kh-auth-"));
  try {
    const global1 = path.join(base, "ssh_known_hosts");
    const user1 = path.join(base, "user_known_hosts");
    fs.writeFileSync(global1, "host.example ssh-ed25519 AAAGLOBAL\nadmin.example ssh-rsa AAAADMIN\n");
    fs.writeFileSync(user1, "host.example ssh-ed25519 AAAUSER\nother.example ssh-ed25519 AAAOTHER\n");

    const content = buildAuthoritativeKnownHostsContent({
      knownHosts: [{
        hostname: "host.example",
        keyType: "ssh-ed25519",
        publicKey: "ssh-ed25519 AAAVAULT",
      }],
      fs,
      globalPaths: [global1],
      userPaths: [user1],
    });

    assert.match(content, /host\.example ssh-ed25519 AAAVAULT/);
    assert.doesNotMatch(content, /AAAGLOBAL/);
    assert.doesNotMatch(content, /AAAUSER/);
    assert.match(content, /admin\.example ssh-rsa AAAADMIN/);
    assert.match(content, /other\.example ssh-ed25519 AAAOTHER/);
  } finally {
    fs.rmSync(base, { recursive: true, force: true });
  }
});

test("buildAuthoritativeKnownHostsContent is empty when vault has no usable pins", () => {
  assert.equal(
    buildAuthoritativeKnownHostsContent({
      knownHosts: [{ hostname: "x", fingerprint: "only" }],
      fs,
      globalPaths: [],
      userPaths: [],
    }),
    "",
  );
});

test("resolveExternalStrictHostKeyChecking matches protocol constraints", () => {
  assert.equal(resolveExternalStrictHostKeyChecking({ protocol: "et" }), "accept-new");
  assert.equal(resolveExternalStrictHostKeyChecking({ protocol: "mosh" }), "ask");
  assert.equal(
    resolveExternalStrictHostKeyChecking({ protocol: "et", verifyHostKeys: false }),
    "no",
  );
  assert.equal(
    resolveExternalStrictHostKeyChecking({ protocol: "mosh", verifyHostKeys: false }),
    "no",
  );
});

test("quoteOpenSshOptionValue quotes paths with whitespace", () => {
  assert.equal(quoteOpenSshOptionValue("/tmp/plain"), "/tmp/plain");
  assert.equal(
    quoteOpenSshOptionValue("/Users/Foo Bar/known_hosts"),
    '"/Users/Foo Bar/known_hosts"',
  );
});

test("buildExternalHostKeySshOptions uses authoritative trust for both slots", () => {
  const values = buildExternalHostKeySshOptions({
    authoritativeKnownHostsPath: "/tmp/auth-kh",
    protocol: "et",
    style: "values",
  });
  assert.deepEqual(values, [
    "UserKnownHostsFile=/tmp/auth-kh",
    "GlobalKnownHostsFile=/tmp/auth-kh",
    "StrictHostKeyChecking=accept-new",
  ]);

  const moshArgs = buildExternalHostKeySshOptions({
    authoritativeKnownHostsPath: "/tmp/auth-kh",
    protocol: "mosh",
    style: "args",
  });
  assert.deepEqual(moshArgs, [
    "-o", "UserKnownHostsFile=/tmp/auth-kh",
    "-o", "GlobalKnownHostsFile=/tmp/auth-kh",
    "-o", "StrictHostKeyChecking=ask",
  ]);
});

test("buildExternalHostKeySshOptions quotes whitespace paths", () => {
  const values = buildExternalHostKeySshOptions({
    authoritativeKnownHostsPath: "/tmp/user name/auth-kh",
    protocol: "et",
    style: "values",
  });
  assert.deepEqual(values, [
    'UserKnownHostsFile="/tmp/user name/auth-kh"',
    'GlobalKnownHostsFile="/tmp/user name/auth-kh"',
    "StrictHostKeyChecking=accept-new",
  ]);
});

test("buildExternalHostKeySshOptions neutralizes trust when verification is disabled", () => {
  const disabled = buildExternalHostKeySshOptions({
    authoritativeKnownHostsPath: "/tmp/auth-kh",
    emptyKnownHostsPath: "/tmp/empty-kh",
    verifyHostKeys: false,
    protocol: "mosh",
    style: "args",
  });
  assert.deepEqual(disabled, [
    "-o", "UserKnownHostsFile=/tmp/empty-kh",
    "-o", "GlobalKnownHostsFile=/tmp/empty-kh",
    "-o", "StrictHostKeyChecking=no",
  ]);
  assert.equal(disabled.some((part) => String(part).includes("/tmp/auth-kh")), false);
});

test("buildExternalHostKeyConfigLines formats indented jump-host stanzas", () => {
  const lines = buildExternalHostKeyConfigLines({
    authoritativeKnownHostsPath: "/tmp/auth-kh",
    protocol: "et",
  });
  assert.deepEqual(lines, [
    "  UserKnownHostsFile /tmp/auth-kh",
    "  GlobalKnownHostsFile /tmp/auth-kh",
    "  StrictHostKeyChecking accept-new",
  ]);
});
