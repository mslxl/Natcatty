import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  normalizeAppLockSettings,
  verifyAppLockPassword,
  type AppLockPasswordVerifier,
  type AppLockSettings,
} from '../../domain/appLock';

export type AppLockReason = 'startup' | 'idle' | 'manual';
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
  return now - lastActivityAt >= normalized.timeoutMinutes * 60_000;
}

export function getIdleLockDelayMs(
  settings: AppLockSettings,
  lastActivityAt: number,
  now: number,
): number | null {
  const normalized = normalizeAppLockSettings(settings);
  if (!normalized.enabled || !normalized.passwordVerifier) return null;
  const timeoutMs = normalized.timeoutMinutes * 60_000;
  return Math.max(0, timeoutMs - (now - lastActivityAt));
}

export async function resolveUnlockAttempt(
  password: string,
  verifier: AppLockPasswordVerifier | null,
): Promise<AppLockUnlockResult> {
  if (!password) return { ok: false, error: 'empty' };
  const ok = await verifyAppLockPassword(password, verifier);
  return ok ? { ok: true } : { ok: false, error: 'incorrect' };
}

export function useAppLockState(settings: AppLockSettings) {
  const normalizedSettings = useMemo(() => normalizeAppLockSettings(settings), [settings]);
  const [locked, setLocked] = useState(() => shouldLockOnStartup(normalizedSettings));
  const [lockReason, setLockReason] = useState<AppLockReason | null>(() =>
    shouldLockOnStartup(normalizedSettings) ? 'startup' : null
  );
  const lastActivityAtRef = useRef(Date.now());
  const lockedRef = useRef(locked);
  lockedRef.current = locked;

  const lockNow = useCallback((reason: AppLockReason = 'manual') => {
    if (!shouldLockOnStartup(normalizedSettings)) return;
    setLocked(true);
    setLockReason(reason);
  }, [normalizedSettings]);

  const recordActivity = useCallback(() => {
    if (lockedRef.current) return;
    lastActivityAtRef.current = Date.now();
  }, []);

  const unlock = useCallback(async (password: string): Promise<AppLockUnlockResult> => {
    const result = await resolveUnlockAttempt(password, normalizedSettings.passwordVerifier);
    if (result.ok) {
      lastActivityAtRef.current = Date.now();
      setLocked(false);
      setLockReason(null);
    }
    return result;
  }, [normalizedSettings.passwordVerifier]);

  useEffect(() => {
    if (!shouldLockOnStartup(normalizedSettings)) {
      setLocked(false);
      setLockReason(null);
      return;
    }
    if (!lockedRef.current) return;
    setLocked(true);
    setLockReason((prev) => prev ?? 'startup');
  }, [normalizedSettings]);

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
    if (!shouldLockOnStartup(normalizedSettings) || locked) return undefined;
    let timeout: number | undefined;

    const checkIdle = () => {
      if (shouldLockAfterIdle(normalizedSettings, lastActivityAtRef.current, Date.now())) {
        lockNow('idle');
        return;
      }
      scheduleNextCheck();
    };

    const scheduleNextCheck = () => {
      const delayMs = getIdleLockDelayMs(normalizedSettings, lastActivityAtRef.current, Date.now());
      if (delayMs === null) return undefined;
      timeout = window.setTimeout(checkIdle, delayMs);
      return timeout;
    };

    scheduleNextCheck();
    return () => window.clearTimeout(timeout);
  }, [normalizedSettings, locked, lockNow]);

  return {
    locked,
    lockReason,
    lockNow,
    unlock,
    recordActivity,
  };
}
