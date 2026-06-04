import test from 'node:test';
import assert from 'node:assert/strict';

import { buildManagedAgentState } from '../settings/tabs/ai/managedAgentState';
import type { ExternalAgentConfig } from '../../infrastructure/ai/types';

test('buildManagedAgentState removes stale managed agents when path detection fails', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_codex',
      name: 'Codex CLI',
      command: '/usr/local/bin/codex',
      enabled: true,
      acpCommand: 'codex-acp',
      acpArgs: [],
    },
    {
      id: 'custom-agent',
      name: 'Custom Agent',
      command: '/usr/local/bin/custom-agent',
      enabled: true,
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'discovered_codex',
    'codex',
    { path: '/usr/local/bin/codex', version: null, available: false },
  );

  assert.deepEqual(
    state.agents.map((agent) => agent.id),
    ['custom-agent'],
  );
  assert.equal(state.defaultAgentId, 'catty');
});

test('buildManagedAgentState keeps unrelated defaults when removing stale managed agents', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_claude',
      name: 'Claude Code',
      command: '/usr/local/bin/claude',
      enabled: true,
      acpCommand: 'claude-agent-acp',
      acpArgs: [],
    },
    {
      id: 'custom-agent',
      name: 'Custom Agent',
      command: '/usr/local/bin/custom-agent',
      enabled: true,
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'custom-agent',
    'claude',
    { path: '/usr/local/bin/claude', version: null, available: false },
  );

  assert.deepEqual(
    state.agents.map((agent) => agent.id),
    ['custom-agent'],
  );
  assert.equal(state.defaultAgentId, 'custom-agent');
});

test('buildManagedAgentState stores the system Claude executable for ACP runs', () => {
  const state = buildManagedAgentState(
    [],
    'catty',
    'claude',
    { path: '/opt/homebrew/bin/claude', version: '2.1.145 (Claude Code)', available: true },
  );

  assert.equal(state.agents.length, 1);
  assert.equal(state.agents[0].command, '/opt/homebrew/bin/claude');
  assert.deepEqual(state.agents[0].env, {
    CLAUDE_CODE_EXECUTABLE: '/opt/homebrew/bin/claude',
  });
});

test('buildManagedAgentState does not remove user-created matching agents', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'my-claude-wrapper',
      name: 'My Claude Wrapper',
      command: '/usr/local/bin/claude',
      enabled: true,
      acpCommand: 'claude-agent-acp',
      acpArgs: [],
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'my-claude-wrapper',
    'claude',
    { path: '/usr/local/bin/claude', version: null, available: false },
  );

  assert.deepEqual(state.agents, agents);
  assert.equal(state.defaultAgentId, 'my-claude-wrapper');
});

test('buildManagedAgentState preserves pre-configured env when CLI is unavailable', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_codebuddy',
      name: 'CodeBuddy Code',
      command: 'codebuddy',
      enabled: true,
      acpCommand: 'codebuddy',
      acpArgs: ['--acp'],
      env: { CODEBUDDY_AUTH_TOKEN: 'some-token', CODEBUDDY_INTERNET_ENVIRONMENT: 'HTTP_PROXY=http://proxy:8080' },
    },
  ];

  // CLI not found — path detection fails
  const state = buildManagedAgentState(
    agents,
    'discovered_codebuddy',
    'codebuddy',
    { path: '', version: null, available: false },
  );

  // Entry should be preserved (disabled) so user's env config survives
  assert.equal(state.agents.length, 1);
  assert.equal(state.agents[0].id, 'discovered_codebuddy');
  assert.equal(state.agents[0].enabled, false);
  assert.equal(state.agents[0].autoDisabledUntilAvailable, true);
  assert.deepEqual(state.agents[0].env, {
    CODEBUDDY_AUTH_TOKEN: 'some-token',
    CODEBUDDY_INTERNET_ENVIRONMENT: 'HTTP_PROXY=http://proxy:8080',
  });
  assert.equal(state.defaultAgentId, 'catty');
});

