const test = require("node:test");
const assert = require("node:assert/strict");

const {
  buildExternalHostKeyConfigLines,
  buildExternalHostKeySshOptions,
  buildVaultKnownHostsContent,
  formatVaultKnownHostLine,
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

test("buildExternalHostKeySshOptions injects vault GlobalKnownHostsFile", () => {
  const values = buildExternalHostKeySshOptions({
    vaultKnownHostsPath: "/tmp/vault-kh",
    protocol: "et",
    style: "values",
  });
  assert.deepEqual(values, [
    "GlobalKnownHostsFile=/tmp/vault-kh",
    "StrictHostKeyChecking=accept-new",
  ]);

  const moshArgs = buildExternalHostKeySshOptions({
    vaultKnownHostsPath: "/tmp/vault-kh",
    protocol: "mosh",
    style: "args",
  });
  assert.deepEqual(moshArgs, [
    "-o", "GlobalKnownHostsFile=/tmp/vault-kh",
  ]);

  const disabled = buildExternalHostKeySshOptions({
    vaultKnownHostsPath: "/tmp/vault-kh",
    verifyHostKeys: false,
    protocol: "mosh",
    style: "args",
  });
  assert.deepEqual(disabled, [
    "-o", "GlobalKnownHostsFile=/tmp/vault-kh",
    "-o", "StrictHostKeyChecking=no",
  ]);
});

test("buildExternalHostKeyConfigLines formats indented jump-host stanzas", () => {
  const lines = buildExternalHostKeyConfigLines({
    vaultKnownHostsPath: "/tmp/vault-kh",
    protocol: "et",
    quotePath: (v) => `"${v}"`,
  });
  assert.deepEqual(lines, [
    '  GlobalKnownHostsFile "/tmp/vault-kh"',
    "  StrictHostKeyChecking accept-new",
  ]);
});
