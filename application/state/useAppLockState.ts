import { useCallback, useEffect, useMemo } from 'react';

import {
  normalizeAppLockSettings,
  type AppLockSettings,
} from '../../domain/appLock';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';
import {
  normalizeRuntimeAppLockState,
  useAppLockRuntime,
  type RuntimeAppLockReason,
  type RuntimeAppLockState,
} from './useAppLockRuntime';

export type AppLockReason = RuntimeAppLockReason;
export type AppLockUnlockResult =
  | { ok: true }
  | { ok: false; error: 'empty' | 'incorrect' };

export function shouldLockOnStartup(settings: AppLockSettings): boolean {
  const normalized = normalizeAppLockSettings(settings);
  return normalized.enabled && normalized.passwordVerifier !== null;
}

export function shouldLockAfterIdle(
  settings: AppLockSettings,
  lastActivityAt: number,
  now: number,
): boolean {
  const normalized = normalizeAppLockSettings(settings);
  if (!normalized.enabled || !normalized.passwordVerifier) return false;
  if (normalized.timeoutMinutes <= 0) return false;
  return now - lastActivityAt >= normalized.timeoutMinutes * 60_000;
}

export function getIdleLockDelayMs(
  settings: AppLockSettings,
  lastActivityAt: number,
  now: number,
): number | null {
  const normalized = normalizeAppLockSettings(settings);
  if (!normalized.enabled || !normalized.passwordVerifier) return null;
  if (normalized.timeoutMinutes <= 0) return null;
  const timeoutMs = normalized.timeoutMinutes * 60_000;
  return Math.max(0, timeoutMs - (now - lastActivityAt));
}

export async function resolveUnlockAttempt(password: string): Promise<AppLockUnlockResult> {
  if (!password) return { ok: false, error: 'empty' };
  try {
    return await netcattyBridge.get()?.requestAppLockUnlock?.(password) ?? { ok: false, error: 'incorrect' };
  } catch {
    return { ok: false, error: 'incorrect' };
  }
}

export function createOptimisticUnlockedRuntimeState(
  input: RuntimeAppLockState,
  now: number,
): RuntimeAppLockState {
  const current = normalizeRuntimeAppLockState(input);
  if (current.initialized && !current.locked && current.reason === null) {
    return current;
  }

  return {
    ...current,
    initialized: true,
    locked: false,
    reason: null,
    version: current.version + 1,
    lastUnlockedAt: now,
    lastActivityAt: now,
  };
}

export function useAppLockState(settings: AppLockSettings) {
  const normalizedSettings = useMemo(() => normalizeAppLockSettings(settings), [settings]);
  const bridge = netcattyBridge.get();
  const { runtimeState, refreshRuntimeState, setRuntimeState } = useAppLockRuntime(bridge);
  const normalizedRuntimeState = useMemo(
    () => normalizeRuntimeAppLockState(runtimeState),
    [runtimeState],
  );
  const effectiveRuntimeState = useMemo(() => {
    if (normalizedRuntimeState.initialized) return normalizedRuntimeState;
    if (shouldLockOnStartup(normalizedSettings)) {
      return {
        ...normalizedRuntimeState,
        locked: true,
        reason: 'startup' as const,
      };
    }
    return normalizedRuntimeState;
  }, [normalizedRuntimeState, normalizedSettings]);

  const lockNow = useCallback((reason: AppLockReason = 'manual') => {
    if (!shouldLockOnStartup(normalizedSettings) || !reason) return;
    void bridge?.setAppLockRuntimeLocked?.(reason);
  }, [bridge, normalizedSettings]);

  const recordActivity = useCallback(() => {
    if (effectiveRuntimeState.locked) return;
    void bridge?.reportAppLockActivity?.();
  }, [bridge, effectiveRuntimeState.locked]);

  const unlock = useCallback(async (password: string): Promise<AppLockUnlockResult> => {
    const result = await resolveUnlockAttempt(password);
    if (result.ok) {
      const unlockedAt = Date.now();
      setRuntimeState((current) => createOptimisticUnlockedRuntimeState(current, unlockedAt));
      await refreshRuntimeState().catch(() => {});
    }
    return result;
  }, [refreshRuntimeState, setRuntimeState]);

  useEffect(() => {
    if (!shouldLockOnStartup(normalizedSettings) && effectiveRuntimeState.locked) {
      const unlockedAt = Date.now();
      void bridge?.requestAppLockUnlock?.('')
        ?.then((result) => {
          if (result?.ok !== true) return;
          setRuntimeState((current) => createOptimisticUnlockedRuntimeState(current, unlockedAt));
          return refreshRuntimeState();
        })
        .catch(() => {});
    }
  }, [bridge, effectiveRuntimeState.locked, normalizedSettings, refreshRuntimeState, setRuntimeState]);

  useEffect(() => {
    if (!shouldLockOnStartup(normalizedSettings)) return undefined;

    const events: Array<keyof WindowEventMap> = ['pointerdown', 'keydown', 'wheel', 'touchstart', 'focus'];
    for (const eventName of events) {
      window.addEventListener(eventName, recordActivity, { passive: true });
    }

    return () => {
      for (const eventName of events) {
        window.removeEventListener(eventName, recordActivity);
      }
    };
  }, [normalizedSettings, recordActivity]);

  useEffect(() => {
    if (!shouldLockOnStartup(normalizedSettings)) return undefined;
    void bridge?.reportAppLockActivity?.();
    return undefined;
  }, [bridge, normalizedSettings]);

  return {
    initialized: effectiveRuntimeState.initialized,
    locked: effectiveRuntimeState.locked,
    lockReason: effectiveRuntimeState.reason,
    lockNow,
    unlock,
    recordActivity,
    resync: refreshRuntimeState,
  };
}
