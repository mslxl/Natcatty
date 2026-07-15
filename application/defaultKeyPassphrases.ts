import type { SSHKey } from "../domain/models";
import { isEncryptedCredentialPlaceholder } from "../domain/credentials";
import { STORAGE_KEY_DEFAULT_KEY_PASSPHRASES } from "../infrastructure/config/storageKeys";
import { localStorageAdapter } from "../infrastructure/persistence/localStorageAdapter";
import { encryptField, decryptField } from "../infrastructure/persistence/secureFieldAdapter";
import { netcattyBridge } from "../infrastructure/services/netcattyBridge";

async function resolveDefaultKeyPassphraseAliases(keyPath: string): Promise<string[]> {
  const aliases = new Set([keyPath]);
  try {
    const homeDir = await netcattyBridge.get()?.getHomeDir?.();
    if (!homeDir) return [...aliases];

    const normalizedHome = homeDir.replace(/\\/g, "/").replace(/\/$/u, "");
    const normalizedKeyPath = keyPath.replace(/\\/g, "/");
    if (normalizedKeyPath.startsWith(`${normalizedHome}/`)) {
      aliases.add(`~/${normalizedKeyPath.slice(normalizedHome.length + 1)}`);
    } else if (normalizedKeyPath.startsWith("~/")) {
      aliases.add(`${normalizedHome}/${normalizedKeyPath.slice(2)}`);
    }
  } catch {
    // The renderer bridge may be unavailable in tests or web fallback mode.
  }
  return [...aliases];
}

export async function saveDefaultKeyPassphrase(keyPath: string, passphrase: string): Promise<void> {
  const store = localStorageAdapter.read<Record<string, string>>(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES) ?? {};
  store[keyPath] = await encryptField(passphrase) ?? passphrase;
  localStorageAdapter.write(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES, store);
}

export async function loadDefaultKeyPassphrase(keyPath: string): Promise<string | null> {
  const store = localStorageAdapter.read<Record<string, string>>(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES);
  const aliases = await resolveDefaultKeyPassphraseAliases(keyPath);
  const enc = aliases.map((alias) => store?.[alias]).find(Boolean);
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
  let changed = false;
  for (const keyPath of keyPaths) {
    if (keyPath in store) {
      delete store[keyPath];
      changed = true;
    }
  }
  if (changed) {
    localStorageAdapter.write(STORAGE_KEY_DEFAULT_KEY_PASSPHRASES, store);
  }
}

export function clearReferenceKeyPassphrases(keys: SSHKey[], keyPaths: string[]): SSHKey[] {
  let changed = false;
  const updated = keys.map((key) => {
    if (key.source === "reference" && key.filePath && keyPaths.includes(key.filePath) && key.passphrase) {
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
  await saveDefaultKeyPassphrase(keyPath, passphrase);

  const refKey = keys.find((key) => key.source === "reference" && key.filePath === keyPath);
  if (!refKey) return;

  const updated = keys.map((key) =>
    key.id === refKey.id
      ? { ...key, passphrase, savePassphrase: true }
      : key
  );
  setCurrentKeys?.(updated);
  await updateKeys(updated);
}
