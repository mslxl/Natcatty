import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { haveSameVaultAgentSnapshot, resolveVaultAgentEffectiveHost } from './useVaultAgentBridge';

type Snapshot = Parameters<typeof haveSameVaultAgentSnapshot>[0];

describe('haveSameVaultAgentSnapshot', () => {
  it('compares every snapshot field by reference', () => {
    const snapshot: Snapshot = {
      hosts: [], keys: [], notes: [], snippets: [], customGroups: [], groupConfigs: [],
      portForwardingRules: [], managedSources: [],
    };
    assert.equal(haveSameVaultAgentSnapshot(snapshot, { ...snapshot }), true);
    for (const key of Object.keys(snapshot) as Array<keyof Snapshot>) {
      assert.equal(
        haveSameVaultAgentSnapshot(snapshot, { ...snapshot, [key]: [] }),
        false,
        key,
      );
    }
  });
});

describe('resolveVaultAgentEffectiveHost', () => {
  it('uses the latest snapshotted group defaults', () => {
    const host = {
      id: 'host-1', label: 'Host', hostname: 'host.test', username: 'root',
      group: 'production', tags: [], os: 'linux',
    } as const;

    assert.equal(
      resolveVaultAgentEffectiveHost(host, [{ path: 'production', protocol: 'telnet' }], []).protocol,
      'telnet',
    );
  });
});
