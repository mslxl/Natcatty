import { useEffect, useMemo, useState } from 'react';
import {
  fetchProviderModelCatalog,
  seedProviderModelCatalog,
} from '../../infrastructure/ai/cattyProviderModels';
import type { ComposerPickerModel } from '../../infrastructure/ai/composerPicker';
import type { ProviderConfig } from '../../infrastructure/ai/types';
import { getFetchBridge } from '../settings/tabs/ai/types';

export interface ProviderModelCatalog {
  models: ComposerPickerModel[];
  fetched: boolean;
  loading: boolean;
  error?: string;
}

export function useProviderModelCatalog(
  provider: ProviderConfig | undefined,
  enabled: boolean,
): ProviderModelCatalog {
  const seed = useMemo(
    () => (provider ? seedProviderModelCatalog(provider) : { models: [], fetched: false }),
    [provider],
  );
  const [catalog, setCatalog] = useState<Omit<ProviderModelCatalog, 'loading'>>(seed);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!enabled || !provider) {
      setCatalog({ models: [], fetched: false });
      setLoading(false);
      return;
    }

    let cancelled = false;
    setCatalog(seedProviderModelCatalog(provider));
    setLoading(true);
    void fetchProviderModelCatalog(provider, getFetchBridge()).then((next) => {
      if (cancelled) return;
      setCatalog(next);
      setLoading(false);
    });

    return () => {
      cancelled = true;
    };
  }, [enabled, provider]);

  return {
    models: catalog.models.length > 0 ? catalog.models : seed.models,
    fetched: catalog.fetched,
    loading,
    error: catalog.error,
  };
}
