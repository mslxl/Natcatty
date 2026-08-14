import {
  DEFAULT_CLOUD_SYNC_STRATEGY,
  normalizeCloudSyncStrategy,
  type CloudSyncStrategy,
} from '../../../domain/syncStrategy';

export type SyncPreferencePersistFields = {
  autoSync: boolean;
  interval: number;
  syncStrategy: CloudSyncStrategy;
};

export type SyncVersionPersistFields = {
  localVersion: number;
  localUpdatedAt: number;
  remoteVersion: number;
  remoteUpdatedAt: number;
};

export type SyncConfigPersistFields = SyncPreferencePersistFields & SyncVersionPersistFields;

/**
 * Prefer the dedicated preferences key; fall back to legacy fields still
 * embedded in SYNC_CONFIG from older builds.
 */
export function coalesceStoredSyncPreferences(
  preferences: Partial<SyncPreferencePersistFields> | null | undefined,
  legacyConfig: Partial<SyncPreferencePersistFields> | null | undefined,
): Partial<SyncPreferencePersistFields> | null {
  if (preferences && typeof preferences === 'object') return preferences;
  if (legacyConfig && typeof legacyConfig === 'object') return legacyConfig;
  return null;
}

export function resolveSyncPreferencesForPersist(input: {
  memory: SyncPreferencePersistFields;
  stored: Partial<SyncPreferencePersistFields> | null | undefined;
  /**
   * When true, preference fields come from this window's memory — used by
   * setAutoSync / setSyncStrategy. When false (default), preference fields
   * are taken from storage so a version-only save cannot invent prefs.
   */
  preferencesFromMemory?: boolean;
}): SyncPreferencePersistFields {
  const { memory, stored, preferencesFromMemory = false } = input;
  const fromStorage = stored && typeof stored === 'object' ? stored : null;

  if (preferencesFromMemory || !fromStorage) {
    return {
      autoSync: Boolean(memory.autoSync),
      interval: Number(memory.interval),
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
    syncStrategy: normalizeCloudSyncStrategy(
      fromStorage.syncStrategy !== undefined
        ? fromStorage.syncStrategy
        : memory.syncStrategy ?? DEFAULT_CLOUD_SYNC_STRATEGY,
    ),
  };
}

export function resolveSyncVersionsForPersist(
  memory: SyncVersionPersistFields,
): SyncVersionPersistFields {
  return {
    localVersion: Number(memory.localVersion),
    localUpdatedAt: Number(memory.localUpdatedAt),
    remoteVersion: Number(memory.remoteVersion),
    remoteUpdatedAt: Number(memory.remoteUpdatedAt),
  };
}

/**
 * @deprecated Prefer resolveSyncPreferencesForPersist + resolveSyncVersionsForPersist.
 * Kept for unit tests that assert the combined legacy shape.
 */
export function resolveSyncConfigForPersist(input: {
  memory: SyncConfigPersistFields;
  stored: Partial<SyncConfigPersistFields> | null | undefined;
  preferencesFromMemory?: boolean;
}): SyncConfigPersistFields {
  return {
    ...resolveSyncPreferencesForPersist({
      memory: {
        autoSync: input.memory.autoSync,
        interval: input.memory.interval,
        syncStrategy: input.memory.syncStrategy,
      },
      stored: input.stored,
      preferencesFromMemory: input.preferencesFromMemory,
    }),
    ...resolveSyncVersionsForPersist(input.memory),
  };
}
