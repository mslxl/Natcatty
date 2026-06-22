import test from "node:test";
import assert from "node:assert/strict";
import type { Host } from "./models";
import {
  buildSshDeepLinkConnectionHost,
  buildSshDeepLinkHostDraft,
  findSshDeepLinkHost,
  parseSshDeepLink,
  shouldHandleSshDeepLink,
} from "./sshDeepLink";

const host = (overrides: Partial<Host>): Host => ({
  id: overrides.id || "host-1",
  label: overrides.label || "Example",
  hostname: overrides.hostname || "example.com",
  username: overrides.username ?? "root",
  port: overrides.port,
  group: "",
  tags: [],
  os: "linux",
  protocol: overrides.protocol ?? "ssh",
  ...overrides,
});

test("parseSshDeepLink accepts username host and port", () => {
  assert.deepEqual(parseSshDeepLink("ssh://alice@example.com:2200"), {
    rawUrl: "ssh://alice@example.com:2200",
    username: "alice",
    hostname: "example.com",
    port: 2200,
  });
});

test("parseSshDeepLink accepts IPv6 hosts", () => {
  assert.deepEqual(parseSshDeepLink("ssh://bob@[2001:db8::10]:2222"), {
    rawUrl: "ssh://bob@[2001:db8::10]:2222",
    username: "bob",
    hostname: "2001:db8::10",
    port: 2222,
  });
});

test("parseSshDeepLink rejects unsupported or incomplete links", () => {
  assert.equal(parseSshDeepLink("https://example.com"), null);
  assert.equal(parseSshDeepLink("ssh://"), null);
  assert.equal(parseSshDeepLink("ssh://example.com:99999"), null);
});

test("shouldHandleSshDeepLink respects the user setting", () => {
  assert.equal(shouldHandleSshDeepLink("ssh://alice@example.com", true), true);
  assert.equal(shouldHandleSshDeepLink("ssh://alice@example.com", false), false);
  assert.equal(shouldHandleSshDeepLink("https://example.com", true), false);
});

test("findSshDeepLinkHost matches saved ssh hosts by username hostname and port", () => {
  const hosts = [
    host({ id: "wrong-port", hostname: "example.com", username: "alice", port: 22 }),
    host({ id: "match", hostname: "example.com", username: "alice", port: 2200 }),
    host({ id: "telnet", hostname: "example.com", username: "alice", port: 2200, protocol: "telnet" }),
  ];

  const match = findSshDeepLinkHost(hosts, parseSshDeepLink("ssh://alice@example.com:2200")!);

  assert.equal(match?.id, "match");
});

test("findSshDeepLinkHost avoids ambiguous saved hosts", () => {
  const hosts = [
    host({ id: "one", hostname: "example.com", username: "alice", port: 2200 }),
    host({ id: "two", hostname: "example.com", username: "alice", port: 2222 }),
  ];

  const match = findSshDeepLinkHost(hosts, parseSshDeepLink("ssh://alice@example.com")!);

  assert.equal(match, null);
});

test("findSshDeepLinkHost treats omitted ports as the ssh default", () => {
  const hosts = [
    host({ id: "custom-port", hostname: "example.com", username: "alice", port: 2200 }),
    host({ id: "default-port", hostname: "example.com", username: "alice" }),
  ];

  const match = findSshDeepLinkHost(hosts, parseSshDeepLink("ssh://alice@example.com")!);

  assert.equal(match?.id, "default-port");
});

test("buildSshDeepLinkConnectionHost forces a saved host to open with ssh", () => {
  const savedHost = host({
    id: "saved",
    hostname: "example.com",
    username: "alice",
    moshEnabled: true,
    etEnabled: true,
  });

  const connectionHost = buildSshDeepLinkConnectionHost(savedHost);

  assert.equal(connectionHost.protocol, "ssh");
  assert.equal(connectionHost.moshEnabled, false);
  assert.equal(connectionHost.etEnabled, false);
  assert.equal(savedHost.moshEnabled, true);
  assert.equal(savedHost.etEnabled, true);
});

test("buildSshDeepLinkHostDraft prepares an editable new ssh host", () => {
  const draft = buildSshDeepLinkHostDraft(
    parseSshDeepLink("ssh://alice@example.com:2200")!,
    { id: "new-id", now: 123 },
  );

  assert.deepEqual(draft, {
    id: "new-id",
    label: "alice@example.com",
    hostname: "example.com",
    username: "alice",
    port: 2200,
    group: "",
    tags: [],
    os: "linux",
    protocol: "ssh",
    createdAt: 123,
  });
});
