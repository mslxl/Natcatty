const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const {
  buildExternalHostKeyConfigLines,
  buildExternalHostKeySshOptions,
  buildMergedGlobalKnownHostsContent,
  buildVaultKnownHostsContent,
  formatVaultKnownHostLine,
  getDefaultGlobalKnownHostsPaths,
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
  assert.equal(
    formatVaultKnownHostLine({
      hostname: "host.example",
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
  assert.equal(buildVaultKnownHostsContent([]), "");
  assert.equal(buildVaultKnownHostsContent(undefined), "");
});

test("getDefaultGlobalKnownHostsPaths covers Unix and Windows defaults", () => {
  assert.deepEqual(getDefaultGlobalKnownHostsPaths({ platform: "darwin" }), [
    "/etc/ssh/ssh_known_hosts",
    "/etc/ssh/ssh_known_hosts2",
  ]);
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

test("buildMergedGlobalKnownHostsContent merges system globals with vault", () => {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-kh-merge-"));
  try {
    const global1 = path.join(base, "ssh_known_hosts");
    const global2 = path.join(base, "ssh_known_hosts2");
    fs.writeFileSync(global1, "admin.example ssh-ed25519 AAAADMIN\n");
    fs.writeFileSync(global2, "admin2.example ssh-rsa AAAADMIN2\n");

    const content = buildMergedGlobalKnownHostsContent({
      knownHosts: [{
        hostname: "vault.example",
        keyType: "ssh-ed25519",
        publicKey: "ssh-ed25519 AAAVAULT",
      }],
      fs,
      globalPaths: [global1, global2, path.join(base, "missing")],
    });

    assert.match(content, /admin\.example ssh-ed25519 AAAADMIN/);
    assert.match(content, /admin2\.example ssh-rsa AAAADMIN2/);
    assert.match(content, /vault\.example ssh-ed25519 AAAVAULT/);
  } finally {
    fs.rmSync(base, { recursive: true, force: true });
  }
});

test("buildMergedGlobalKnownHostsContent is empty when vault has no usable pins", () => {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-kh-empty-"));
  try {
    const global1 = path.join(base, "ssh_known_hosts");
    fs.writeFileSync(global1, "admin.example ssh-ed25519 AAAADMIN\n");
    assert.equal(
      buildMergedGlobalKnownHostsContent({
        knownHosts: [{ hostname: "x", fingerprint: "only" }],
        fs,
        globalPaths: [global1],
      }),
      "",
    );
    assert.equal(
      buildMergedGlobalKnownHostsContent({
        knownHosts: [],
        fs,
        globalPaths: [global1],
      }),
      "",
    );
  } finally {
    fs.rmSync(base, { recursive: true, force: true });
  }
});

test("resolveExternalStrictHostKeyChecking matches protocol constraints", () => {
  assert.equal(resolveExternalStrictHostKeyChecking({ protocol: "et" }), "accept-new");
  assert.equal(resolveExternalStrictHostKeyChecking({ protocol: "mosh" }), null);
  assert.equal(
    resolveExternalStrictHostKeyChecking({ protocol: "et", verifyHostKeys: false }),
    "no",
  );
  assert.equal(
    resolveExternalStrictHostKeyChecking({ protocol: "mosh", verifyHostKeys: false }),
    "no",
  );
});

test("buildExternalHostKeySshOptions injects merged GlobalKnownHostsFile when verifying", () => {
  const values = buildExternalHostKeySshOptions({
    mergedGlobalKnownHostsPath: "/tmp/merged-kh",
    protocol: "et",
    style: "values",
  });
  assert.deepEqual(values, [
    "GlobalKnownHostsFile=/tmp/merged-kh",
    "StrictHostKeyChecking=accept-new",
  ]);

  const moshArgs = buildExternalHostKeySshOptions({
    mergedGlobalKnownHostsPath: "/tmp/merged-kh",
    protocol: "mosh",
    style: "args",
  });
  assert.deepEqual(moshArgs, [
    "-o", "GlobalKnownHostsFile=/tmp/merged-kh",
  ]);
});

test("buildExternalHostKeySshOptions omits trust file when verification is disabled", () => {
  const disabled = buildExternalHostKeySshOptions({
    mergedGlobalKnownHostsPath: "/tmp/merged-kh",
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
  // Must not keep pointing at the vault/merged trust file.
  assert.equal(disabled.some((part) => String(part).includes("/tmp/merged-kh")), false);
});

test("buildExternalHostKeyConfigLines formats indented jump-host stanzas", () => {
  const lines = buildExternalHostKeyConfigLines({
    mergedGlobalKnownHostsPath: "/tmp/merged-kh",
    protocol: "et",
    quotePath: (v) => `"${v}"`,
  });
  assert.deepEqual(lines, [
    '  GlobalKnownHostsFile "/tmp/merged-kh"',
    "  StrictHostKeyChecking accept-new",
  ]);

  const disabled = buildExternalHostKeyConfigLines({
    emptyKnownHostsPath: "/tmp/empty-kh",
    verifyHostKeys: false,
    protocol: "et",
    quotePath: (v) => `"${v}"`,
  });
  assert.deepEqual(disabled, [
    '  UserKnownHostsFile "/tmp/empty-kh"',
    '  GlobalKnownHostsFile "/tmp/empty-kh"',
    "  StrictHostKeyChecking no",
  ]);
});
