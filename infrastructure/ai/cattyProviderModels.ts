import { decryptField } from '../persistence/secureFieldAdapter';
import { buildModelDiscoveryHeaders, resolveModelsDiscoveryEndpoint } from './modelDiscoveryHeaders';
import { normalizeOllamaSdkBaseURL } from './ollamaCompatBaseUrl';
import { buildProviderProbeUrl } from './providerConnectionProbe';
import { resolveProviderStyle, type ProviderConfig } from './types';
import {
  buildProviderSeedModels,
  mergeComposerModels,
  type ComposerPickerModel,
} from './composerPicker';

export interface ProviderModelCatalog {
  models: ComposerPickerModel[];
  fetched: boolean;
  error?: string;
}

type FetchBridge = {
  aiFetch?: (
    url: string,
    method?: string,
    headers?: Record<string, string>,
    body?: string,
    providerId?: string,
    skipHostCheck?: boolean,
    followRedirects?: boolean,
    skipTLSVerify?: boolean,
  ) => Promise<{ ok: boolean; status?: number; data: string; error?: string }>;
  aiAllowlistAddHost?: (baseURL: string) => Promise<{ ok: boolean }>;
};

const catalogCache = new Map<string, { models: ComposerPickerModel[]; expiresAt: number }>();
const CATALOG_TTL_MS = 5 * 60 * 1000;

export function providerModelCacheKey(provider: ProviderConfig): string {
  return [
    provider.id,
    provider.providerId,
    provider.style ?? '',
    provider.baseURL ?? '',
    provider.skipTLSVerify ? '1' : '0',
  ].join('|');
}

export function seedProviderModelCatalog(provider: ProviderConfig): ProviderModelCatalog {
  return {
    models: buildProviderSeedModels(provider),
    fetched: false,
  };
}

export function clearProviderModelCatalogCache(): void {
  catalogCache.clear();
}

function parseDiscoveredModels(parsed: unknown): ComposerPickerModel[] {
  const record = parsed && typeof parsed === 'object' ? parsed as Record<string, unknown> : {};
  const rawModels = Array.isArray(record.data)
    ? record.data
    : Array.isArray(record.models)
      ? record.models
      : [];
  return rawModels
    .map((raw): ComposerPickerModel | null => {
      if (!raw || typeof raw !== 'object') return null;
      const model = raw as Record<string, unknown>;
      if (typeof model.id !== 'string' || !model.id) return null;
      return {
        id: model.id,
        name: typeof model.name === 'string' && model.name ? model.name : model.id,
      };
    })
    .filter((model): model is ComposerPickerModel => model != null)
    .sort((a, b) => a.name.localeCompare(b.name));
}

export async function fetchProviderModelCatalog(
  provider: ProviderConfig,
  bridge: FetchBridge | undefined,
): Promise<ProviderModelCatalog> {
  const seed = seedProviderModelCatalog(provider);
  const cacheKey = providerModelCacheKey(provider);
  const cached = catalogCache.get(cacheKey);
  if (cached && cached.expiresAt > Date.now()) {
    return {
      models: mergeComposerModels(seed.models, cached.models),
      fetched: true,
    };
  }

  const style = resolveProviderStyle(provider);
  const endpoint = resolveModelsDiscoveryEndpoint(style, undefined);
  if (!endpoint || !provider.baseURL || !bridge?.aiFetch) {
    return seed;
  }

  try {
    const apiKey = await decryptField(provider.apiKey);
    if (provider.providerId !== 'ollama' && !apiKey) {
      return seed;
    }
    const baseURL = provider.providerId === 'ollama'
      ? normalizeOllamaSdkBaseURL(provider.baseURL)
      : provider.baseURL;
    if (bridge.aiAllowlistAddHost) {
      await bridge.aiAllowlistAddHost(baseURL);
    }
    const url = buildProviderProbeUrl(baseURL, endpoint);
    const headers = buildModelDiscoveryHeaders(style, apiKey);
    const result = await bridge.aiFetch(
      url,
      'GET',
      headers,
      undefined,
      undefined,
      undefined,
      undefined,
      provider.skipTLSVerify,
    );
    if (!result.ok) {
      return { ...seed, error: result.error || 'Failed to fetch models' };
    }
    const fetched = parseDiscoveredModels(JSON.parse(result.data) as unknown);
    catalogCache.set(cacheKey, { models: fetched, expiresAt: Date.now() + CATALOG_TTL_MS });
    return {
      models: mergeComposerModels(seed.models, fetched),
      fetched: true,
    };
  } catch (error) {
    return {
      ...seed,
      error: error instanceof Error ? error.message : 'Failed to fetch models',
    };
  }
}
