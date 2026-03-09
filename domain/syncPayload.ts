/**
 * Sync Payload Builders — Single source of truth for constructing and applying
 * the encrypted cloud-sync payload.
 *
 * Both the main window (App.tsx) and the settings window (SettingsSyncTab.tsx)
 * must use these helpers to guarantee every field is included and no data is
 * silently dropped.
 */

import type {
  Host,
  Identity,
  KnownHost,
  PortForwardingRule,
  Snippet,
  SSHKey,
} from './models';
import type { SyncPayload } from './sync';

// ---------------------------------------------------------------------------
// Input types
// ---------------------------------------------------------------------------

/** All vault-owned data that participates in cloud sync. */
export interface SyncableVaultData {
  hosts: Host[];
  keys: SSHKey[];
  identities: Identity[];
  snippets: Snippet[];
  customGroups: string[];
  knownHosts: KnownHost[];
}

/** Callbacks used by `applySyncPayload` to import data into local state. */
export interface SyncPayloadImporters {
  /** Import vault data (hosts, keys, identities, snippets, customGroups, knownHosts). */
  importVaultData: (jsonString: string) => void;
  /** Import port-forwarding rules (lives outside the vault hook). */
  importPortForwardingRules?: (rules: PortForwardingRule[]) => void;
}

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

/**
 * Build a complete `SyncPayload` from local data.
 *
 * Port-forwarding rules are optional because they are managed by a separate
 * state hook (`usePortForwardingState`).  Callers should strip transient
 * runtime fields (status, error, lastUsedAt) before passing them in.
 */
export function buildSyncPayload(
  vault: SyncableVaultData,
  portForwardingRules?: PortForwardingRule[],
): SyncPayload {
  return {
    hosts: vault.hosts,
    keys: vault.keys,
    identities: vault.identities,
    snippets: vault.snippets,
    customGroups: vault.customGroups,
    knownHosts: vault.knownHosts,
    portForwardingRules,
    syncedAt: Date.now(),
  };
}

/**
 * Apply a downloaded `SyncPayload` to local state via the provided importers.
 *
 * This ensures both vault data and port-forwarding rules are imported
 * consistently across windows.
 */
export function applySyncPayload(
  payload: SyncPayload,
  importers: SyncPayloadImporters,
): void {
  importers.importVaultData(
    JSON.stringify({
      hosts: payload.hosts,
      keys: payload.keys,
      identities: payload.identities,
      snippets: payload.snippets,
      customGroups: payload.customGroups,
      // Older payloads may omit knownHosts — treat as empty to ensure
      // "download and replace" truly replaces all data.
      knownHosts: payload.knownHosts ?? [],
    }),
  );

  // Always import port-forwarding rules (empty array if absent in payload)
  // so that "download and replace" clears stale local-only rules.
  if (importers.importPortForwardingRules) {
    importers.importPortForwardingRules(payload.portForwardingRules ?? []);
  }
}
