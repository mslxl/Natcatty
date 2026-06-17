import { LockKeyhole } from 'lucide-react';
import React, { useEffect, useRef, useState } from 'react';

import { useI18n } from '../application/i18n/I18nProvider';
import type { AppLockReason, AppLockUnlockResult } from '../application/state/useAppLockState';
import { Button } from './ui/button';
import { Input } from './ui/input';
import { Label } from './ui/label';

interface AppLockOverlayProps {
  locked: boolean;
  reason: AppLockReason | null;
  onUnlock: (password: string) => Promise<AppLockUnlockResult>;
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

export const AppLockOverlay: React.FC<AppLockOverlayProps> = ({ locked, reason, onUnlock }) => {
  const { t } = useI18n();
  const passwordRef = useRef<HTMLInputElement>(null);
  const [password, setPassword] = useState('');
  const [error, setError] = useState<AppLockUnlockResult['error'] | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  useEffect(() => {
    if (!locked) {
      setPassword('');
      setError(null);
      setIsSubmitting(false);
      return;
    }
    const timeout = window.setTimeout(() => passwordRef.current?.focus(), 0);
    return () => window.clearTimeout(timeout);
  }, [locked]);

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

  return (
    <div
      className="fixed inset-0 z-[200000] flex items-center justify-center bg-background px-6 text-foreground"
      role="dialog"
      aria-modal="true"
      aria-labelledby="app-lock-title"
    >
      <form
        className="flex w-full max-w-[360px] flex-col items-center gap-5"
        onSubmit={handleSubmit}
      >
        <div className="flex h-14 w-14 items-center justify-center rounded-full border border-border/70 bg-card text-foreground shadow-sm">
          <LockKeyhole size={24} />
        </div>

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
      </form>
    </div>
  );
};
