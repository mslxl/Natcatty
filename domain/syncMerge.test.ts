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
    fingerprint: `SHA256:${i}`,
  })) as SyncPayload["knownHosts"];

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
