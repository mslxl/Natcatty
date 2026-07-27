import assert from 'node:assert/strict';
import test from 'node:test';

import type { CodingCliProviderId } from '../../domain/codingCliProviders';
import type { DynamicTabTitleMode } from '../../domain/models';
import { createCodingCliSessionSignalController } from './codingCliSessionSignalController';

test('connected sessions stop every icon mutation while dynamic titles are off and resume when enabled', () => {
  let mode: DynamicTabTitleMode = 'agent';
  const session: { id: string; codingCliProviderId?: CodingCliProviderId } = {
    id: 'session-1',
    codingCliProviderId: 'claude',
  };
  const providerUpdates: Array<CodingCliProviderId | null> = [];
  const controller = createCodingCliSessionSignalController({
    getDynamicTabTitleMode: () => mode,
    getSession: (sessionId) => sessionId === session.id ? session : undefined,
    onUpdateSessionCodingCliProvider: (_sessionId, providerId) => {
      providerUpdates.push(providerId);
      session.codingCliProviderId = providerId ?? undefined;
    },
  });

  mode = 'off';
  controller.handleTerminalOutput(session.id, 'Welcome to Claude Code');
  controller.handleCommandSubmitted(session.id, 'opencode');
  controller.handleTerminalTitleChange(session.id, null);
  controller.handleTerminalTitleChange(session.id, 'root@host:~/project');
  controller.handleTerminalTitleChange(session.id, 'OpenAI Codex');

  assert.deepEqual(providerUpdates, []);
  assert.equal(session.codingCliProviderId, 'claude');

  mode = 'agent';
  controller.handleTerminalTitleChange(session.id, 'root@host:~/project');
  controller.handleCommandSubmitted(session.id, 'opencode');

  assert.deepEqual(providerUpdates, [null, 'opencode']);
  assert.equal(session.codingCliProviderId, 'opencode');
});

test('output scanning paused by the setting can detect a real banner after re-enable', () => {
  let mode: DynamicTabTitleMode = 'off';
  const session: { id: string; codingCliProviderId?: CodingCliProviderId } = { id: 'session-1' };
  const providerUpdates: Array<CodingCliProviderId | null> = [];
  const controller = createCodingCliSessionSignalController({
    getDynamicTabTitleMode: () => mode,
    getSession: () => session,
    onUpdateSessionCodingCliProvider: (_sessionId, providerId) => {
      providerUpdates.push(providerId);
      session.codingCliProviderId = providerId ?? undefined;
    },
  });

  controller.handleTerminalOutput(session.id, 'Welcome to Claude Code');
  assert.deepEqual(providerUpdates, []);

  mode = 'agent';
  controller.handleTerminalOutput(session.id, 'Welcome to Claude Code');
  assert.deepEqual(providerUpdates, ['claude']);
});

test('mode changes discard partial and exhausted output scanner state', () => {
  let mode: DynamicTabTitleMode = 'agent';
  const session: { id: string; codingCliProviderId?: CodingCliProviderId } = { id: 'session-1' };
  const providerUpdates: Array<CodingCliProviderId | null> = [];
  const controller = createCodingCliSessionSignalController({
    getDynamicTabTitleMode: () => mode,
    getSession: () => session,
    onUpdateSessionCodingCliProvider: (_sessionId, providerId) => {
      providerUpdates.push(providerId);
      session.codingCliProviderId = providerId ?? undefined;
    },
  });

  controller.handleTerminalOutput(session.id, 'Welcome to Claude ');
  mode = 'off';
  controller.handleDynamicTabTitleModeChange(mode);
  controller.handleTerminalOutput(session.id, 'ignored while disabled');
  mode = 'agent';
  controller.handleDynamicTabTitleModeChange(mode);
  controller.handleTerminalOutput(session.id, 'Code');
  assert.deepEqual(providerUpdates, []);

  controller.handleTerminalOutput(session.id, 'x'.repeat(16384));
  mode = 'off';
  controller.handleTerminalOutput(session.id, 'ignored while disabled');
  mode = 'agent';
  controller.handleTerminalOutput(session.id, 'Welcome to Claude Code');
  assert.deepEqual(providerUpdates, ['claude']);
});

test('switching between enabled modes preserves exhausted output scans', () => {
  let mode: DynamicTabTitleMode = 'agent';
  const session: { id: string; codingCliProviderId?: CodingCliProviderId } = { id: 'session-1' };
  const providerUpdates: Array<CodingCliProviderId | null> = [];
  const controller = createCodingCliSessionSignalController({
    getDynamicTabTitleMode: () => mode,
    getSession: () => session,
    onUpdateSessionCodingCliProvider: (_sessionId, providerId) => {
      providerUpdates.push(providerId);
      session.codingCliProviderId = providerId ?? undefined;
    },
  });

  controller.handleTerminalOutput(session.id, 'x'.repeat(16384));
  mode = 'all';
  controller.handleDynamicTabTitleModeChange(mode);
  controller.handleTerminalOutput(session.id, 'Welcome to Claude Code');
  assert.deepEqual(providerUpdates, []);
});