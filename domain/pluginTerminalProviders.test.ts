import assert from 'node:assert/strict';
import test from 'node:test';

import {
  isSafePluginDecorationPattern,
  mergePluginDecorationRules,
  mergePluginCompletionItems,
  normalizePluginBackgroundResult,
  normalizePluginBackgroundRefreshAfterMs,
  normalizePluginCompletionResult,
  normalizePluginDecorationResult,
  normalizePluginHoverResult,
  normalizePluginLinkResult,
  normalizePluginMatcherResult,
  normalizePluginPromptResult,
  normalizePluginSemanticResult,
} from './pluginTerminalProviders.ts';

test('plugin completion results are bounded, normalized, ranked, and deduplicated', () => {
  const alpha = normalizePluginCompletionResult('alpha', {
    items: [
      { text: 'git status', displayText: 'status', score: 10 },
      { text: '', score: 100 },
    ],
  });
  const beta = normalizePluginCompletionResult('beta', {
    items: [
      { text: 'git status', score: 20 },
      { text: 'git stash', description: 'Stash changes', score: 5 },
    ],
  });
  assert.deepEqual(mergePluginCompletionItems([alpha, beta], 10).map((item) => item.text), [
    'git status',
    'git stash',
  ]);
  assert.equal(alpha[0]?.displayText, 'git status');
  assert.deepEqual(normalizePluginCompletionResult('transparent', {
    items: [{
      text: 'rm -rf -- /important-data',
      displayText: 'Refresh project index',
      score: 100,
    }],
  }), [{
    text: 'rm -rf -- /important-data',
    displayText: 'rm -rf -- /important-data',
    score: 100,
    providerId: 'transparent',
  }]);
  assert.deepEqual(normalizePluginCompletionResult('unsafe', {
    items: [{ text: 'echo safe\nrm -rf /', score: 100 }, { text: 'safe', displayText: '\u202eevil' }],
  }), [{
    text: 'safe',
    displayText: 'safe',
    score: 0,
    providerId: 'unsafe',
  }]);
});

test('ordinary terminal Provider results enforce exact ranges and safe visible values', () => {
  assert.deepEqual(normalizePluginLinkResult('links', {
    links: [
      { start: 0, length: 7, uri: 'https://example.com/path', label: 'Example' },
      { start: 0, length: 99, uri: 'https://example.com' },
      { start: 0, length: 4, uri: 'javascript:alert(1)' },
      { start: 0, length: 4, uri: 'https://user:secret@example.com' },
    ],
  }, 10), [{
    start: 0,
    length: 7,
    uri: 'https://example.com/path',
    label: 'Example',
    providerId: 'links',
  }]);
  assert.deepEqual(normalizePluginHoverResult('hover', {
    hovers: [{ start: 2, length: 3, contents: 'Details' }],
  }, 10), [{ start: 2, length: 3, contents: 'Details', providerId: 'hover' }]);
  assert.deepEqual(normalizePluginMatcherResult('matcher', {
    matches: [{ lineId: 'line-1', start: 1, length: 4, label: 'Failure', severity: 'error', color: '#ff0000' }],
  }, new Map([['line-1', 10]])), [{
    lineId: 'line-1',
    start: 1,
    length: 4,
    label: 'Failure',
    severity: 'error',
    color: '#ff0000',
    providerId: 'matcher',
  }]);
  assert.deepEqual(normalizePluginMatcherResult('matcher', {
    matches: [{ lineId: 'unknown', start: 0, length: 1, label: 'Hidden' }],
  }, new Map([['line-1', 10]])), []);
});

test('semantic, prompt, and background results are bounded and presentation-only', () => {
  assert.deepEqual(normalizePluginSemanticResult('semantic', {
    classification: 'deployment',
    description: 'Deploys the current build',
    destructive: true,
    idempotent: false,
    annotations: [{ text: 'production', color: '#ff0000' }],
  }), {
    classification: 'deployment',
    description: 'Deploys the current build',
    destructive: true,
    idempotent: false,
    annotations: [{ text: 'production', color: '#ff0000', providerId: 'semantic' }],
  });
  assert.deepEqual(normalizePluginPromptResult('prompt', {
    annotations: [{ text: 'venv', color: '#00ff00' }, { text: '\u202eevil' }],
  }), [{ text: 'venv', color: '#00ff00', providerId: 'prompt' }]);
  assert.deepEqual(normalizePluginBackgroundResult('background', {
    layers: [
      { id: 'tint', color: '#102030', opacity: 0.25 },
      { id: 'default-opacity', color: '#203040' },
      { id: 'invalid', color: 'url(https://example.com)', opacity: 1 },
    ],
  }), [
    {
      id: 'background:tint',
      color: '#102030',
      opacity: 0.25,
      providerId: 'background',
    },
    {
      id: 'background:default-opacity',
      color: '#203040',
      opacity: 0.15,
      providerId: 'background',
    },
  ]);
  assert.equal(normalizePluginBackgroundRefreshAfterMs({ refreshAfterMs: 250 }), 250);
  assert.equal(normalizePluginBackgroundRefreshAfterMs({ refreshAfterMs: 249 }), undefined);
});

test('plugin decoration results reject unsafe expressions and namespace rule identity', () => {
  assert.equal(isSafePluginDecorationPattern('\\berror\\b'), true);
  assert.equal(isSafePluginDecorationPattern('^(a+)+$'), false);
  assert.equal(isSafePluginDecorationPattern('a*a*a*a*a*a*a*a*a*a*b'), false);
  assert.equal(isSafePluginDecorationPattern('a*A*B'), false);
  assert.equal(isSafePluginDecorationPattern('[a-z]*[A-Z]*missing'), false);
  assert.equal(isSafePluginDecorationPattern('[a-z]*[m-z]*missing'), false);
  assert.equal(isSafePluginDecorationPattern('[a-f]*[0-9]*value'), true);
  assert.equal(isSafePluginDecorationPattern('\\berror\\s+\\d+\\b'), true);
  assert.equal(isSafePluginDecorationPattern('['), false);
  assert.deepEqual(normalizePluginDecorationResult('com.example.decoration', {
    rules: [{ id: 'error', label: 'Error', patterns: ['\\berror\\b'], color: '#ff0000' }],
  }), [{
    id: 'com.example.decoration:error',
    label: 'Error',
    patterns: ['\\berror\\b'],
    color: '#ff0000',
    enabled: true,
    providerId: 'com.example.decoration',
  }]);
  assert.deepEqual(normalizePluginDecorationResult('com.example.decoration', {
    rules: [{ id: 'unsafe', label: 'Unsafe', patterns: ['^(a+)+$'], color: '#ff0000' }],
  }), []);
  const groups = Array.from({ length: 3 }, (_, group) => normalizePluginDecorationResult(
    `com.example.decoration${group}`,
    { rules: Array.from({ length: 32 }, (_, index) => ({
      id: `rule${index}`,
      label: `Rule ${index}`,
      patterns: [`value-${group}-${index}`],
      color: '#ff0000',
    })) },
  ));
  assert.equal(mergePluginDecorationRules(groups).length, 64);
});
