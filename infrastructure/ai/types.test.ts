import test from 'node:test';
import assert from 'node:assert/strict';

import {
  CLAUDE_MODEL_PRESETS,
  CODEBUDDY_MODEL_PRESETS,
  CODEX_MODEL_PRESETS,
  getAgentModelPresets,
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
    ],
  );
  assert.equal(byId['gpt-5.6-sol']?.description, 'Latest');
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
