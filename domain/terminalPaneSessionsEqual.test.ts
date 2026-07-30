import assert from 'node:assert/strict';
import test from 'node:test';

import { terminalPaneSessionsEqual } from './terminalPaneSessionsEqual.ts';
import type { TerminalSession } from './models.ts';

const session = (overrides: Partial<TerminalSession> = {}): TerminalSession => ({
  id: 's1',
  hostId: 'h1',
  hostLabel: 'example',
  status: 'connected',
  hostname: 'example.test',
  username: 'root',
  port: 22,
  protocol: 'ssh',
  ...overrides,
});

test('terminalPaneSessionsEqual ignores dynamicTitle and codingCliProviderId', () => {
  const prev = [session({ dynamicTitle: 'old', codingCliProviderId: 'claude' })];
  const next = [session({ dynamicTitle: 'new', codingCliProviderId: 'codex' })];
  assert.equal(terminalPaneSessionsEqual(prev, next), true);
});

test('terminalPaneSessionsEqual detects status and fontSize changes', () => {
  const base = session();
  assert.equal(
    terminalPaneSessionsEqual([base], [session({ status: 'disconnected' })]),
    false,
  );
  assert.equal(
    terminalPaneSessionsEqual([base], [session({ fontSize: 18 })]),
    false,
  );
});

test('terminalPaneSessionsEqual detects workspace membership changes', () => {
  const base = session();
  assert.equal(
    terminalPaneSessionsEqual([base], [session({ workspaceId: 'ws-1' })]),
    false,
  );
});
