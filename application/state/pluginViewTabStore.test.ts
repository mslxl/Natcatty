import assert from 'node:assert/strict';
import test from 'node:test';

import {
  PluginViewTabStore,
  resolvePluginViewRequest,
  toPluginViewTabId,
} from './pluginViewTabStore.ts';

function fixture() {
  let activeTabId = 'vault';
  const store = new PluginViewTabStore({
    getActiveTabId: () => activeTabId,
    setActiveTabId: (next) => { activeTabId = next; },
  });
  return { store, getActiveTabId: () => activeTabId };
}

test('plugin view tab IDs include both plugin and contribution ownership', () => {
  assert.equal(
    toPluginViewTabId('com.example.owner', 'shared.view'),
    'plugin-view:com.example.owner:shared.view',
  );
});

test('closing or withdrawing an active plugin view tab returns focus to a safe root page', () => {
  const first = fixture();
  const tab = first.store.open({
    pluginId: 'com.example.owner',
    pluginName: 'Owner',
    viewId: 'com.example.owner.view',
    title: 'View',
  });
  assert.equal(first.getActiveTabId(), tab.id);
  first.store.close(tab.id);
  assert.equal(first.getActiveTabId(), 'vault');

  const second = fixture();
  second.store.open({
    pluginId: 'com.example.owner',
    pluginName: 'Owner',
    viewId: 'com.example.owner.view',
    title: 'View',
  });
  second.store.retain(new Set());
  assert.equal(second.getActiveTabId(), 'vault');
});

test('an explicit open request takes precedence over the currently active plugin tab', () => {
  const activeTab = { viewId: 'com.example.current', context: { source: 'tab' } };
  assert.deepEqual(resolvePluginViewRequest({
    viewId: 'com.example.requested',
    context: { source: 'menu' },
  }, activeTab), {
    viewId: 'com.example.requested',
    context: { source: 'menu' },
  });
  assert.deepEqual(resolvePluginViewRequest(null, activeTab), activeTab);
});
