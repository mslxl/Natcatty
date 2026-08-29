/**
 * Secure Field Adapter — Renderer-side helpers for field-level encryption
 *
 * Encrypts / decrypts individual sensitive fields within domain models before
 * they are written to (or after they are read from) localStorage.
 *
 * The heavy lifting is done by Electron's safeStorage via the credential
 * bridge IPC.  When the bridge is unavailable (web fallback, tests) plaintext
 * values pass through unmodified. Ciphertext (`enc:v1:` placeholders) is never
 * echoed as plaintext — decrypt returns undefined so callers can wait or treat
 * the field as unread.
 */

import type { GroupConfig, Host, Identity, ProxyProfile, SSHKey } from "../../domain/models";
import type { ProviderConnection, S3Config, WebDAVConfig } from "../../domain/sync";
import {
  isEncryptedCredentialPlaceholder,
  needsVaultStoredKeyHydration,
  sanitizeCredentialValue,
} from "../../domain/credentials";
import { STORAGE_KEY_KEYS } from "../config/storageKeys";
import { netcattyBridge } from "../services/netcattyBridge";
import { localStorageAdapter } from "./localStorageAdapter";

// ---------------------------------------------------------------------------
// Primitive helpers
// ---------------------------------------------------------------------------

const bridge = () => netcattyBridge.get();

const STORED_KEY_HYDRATE_RETRY_DELAY_MS = 50;
const STORED_KEY_HYDRATE_TIMEOUT_MS = 2000;

const sleep = (ms: number): Promise<void> => new Promise((resolve) => {
  setTimeout(resolve, ms);
});

export async function encryptField(value: string | undefined): Promise<string | undefined> {
  if (!value) return value;
  const b = bridge();
  if (!b?.credentialsEncrypt) return value;
  return b.credentialsEncrypt(value);
}

export async function decryptField(value: string | undefined): Promise<string | undefined> {
  if (!value) return value;
  const encrypted = isEncryptedCredentialPlaceholder(value);
  const b = bridge();
  if (!b?.credentialsDecrypt) {
    return encrypted ? undefined : value;
  }
  try {
    const decrypted = await b.credentialsDecrypt(value);
    if (
      encrypted
      && (
        !decrypted
        || decrypted === value
        || isEncryptedCredentialPlaceholder(decrypted)
      )
    ) {
      return undefined;
    }
    return decrypted;
  } catch (err) {
    if (encrypted) return undefined;
    throw err;
  }
}

const readStoredSshKey = (id: string): SSHKey | undefined => {
  try {
    const stored = localStorageAdapter.read<SSHKey[]>(STORAGE_KEY_KEYS);
    if (!Array.isArray(stored)) return undefined;
    return stored.find((key) => key?.id === id);
  } catch {
    return undefined;
  }
};

export type HydratedStoredKey = {
  key: SSHKey;
  unreadable: boolean;
};

/**
 * Retry decrypt for vault-stored (imported/generated) private keys instead of
 * treating enc:v1: ciphertext as key material. Re-reads the encrypted snapshot
 * from storage when in-memory privateKey was already stripped.
 */
