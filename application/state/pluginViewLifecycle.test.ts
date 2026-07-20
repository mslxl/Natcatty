import assert from 'node:assert/strict';
import test from 'node:test';

import {
  consumeClosedPluginViewInstance,
  reconcileClosedPluginView,
  rememberClosedPluginViewInstance,
  type HostedPluginViewState,
} from './pluginViewLifecycle.ts';

function view(id: string, tabId?: string): HostedPluginViewState {
  return {
    id,
    viewId: `view.${id}`,
    scopeId: 'window:main',
    retainContextWhenHidden: false,
    ...(tabId ? { tabId } : {}),
  };
}

test('host close events clear the active renderer instance and identify its native tab', () => {
  const current = view('active', 'plugin-view:publisher.plugin:view.active');
  const result = reconcileClosedPluginView({
    current,
    retained: new Map([['retained', view('retained')]]),
    instanceId: 'active',
  });

  assert.equal(result.current, null);
  assert.equal(result.matchedCurrent, true);
  assert.equal(result.closedTabId, current.tabId);
  assert.equal(result.retained.size, 1);
});

test('host close events remove retained instances without dismissing another active view', () => {
  const current = view('active');
  const result = reconcileClosedPluginView({
    current,
    retained: new Map([
      ['closed', view('retained-closed')],
      ['kept', view('retained-kept')],
    ]),
    instanceId: 'retained-closed',
  });

  assert.equal(result.current, current);
  assert.equal(result.matchedCurrent, false);
  assert.equal(result.matchedRetained, true);
  assert.deepEqual([...result.retained.keys()], ['kept']);
  assert.equal(result.closedTabId, undefined);
});

test('an early host close tombstone is consumed when the open response arrives later', () => {
  const tombstones = new Set<string>();
  rememberClosedPluginViewInstance(tombstones, 'instance-early');
  assert.equal(consumeClosedPluginViewInstance(tombstones, 'instance-early'), true);
  assert.equal(consumeClosedPluginViewInstance(tombstones, 'instance-early'), false);

  for (let index = 0; index < 300; index += 1) {
    rememberClosedPluginViewInstance(tombstones, `instance-${index}`);
  }
  assert.equal(tombstones.size, 256);
  assert.equal(tombstones.has('instance-0'), false);
});
