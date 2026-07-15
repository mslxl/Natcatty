import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import type { Host, ManagedSource, ProxyProfile } from './models';
import { deleteGroup, upsertGroup } from './vaultGroupAgentOps';

const hosts: Host[] = [
  { id: 'host-1', label: 'Prod', hostname: 'prod.test', username: 'root', group: 'prod/web', tags: [], os: 'linux' },
  { id: 'jump-1', label: 'Jump', hostname: 'jump.test', username: 'root', tags: [], os: 'linux' },
];
const proxyProfiles: ProxyProfile[] = [{
  id: 'proxy-1', label: 'Proxy', config: { type: 'socks5', host: '127.0.0.1', port: 1080 }, createdAt: 1,
}];

describe('vaultGroupAgentOps', () => {
  it('renames a group and descendants across configs, hosts, and managed sources', () => {
    const result = upsertGroup({
      groups: ['prod', 'prod/web'], configs: [{ path: 'prod/web', username: 'old' }], hosts,
      managedSources: [{ id: 'source-1', groupName: 'prod/web' } as ManagedSource],
    }, 'prod', '{"username":"deploy","proxyProfileId":"proxy-1","jumpHostIds":["jump-1"]}', [], proxyProfiles, { newPath: 'production' });
    assert.equal(result.ok, true);
    if (!result.ok) return;
    assert.deepEqual(result.state.groups, ['production', 'production/web']);
    assert.equal(result.state.hosts[0]?.group, 'production/web');
    assert.equal(result.state.managedSources[0]?.groupName, 'production/web');
    assert.equal(result.config?.username, 'deploy');
  });

  it('rejects missing identities, proxy profiles, and jump hosts', () => {
    const state = { groups: ['prod'], configs: [], hosts, managedSources: [] };
    assert.equal(upsertGroup(state, 'prod', '{"identityId":"missing"}', [], proxyProfiles).ok, false);
    assert.equal(upsertGroup(state, 'prod', '{"proxyProfileId":"missing"}', [], proxyProfiles).ok, false);
    assert.equal(upsertGroup(state, 'prod', '{"jumpHostIds":["missing"]}', [], proxyProfiles).ok, false);
  });

  it('moves hosts to root by default and refuses managed group deletion', () => {
    const state = { groups: ['prod', 'prod/web'], configs: [{ path: 'prod' }], hosts, managedSources: [] };
    const removed = deleteGroup(state, 'prod', false);
    assert.equal(removed.ok, true);
    if (removed.ok) assert.equal(removed.state.hosts[0]?.group, undefined);
    const managed = deleteGroup({
      ...state, managedSources: [{ id: 'source-1', groupName: 'prod' } as ManagedSource],
    }, 'prod', false);
    assert.equal(managed.ok, false);
  });
});
