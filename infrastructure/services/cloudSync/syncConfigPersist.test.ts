import test from 'node:test';
import assert from 'node:assert/strict';

import { resolveSyncConfigForPersist } from './syncConfigPersist.ts';

const memoryBase = {
  autoSync: true,
  interval: 5,
  localVersion: 12,
  localUpdatedAt: 1000,
  remoteVersion: 11,
  remoteUpdatedAt: 900,
  syncStrategy: 'smartMerge' as const,
};

test('version-only persist keeps another window autoSync=false from storage', () => {
  const next = resolveSyncConfigForPersist({
    memory: memoryBase,
    stored: {
      autoSync: false,
      interval: 5,
      localVersion: 10,
      localUpdatedAt: 800,
      remoteVersion: 10,
      remoteUpdatedAt: 800,
      syncStrategy: 'smartMerge',
    },
    preferencesFromMemory: false,
  });

  assert.equal(next.autoSync, false);
  assert.equal(next.localVersion, 12);
  assert.equal(next.remoteVersion, 11);
});

test('explicit preference persist writes memory autoSync', () => {
  const next = resolveSyncConfigForPersist({
    memory: { ...memoryBase, autoSync: false },
    stored: {
      autoSync: true,
      interval: 5,
      localVersion: 10,
      localUpdatedAt: 800,
      remoteVersion: 10,
      remoteUpdatedAt: 800,
      syncStrategy: 'smartMerge',
    },
    preferencesFromMemory: true,
  });

  assert.equal(next.autoSync, false);
  assert.equal(next.localVersion, 12);
});

test('version-only persist keeps stored syncStrategy when memory differs', () => {
  const next = resolveSyncConfigForPersist({
    memory: { ...memoryBase, syncStrategy: 'preferLocal' },
    stored: {
      autoSync: false,
      interval: 15,
      syncStrategy: 'preferCloud',
    },
  });

  assert.equal(next.autoSync, false);
  assert.equal(next.interval, 15);
  assert.equal(next.syncStrategy, 'preferCloud');
});
