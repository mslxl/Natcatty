export const APP_LOCK_TIMEOUT_OPTIONS_MINUTES = [0, 1, 5, 15, 30, 60] as const;
export type AppLockTimeoutMinutes = typeof APP_LOCK_TIMEOUT_OPTIONS_MINUTES[number];

export interface AppLockPasswordVerifier {
  version: 1;
  algorithm: 'PBKDF2-SHA256';
  iterations: number;
  salt: string;
  hash: string;
}

export interface AppLockSettings {
  enabled: boolean;
  timeoutMinutes: AppLockTimeoutMinutes;
  systemUnlockEnabled: boolean;
  passwordVerifier: AppLockPasswordVerifier | null;
}

export const DEFAULT_APP_LOCK_SETTINGS: AppLockSettings = {
  enabled: false,
  timeoutMinutes: 15,
  systemUnlockEnabled: false,
  passwordVerifier: null,
};

const APP_LOCK_VERIFIER_VERSION = 1;
const APP_LOCK_ALGORITHM = 'PBKDF2-SHA256';
const APP_LOCK_HASH_ITERATIONS = 210000;
const APP_LOCK_SALT_BYTES = 16;
const APP_LOCK_HASH_BYTES = 32;

const textEncoder = new TextEncoder();

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

function base64ToBytes(value: string): Uint8Array | null {
  try {
    const binary = atob(value);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) {
      bytes[i] = binary.charCodeAt(i);
    }
    return bytes;
  } catch {
    return null;
  }
}

function timingSafeEqualBase64(a: string, b: string): boolean {
  const aBytes = base64ToBytes(a);
  const bBytes = base64ToBytes(b);
  if (!aBytes || !bBytes || aBytes.length !== bBytes.length) return false;

  let diff = 0;
  for (let i = 0; i < aBytes.length; i += 1) {
    diff |= aBytes[i] ^ bBytes[i];
  }
  return diff === 0;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

export function normalizeAppLockTimeoutMinutes(input: unknown): AppLockTimeoutMinutes {
  const value = typeof input === 'string' && input.trim() !== '' ? Number(input) : input;
  return APP_LOCK_TIMEOUT_OPTIONS_MINUTES.includes(value as AppLockTimeoutMinutes)
    ? value as AppLockTimeoutMinutes
    : DEFAULT_APP_LOCK_SETTINGS.timeoutMinutes;
}

export function normalizeAppLockPasswordVerifier(input: unknown): AppLockPasswordVerifier | null {
  if (!isRecord(input)) return null;
  if (input.version !== APP_LOCK_VERIFIER_VERSION) return null;
  if (input.algorithm !== APP_LOCK_ALGORITHM) return null;
  if (typeof input.iterations !== 'number' || !Number.isInteger(input.iterations) || input.iterations < 100000) {
    return null;
  }
  if (typeof input.salt !== 'string' || base64ToBytes(input.salt)?.length !== APP_LOCK_SALT_BYTES) {
    return null;
  }
  if (typeof input.hash !== 'string' || base64ToBytes(input.hash)?.length !== APP_LOCK_HASH_BYTES) {
    return null;
  }

  return {
    version: APP_LOCK_VERIFIER_VERSION,
    algorithm: APP_LOCK_ALGORITHM,
    iterations: input.iterations,
    salt: input.salt,
    hash: input.hash,
  };
}

export function normalizeAppLockSettings(input: unknown): AppLockSettings {
  if (!isRecord(input)) return DEFAULT_APP_LOCK_SETTINGS;

  const timeoutMinutes = normalizeAppLockTimeoutMinutes(input.timeoutMinutes);
  const passwordVerifier = normalizeAppLockPasswordVerifier(input.passwordVerifier);
  const enabled = input.enabled === true && passwordVerifier !== null;
  const systemUnlockEnabled = input.systemUnlockEnabled === true && passwordVerifier !== null;

  return {
    enabled,
    timeoutMinutes,
    systemUnlockEnabled,
    passwordVerifier,
  };
}

async function derivePasswordHash(
  password: string,
  salt: Uint8Array,
  iterations: number,
): Promise<string> {
  const keyMaterial = await crypto.subtle.importKey(
    'raw',
    textEncoder.encode(password),
    'PBKDF2',
    false,
    ['deriveBits'],
  );
  const bits = await crypto.subtle.deriveBits(
    {
      name: 'PBKDF2',
      hash: 'SHA-256',
      salt,
      iterations,
    },
    keyMaterial,
    APP_LOCK_HASH_BYTES * 8,
  );
  return bytesToBase64(new Uint8Array(bits));
}

export async function createAppLockPasswordVerifier(password: string): Promise<AppLockPasswordVerifier> {
  if (!password.trim()) {
    throw new Error('App lock password is required');
  }

  const salt = crypto.getRandomValues(new Uint8Array(APP_LOCK_SALT_BYTES));
  return {
    version: APP_LOCK_VERIFIER_VERSION,
    algorithm: APP_LOCK_ALGORITHM,
    iterations: APP_LOCK_HASH_ITERATIONS,
    salt: bytesToBase64(salt),
    hash: await derivePasswordHash(password, salt, APP_LOCK_HASH_ITERATIONS),
  };
}

export async function verifyAppLockPassword(
  password: string,
  verifier: AppLockPasswordVerifier | null,
): Promise<boolean> {
  const normalized = normalizeAppLockPasswordVerifier(verifier);
  if (!password || !normalized) return false;

  const salt = base64ToBytes(normalized.salt);
  if (!salt) return false;

  const candidate = await derivePasswordHash(password, salt, normalized.iterations);
  return timingSafeEqualBase64(candidate, normalized.hash);
}

export function canEnableAppLock(settings: AppLockSettings): boolean {
  return normalizeAppLockPasswordVerifier(settings.passwordVerifier) !== null;
}
