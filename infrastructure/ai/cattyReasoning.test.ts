import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildCattyReasoningProviderOptions,
  cattyReasoningLevelsForSelection,
  openaiModelLikelySupportsReasoning,
  openaiModelSupportsNoneReasoning,
  resolveVisibleCattyThinkingLevel,
} from './cattyReasoning';

test('buildCattyReasoningProviderOptions is omitted when effort is off', () => {
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, 'off'),
    undefined,
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, undefined),
    undefined,
  );
});

test('buildCattyReasoningProviderOptions maps OpenAI-compatible effort', () => {
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'deepseek' }, 'high'),
    { openai: { reasoningEffort: 'high' } },
  );
});

test('buildCattyReasoningProviderOptions omits reasoningEffort for non-reasoning OpenAI models', () => {
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, 'high', 'gpt-4o'),
    undefined,
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, 'high', 'gpt-5.5'),
    { openai: { reasoningEffort: 'high' } },
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, 'off', 'o3-mini'),
    undefined,
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, 'off', 'gpt-5'),
    undefined,
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'openai' }, 'off', 'gpt-5.5'),
    { openai: { reasoningEffort: 'none' } },
  );
  assert.equal(openaiModelSupportsNoneReasoning('o4-mini'), false);
  assert.equal(openaiModelSupportsNoneReasoning('gpt-5.1-codex'), true);
});

test('cattyReasoningLevelsForSelection hides the chip unless the model can take effort', () => {
  assert.equal(openaiModelLikelySupportsReasoning('gpt-4o'), false);
  assert.equal(openaiModelLikelySupportsReasoning('gpt-5.5'), true);
  assert.deepEqual(cattyReasoningLevelsForSelection({ providerId: 'openai' }, 'gpt-4o'), []);
  assert.ok(cattyReasoningLevelsForSelection({ providerId: 'openai' }, 'gpt-5.5').includes('high'));
  assert.deepEqual(cattyReasoningLevelsForSelection({ providerId: 'google' }, 'gemini-1.5-flash'), []);
  assert.deepEqual(
    cattyReasoningLevelsForSelection({ providerId: 'google' }, 'gemini-3-flash'),
    ['minimal', 'low', 'medium', 'high'],
  );
  assert.ok(cattyReasoningLevelsForSelection({ providerId: 'google' }, 'gemini-3-flash').includes('minimal'));
  assert.ok(!cattyReasoningLevelsForSelection({ providerId: 'google' }, 'gemini-3-flash').includes('off'));
  assert.ok(cattyReasoningLevelsForSelection({ providerId: 'anthropic' }, 'claude-opus-4-6').includes('high'));
  assert.deepEqual(
    cattyReasoningLevelsForSelection({ providerId: 'anthropic' }, 'claude-3-haiku-20240307'),
    [],
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'anthropic' }, 'high', 'claude-3-haiku-20240307'),
    undefined,
  );
  assert.deepEqual(
    cattyReasoningLevelsForSelection({ providerId: 'google' }, 'gemini-3-pro'),
    ['low', 'medium', 'high'],
  );
  assert.deepEqual(
    cattyReasoningLevelsForSelection({ providerId: 'google' }, 'gemini-2.5-pro'),
    ['low', 'medium', 'high'],
  );
});

test('resolveVisibleCattyThinkingLevel drops stale levels after a model switch', () => {
  assert.equal(
    resolveVisibleCattyThinkingLevel(['low', 'medium', 'high'], 'minimal'),
    'low',
  );
  assert.equal(
    resolveVisibleCattyThinkingLevel(['minimal', 'low', 'medium', 'high'], 'off'),
    'minimal',
  );
  assert.equal(
    resolveVisibleCattyThinkingLevel(['off', 'low', 'medium', 'high'], 'high'),
    'high',
  );
});

test('buildCattyReasoningProviderOptions maps Anthropic thinking budgets', () => {
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'anthropic' }, 'medium'),
    { anthropic: { thinking: { type: 'enabled', budgetTokens: 10_000 } } },
  );
});

test('buildCattyReasoningProviderOptions respects an explicit style override', () => {
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'custom', style: 'openai' }, 'low'),
    { openai: { reasoningEffort: 'low' } },
  );
});

test('buildCattyReasoningProviderOptions maps Gemini thinking levels', () => {
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'high', 'gemini-3-pro'),
    { google: { thinkingConfig: { thinkingLevel: 'high', includeThoughts: true } } },
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'off', 'gemini-3-pro'),
    undefined,
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'medium', 'gemini-3-pro'),
    { google: { thinkingConfig: { thinkingLevel: 'high', includeThoughts: true } } },
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'off', 'gemini-3-flash'),
    { google: { thinkingConfig: { thinkingLevel: 'minimal', includeThoughts: false } } },
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'high', 'gemini-2.5-pro'),
    { google: { thinkingConfig: { thinkingBudget: 16_384, includeThoughts: true } } },
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'off', 'gemini-2.5-pro'),
    undefined,
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'off', 'gemini-2.5-flash'),
    { google: { thinkingConfig: { thinkingBudget: 0, includeThoughts: false } } },
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'high'),
    undefined,
  );
  assert.equal(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'high', 'gemini-1.5-flash'),
    undefined,
  );
});
