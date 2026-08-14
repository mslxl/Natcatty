import test from 'node:test';
import assert from 'node:assert/strict';

import { SYNC_STORAGE_KEYS } from '../../../domain/sync.ts';
import { saveSyncConfigImpl, setAutoSyncImpl } from './syncAllStorageMethods.ts';

test('version saveSyncConfig does not clobber another window autoSync=false', () => {
  const storage = new Map<string, unknown>();
  storage.set(SYNC_STORAGE_KEYS.SYNC_CONFIG, {
    autoSync: false,
    interval: 5,
    localVersion: 3,
    localUpdatedAt: 100,
    remoteVersion: 3,
    remoteUpdatedAt: 100,
    syncStrategy: 'smartMerge',
  });

  let stopCount = 0;
  const manager = {
    state: {
      autoSyncEnabled: true, // stale — this window has not observed the other window yet
      autoSyncInterval: 5,
      localVersion: 4,
      localUpdatedAt: 200,
      remoteVersion: 4,
      remoteUpdatedAt: 200,
      syncStrategy: 'smartMerge',
      securityState: 'UNLOCKED',
    },
    loadFromStorage(key: string) {
      return storage.get(key) ?? null;
    },
    saveToStorage(key: string, value: unknown) {
      storage.set(key, value);
      return true;
    },
    saveSyncConfig() {
      saveSyncConfigImpl.call(this);
    },
    notifyStateChange() {},
    startAutoSync() {},
    stopAutoSync() {
      stopCount += 1;
    },
  };

  // Simulate post-upload version bump save (default: preferences from storage)
  saveSyncConfigImpl.call(manager);

  const saved = storage.get(SYNC_STORAGE_KEYS.SYNC_CONFIG) as { autoSync: boolean; localVersion: number };
  assert.equal(saved.autoSync, false, 'must not re-enable auto-sync from stale memory');
  assert.equal(saved.localVersion, 4);
  assert.equal(manager.state.autoSyncEnabled, false, 'memory should adopt storage preference');
  assert.equal(stopCount, 1);
});

test('setAutoSync still persists the explicit preference from memory', () => {
  const storage = new Map<string, unknown>();
  storage.set(SYNC_STORAGE_KEYS.SYNC_CONFIG, {
    autoSync: true,
    interval: 5,
    localVersion: 1,
    localUpdatedAt: 1,
    remoteVersion: 1,
    remoteUpdatedAt: 1,
    syncStrategy: 'smartMerge',
  });

  const manager = {
    state: {
      autoSyncEnabled: true,
      autoSyncInterval: 5,
      localVersion: 1,
      localUpdatedAt: 1,
      remoteVersion: 1,
      remoteUpdatedAt: 1,
      syncStrategy: 'smartMerge',
      securityState: 'UNLOCKED',
    },
    loadFromStorage(key: string) {
      return storage.get(key) ?? null;
    },
    saveToStorage(key: string, value: unknown) {
      storage.set(key, value);
      return true;
    },
    saveSyncConfig(opts?: { preferencesFromMemory?: boolean }) {
      saveSyncConfigImpl.call(this, opts);
    },
    notifyStateChange() {},
    startAutoSync() {},
    stopAutoSync() {},
  };

  setAutoSyncImpl.call(manager, false);

  const saved = storage.get(SYNC_STORAGE_KEYS.SYNC_CONFIG) as { autoSync: boolean };
  assert.equal(saved.autoSync, false);
  assert.equal(manager.state.autoSyncEnabled, false);
});
