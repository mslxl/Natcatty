import test from "node:test";
import assert from "node:assert/strict";

import { mergeSyncPayloads } from "./syncMerge.ts";
import type { SyncPayload } from "./sync.ts";

function payload(overrides: Partial<SyncPayload> = {}): SyncPayload {
  return {
    hosts: [],
    keys: [],
    identities: [],
    snippets: [],
    customGroups: [],
    snippetPackages: [],
    portForwardingRules: [],
    groupConfigs: [],
    settings: undefined,
    syncedAt: 0,
    ...overrides,
  };
}

const knownHosts = (n: number): SyncPayload["knownHosts"] =>
  Array.from({ length: n }, (_, i) => ({
    id: `kh-${i}`,
    hostname: `host-${i}.example.com`,
    port: 22,
    keyType: "ssh-ed25519",
    publicKey: `SHA256:${i}`,
    discoveredAt: 1,
  }));

test("mergeSyncPayloads does not carry legacy known hosts forward", () => {
  const result = mergeSyncPayloads(
    payload({ knownHosts: knownHosts(2) }),
    payload(),
    payload({ knownHosts: knownHosts(3) }),
  );

  assert.equal("knownHosts" in result.payload, false);
});

test("mergeSyncPayloads merges reusable proxy profiles by id", () => {
  const localProfile = {
    id: "proxy-local",
    label: "Local Proxy",
    config: { type: "http", host: "local.example.com", port: 3128 },
    createdAt: 1,
    updatedAt: 1,
  };
  const remoteProfile = {
    id: "proxy-remote",
    label: "Remote Proxy",
    config: { type: "socks5", host: "remote.example.com", port: 1080 },
    createdAt: 2,
    updatedAt: 2,
  };

  const result = mergeSyncPayloads(
    payload(),
    payload({ proxyProfiles: [localProfile] } as Partial<SyncPayload>),
    payload({ proxyProfiles: [remoteProfile] } as Partial<SyncPayload>),
  );

  assert.deepEqual(result.payload.proxyProfiles?.map((item) => item.id).sort(), [
    "proxy-local",
    "proxy-remote",
  ]);
});

test("mergeSyncPayloads preserves proxy profiles when remote payload predates them", () => {
  const proxy = {
    id: "proxy-1",
    label: "Office Proxy",
    config: { type: "http", host: "proxy.example.com", port: 3128 },
    createdAt: 1,
  };

  const result = mergeSyncPayloads(
    payload({ proxyProfiles: [proxy] } as Partial<SyncPayload>),
    payload({ proxyProfiles: [proxy] } as Partial<SyncPayload>),
    payload(),
  );

  assert.deepEqual(result.payload.proxyProfiles, [proxy]);
});

test("mergeSyncPayloads keeps missing proxy references visible to connection guards", () => {
  const result = mergeSyncPayloads(
    payload({
      hosts: [{
        id: "host-1",
        label: "Host",
        hostname: "example.com",
        username: "root",
        tags: [],
        os: "linux",
        proxyProfileId: "proxy-1",
      }],
      proxyProfiles: [{
        id: "proxy-1",
        label: "Old Proxy",
        config: { type: "http", host: "old.example.com", port: 3128 },
        createdAt: 1,
      }],
      groupConfigs: [{ path: "prod", proxyProfileId: "proxy-1" }],
    }),
    payload({
      hosts: [{
        id: "host-1",
        label: "Host",
        hostname: "example.com",
        username: "root",
        tags: [],
        os: "linux",
        proxyProfileId: "proxy-1",
      }],
      proxyProfiles: [],
      groupConfigs: [{ path: "prod", proxyProfileId: "proxy-1" }],
    }),
    payload({
      hosts: [{
        id: "host-1",
        label: "Host",
        hostname: "example.com",
        username: "root",
        tags: [],
        os: "linux",
        proxyProfileId: "proxy-1",
      }],
      proxyProfiles: [],
      groupConfigs: [{ path: "prod", proxyProfileId: "proxy-1" }],
    }),
  );

  assert.equal(result.payload.hosts[0]?.proxyProfileId, "proxy-1");
  assert.equal(result.payload.groupConfigs?.[0]?.proxyProfileId, "proxy-1");
});