test('buildManagedAgentState preserves pre-configured env when CLI is temporarily missing (path=null)', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_codebuddy',
      name: 'CodeBuddy Code',
      command: '/usr/local/bin/codebuddy',
      enabled: true,
      acpCommand: '/usr/local/bin/codebuddy',
      acpArgs: ['--acp'],
      env: { CODEBUDDY_AUTH_TOKEN: 'tok-123' },
    },
    {
      id: 'custom-agent',
      name: 'Custom',
      command: 'custom',
      enabled: true,
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'discovered_codebuddy',
    'codebuddy',
    null,
  );

  assert.deepEqual(
    state.agents.map((agent) => agent.id),
    ['custom-agent', 'discovered_codebuddy'],
  );
  assert.equal(state.agents[1].enabled, false);
  assert.equal(state.agents[1].autoDisabledUntilAvailable, true);
  assert.deepEqual(state.agents[1].env, { CODEBUDDY_AUTH_TOKEN: 'tok-123' });
  assert.equal(state.defaultAgentId, 'catty');
});

test('buildManagedAgentState re-enables auto-disabled CodeBuddy after CLI becomes available', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_codebuddy',
      name: 'CodeBuddy Code',
      command: 'codebuddy',
      enabled: false,
      acpCommand: 'codebuddy',
      acpArgs: ['--acp'],
      env: { CODEBUDDY_AUTH_TOKEN: 'tok-123' },
      autoDisabledUntilAvailable: true,
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'catty',
    'codebuddy',
    { path: '/usr/local/bin/codebuddy', version: '1.2.3', available: true },
  );

  assert.equal(state.agents.length, 1);
  assert.equal(state.agents[0].id, 'discovered_codebuddy');
  assert.equal(state.agents[0].command, '/usr/local/bin/codebuddy');
  assert.equal(state.agents[0].acpCommand, '/usr/local/bin/codebuddy');
  assert.equal(state.agents[0].enabled, true);
  assert.equal(state.agents[0].autoDisabledUntilAvailable, undefined);
  assert.deepEqual(state.agents[0].env, { CODEBUDDY_AUTH_TOKEN: 'tok-123' });
});

test('buildManagedAgentState re-enables preconfigured CodeBuddy env from older state without marker', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_codebuddy',
      name: 'CodeBuddy Code',
      command: 'codebuddy',
      enabled: false,
      acpCommand: 'codebuddy',
      acpArgs: ['--acp'],
      env: { CODEBUDDY_AUTH_TOKEN: 'tok-123' },
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'catty',
    'codebuddy',
    { path: '/usr/local/bin/codebuddy', version: '1.2.3', available: true },
  );

  assert.equal(state.agents.length, 1);
  assert.equal(state.agents[0].command, '/usr/local/bin/codebuddy');
  assert.equal(state.agents[0].enabled, true);
  assert.equal(state.agents[0].autoDisabledUntilAvailable, undefined);
});

test('buildManagedAgentState keeps manually disabled managed agents disabled after detection', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'discovered_codebuddy',
      name: 'CodeBuddy Code',
      command: '/usr/local/bin/codebuddy',
      enabled: false,
      acpCommand: '/usr/local/bin/codebuddy',
      acpArgs: ['--acp'],
      env: { CODEBUDDY_AUTH_TOKEN: 'tok-123' },
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'catty',
    'codebuddy',
    { path: '/usr/local/bin/codebuddy', version: '1.2.3', available: true },
  );

  assert.equal(state.agents.length, 1);
  assert.equal(state.agents[0].enabled, false);
  assert.equal(state.agents[0].autoDisabledUntilAvailable, undefined);
});

test('buildManagedAgentState only rewrites settings-managed discovered agents', () => {
  const agents: ExternalAgentConfig[] = [
    {
      id: 'my-codex-wrapper',
      name: 'My Codex Wrapper',
      command: '/usr/local/bin/codex',
      enabled: true,
      acpCommand: 'codex-acp',
      acpArgs: [],
    },
  ];

  const state = buildManagedAgentState(
    agents,
    'my-codex-wrapper',
    'codex',
    { path: '/opt/netcatty/codex-acp', version: 'Bundled ACP', available: true },
  );

  assert.deepEqual(
    state.agents.map((agent) => agent.id),
    ['my-codex-wrapper', 'discovered_codex'],
  );
  assert.equal(state.agents[0], agents[0]);
  assert.equal(state.defaultAgentId, 'my-codex-wrapper');
});
