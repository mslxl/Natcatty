import test from 'node:test';
import assert from 'node:assert/strict';

import {
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
