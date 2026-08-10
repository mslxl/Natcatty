import assert from 'node:assert/strict';
import test from 'node:test';

import {
  remapMountedSidePanelTabIds,
  remapSidePanelTabMap,
} from './workspaceSidePanelTabRemap.ts';

test('promote copies preferred member open tool onto the new workspace tab', () => {
  const source = new Map([
    ['term-a', 'ai'],
    ['term-b', 'sftp'],
  ]);

  assert.deepEqual(
    [...remapSidePanelTabMap(source, {
      kind: 'promote',
      fromTabIds: ['term-a', 'term-b'],
      toTabId: 'ws-1',
      preferredFromTabId: 'term-a',
    })],
    [
      ['term-a', 'ai'],
      ['term-b', 'sftp'],
      ['ws-1', 'ai'],
    ],
  );
});

test('promote copies split layouts onto the new workspace tab', () => {
  const layoutA = { focusedPaneId: 'pane-a', root: { type: 'pane', id: 'pane-a', tool: 'ai' } };
  const layoutB = { focusedPaneId: 'pane-b', root: { type: 'pane', id: 'pane-b', tool: 'sftp' } };
  const source = new Map([
    ['term-a', layoutA],
    ['term-b', layoutB],
  ]);

  const next = remapSidePanelTabMap(source, {
    kind: 'promote',
    fromTabIds: ['term-a', 'term-b'],
    toTabId: 'ws-1',
    preferredFromTabId: 'term-a',
  });

  assert.equal(next.get('ws-1'), layoutA);
  assert.equal(next.get('term-a'), layoutA);
});

test('promote is a no-op when the workspace tab already has an open tool', () => {
  const source = new Map([
    ['term-a', 'ai'],
    ['ws-1', 'notes'],
  ]);

  assert.equal(
    remapSidePanelTabMap(source, {
      kind: 'promote',
      fromTabIds: ['term-a'],
      toTabId: 'ws-1',
      preferredFromTabId: 'term-a',
    }),
    source,
  );
});

test('demote copies workspace open tool onto the preferred orphan tab', () => {
  const source = new Map([
    ['ws-1', 'ai'],
    ['term-b', 'sftp'],
  ]);

  assert.deepEqual(
    [...remapSidePanelTabMap(source, {
      kind: 'demote',
      fromTabId: 'ws-1',
      toTabIds: ['term-a', 'term-b'],
      preferredToTabId: 'term-a',
    })],
    [
      ['ws-1', 'ai'],
      ['term-b', 'sftp'],
      ['term-a', 'ai'],
    ],
  );
});

test('demote overwrites a stale preferred member entry kept from merge', () => {
  const source = new Map([
    ['ws-1', 'notes'],
    ['term-a', 'ai'],
  ]);

  assert.deepEqual(
    [...remapSidePanelTabMap(source, {
      kind: 'demote',
      fromTabId: 'ws-1',
      toTabIds: ['term-a', 'term-b'],
      preferredToTabId: 'term-a',
    })],
    [
      ['ws-1', 'notes'],
      ['term-a', 'notes'],
    ],
  );
});

test('mounted tab ids gain the destination tab when the source was mounted', () => {
  assert.deepEqual(
    remapMountedSidePanelTabIds(['term-a'], {
      kind: 'promote',
      fromTabIds: ['term-a', 'term-b'],
      toTabId: 'ws-1',
      preferredFromTabId: 'term-a',
    }),
    ['term-a', 'ws-1'],
  );

  assert.deepEqual(
    remapMountedSidePanelTabIds(['ws-1'], {
      kind: 'demote',
      fromTabId: 'ws-1',
      toTabIds: ['term-a', 'term-b'],
      preferredToTabId: 'term-a',
    }),
    ['ws-1', 'term-a'],
  );
});
