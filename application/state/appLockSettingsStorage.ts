import {
  createAppLockPasswordVerifier,
  normalizeAppLockSettings,
  normalizeAppLockPasswordVerifier,
  normalizeAppLockTimeoutMinutes,
  verifyAppLockPassword,
  type AppLockSettings,
} from '../../domain/appLock';

export type AppLockSettingsChangeError =
  | 'empty-current'
  | 'empty-next'
  | 'incorrect';

export type AppLockSettingsChangeResult =
  | AppLockSettings
  | { ok: false; error: AppLockSettingsChangeError };

export async function applyAppLockEnabledChange(
  settings: AppLockSettings,
  enabled: boolean,
  currentPassword?: string,
): Promise<AppLockSettingsChangeResult> {
  const timeoutMinutes = normalizeAppLockTimeoutMinutes(settings.timeoutMinutes);
  const passwordVerifier = normalizeAppLockPasswordVerifier(settings.passwordVerifier);
  const systemUnlockEnabled = settings.systemUnlockEnabled === true && passwordVerifier !== null;
  if (enabled) {
    return passwordVerifier
      ? { enabled: true, timeoutMinutes, systemUnlockEnabled, passwordVerifier }
      : { enabled: false, timeoutMinutes, systemUnlockEnabled: false, passwordVerifier: null };
  }

  if (passwordVerifier) {
    if (!currentPassword) return { ok: false, error: 'empty-current' };
    const verified = await verifyAppLockPassword(currentPassword, passwordVerifier);
    if (!verified) return { ok: false, error: 'incorrect' };
  }

  return {
    enabled: false,
    timeoutMinutes,
    systemUnlockEnabled: false,
    passwordVerifier: null,
  };
}

export async function replaceAppLockPassword(
  settings: AppLockSettings,
  input: {
    currentPassword?: string;
    nextPassword: string;
  },
): Promise<AppLockSettingsChangeResult> {
  const normalized = normalizeAppLockSettings(settings);
  if (!input.nextPassword.trim()) return { ok: false, error: 'empty-next' };

  if (normalized.passwordVerifier) {
    if (!input.currentPassword) return { ok: false, error: 'empty-current' };
    const verified = await verifyAppLockPassword(input.currentPassword, normalized.passwordVerifier);
    if (!verified) return { ok: false, error: 'incorrect' };
  }

  return {
    enabled: normalized.enabled || !normalized.passwordVerifier,
    timeoutMinutes: normalized.timeoutMinutes,
    systemUnlockEnabled: normalized.systemUnlockEnabled,
    passwordVerifier: await createAppLockPasswordVerifier(input.nextPassword),
  };
}
