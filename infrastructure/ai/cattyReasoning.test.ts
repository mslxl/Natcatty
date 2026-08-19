import test from 'node:test';
import assert from 'node:assert/strict';

import { buildCattyReasoningProviderOptions } from './cattyReasoning';

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
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'high', 'gemini-2.5-pro'),
    { google: { thinkingConfig: { thinkingBudget: 16_384, includeThoughts: true } } },
  );
  assert.deepEqual(
    buildCattyReasoningProviderOptions({ providerId: 'google' }, 'off', 'gemini-2.5-pro'),
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
