import test from 'node:test';
import assert from 'node:assert/strict';

import {
  clearProviderModelCatalogCache,
  fetchProviderModelCatalog,
  seedProviderModelCatalog,
} from './cattyProviderModels';
import type { ProviderConfig } from './types';

const provider: ProviderConfig = {
  id: 'p1',
  providerId: 'deepseek',
  name: 'DeepSeek',
  defaultModel: 'deepseek-chat',
  baseURL: 'https://api.deepseek.com/v1',
  apiKey: 'test-key',
  enabled: true,
};

test('seedProviderModelCatalog includes the default and curated models', () => {
  const seed = seedProviderModelCatalog(provider);
  assert.equal(seed.fetched, false);
  assert.ok(seed.models.some((model) => model.id === 'deepseek-chat'));
  assert.ok(seed.models.some((model) => model.id === 'deepseek-v4-pro'));
});

test('fetchProviderModelCatalog merges discovered models and caches them', async () => {
  clearProviderModelCatalogCache();
  const catalog = await fetchProviderModelCatalog(provider, {
    aiFetch: async () => ({
      ok: true,
      data: JSON.stringify({ data: [{ id: 'deepseek-reasoner', name: 'Reasoner' }] }),
    }),
  });
  assert.equal(catalog.fetched, true);
  assert.ok(catalog.models.some((model) => model.id === 'deepseek-reasoner'));
  assert.ok(catalog.models.some((model) => model.id === 'deepseek-chat'));

  const cached = await fetchProviderModelCatalog(provider, {
    aiFetch: async () => {
      throw new Error('should not refetch');
    },
  });
  assert.equal(cached.fetched, true);
  assert.ok(cached.models.some((model) => model.id === 'deepseek-reasoner'));
});

test('fetchProviderModelCatalog appends /v1 when listing Ollama Cloud from a bare origin', async () => {
  clearProviderModelCatalogCache();
  const requested: string[] = [];
  const catalog = await fetchProviderModelCatalog(
    {
      id: 'ollama-cloud',
      providerId: 'ollama',
      name: 'Ollama',
      defaultModel: 'deepseek-v4-flash:0731',
      baseURL: 'https://ollama.com',
      apiKey: 'cloud-key',
      enabled: true,
    },
    {
      aiFetch: async (url) => {
        requested.push(url);
        return {
          ok: true,
          data: JSON.stringify({ data: [{ id: 'deepseek-v4-flash:0731' }] }),
        };
      },
    },
  );
  assert.deepEqual(requested, ['https://ollama.com/v1/models']);
  assert.equal(catalog.fetched, true);
  assert.ok(catalog.models.some((model) => model.id === 'deepseek-v4-flash:0731'));
});
