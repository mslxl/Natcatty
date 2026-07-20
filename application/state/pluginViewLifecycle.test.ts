import assert from 'node:assert/strict';
import test from 'node:test';

import {
  consumeClosedPluginViewInstance,
  markPluginViewOpenTokensClosed,
  reconcileClosedPluginView,
  rememberClosedPluginViewInstance,
  resolvePluginViewSnapshotSelection,
  shouldReconcilePluginViewTabCatalog,
  withdrawPluginViewTab,
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
  const retainedTab = view('retained-closed', 'plugin-view:publisher.plugin:view.retained');
  const result = reconcileClosedPluginView({
    current,
    retained: new Map([
      ['closed', retainedTab],
      ['kept', view('retained-kept')],
    ]),
    instanceId: 'retained-closed',
  });

  assert.equal(result.current, current);
  assert.equal(result.matchedCurrent, false);
  assert.equal(result.matchedRetained, true);
  assert.deepEqual([...result.retained.keys()], ['kept']);
  assert.equal(result.closedTabId, retainedTab.tabId);
});

test('explicit native-tab close destroys active and retained instances instead of retaining them', () => {
  const tabId = 'plugin-view:publisher.plugin:view.shared';
  const result = withdrawPluginViewTab({
    current: view('active', tabId),
    retained: new Map([
      ['same-tab', view('retained-same', tabId)],
      ['other-tab', view('retained-other', 'plugin-view:publisher.plugin:view.other')],
    ]),
    tabId,
  });

  assert.equal(result.current, null);
  assert.equal(result.matchedCurrent, true);
  assert.equal(result.matchedRetained, true);
  assert.deepEqual(result.instanceIds, ['active', 'retained-same']);
  assert.deepEqual([...result.retained.keys()], ['other-tab']);
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

test('explicit close marks only in-flight opens owned by the closed surface', () => {
  const first = Symbol('first');
  const second = Symbol('second');
  const explicitlyClosed = new Set<symbol>();
  const opening = new Map<string, Set<symbol>>([
    ['window:main\0view.first', new Set([first])],
    ['window:main\0view.second', new Set([second])],
  ]);

  assert.equal(markPluginViewOpenTokensClosed(
    opening,
    explicitlyClosed,
    'window:main\0view.first',
  ), 1);
  assert.deepEqual([...explicitlyClosed], [first]);
  assert.equal(markPluginViewOpenTokensClosed(opening, explicitlyClosed, null), 0);
});

test('locale-only snapshot refresh keeps the owned view alive without weakening context fail-closed behavior', () => {
  const previous = {
    requestViewId: 'publisher.plugin.view',
    contextKey: '{"netcatty.surface":"view"}',
    value: { id: 'resolved-view' },
  };
  assert.equal(resolvePluginViewSnapshotSelection({
    resolved: null,
    previous,
    loading: true,
    requestedViewId: previous.requestViewId,
    contextKey: previous.contextKey,
  }), previous.value);
  assert.equal(resolvePluginViewSnapshotSelection({
    resolved: null,
    previous,
    loading: true,
    requestedViewId: previous.requestViewId,
    contextKey: '{"netcatty.surface":"terminal/toolbar"}',
  }), null);
  assert.equal(resolvePluginViewSnapshotSelection({
    resolved: null,
    previous,
    loading: false,
    requestedViewId: previous.requestViewId,
    contextKey: previous.contextKey,
  }), null);
});

test('native tab catalog reconciliation pauses only for same-context localized reloads', () => {
  assert.equal(shouldReconcilePluginViewTabCatalog({
    loading: true,
    currentContextKey: 'same',
    loadedContextKey: 'same',
  }), false);
  assert.equal(shouldReconcilePluginViewTabCatalog({
    loading: true,
    currentContextKey: 'new',
    loadedContextKey: 'old',
  }), true);
  assert.equal(shouldReconcilePluginViewTabCatalog({
    loading: false,
    currentContextKey: 'same',
    loadedContextKey: 'same',
  }), true);
});
