import type { SSHKey } from "../domain/models";
import { isEncryptedCredentialPlaceholder } from "../domain/credentials";
import { STORAGE_KEY_DEFAULT_KEY_PASSPHRASES } from "../infrastructure/config/storageKeys";
import { localStorageAdapter } from "../infrastructure/persistence/localStorageAdapter";
import { encryptField, decryptField } from "../infrastructure/persistence/secureFieldAdapter";
import { netcattyBridge } from "../infrastructure/services/netcattyBridge";

function defaultKeyPassphrasePathKey(keyPath: string): string {
  const isWindowsPath = /^[A-Za-z]:[\\/]/u.test(keyPath) || /^[\\/]{2}/u.test(keyPath);
  if (!isWindowsPath) return keyPath;
  const normalized = keyPath.replace(/\\/g, "/");
  return normalized.toLowerCase();
}

function matchingPathKeys(keyPaths: string[]): Set<string> {
  return new Set(keyPaths.map(defaultKeyPassphrasePathKey));
}

export async function resolveDefaultKeyPassphraseAliases(keyPath: string): Promise<string[]> {
  const aliases = new Set([keyPath]);
  const isWindowsPath = /^[A-Za-z]:[\\/]/u.test(keyPath) || /^\\\\/u.test(keyPath);
  const normalizedKeyPath = isWindowsPath ? keyPath.replace(/\\/g, "/") : keyPath;
  aliases.add(normalizedKeyPath);
  try {
    const homeDir = await netcattyBridge.get()?.getHomeDir?.();
    if (!homeDir) return [...aliases];

    const normalizedHome = homeDir.replace(/\\/g, "/").replace(/\/$/u, "");
    const comparableHome = defaultKeyPassphrasePathKey(normalizedHome);
    const comparableKeyPath = defaultKeyPassphrasePathKey(normalizedKeyPath);
    if (comparableKeyPath.startsWith(`${comparableHome}/`)) {
      aliases.add(`~/${normalizedKeyPath.slice(normalizedHome.length + 1)}`);
    } else if (normalizedKeyPath.startsWith("~/")) {
      const suffix = normalizedKeyPath.slice(2);
      aliases.add(`${normalizedHome}/${suffix}`);
      const nativeHome = homeDir.replace(/[\\/]+$/u, "");
      const nativeSeparator = homeDir.includes("\\") ? "\\" : "/";
      aliases.add(`${nativeHome}${nativeSeparator}${suffix.replace(/\//g, nativeSeparator)}`);
    }
  } catch {
    // The renderer bridge may be unavailable in tests or web fallback mode.
  }
  return [...aliases];
}

export async function saveDefaultKeyPassphrase(keyPath: string, passphrase: string): Promise<void> {
  const store = localStorageAdapter.read<Record<string, string>>(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES) ?? {};
  const aliases = await resolveDefaultKeyPassphraseAliases(keyPath);
  const aliasKeys = matchingPathKeys(aliases);
  for (const storedPath of Object.keys(store)) {
    if (storedPath !== keyPath && aliasKeys.has(defaultKeyPassphrasePathKey(storedPath))) {
      delete store[storedPath];
    }
  }
  store[keyPath] = await encryptField(passphrase) ?? passphrase;
  localStorageAdapter.write(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES, store);
}

export async function loadDefaultKeyPassphrase(keyPath: string): Promise<string | null> {
  const store = localStorageAdapter.read<Record<string, string>>(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES);
  const aliases = await resolveDefaultKeyPassphraseAliases(keyPath);
  const aliasKeys = matchingPathKeys(aliases);
  const storedPath = Object.keys(store ?? {}).find((path) => (
    aliasKeys.has(defaultKeyPassphrasePathKey(path))
  ));
  const enc = storedPath ? store?.[storedPath] : undefined;
  if (!enc) return null;
  const decrypted = await decryptField(enc);
  if (!decrypted || isEncryptedCredentialPlaceholder(decrypted)) {
    removeDefaultKeyPassphrases(aliases);
    return null;
  }
  return decrypted;
}

export function removeDefaultKeyPassphrases(keyPaths: string[]): void {
  const store = localStorageAdapter.read<Record<string, string>>(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES);
  if (!store) return;
  const pathKeys = matchingPathKeys(keyPaths);
  let changed = false;
  for (const storedPath of Object.keys(store)) {
    if (pathKeys.has(defaultKeyPassphrasePathKey(storedPath))) {
      delete store[storedPath];
      changed = true;
    }
  }
  if (changed) {
    localStorageAdapter.write(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES, store);
  }
}

export async function removeDefaultKeyPassphraseAliases(keyPaths: string[]): Promise<string[]> {
  const aliases = Array.from(new Set((await Promise.all(
    keyPaths.map(resolveDefaultKeyPassphraseAliases),
  )).flat()));
  removeDefaultKeyPassphrases(aliases);
  return aliases;
}

export function clearReferenceKeyPassphrases(keys: SSHKey[], keyPaths: string[]): SSHKey[] {
  const pathKeys = matchingPathKeys(keyPaths);
  let changed = false;
  const updated = keys.map((key) => {
    if (
      key.source === "reference"
      && key.filePath
      && pathKeys.has(defaultKeyPassphrasePathKey(key.filePath))
      && key.passphrase
    ) {
      changed = true;
      return { ...key, passphrase: undefined, savePassphrase: false };
    }
    return key;
  });
  return changed ? updated : keys;
}

export function clearKeyPassphrasesByIds(keys: SSHKey[], keyIds: string[] = []): SSHKey[] {
  if (keyIds.length === 0) return keys;
  const ids = new Set(keyIds);
  let changed = false;
  const updated = keys.map((key) => {
    if (ids.has(key.id) && key.passphrase) {
      changed = true;
      return { ...key, passphrase: undefined, savePassphrase: false };
    }
    return key;
  });
  return changed ? updated : keys;
}

export function shouldUpdateReferenceKeyPassphrase(key?: SSHKey | null): boolean {
  return Boolean(
    key &&
      (!key.passphrase || isEncryptedCredentialPlaceholder(key.passphrase)),
  );
}

export async function rememberKeyPassphrase(args: {
  keyPath: string;
  passphrase: string;
  keys: SSHKey[];
  updateKeys: (keys: SSHKey[]) => Promise<unknown> | unknown;
  setCurrentKeys?: (keys: SSHKey[]) => void;
}): Promise<void> {
  const { keyPath, passphrase, keys, updateKeys, setCurrentKeys } = args;
  const aliases = await resolveDefaultKeyPassphraseAliases(keyPath);
  const aliasKeys = matchingPathKeys(aliases);
  await saveDefaultKeyPassphrase(keyPath, passphrase);

  let changed = false;
  const updated = keys.map((key) => {
    if (
      key.source !== "reference"
      || !key.filePath
      || !aliasKeys.has(defaultKeyPassphrasePathKey(key.filePath))
    ) return key;
    changed = true;
    return { ...key, passphrase, savePassphrase: true };
  });
  if (!changed) return;
  setCurrentKeys?.(updated);
  await updateKeys(updated);
}
