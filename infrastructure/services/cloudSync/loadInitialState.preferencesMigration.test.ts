import test from 'node:test';
import assert from 'node:assert/strict';

import { SYNC_STORAGE_KEYS } from '../../../domain/sync.ts';
import { loadInitialStateImpl, loadProviderConnectionImpl } from './stateAndSecurityMethods.ts';

test('startup preference migration does not clobber a concurrent autoSync=false', () => {
  const storage = new Map<string, unknown>();
  storage.set(SYNC_STORAGE_KEYS.DEVICE_ID, 'test-device');
  storage.set(SYNC_STORAGE_KEYS.DEVICE_NAME, 'Test Device');
  // Legacy combined blob from pre-split builds.
  storage.set(SYNC_STORAGE_KEYS.SYNC_CONFIG, {
    autoSync: true,
    interval: 5,
    syncStrategy: 'smartMerge',
    localVersion: 2,
    localUpdatedAt: 100,
    remoteVersion: 2,
    remoteUpdatedAt: 100,
  });

  let preferenceReads = 0;
  const manager = {
    providerWriteSeq: {} as Record<string, number>,
    providerDecryptSeq: {} as Record<string, number>,
    providerDecrypted: {} as Record<string, boolean>,
    providerAuthAttemptSeq: {} as Record<string, number>,
    providerAuthRestoreState: {} as Record<string, unknown>,
    loadFromStorage(key: string) {
      if (key === SYNC_STORAGE_KEYS.SYNC_PREFERENCES) {
        preferenceReads += 1;
        // Initial load: absent. Recheck + post-adopt: concurrent window
        // already wrote autoSync=false.
        if (preferenceReads === 1) return null;
        return {
          autoSync: false,
          interval: 5,
          syncStrategy: 'smartMerge',
        };
      }
      return storage.get(key) ?? null;
    },
    saveToStorage(key: string, value: unknown) {
      storage.set(key, value);
      return true;
    },
    loadProviderConnection(provider: string) {
      return loadProviderConnectionImpl.call(this, provider);
    },
  };

  const state = loadInitialStateImpl.call(manager);

  assert.equal(
    storage.has(SYNC_STORAGE_KEYS.SYNC_PREFERENCES),
    false,
    'must not write over a concurrent preference key',
  );
  assert.equal(state.autoSyncEnabled, false);
});

test('startup preference migration writes once when key stays absent', () => {
  const storage = new Map<string, unknown>();
  storage.set(SYNC_STORAGE_KEYS.DEVICE_ID, 'test-device');
  storage.set(SYNC_STORAGE_KEYS.DEVICE_NAME, 'Test Device');
  storage.set(SYNC_STORAGE_KEYS.SYNC_CONFIG, {
    autoSync: true,
    interval: 15,
    syncStrategy: 'preferCloud',
    localVersion: 1,
    localUpdatedAt: 1,
    remoteVersion: 1,
    remoteUpdatedAt: 1,
  });

  const manager = {
    providerWriteSeq: {} as Record<string, number>,
    providerDecryptSeq: {} as Record<string, number>,
    providerDecrypted: {} as Record<string, boolean>,
    providerAuthAttemptSeq: {} as Record<string, number>,
    providerAuthRestoreState: {} as Record<string, unknown>,
    loadFromStorage(key: string) {
      return storage.get(key) ?? null;
    },
    saveToStorage(key: string, value: unknown) {
      storage.set(key, value);
      return true;
    },
    loadProviderConnection(provider: string) {
      return loadProviderConnectionImpl.call(this, provider);
    },
  };

  const state = loadInitialStateImpl.call(manager);
  const saved = storage.get(SYNC_STORAGE_KEYS.SYNC_PREFERENCES) as {
    autoSync: boolean;
    interval: number;
  };

  assert.equal(saved.autoSync, true);
  assert.equal(saved.interval, 15);
  assert.equal(state.autoSyncEnabled, true);
  assert.equal(state.autoSyncInterval, 15);
});
