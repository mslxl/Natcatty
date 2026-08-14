import test from 'node:test';
import assert from 'node:assert/strict';

import { SYNC_CONSTANTS, SYNC_STORAGE_KEYS } from '../../../domain/sync.ts';
import { handleStorageEventImpl } from './stateAndSecurityMethods.ts';

test('SYNC_CONFIG storage event stops auto-sync when another window disables it', () => {
  const fakeStorage = {};
  const originalWindow = globalThis.window;
  let stopCount = 0;
  let startCount = 0;

  (globalThis as typeof globalThis & { window?: unknown }).window = {
    localStorage: fakeStorage,
  };

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
    safeJsonParse: (value: string | null) => (value ? JSON.parse(value) : null),
    startAutoSync: () => {
      startCount += 1;
    },
    stopAutoSync: () => {
      stopCount += 1;
    },
    notifyStateChange: () => {},
  };

  try {
    handleStorageEventImpl.call(manager, {
      storageArea: fakeStorage,
      key: SYNC_STORAGE_KEYS.SYNC_CONFIG,
      newValue: JSON.stringify({
        autoSync: false,
        interval: SYNC_CONSTANTS.DEFAULT_AUTO_SYNC_INTERVAL,
        localVersion: 2,
        localUpdatedAt: 2,
        remoteVersion: 2,
        remoteUpdatedAt: 2,
        syncStrategy: 'smartMerge',
      }),
    } as StorageEvent);
  } finally {
    (globalThis as typeof globalThis & { window?: unknown }).window = originalWindow;
  }

  assert.equal(manager.state.autoSyncEnabled, false);
  assert.equal(stopCount, 1);
  assert.equal(startCount, 0);
});