export async function hydrateStoredKeySecrets(
  key: SSHKey,
  options?: { timeoutMs?: number; retryDelayMs?: number },
): Promise<HydratedStoredKey> {
  if (key.source === "reference" || !needsVaultStoredKeyHydration(key)) {
    return { key, unreadable: false };
  }

  const timeoutMs = options?.timeoutMs ?? STORED_KEY_HYDRATE_TIMEOUT_MS;
  const retryDelayMs = options?.retryDelayMs ?? STORED_KEY_HYDRATE_RETRY_DELAY_MS;
  const startedAt = Date.now();
  let candidate = key;
  let sawCiphertext = isEncryptedCredentialPlaceholder(candidate.privateKey);

  while (true) {
    if (!candidate.privateKey) {
      const stored = readStoredSshKey(key.id);
      if (stored?.privateKey && stored.privateKey !== candidate.privateKey) {
        candidate = {
          ...candidate,
          privateKey: stored.privateKey,
          passphrase: stored.passphrase ?? candidate.passphrase,
        };
      }
    }

    if (isEncryptedCredentialPlaceholder(candidate.privateKey)) {
      sawCiphertext = true;
    }

    const decryptedPrivate = await decryptField(candidate.privateKey || undefined);
    const privateKey = sanitizeCredentialValue(decryptedPrivate);
    if (privateKey) {
      const decryptedPassphrase = candidate.passphrase != null
        ? await decryptField(candidate.passphrase)
        : undefined;
      return {
        key: {
          ...candidate,
          privateKey,
          passphrase: sanitizeCredentialValue(decryptedPassphrase) ?? (
            isEncryptedCredentialPlaceholder(candidate.passphrase)
              ? undefined
              : candidate.passphrase
          ),
        },
        unreadable: false,
      };
    }

    const decryptReady = Boolean(bridge()?.credentialsDecrypt);
    if (!decryptReady || Date.now() - startedAt >= timeoutMs) {
      return {
        key: {
          ...candidate,
          privateKey: sanitizeCredentialValue(candidate.privateKey) ?? "",
          passphrase: isEncryptedCredentialPlaceholder(candidate.passphrase)
            ? undefined
            : candidate.passphrase,
        },
        unreadable: sawCiphertext,
      };
    }

    await sleep(retryDelayMs);
    const stored = readStoredSshKey(key.id);
    if (stored?.privateKey) {
      candidate = {
        ...candidate,
        privateKey: stored.privateKey,
        passphrase: stored.passphrase ?? candidate.passphrase,
      };
    }
  }
}

export async function hydrateVaultStoredKeys(
  keys: SSHKey[],
  options?: { timeoutMs?: number; retryDelayMs?: number },
): Promise<{ keys: SSHKey[]; unreadableKeyIds: Set<string> }> {
  const unreadableKeyIds = new Set<string>();
  const next = await Promise.all(keys.map(async (key) => {
    if (!needsVaultStoredKeyHydration(key)) return key;
    const hydrated = await hydrateStoredKeySecrets(key, options);
    if (hydrated.unreadable) unreadableKeyIds.add(key.id);
    return hydrated.key;
  }));
  return { keys: next, unreadableKeyIds };
}

// ---------------------------------------------------------------------------
// Host
// ---------------------------------------------------------------------------

export async function encryptHostSecrets(host: Host): Promise<Host> {
  const out = { ...host };
  out.password = await encryptField(out.password);
  out.telnetPassword = await encryptField(out.telnetPassword);
  if (out.proxyConfig?.password) {
    out.proxyConfig = { ...out.proxyConfig, password: await encryptField(out.proxyConfig.password) };
  }
  return out;
}

export async function decryptHostSecrets(host: Host): Promise<Host> {
  const out = { ...host };
  out.password = await decryptField(out.password);
  out.telnetPassword = await decryptField(out.telnetPassword);
  if (out.proxyConfig?.password) {
    out.proxyConfig = { ...out.proxyConfig, password: await decryptField(out.proxyConfig.password) };
  }
  return out;
}

// ---------------------------------------------------------------------------
// SSHKey
// ---------------------------------------------------------------------------

export async function encryptKeySecrets(key: SSHKey): Promise<SSHKey> {
  const out = { ...key };
  out.passphrase = await encryptField(out.passphrase);
  out.privateKey = (await encryptField(out.privateKey)) ?? "";
  return out;
}

