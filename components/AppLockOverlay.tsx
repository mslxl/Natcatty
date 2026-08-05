import React, { useCallback, useEffect, useRef, useState } from 'react';

import { useI18n } from '../application/i18n/I18nProvider';
import type {
  AppLockReason,
  AppLockSystemUnlockResult,
  AppLockSystemUnlockStatus,
  AppLockUnlockResult,
} from '../application/state/useAppLockState';
import { AppLogo } from './AppLogo';
import { Button } from './ui/button';
import { Input } from './ui/input';
import { Label } from './ui/label';

const RESET_REVEAL_CLICK_COUNT = 5;
const RESET_REVEAL_WINDOW_MS = 1500;

function isDocumentVisible(): boolean {
  return typeof document === 'undefined' || document.visibilityState !== 'hidden';
}

interface AppLockOverlayProps {
  locked: boolean;
  reason: AppLockReason | null;
  onUnlock: (password: string) => Promise<AppLockUnlockResult>;
  systemUnlockStatus?: AppLockSystemUnlockStatus;
  onSystemUnlock?: () => Promise<AppLockSystemUnlockResult>;
  onResetAppLock: (currentPassword: string) => Promise<void>;
  autoPromptSystemUnlock?: boolean;
  reopenSignal?: number;
}

export function getAppLockReasonMessageKey(reason: AppLockReason | null): string {
  switch (reason) {
    case 'startup':
      return 'appLock.reason.startup';
    case 'idle':
      return 'appLock.reason.idle';
    case 'manual':
      return 'appLock.reason.manual';
    default:
      return 'appLock.reason.default';
  }
}

export function getAppLockErrorMessageKey(error: AppLockUnlockResult['error'] | null): string | null {
  switch (error) {
    case 'empty':
      return 'appLock.error.emptyPassword';
    case 'incorrect':
      return 'appLock.error.incorrectPassword';
    default:
      return null;
  }
}

