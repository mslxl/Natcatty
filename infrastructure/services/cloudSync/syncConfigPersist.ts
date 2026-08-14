import {
  DEFAULT_CLOUD_SYNC_STRATEGY,
  normalizeCloudSyncStrategy,
  type CloudSyncStrategy,
} from '../../../domain/syncStrategy';

export type SyncConfigPersistFields = {
  autoSync: boolean;
  interval: number;
  localVersion: number;
  localUpdatedAt: number;
  remoteVersion: number;
  remoteUpdatedAt: number;
  syncStrategy: CloudSyncStrategy;
};

export type SyncConfigPersistInput = {
  memory: SyncConfigPersistFields;
  stored: Partial<SyncConfigPersistFields> | null | undefined;
  /**
   * When true, preference fields (autoSync / interval / syncStrategy) come from
   * this window's memory — used by setAutoSync / setSyncStrategy.
   * When false (default), preference fields are taken from storage so a
   * version-only save in one window cannot clobber another window's toggle.
   */
  preferencesFromMemory?: boolean;
};

/**
 * Build the SYNC_CONFIG object to persist.
 *
 * Cross-window hazard: upload/download paths call saveSyncConfig to bump
 * versions. If another window just disabled auto-sync in storage, writing
 * this window's stale in-memory autoSync=true would re-enable it and keep
 * auto-pushing after local edits (#2976).
 */
export function resolveSyncConfigForPersist(
  input: SyncConfigPersistInput,
): SyncConfigPersistFields {
  const { memory, stored, preferencesFromMemory = false } = input;
  const fromStorage = stored && typeof stored === 'object' ? stored : null;

  if (preferencesFromMemory || !fromStorage) {
    return {
      autoSync: Boolean(memory.autoSync),
      interval: Number(memory.interval),
      localVersion: Number(memory.localVersion),
      localUpdatedAt: Number(memory.localUpdatedAt),
      remoteVersion: Number(memory.remoteVersion),
      remoteUpdatedAt: Number(memory.remoteUpdatedAt),
      syncStrategy: normalizeCloudSyncStrategy(memory.syncStrategy),
    };
  }

  return {
    autoSync: Boolean(
      fromStorage.autoSync !== undefined ? fromStorage.autoSync : memory.autoSync,
    ),
    interval: Number(
      fromStorage.interval !== undefined ? fromStorage.interval : memory.interval,
    ),
    localVersion: Number(memory.localVersion),
    localUpdatedAt: Number(memory.localUpdatedAt),
    remoteVersion: Number(memory.remoteVersion),
    remoteUpdatedAt: Number(memory.remoteUpdatedAt),
    syncStrategy: normalizeCloudSyncStrategy(
      fromStorage.syncStrategy !== undefined
        ? fromStorage.syncStrategy
        : memory.syncStrategy ?? DEFAULT_CLOUD_SYNC_STRATEGY,
    ),
  };
}
