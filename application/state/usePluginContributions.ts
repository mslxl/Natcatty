import { useCallback, useEffect, useMemo, useState } from 'react';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';

const EMPTY_SNAPSHOT: NetcattyPluginContributionSnapshot = Object.freeze({
  locale: 'en',
  plugins: Object.freeze([]),
});

export function comparePluginMenus(
  left: NetcattyPluginContributionSnapshot['plugins'][number]['menus'][number],
  right: NetcattyPluginContributionSnapshot['plugins'][number]['menus'][number],
): number {
  return (left.group ?? '').localeCompare(right.group ?? '')
    || (left.order ?? 0) - (right.order ?? 0)
    || left.id.localeCompare(right.id);
}

export interface UsePluginContributionsResult {
  available: boolean;
  loading: boolean;
  error: Error | null;
  snapshot: NetcattyPluginContributionSnapshot;
  refresh(): Promise<void>;
  executeCommand(command: string, args?: unknown, context?: Record<string, unknown>): Promise<unknown>;
  updateSetting(pluginId: string, settingId: string, value: unknown, scopeId?: string): Promise<{ restartRequired: boolean }>;
  resetSetting(pluginId: string, settingId: string, scopeId?: string): Promise<{ restartRequired: boolean }>;
  selectSettingPath(kind: 'file' | 'directory', title: string, defaultPath?: string): Promise<string | null>;
  openView(payload: NetcattyPluginViewOpenRequest): Promise<{ instanceId: string }>;
  closeView(instanceId: string): Promise<void>;
  setViewBounds(instanceId: string, bounds: { x: number; y: number; width: number; height: number }): Promise<void>;
  setEnvironment(environment: NetcattyPluginEnvironment): Promise<void>;
}

export function usePluginContributions(
  query: NetcattyPluginContributionQuery = {},
): UsePluginContributionsResult {
  const bridge = typeof window === 'undefined' ? undefined : netcattyBridge.get();
  const queryKey = useMemo(() => JSON.stringify(query), [query]);
  const [snapshot, setSnapshot] = useState<NetcattyPluginContributionSnapshot>(EMPTY_SNAPSHOT);
  const [available, setAvailable] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);

  const refresh = useCallback(async () => {
    if (!bridge?.getPluginRuntimeStatus || !bridge.getPluginContributions) {
      setAvailable(false);
      setSnapshot(EMPTY_SNAPSHOT);
      setLoading(false);
      return;
    }
    try {
      const status = await bridge.getPluginRuntimeStatus();
      setAvailable(status.available);
      if (!status.available) {
        setSnapshot(EMPTY_SNAPSHOT);
        setError(null);
        return;
      }
      setSnapshot(await bridge.getPluginContributions(JSON.parse(queryKey)));
      setError(null);
    } catch (cause) {
      setAvailable(false);
      setError(cause instanceof Error ? cause : new Error(String(cause)));
    } finally {
      setLoading(false);
    }
  }, [bridge, queryKey]);

  useEffect(() => {
    void refresh();
    return bridge?.onPluginContributionsChanged?.(() => { void refresh(); });
  }, [bridge, refresh]);

  const executeCommand = useCallback(async (command: string, args?: unknown, context?: Record<string, unknown>) => {
    if (!bridge?.executePluginCommand) throw new Error('Plugin commands are unavailable');
    return bridge.executePluginCommand(command, args, context);
  }, [bridge]);

  const updateSetting = useCallback(async (
    pluginId: string,
    settingId: string,
    value: unknown,
    scopeId?: string,
  ) => {
    if (!bridge?.updatePluginSetting) throw new Error('Plugin settings are unavailable');
    const result = await bridge.updatePluginSetting(pluginId, settingId, value, scopeId);
    await refresh();
    return result;
  }, [bridge, refresh]);

  const resetSetting = useCallback(async (pluginId: string, settingId: string, scopeId?: string) => {
    if (!bridge?.resetPluginSetting) throw new Error('Plugin settings are unavailable');
    const result = await bridge.resetPluginSetting(pluginId, settingId, scopeId);
    await refresh();
    return result;
  }, [bridge, refresh]);

  const selectSettingPath = useCallback(async (kind: 'file' | 'directory', title: string, defaultPath?: string) => {
    const picker = kind === 'file' ? bridge?.selectFile : bridge?.selectDirectory;
    if (!picker) throw new Error('Plugin path selection is unavailable');
    return picker(title, defaultPath);
  }, [bridge]);

  const openView = useCallback(async (payload: NetcattyPluginViewOpenRequest) => {
    if (!bridge?.openPluginView) throw new Error('Plugin views are unavailable');
    return bridge.openPluginView(payload);
  }, [bridge]);

  const closeView = useCallback(async (instanceId: string) => {
    if (!bridge?.closePluginView) return;
    await bridge.closePluginView(instanceId);
  }, [bridge]);

  const setViewBounds = useCallback(async (
    instanceId: string,
    bounds: { x: number; y: number; width: number; height: number },
  ) => {
    if (!bridge?.setPluginViewBounds) return;
    await bridge.setPluginViewBounds(instanceId, bounds);
  }, [bridge]);

  const setEnvironment = useCallback(async (environment: NetcattyPluginEnvironment) => {
    if (!bridge?.setPluginEnvironment) return;
    await bridge.setPluginEnvironment(environment);
  }, [bridge]);

  return {
    available,
    loading,
    error,
    snapshot,
    refresh,
    executeCommand,
    updateSetting,
    resetSetting,
    selectSettingPath,
    openView,
    closeView,
    setViewBounds,
    setEnvironment,
  };
}
