import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { haveSameVaultAgentSnapshot } from './useVaultAgentBridge';

type Snapshot = Parameters<typeof haveSameVaultAgentSnapshot>[0];

describe('haveSameVaultAgentSnapshot', () => {
  it('compares every snapshot field by reference', () => {
    const snapshot: Snapshot = {
      hosts: [], notes: [], snippets: [], customGroups: [], groupConfigs: [],
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
