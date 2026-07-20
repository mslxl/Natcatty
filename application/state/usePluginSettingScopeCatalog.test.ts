import assert from 'node:assert/strict';
import test from 'node:test';

import { resolvePluginSettingScopeSelection } from './usePluginSettingScopeCatalog.ts';

const catalog: NetcattyPluginScopeCatalog = {
  workspace: [{ id: 'workspace-1', label: 'Workspace 1' }],
  host: [{ id: 'host-1', label: 'Host 1' }, { id: 'host-2', label: 'Host 2' }],
  session: [],
  device: [{ id: 'device', label: 'This device' }],
};

test('setting scope selection preserves valid host choices and fills each missing scope', () => {
  assert.deepEqual(resolvePluginSettingScopeSelection(catalog, { host: 'host-2' }), {
    workspace: 'workspace-1',
    host: 'host-2',
    session: undefined,
    device: 'device',
  });
});

test('setting scope selection replaces removed host-owned targets', () => {
  assert.equal(resolvePluginSettingScopeSelection(catalog, { host: 'missing' }).host, 'host-1');
});
