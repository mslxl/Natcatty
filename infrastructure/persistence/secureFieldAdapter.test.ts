import test from "node:test";
import assert from "node:assert/strict";

import type { SSHKey } from "../../domain/models";
import { ENCRYPTED_CREDENTIAL_PLACEHOLDER } from "../../domain/credentialsTestFixtures";
import { STORAGE_KEY_KEYS } from "../config/storageKeys";
import {
  decryptField,
  decryptKeySecrets,
  hydrateStoredKeySecrets,
} from "./secureFieldAdapter.ts";

const PRIVATE_KEY = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----";

const storedKey = (overrides: Partial<SSHKey> = {}): SSHKey => ({
  id: "key-1",
  label: "Imported",
  type: "ED25519",
  privateKey: ENCRYPTED_CREDENTIAL_PLACEHOLDER,
  source: "imported",
  category: "key",
  created: 1,
  ...overrides,
});

function installLocalStorage(t: test.TestContext): Map<string, string> {
  const store = new Map<string, string>();
  const storage: Storage = {
    get length() {
      return store.size;
    },
    clear() {
      store.clear();
    },
    getItem(key: string) {
      return store.get(key) ?? null;
    },
    key(index: number) {
      return Array.from(store.keys())[index] ?? null;
    },
    removeItem(key: string) {
      store.delete(key);
    },
    setItem(key: string, value: string) {
      store.set(key, value);
    },
  };
  const previousLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: storage,
  });
  t.after(() => {
    if (previousLocalStorage) Object.defineProperty(globalThis, "localStorage", previousLocalStorage);
    else delete (globalThis as { localStorage?: Storage }).localStorage;
  });
  return store;
}

function installBridge(
  t: test.TestContext,
  netcatty: { credentialsDecrypt?: (value: string) => Promise<string> } | undefined,
): void {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { netcatty },
  });
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else delete (globalThis as { window?: unknown }).window;
  });
}

test("decryptField does not echo enc:v1 ciphertext when the credential bridge is missing", async (t) => {
  installBridge(t, undefined);
  assert.equal(await decryptField(ENCRYPTED_CREDENTIAL_PLACEHOLDER), undefined);
  assert.equal(await decryptField("plain-secret"), "plain-secret");
});

test("decryptField does not echo enc:v1 ciphertext when decrypt returns the same value", async (t) => {
  installBridge(t, {
    credentialsDecrypt: async (value: string) => value,
  });
  assert.equal(await decryptField(ENCRYPTED_CREDENTIAL_PLACEHOLDER), undefined);
});

test("decryptField does not echo enc:v1 ciphertext when decrypt throws", async (t) => {
  installBridge(t, {
    credentialsDecrypt: async () => {
      throw new Error("safeStorage unavailable");
    },
  });
  assert.equal(await decryptField(ENCRYPTED_CREDENTIAL_PLACEHOLDER), undefined);
});

test("decryptField returns plaintext once the credential bridge decrypts", async (t) => {
  installBridge(t, {
    credentialsDecrypt: async (value: string) => {
      assert.equal(value, ENCRYPTED_CREDENTIAL_PLACEHOLDER);
      return PRIVATE_KEY;
    },
  });
  assert.equal(await decryptField(ENCRYPTED_CREDENTIAL_PLACEHOLDER), PRIVATE_KEY);
});

test("decryptKeySecrets does not keep enc:v1 privateKey material in memory", async (t) => {
  installBridge(t, undefined);
  const decrypted = await decryptKeySecrets(storedKey());
  assert.equal(decrypted.privateKey, "");
});

test("hydrateStoredKeySecrets waits until ciphertext decrypts", async (t) => {
  installLocalStorage(t);
  let attempts = 0;
  installBridge(t, {
    credentialsDecrypt: async (value: string) => {
      attempts += 1;
      if (attempts < 3) return value;
      return PRIVATE_KEY;
    },
  });
  const hydrated = await hydrateStoredKeySecrets(storedKey(), {
    timeoutMs: 500,
    retryDelayMs: 10,
  });
  assert.equal(hydrated.unreadable, false);
  assert.equal(hydrated.key.privateKey, PRIVATE_KEY);
  assert.ok(attempts >= 3);
});

test("hydrateStoredKeySecrets re-reads storage when in-memory privateKey was stripped", async (t) => {
  const store = installLocalStorage(t);
  store.set(STORAGE_KEY_KEYS, JSON.stringify([storedKey()]));
  installBridge(t, {
    credentialsDecrypt: async (value: string) => {
      assert.equal(value, ENCRYPTED_CREDENTIAL_PLACEHOLDER);
      return PRIVATE_KEY;
    },
  });
  const hydrated = await hydrateStoredKeySecrets(storedKey({ privateKey: "" }), {
    timeoutMs: 100,
    retryDelayMs: 10,
  });
  assert.equal(hydrated.unreadable, false);
  assert.equal(hydrated.key.privateKey, PRIVATE_KEY);
});

test("hydrateStoredKeySecrets does not wait when there is no decrypt bridge", async (t) => {
  installBridge(t, undefined);
  const started = Date.now();
  const hydrated = await hydrateStoredKeySecrets(storedKey(), {
    timeoutMs: 2000,
    retryDelayMs: 50,
  });
  assert.equal(hydrated.unreadable, true);
  assert.equal(hydrated.key.privateKey, "");
  assert.ok(Date.now() - started < 200);
});
