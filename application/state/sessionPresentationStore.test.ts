import assert from 'node:assert/strict';
import test from 'node:test';

import {
  retainStableSessionsIgnoringPresentation,
  terminalPaneSessionsEqual,
} from '../../domain/terminalPaneSessionsEqual.ts';
import { topTabsSessionsEqual } from '../../domain/topTabsSessionsEqual.ts';
import {
  applySessionPresentation,
  publishSessionCodingCliProvider,
  publishSessionDynamicTitle,
  sessionPresentationStore,
} from './sessionPresentationStore.ts';

const session = (overrides: Record<string, unknown> = {}) => ({
  id: 's1',
  hostId: 'h1',
  hostLabel: 'host',
  username: 'root',
  hostname: 'example.test',
  status: 'connected' as const,
  ...overrides,
});

test('presentation store notifies on title and provider updates', () => {
  sessionPresentationStore.clearSession('s1');
  const versions: number[] = [];
  const unsub = sessionPresentationStore.subscribe(() => {
    versions.push(sessionPresentationStore.getVersion());
  });
  const base = sessionPresentationStore.getVersion();
  publishSessionDynamicTitle('s1', 'Claude Code');
  assert.equal(sessionPresentationStore.getPresentation('s1')?.dynamicTitle, 'Claude Code');
  publishSessionCodingCliProvider('s1', 'claude');
  assert.equal(sessionPresentationStore.getPresentation('s1')?.codingCliProviderId, 'claude');
  assert.ok(sessionPresentationStore.getVersion() > base);
  assert.ok(versions.length >= 2);
  unsub();
  sessionPresentationStore.clearSession('s1');
});

test('title-only session changes stay equal for TopTabs structural compare', () => {
  const prev = [session({ dynamicTitle: 'old' })];
  const next = [session({ dynamicTitle: 'new', codingCliProviderId: 'claude' })];
  assert.equal(topTabsSessionsEqual(prev, next), true);
  assert.equal(terminalPaneSessionsEqual(prev, next), true);
  assert.equal(
    topTabsSessionsEqual(prev, [session({ status: 'disconnected' })]),
    false,
  );
});

test('applySessionPresentation overlays store onto orphan-style snapshots', () => {
  sessionPresentationStore.clearSession('orphan-1');
  publishSessionDynamicTitle('orphan-1', 'agent: refactor');
  publishSessionCodingCliProvider('orphan-1', 'claude');
  const base = session({ id: 'orphan-1', dynamicTitle: 'stale', codingCliProviderId: undefined });
  const merged = applySessionPresentation(base);
  assert.equal(merged.dynamicTitle, 'agent: refactor');
  assert.equal(merged.codingCliProviderId, 'claude');
  // Same store values → retain object identity when already up to date.
  assert.equal(applySessionPresentation(merged), merged);
  sessionPresentationStore.clearSession('orphan-1');
});

test('retainStableSessionsIgnoringPresentation keeps array identity on title-only churn', () => {
  const prev = [session({ dynamicTitle: 'old' })];
  const next = [session({ dynamicTitle: 'new', codingCliProviderId: 'codex' })];
  const retained = retainStableSessionsIgnoringPresentation(prev, next);
  assert.equal(retained, prev);
  const structural = [session({ status: 'disconnected' })];
  assert.notEqual(retainStableSessionsIgnoringPresentation(prev, structural), prev);
});

test('applySessionPresentation is the shared overlay for focus sidebar and panes', () => {
  sessionPresentationStore.clearSession('focus-1');
  publishSessionDynamicTitle('focus-1', 'agent: search me');
  const base = session({ id: 'focus-1', dynamicTitle: undefined });
  const merged = applySessionPresentation(base);
  assert.equal(merged.dynamicTitle, 'agent: search me');
  assert.ok(merged.dynamicTitle?.toLowerCase().includes('search'));
  sessionPresentationStore.clearSession('focus-1');
});
