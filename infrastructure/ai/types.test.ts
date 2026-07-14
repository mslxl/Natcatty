import test from 'node:test';
import assert from 'node:assert/strict';

import {
  CLAUDE_MODEL_PRESETS,
  CODEBUDDY_MODEL_PRESETS,
  CODEX_GPT_5_6_MIN_CLI_VERSION,
  CODEX_MODEL_PRESETS,
  extractCliSemver,
  filterAgentModelPresetsForCliVersion,
  getAgentModelPresets,
  resolveAgentModelSelection,
  resolveDiscoveredAgentCliVersion,
} from './types';

test('getAgentModelPresets returns CodeBuddy fallback models for command paths', () => {
  assert.deepEqual(
    getAgentModelPresets('/opt/homebrew/bin/codebuddy'),
    CODEBUDDY_MODEL_PRESETS,
  );
  assert.ok(CODEBUDDY_MODEL_PRESETS.some((model) => model.id === 'deepseek-v4-pro'));
});

test('getAgentModelPresets keeps Codex presets separate from CodeBuddy presets', () => {
  assert.deepEqual(getAgentModelPresets('codex'), CODEX_MODEL_PRESETS);
  assert.notDeepEqual(CODEBUDDY_MODEL_PRESETS, CODEX_MODEL_PRESETS);
});

test('CODEX_MODEL_PRESETS lists GPT-5.6 Sol/Terra/Luna with official reasoning levels', () => {
  const byId = Object.fromEntries(CODEX_MODEL_PRESETS.map((model) => [model.id, model]));
  assert.deepEqual(
    CODEX_MODEL_PRESETS.map((model) => model.id),
    [
      'gpt-5.6-sol',
      'gpt-5.6-terra',
      'gpt-5.6-luna',
      'gpt-5.5',
      'gpt-5.4',
      'gpt-5.4-mini',
      'gpt-5.2',
    ],
  );
  assert.equal(byId['gpt-5.6-sol']?.description, 'Latest');
  assert.equal(byId['gpt-5.6-sol']?.defaultThinkingLevel, 'low');
  assert.equal(byId['gpt-5.6-terra']?.defaultThinkingLevel, 'medium');
  assert.equal(byId['gpt-5.6-luna']?.defaultThinkingLevel, 'medium');
  assert.deepEqual(byId['gpt-5.6-sol']?.thinkingLevels, [
    'low',
    'medium',
    'high',
    'xhigh',
    'max',
    'ultra',
  ]);
  assert.deepEqual(byId['gpt-5.6-terra']?.thinkingLevels, [
    'low',
    'medium',
    'high',
    'xhigh',
    'max',
    'ultra',
  ]);
  // Luna omits ultra per openai/codex models-manager catalog (+ lobehub).
  assert.deepEqual(byId['gpt-5.6-luna']?.thinkingLevels, [
    'low',
    'medium',
    'high',
    'xhigh',
    'max',
  ]);
  assert.deepEqual(byId['gpt-5.5']?.thinkingLevels, ['low', 'medium', 'high', 'xhigh']);
  assert.deepEqual(byId['gpt-5.4']?.thinkingLevels, ['low', 'medium', 'high', 'xhigh']);
  assert.deepEqual(byId['gpt-5.4-mini']?.thinkingLevels, ['low', 'medium', 'high', 'xhigh']);
  assert.deepEqual(byId['gpt-5.2']?.thinkingLevels, ['low', 'medium', 'high', 'xhigh']);
});

test('resolveAgentModelSelection uses catalog default effort, not last array entry', () => {
  assert.equal(resolveAgentModelSelection(CODEX_MODEL_PRESETS[0]!), 'gpt-5.6-sol/low');
  assert.equal(
    resolveAgentModelSelection(CODEX_MODEL_PRESETS.find((m) => m.id === 'gpt-5.6-terra')!),
    'gpt-5.6-terra/medium',
  );
  assert.equal(
    resolveAgentModelSelection({
      id: 'custom',
      name: 'Custom',
      thinkingLevels: ['low', 'high'],
    }),
    'custom/low',
  );
  assert.equal(
    resolveAgentModelSelection({ id: 'plain', name: 'Plain' }),
    'plain',
  );
});

test('filterAgentModelPresetsForCliVersion gates GPT-5.6 on CLI < 0.144.0', () => {
  assert.equal(extractCliSemver('codex-cli 0.136.0'), '0.136.0');
  assert.equal(CODEX_GPT_5_6_MIN_CLI_VERSION, '0.144.0');

  const oldCli = filterAgentModelPresetsForCliVersion(CODEX_MODEL_PRESETS, '0.136.0');
  assert.deepEqual(
    oldCli.map((model) => model.id),
    ['gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini', 'gpt-5.2'],
  );
  assert.equal(resolveAgentModelSelection(oldCli[0]!), 'gpt-5.5/medium');

  const newCli = filterAgentModelPresetsForCliVersion(CODEX_MODEL_PRESETS, 'codex-cli 0.144.1');
  assert.equal(newCli[0]?.id, 'gpt-5.6-sol');

  // Unknown version: keep full list so the picker is not empty pre-discovery.
  assert.equal(
    filterAgentModelPresetsForCliVersion(CODEX_MODEL_PRESETS, undefined).length,
    CODEX_MODEL_PRESETS.length,
  );
});

test('resolveDiscoveredAgentCliVersion matches command path or sdk backend', () => {
  assert.equal(
    resolveDiscoveredAgentCliVersion(
      { command: '/usr/local/bin/codex', sdkBackend: 'codex' },
      [{ command: 'codex', path: '/usr/local/bin/codex', binPath: '/usr/local/bin/codex', sdkBackend: 'codex', version: '0.144.1' }],
    ),
    '0.144.1',
  );
  assert.equal(
    resolveDiscoveredAgentCliVersion(
      { command: 'codex', sdkBackend: 'codex' },
      [{ command: 'claude', path: '/bin/claude', binPath: '/bin/claude', sdkBackend: 'claude', version: '1.0.0' }],
    ),
    undefined,
  );
});

test('getAgentModelPresets resolves Windows command paths with backslashes', () => {
  assert.deepEqual(
    getAgentModelPresets('C\\Users\\foo\\AppData\\Roaming\\npm\\codex.cmd'),
    CODEX_MODEL_PRESETS,
  );
  assert.deepEqual(
    getAgentModelPresets('C\\Program Files\\nodejs\\claude.exe'),
    CLAUDE_MODEL_PRESETS,
  );
});
