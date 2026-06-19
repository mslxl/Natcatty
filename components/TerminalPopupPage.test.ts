import test from 'node:test';
import assert from 'node:assert/strict';

import type { Host, TerminalSession } from '../types';
import type { TerminalPopupPayload } from '../domain/systemManager/types';
import { resolveTerminalPopupHost, resolveTerminalPopupReuseId } from './TerminalPopupPage';

const vaultHost = (overrides: Partial<Host> = {}): Host => ({
  id: 'host-1',
  label: 'Prod',
  hostname: 'prod.example.com',
  username: 'deploy',
  port: 22,
  protocol: 'ssh',
  tags: [],
  os: 'linux',
  moshEnabled: true,
  ...overrides,
});

const sourceSession = (overrides: Partial<TerminalSession> = {}): TerminalSession => ({
  id: 'session-1',
  hostId: 'host-1',
  hostLabel: 'Prod',
  hostname: 'prod.example.com',
  username: 'deploy',
  status: 'connected',
  protocol: 'ssh',
  port: 22,
  moshEnabled: false,
  ...overrides,
});

const popupPayload = (source: TerminalSession): TerminalPopupPayload => ({
  title: 'Prod · docker: api',
  parentSessionId: source.id,
  sourceSession: source,
  startupCommand: 'docker exec -it abc123 sh',
});

test('resolveTerminalPopupHost honors source session transport over saved Mosh host settings', () => {
  const host = resolveTerminalPopupHost(popupPayload(sourceSession()), [vaultHost()]);

  assert.equal(host.id, 'host-1');
  assert.equal(host.hostname, 'prod.example.com');
  assert.equal(host.protocol, 'ssh');
  assert.equal(host.moshEnabled, false);
  assert.equal(host.etEnabled, false);
});

test('resolveTerminalPopupHost still falls back to source session details when the host is missing', () => {
  const host = resolveTerminalPopupHost(popupPayload(sourceSession({ hostId: 'missing' })), []);

  assert.equal(host.id, 'missing');
  assert.equal(host.label, 'Prod');
  assert.equal(host.protocol, 'ssh');
  assert.equal(host.moshEnabled, false);
});

test('resolveTerminalPopupHost does not turn command popups into serial sessions without serial config', () => {
  const host = resolveTerminalPopupHost(
    popupPayload(sourceSession({ protocol: 'serial' })),
    [vaultHost({ protocol: 'serial' })],
  );

  assert.equal(host.protocol, 'ssh');
  assert.equal(host.moshEnabled, false);
});

test('resolveTerminalPopupReuseId uses the explicit reuse id from the prepared source session', () => {
  assert.equal(
    resolveTerminalPopupReuseId(popupPayload(sourceSession({ reuseConnectionFromSessionId: 'session-1' }))),
    'session-1',
  );

  assert.equal(
    resolveTerminalPopupReuseId(popupPayload(sourceSession({ reuseConnectionFromSessionId: undefined }))),
    undefined,
  );
});
