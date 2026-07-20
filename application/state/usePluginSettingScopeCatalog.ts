import { useEffect, useState } from 'react';

import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';

const EMPTY_SCOPE_CATALOG: NetcattyPluginScopeCatalog = Object.freeze({
  workspace: Object.freeze([]),
  host: Object.freeze([]),
  session: Object.freeze([]),
  device: Object.freeze([{ id: 'device', label: 'This device' }]),
});

export function resolvePluginSettingScopeSelection(
  catalog: NetcattyPluginScopeCatalog,
  current: Partial<Record<NetcattyPluginSettingScopeKind, string>>,
): Partial<Record<NetcattyPluginSettingScopeKind, string>> {
  const next = { ...current };
  for (const kind of ['workspace', 'host', 'session', 'device'] as const) {
    const entries = catalog[kind];
    if (!entries.some((entry) => entry.id === current[kind])) next[kind] = entries[0]?.id;
  }
  return next;
}

export function usePluginSettingScopeCatalog(): NetcattyPluginScopeCatalog {
  const [catalog, setCatalog] = useState<NetcattyPluginScopeCatalog>(EMPTY_SCOPE_CATALOG);

  useEffect(() => {
    const bridge = netcattyBridge.get();
    let cancelled = false;
    void bridge?.getPluginScopeCatalog?.().then((next) => {
      if (!cancelled) setCatalog(next);
    }).catch(() => {});
    const unsubscribe = bridge?.onPluginScopeCatalogChanged?.((next) => {
      if (!cancelled) setCatalog(next);
    });
    return () => {
      cancelled = true;
      unsubscribe?.();
    };
  }, []);

  return catalog;
}