export async function decryptKeySecrets(key: SSHKey): Promise<SSHKey> {
  const out = { ...key };
  out.passphrase = await decryptField(out.passphrase);
  out.privateKey = (await decryptField(out.privateKey)) ?? "";
  return out;
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

export async function encryptIdentitySecrets(identity: Identity): Promise<Identity> {
  const out = { ...identity };
  out.password = await encryptField(out.password);
  return out;
}

export async function decryptIdentitySecrets(identity: Identity): Promise<Identity> {
  const out = { ...identity };
  out.password = await decryptField(out.password);
  return out;
}

// ---------------------------------------------------------------------------
// GroupConfig
// ---------------------------------------------------------------------------

export async function encryptGroupConfigSecrets(config: GroupConfig): Promise<GroupConfig> {
  const out = { ...config };
  out.password = await encryptField(out.password);
  out.telnetPassword = await encryptField(out.telnetPassword);
  if (out.proxyConfig?.password) {
    out.proxyConfig = { ...out.proxyConfig, password: await encryptField(out.proxyConfig.password) };
  }
  return out;
}

export async function decryptGroupConfigSecrets(config: GroupConfig): Promise<GroupConfig> {
  const out = { ...config };
  out.password = await decryptField(out.password);
  out.telnetPassword = await decryptField(out.telnetPassword);
  if (out.proxyConfig?.password) {
    out.proxyConfig = { ...out.proxyConfig, password: await decryptField(out.proxyConfig.password) };
  }
  return out;
}

export function encryptGroupConfigs(configs: GroupConfig[]): Promise<GroupConfig[]> {
  return Promise.all(configs.map(encryptGroupConfigSecrets));
}

export function decryptGroupConfigs(configs: GroupConfig[]): Promise<GroupConfig[]> {
  return Promise.all(configs.map(decryptGroupConfigSecrets));
}

// ---------------------------------------------------------------------------
// ProxyProfile
// ---------------------------------------------------------------------------

export async function encryptProxyProfileSecrets(profile: ProxyProfile): Promise<ProxyProfile> {
  const out = { ...profile, config: { ...profile.config } };
  out.config.password = await encryptField(out.config.password);
  return out;
}

export async function decryptProxyProfileSecrets(profile: ProxyProfile): Promise<ProxyProfile> {
  const out = { ...profile, config: { ...profile.config } };
  out.config.password = await decryptField(out.config.password);
  return out;
}

export function encryptProxyProfiles(profiles: ProxyProfile[]): Promise<ProxyProfile[]> {
  return Promise.all(profiles.map(encryptProxyProfileSecrets));
}

export function decryptProxyProfiles(profiles: ProxyProfile[]): Promise<ProxyProfile[]> {
  return Promise.all(profiles.map(decryptProxyProfileSecrets));
}

// ---------------------------------------------------------------------------
// Provider Connection (Cloud Sync)
// ---------------------------------------------------------------------------

/**
 * Host-owned sealed-config envelope. Must be unambiguous against plugin-owned
 * JSON: exactly one reserved key, no extra properties. Never treat a plugin
 * object that merely contains a similar key as already sealed.
 */
const PLUGIN_CONFIG_ENVELOPE_KEY = "__netcatty_plugin_config_v1" as const;
const LEGACY_PLUGIN_CONFIG_ENVELOPE_KEY = "__encryptedPluginConfig" as const;
/** At-rest envelope for ProviderConnection.credential (opaque refs only). */
const PLUGIN_CREDENTIAL_ENVELOPE_KEY = "__netcatty_plugin_credential_v1" as const;

type PluginConfigEnvelope = {
  [PLUGIN_CONFIG_ENVELOPE_KEY]: string;
};

type PluginCredentialEnvelope = {
  [PLUGIN_CREDENTIAL_ENVELOPE_KEY]: string;
};

function isPluginConfigEnvelope(value: unknown): value is PluginConfigEnvelope {
  if (value == null || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  return keys.length === 1
    && keys[0] === PLUGIN_CONFIG_ENVELOPE_KEY
    && typeof record[PLUGIN_CONFIG_ENVELOPE_KEY] === "string";
}

/** Legacy envelope shape (still accepted on decrypt for one migration hop). */
function isLegacyPluginConfigEnvelope(value: unknown): value is { __encryptedPluginConfig: string } {
  if (value == null || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  return keys.length === 1
    && keys[0] === LEGACY_PLUGIN_CONFIG_ENVELOPE_KEY
    && typeof record[LEGACY_PLUGIN_CONFIG_ENVELOPE_KEY] === "string";
}

function isPluginCredentialEnvelope(value: unknown): value is PluginCredentialEnvelope {
  if (value == null || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  return keys.length === 1
    && keys[0] === PLUGIN_CREDENTIAL_ENVELOPE_KEY
    && typeof record[PLUGIN_CREDENTIAL_ENVELOPE_KEY] === "string";
}

export async function encryptProviderSecrets(conn: ProviderConnection): Promise<ProviderConnection> {
  const out = { ...conn };

  if (out.tokens) {
    const t = { ...out.tokens };
    t.accessToken = (await encryptField(t.accessToken)) ?? "";
    t.refreshToken = await encryptField(t.refreshToken);
    out.tokens = t;
  }

  // Config may be a valid falsy scalar (false, 0, "") — only null/undefined means absent.
  if (out.config != null) {
    const providerId = String(out.provider ?? "");
    const isBuiltin = providerId === "webdav"
      || providerId === "s3"
      || providerId === "github"
      || providerId === "google"
      || providerId === "onedrive";
    // Built-in providers use field-level encryption; plugin IDs always seal
    // the whole opaque config so field-name collisions cannot leak secrets.
    if (isBuiltin && typeof out.config === "object" && "authType" in out.config) {
      const c = { ...out.config } as WebDAVConfig;
      c.password = await encryptField(c.password);
      c.token = await encryptField(c.token);
      out.config = c;
    } else if (isBuiltin && typeof out.config === "object" && "secretAccessKey" in out.config) {
      const c = { ...out.config } as S3Config;
      c.secretAccessKey = (await encryptField(c.secretAccessKey)) ?? "";
      c.sessionToken = await encryptField(c.sessionToken);
      out.config = c;
    } else if (!isBuiltin) {
      // Always (re)seal opaque plugin config. An exact marker-shaped object may
      // be either a trusted host envelope or plugin-owned JSON that collides
      // with our key — try unwrap; on failure seal the whole value as opaque.
      let toSeal: unknown = out.config;
      if (isPluginConfigEnvelope(out.config) || isLegacyPluginConfigEnvelope(out.config)) {
        const sealedValue = isPluginConfigEnvelope(out.config)
          ? out.config[PLUGIN_CONFIG_ENVELOPE_KEY]
          : out.config[LEGACY_PLUGIN_CONFIG_ENVELOPE_KEY];
        const plain = await decryptField(sealedValue);
        if (plain != null && plain !== "") {
          try {
            toSeal = JSON.parse(plain);
          } catch {
            toSeal = out.config;
          }
        } else {
          toSeal = out.config;
        }
      }
      const sealed = await encryptField(JSON.stringify(toSeal));
      if (sealed) {
        out.config = {
          [PLUGIN_CONFIG_ENVELOPE_KEY]: sealed,
        } as ProviderConnection["config"];
      }
    }
  }

  // Seal durable plugin credential refs as one opaque blob (same threat model
  // as plugin config: do not leave kind/id/key plaintext in localStorage).
  if (out.credential != null && typeof out.credential === "object") {
    let toSeal: unknown = out.credential;
    if (isPluginCredentialEnvelope(out.credential)) {
      const plain = await decryptField(out.credential[PLUGIN_CREDENTIAL_ENVELOPE_KEY]);
      if (plain != null && plain !== "") {
        try {
          toSeal = JSON.parse(plain);
        } catch {
          toSeal = out.credential;
        }
      } else {
        toSeal = out.credential;
      }
    }
    const kind = (toSeal as { kind?: unknown }).kind;
    const id = (toSeal as { id?: unknown }).id;
    const key = (toSeal as { key?: unknown }).key;
    if ((kind === "secret" || kind === "credential") && typeof id === "string" && id.length > 0) {
      const normalized = {
        kind,
        id,
        ...(typeof key === "string" ? { key } : {}),
      };
      const sealed = await encryptField(JSON.stringify(normalized));
      if (sealed) {
        out.credential = {
          [PLUGIN_CREDENTIAL_ENVELOPE_KEY]: sealed,
        } as unknown as ProviderConnection["credential"];
      }
    } else if (isPluginCredentialEnvelope(toSeal)) {
      // Marker-collision object that is not a durable ref — seal as opaque JSON.
      const sealed = await encryptField(JSON.stringify(toSeal));
      if (sealed) {
        out.credential = {
          [PLUGIN_CREDENTIAL_ENVELOPE_KEY]: sealed,
        } as unknown as ProviderConnection["credential"];
      }
    } else {
      // Drop leases / malformed shapes — never persist them at rest.
      delete out.credential;
    }
  }

  return out;
}

export async function decryptProviderSecrets(conn: ProviderConnection): Promise<ProviderConnection> {
  const out = { ...conn };

  if (out.tokens) {
    const t = { ...out.tokens };
    t.accessToken = (await decryptField(t.accessToken)) ?? "";
    t.refreshToken = await decryptField(t.refreshToken);
    out.tokens = t;
  }

  // Config may be a valid falsy scalar — only null/undefined means absent.
  if (out.config != null) {
    const providerId = String(out.provider ?? "");
    const isBuiltin = providerId === "webdav"
      || providerId === "s3"
      || providerId === "github"
      || providerId === "google"
      || providerId === "onedrive";
    if (isBuiltin && typeof out.config === "object" && "authType" in out.config) {
      const c = { ...out.config } as WebDAVConfig;
      c.password = await decryptField(c.password);
      c.token = await decryptField(c.token);
      out.config = c;
    } else if (isBuiltin && typeof out.config === "object" && "secretAccessKey" in out.config) {
      const c = { ...out.config } as S3Config;
      c.secretAccessKey = (await decryptField(c.secretAccessKey)) ?? "";
      c.sessionToken = await decryptField(c.sessionToken);
      out.config = c;
    } else if (isPluginConfigEnvelope(out.config) || isLegacyPluginConfigEnvelope(out.config)) {
      const sealed = isPluginConfigEnvelope(out.config)
        ? out.config[PLUGIN_CONFIG_ENVELOPE_KEY]
        : out.config[LEGACY_PLUGIN_CONFIG_ENVELOPE_KEY];
      const plain = await decryptField(sealed);
      // plain may be JSON "false"/"0"/'""' — treat empty decrypt as failure only.
      if (plain != null && plain !== "") {
        try {
          out.config = JSON.parse(plain) as ProviderConnection["config"];
        } catch {
          // leave sealed if corrupt
        }
      }
    }
  }

  if (isPluginCredentialEnvelope(out.credential)) {
    const plain = await decryptField(out.credential[PLUGIN_CREDENTIAL_ENVELOPE_KEY]);
    if (plain != null && plain !== "") {
      try {
        const parsed = JSON.parse(plain) as {
          kind?: unknown;
          id?: unknown;
          key?: unknown;
        };
        if (
          (parsed.kind === "secret" || parsed.kind === "credential")
          && typeof parsed.id === "string"
          && parsed.id.length > 0
        ) {
          out.credential = {
            kind: parsed.kind,
            id: parsed.id,
            ...(typeof parsed.key === "string" ? { key: parsed.key } : {}),
          };
        } else {
          delete out.credential;
        }
      } catch {
        // leave sealed if corrupt
      }
    }
  }

  return out;
}

// ---------------------------------------------------------------------------
// Batch helpers
// ---------------------------------------------------------------------------

export function encryptHosts(hosts: Host[]): Promise<Host[]> {
  return Promise.all(hosts.map(encryptHostSecrets));
}

export function decryptHosts(hosts: Host[]): Promise<Host[]> {
  return Promise.all(hosts.map(decryptHostSecrets));
}

export function encryptKeys(keys: SSHKey[]): Promise<SSHKey[]> {
  return Promise.all(keys.map(encryptKeySecrets));
}

export function decryptKeys(keys: SSHKey[]): Promise<SSHKey[]> {
  return Promise.all(keys.map(decryptKeySecrets));
}

export function encryptIdentities(identities: Identity[]): Promise<Identity[]> {
  return Promise.all(identities.map(encryptIdentitySecrets));
}

export function decryptIdentities(identities: Identity[]): Promise<Identity[]> {
  return Promise.all(identities.map(decryptIdentitySecrets));
}
