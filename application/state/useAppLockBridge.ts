import { useCallback } from 'react';

import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';
import type { RuntimeAppLockReason } from './useAppLockRuntime';

export function useAppLockBridge() {
  const getRuntimeState = useCallback(async () => {
    return netcattyBridge.get()?.getAppLockRuntimeState?.();
  }, []);

  const getSettings = useCallback(async () => {
    return netcattyBridge.get()?.getAppLockSettings?.();
  }, []);

  const setRuntimeLocked = useCallback(async (reason: Exclude<RuntimeAppLockReason, null>) => {
    return netcattyBridge.get()?.setAppLockRuntimeLocked?.(reason);
  }, []);

  const requestUnlock = useCallback(async (password: string) => {
    return netcattyBridge.get()?.requestAppLockUnlock?.(password) ?? { ok: false as const, error: 'incorrect' as const };
  }, []);

  const requestReset = useCallback(async () => {
    return netcattyBridge.get()?.requestAppLockReset?.();
  }, []);

  const reportActivity = useCallback(async () => {
    await netcattyBridge.get()?.reportAppLockActivity?.();
  }, []);

  const onRuntimeStateChanged = useCallback((listener: (state: unknown) => void) => {
    try {
      return netcattyBridge.get()?.onAppLockRuntimeStateChanged?.(listener) ?? (() => {});
    } catch {
      return () => {};
    }
  }, []);

  const onSettingsChanged = useCallback((listener: (settings: unknown) => void) => {
    try {
      return netcattyBridge.get()?.onAppLockSettingsChanged?.(listener) ?? (() => {});
    } catch {
      return () => {};
    }
  }, []);

  const notifyRendererReady = useCallback(() => {
    try {
      netcattyBridge.get()?.rendererReady?.();
    } catch {
      // ignore
    }
  }, []);

  const onAppLockReopen = useCallback((listener: () => void) => {
    try {
      return netcattyBridge.get()?.onAppLockReopen?.(listener) ?? (() => {});
    } catch {
      return () => {};
    }
  }, []);

  return {
    getRuntimeState,
    getSettings,
    setRuntimeLocked,
    requestUnlock,
    requestReset,
    reportActivity,
    onRuntimeStateChanged,
    onSettingsChanged,
    notifyRendererReady,
    onAppLockReopen,
  };
}
