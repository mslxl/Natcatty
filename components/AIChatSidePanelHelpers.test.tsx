import test from 'node:test';
import assert from 'node:assert/strict';

import {
  modelPresetsContainId,
  shouldAdoptSdkCurrentModel,
  shouldLoadSdkRuntimeModels,
  shouldUseStoredAgentModel,
} from './AIChatSidePanelHelpers';
import type { AgentModelPreset, ExternalAgentConfig } from '../infrastructure/ai/types';

test('modelPresetsContainId matches plain and thinking-level model ids', () => {
  const presets: AgentModelPreset[] = [
    { id: 'gpt-5.5', name: 'GPT-5.5', thinkingLevels: ['low', 'high'] },
    { id: 'claude-sonnet', name: 'Claude Sonnet' },
  ];

  assert.equal(modelPresetsContainId(presets, 'gpt-5.5/high'), true);
  assert.equal(modelPresetsContainId(presets, 'claude-sonnet'), true);
  assert.equal(modelPresetsContainId(presets, 'gpt-5.5/medium'), false);
});

test('shouldLoadSdkRuntimeModels includes SDK agents with model catalogs', () => {
  const agent = (sdkBackend: string): ExternalAgentConfig => ({
    id: `discovered_${sdkBackend}`,
    name: sdkBackend,
    command: sdkBackend,
    enabled: true,
    sdkBackend,
  });

  assert.equal(shouldLoadSdkRuntimeModels(agent('claude')), true);
  assert.equal(shouldLoadSdkRuntimeModels(agent('copilot')), true);
  assert.equal(shouldLoadSdkRuntimeModels(agent('codebuddy')), true);
  assert.equal(shouldLoadSdkRuntimeModels(agent('opencode')), true);
  assert.equal(shouldLoadSdkRuntimeModels(agent('codex')), false);
  assert.equal(shouldLoadSdkRuntimeModels(undefined), false);
});

test('shouldAdoptSdkCurrentModel keeps SDK defaults when no runtime list is returned', () => {
  assert.equal(shouldAdoptSdkCurrentModel('openai/gpt-5.1', undefined, []), true);
  assert.equal(shouldAdoptSdkCurrentModel('openai/gpt-5.1', 'openai/gpt-5.1', []), true);
  assert.equal(
    shouldAdoptSdkCurrentModel('openai/gpt-5.1', 'anthropic/claude-sonnet', [
      { id: 'anthropic/claude-sonnet', name: 'Claude' },
    ]),
    false,
  );
  assert.equal(shouldAdoptSdkCurrentModel(null, undefined, []), false);
});

test('shouldUseStoredAgentModel trusts SDK defaults when no runtime list is returned', () => {
  const opencodeAgent: ExternalAgentConfig = {
    id: 'managed_opencode',
    name: 'OpenCode',
    command: 'opencode',
    enabled: true,
    sdkBackend: 'opencode',
  };

  assert.equal(shouldUseStoredAgentModel('openai/gpt-5.1', [], opencodeAgent), true);
  assert.equal(shouldUseStoredAgentModel('openai/gpt-5.1', [], undefined), false);
  assert.equal(
    shouldUseStoredAgentModel('anthropic/claude-sonnet', [
      { id: 'anthropic/claude-sonnet', name: 'Claude' },
    ], opencodeAgent),
    true,
  );
  assert.equal(
    shouldUseStoredAgentModel('openai/gpt-5.1', [
      { id: 'anthropic/claude-sonnet', name: 'Claude' },
    ], opencodeAgent),
    false,
  );
});
