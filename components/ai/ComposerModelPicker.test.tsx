import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

import { ComposerModelPicker } from './ComposerModelPicker';

test('Catty picker shows providers and a model search field', () => {
  const html = renderToStaticMarkup(
    <ComposerModelPicker
      providers={[
        {
          id: 'p1',
          providerId: 'deepseek',
          name: 'DeepSeek',
          defaultModel: 'deepseek-v4-pro',
          enabled: true,
        },
        {
          id: 'p2',
          providerId: 'openai',
          name: 'OpenAI',
          defaultModel: 'gpt-5.5',
          enabled: true,
        },
      ]}
      selectedProviderId="p1"
      selectedModelId="deepseek-v4-pro"
      prefs={{ recent: [], pinned: [] }}
      onSelectProviderModel={() => {}}
      onTogglePinned={() => {}}
    />,
  );

  assert.match(html, /DeepSeek/);
  assert.match(html, /OpenAI/);
  assert.match(html, /placeholder="ai\.chat\.searchModels"/);
  assert.match(html, /deepseek-v4-pro/);
});

test('external agent picker lists presets without a provider column', () => {
  const html = renderToStaticMarkup(
    <ComposerModelPicker
      modelPresets={[
        { id: 'gpt-5.5', name: 'GPT-5.5' },
        { id: 'gpt-5.4', name: 'GPT-5.4' },
      ]}
      selectedModelId="gpt-5.5"
      prefs={{ recent: [{ modelId: 'gpt-5.5' }], pinned: [] }}
      onSelectModel={() => {}}
      onTogglePinned={() => {}}
    />,
  );

  assert.match(html, /GPT-5\.5/);
  assert.match(html, /GPT-5\.4/);
  assert.match(html, /ai\.chat\.recent/);
  assert.doesNotMatch(html, /ai\.chat\.providers/);
});
