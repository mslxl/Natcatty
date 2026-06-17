import { useCallback } from 'react';

import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';

export function useAppLockBridge() {
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
    notifyRendererReady,
    onAppLockReopen,
  };
}