export const AppLockOverlay: React.FC<AppLockOverlayProps> = ({
  locked,
  reason,
  onUnlock,
  systemUnlockStatus,
  onSystemUnlock,
  onResetAppLock,
  autoPromptSystemUnlock = false,
  reopenSignal = 0,
}) => {
  const { t } = useI18n();
  const passwordRef = useRef<HTMLInputElement>(null);
  const [password, setPassword] = useState('');
  const [error, setError] = useState<AppLockUnlockResult['error'] | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isSystemUnlocking, setIsSystemUnlocking] = useState(false);
  const [systemUnlockError, setSystemUnlockError] = useState(false);
  const [logoClickCount, setLogoClickCount] = useState(0);
  const [lastLogoClickAt, setLastLogoClickAt] = useState<number | null>(null);
  const [showReset, setShowReset] = useState(false);
  const [isResetting, setIsResetting] = useState(false);
  const [resetError, setResetError] = useState(false);
  const [documentVisible, setDocumentVisible] = useState(() => isDocumentVisible());
  const lastAutoUnlockPresentationRef = useRef<string | null>(null);

  useEffect(() => {
    const updateDocumentVisible = () => {
      setDocumentVisible(isDocumentVisible());
    };

    updateDocumentVisible();
    document.addEventListener('visibilitychange', updateDocumentVisible);
    window.addEventListener('focus', updateDocumentVisible);
    return () => {
      document.removeEventListener('visibilitychange', updateDocumentVisible);
      window.removeEventListener('focus', updateDocumentVisible);
    };
  }, []);

  const overlayRootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!locked) {
      setPassword('');
      setError(null);
      setIsSubmitting(false);
      setIsSystemUnlocking(false);
      setSystemUnlockError(false);
      setLogoClickCount(0);
      setLastLogoClickAt(null);
      setShowReset(false);
      setIsResetting(false);
      setResetError(false);
      lastAutoUnlockPresentationRef.current = null;
      return;
    }
    const timeout = window.setTimeout(() => passwordRef.current?.focus(), 0);
    return () => window.clearTimeout(timeout);
  }, [locked]);

  // Soft focus trap: if Tab would leave the dialog, cycle within it. Background
  // content is also `inert` from AppLockGate (removed from sequential focus),
  // so this mainly covers residual focusable chrome outside that wrapper.
  useEffect(() => {
    if (!locked) return;

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return;
      const root = overlayRootRef.current;
      if (!root) return;
      const focusable = root.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), [href], select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement as HTMLElement | null;
      if (!active || !root.contains(active)) {
        event.preventDefault();
        first.focus();
        return;
      }
      if (event.shiftKey && active === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', onKeyDown, true);
    return () => document.removeEventListener('keydown', onKeyDown, true);
  }, [locked]);

  const handleSystemUnlock = useCallback(async () => {
    if (isSystemUnlocking || !onSystemUnlock) return;
    setIsSystemUnlocking(true);
    setSystemUnlockError(false);
    const result = await onSystemUnlock();
    if (!result.ok) {
      setSystemUnlockError(true);
    }
    setIsSystemUnlocking(false);
  }, [isSystemUnlocking, onSystemUnlock]);

  const requestAutomaticSystemUnlock = useCallback(() => {
    if (isSystemUnlocking || !onSystemUnlock) return false;
    void handleSystemUnlock();
    return true;
  }, [handleSystemUnlock, isSystemUnlocking, onSystemUnlock]);

  useEffect(() => {
    if (!locked) return;
    if (!autoPromptSystemUnlock) return;
    if (!documentVisible) return;
    if (!systemUnlockStatus?.enabled || !systemUnlockStatus.available || !systemUnlockStatus.label) return;
    if (!onSystemUnlock) return;

    const presentationKey = reason === 'background'
      ? `background:${reopenSignal}`
      : `foreground:${reason ?? 'default'}:${locked}`;
    if (reason === 'background' && reopenSignal <= 0) return;
    if (lastAutoUnlockPresentationRef.current === presentationKey) return;

    if (!requestAutomaticSystemUnlock()) return;
    lastAutoUnlockPresentationRef.current = presentationKey;
  }, [
    locked,
    autoPromptSystemUnlock,
    reason,
    reopenSignal,
    documentVisible,
    onSystemUnlock,
    systemUnlockStatus?.available,
    systemUnlockStatus?.enabled,
    systemUnlockStatus?.label,
    requestAutomaticSystemUnlock,
  ]);

  if (!locked) return null;

  const errorKey = getAppLockErrorMessageKey(error);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (isSubmitting) return;
    setIsSubmitting(true);
    setError(null);
    const result = await onUnlock(password);
    if (!result.ok) {
      setError(result.error);
      setIsSubmitting(false);
      return;
    }
    setPassword('');
    setIsSubmitting(false);
  };

  const handleLogoClick = () => {
    const now = Date.now();
    setLogoClickCount((current) => {
      const isQuickSequence = lastLogoClickAt !== null && now - lastLogoClickAt <= RESET_REVEAL_WINDOW_MS;
      const nextCount = isQuickSequence ? current + 1 : 1;
      if (nextCount >= RESET_REVEAL_CLICK_COUNT) {
        setShowReset(true);
      }
      return nextCount;
    });
    setLastLogoClickAt(now);
  };

  const handleReset = async () => {
    if (isResetting) return;
    setIsResetting(true);
    setResetError(false);
    try {
      await onResetAppLock(password);
    } catch {
      setResetError(true);
      setIsResetting(false);
    }
  };

  return (
    <div
      ref={overlayRootRef}
      className="fixed inset-0 z-[200000] flex items-center justify-center bg-background px-6 text-foreground"
      role="dialog"
      aria-modal="true"
      aria-labelledby="app-lock-title"
    >
      <form
        className="flex w-full max-w-[360px] flex-col items-center gap-5"
        onSubmit={handleSubmit}
      >
        <button
          type="button"
          className="flex h-16 w-16 items-center justify-center rounded-[22px] border border-border/70 bg-card p-2 shadow-sm transition hover:border-primary/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={t('appLock.logoLabel')}
          data-testid="app-lock-logo-easter-egg"
          onClick={handleLogoClick}
        >
          <AppLogo className="h-full w-full" />
        </button>

        <div className="space-y-2 text-center">
          <h1 id="app-lock-title" className="text-xl font-semibold tracking-normal">
            {t('appLock.title')}
          </h1>
          <p className="text-sm leading-6 text-muted-foreground">
            {t(getAppLockReasonMessageKey(reason))}
          </p>
        </div>

        <div className="w-full space-y-2">
          <Label htmlFor="app-lock-password">{t('appLock.passwordLabel')}</Label>
          <Input
            ref={passwordRef}
            id="app-lock-password"
            type="password"
            value={password}
            autoComplete="current-password"
            placeholder={t('appLock.passwordPlaceholder')}
            disabled={isSubmitting}
            aria-invalid={Boolean(errorKey)}
            aria-describedby={errorKey ? 'app-lock-error' : undefined}
            onChange={(event) => {
              setPassword(event.target.value);
              if (error) setError(null);
            }}
          />
          {errorKey && (
            <p id="app-lock-error" className="text-sm text-destructive">
              {t(errorKey)}
            </p>
          )}
        </div>

        <Button type="submit" className="w-full" disabled={isSubmitting}>
          {isSubmitting ? t('appLock.unlocking') : t('appLock.unlock')}
        </Button>

        {systemUnlockStatus?.enabled && systemUnlockStatus.available && systemUnlockStatus.label && (
          <div className="w-full space-y-2">
            <Button
              type="button"
              variant="outline"
              className="w-full"
              disabled={isSystemUnlocking}
              onClick={() => void handleSystemUnlock()}
            >
              {t('appLock.systemUnlock.unlockWith').replace('{label}', systemUnlockStatus.label)}
            </Button>
            {systemUnlockError && (
              <p className="text-sm text-destructive">
                {t('appLock.systemUnlock.error')}
              </p>
            )}
          </div>
        )}

        {showReset && (
          <div
            className="w-full rounded-lg border border-border/70 bg-card p-3 text-left shadow-sm"
            role="region"
            aria-live="polite"
          >
            <div className="space-y-1">
              <p className="text-sm font-medium">{t('appLock.reset.title')}</p>
              <p className="text-xs leading-5 text-muted-foreground">
                {t('appLock.reset.description')}
              </p>
            </div>
            {resetError && (
              <p className="mt-2 text-xs text-destructive">
                {t('appLock.reset.error')}
              </p>
            )}
            <div className="mt-3 flex gap-2">
              <Button
                type="button"
                variant="outline"
                className="flex-1"
                disabled={isResetting}
                onClick={() => {
                  setShowReset(false);
                  setLogoClickCount(0);
                  setLastLogoClickAt(null);
                  setResetError(false);
                }}
              >
                {t('appLock.reset.cancel')}
              </Button>
              <Button
                type="button"
                variant="destructive"
                className="flex-1"
                disabled={isResetting}
                onClick={handleReset}
              >
                {isResetting ? t('appLock.reset.resetting') : t('appLock.reset.confirm')}
              </Button>
            </div>
          </div>
        )}
      </form>
    </div>
  );
};
