import assert from "node:assert/strict";
import test from "node:test";
import type { MutableRefObject } from "react";

import type { TransferTask } from "../../../domain/models";
import { STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY } from "../../../infrastructure/config/storageKeys";
import { transferDiscoveredFiles } from "./transferDirectoryFileBatch";

const memoryStore = new Map<string, string>();
(globalThis as { localStorage?: Storage }).localStorage = {
  getItem: (key: string) => memoryStore.get(key) ?? null,
  setItem: (key: string, value: string) => {
    memoryStore.set(key, String(value));
  },
  removeItem: (key: string) => {
    memoryStore.delete(key);
  },
  clear: () => memoryStore.clear(),
  key: () => null,
  length: 0,
} as Storage;
memoryStore.set(STORAGE_KEY_SFTP_TRANSFER_CONCURRENCY, "1");

const rootTask = (): TransferTask => ({
  id: "root",
  fileName: "source",
  sourcePath: "/source",
  targetPath: "/target",
  sourceConnectionId: "local",
  targetConnectionId: "local",
  direction: "upload",
  status: "transferring",
  totalBytes: 1,
  transferredBytes: 0,
  speed: 0,
  startTime: 0,
  isDirectory: true,
});

test("transferDiscoveredFiles re-stats source before skip-unchanged", async () => {
  const transfersRef = { current: [rootTask()] } as MutableRefObject<TransferTask[]>;
  const cancelledTasksRef = { current: new Set<string>() } as MutableRefObject<Set<string>>;
  const activeChildIdsRef = {
    current: new Map<string, Set<string>>(),
  } as MutableRefObject<Map<string, Set<string>>>;
  let transfers = transfersRef.current;
  let transferCalls = 0;
  let sourceStatCalls = 0;

  const staleMtime = 1_700_000_000_000;
  const freshMtime = 1_700_000_100_000;

  await transferDiscoveredFiles({
    rootTask: rootTask(),
    files: [{
      name: "a.txt",
      sourcePath: "/source/a.txt",
      targetPath: "/target/a.txt",
      size: 10,
      lastModified: staleMtime,
    }],
    sourceSftpId: null,
    targetSftpId: null,
    sourceIsLocal: true,
    targetIsLocal: true,
    sourceEncoding: "auto",
    targetEncoding: "auto",
    rootTaskId: "root",
    skipUnchanged: true,
    startEntryIndex: 0,
    cancelledTasksRef,
    activeChildIdsRef,
    transfersRef,
    setTransfers: (updater) => {
      transfers = typeof updater === "function" ? updater(transfers) : updater;
      transfersRef.current = transfers;
    },
    waitWhileTransferPaused: async () => {},
    isPauseLatched: () => false,
    transferFile: async () => {
      transferCalls += 1;
    },
    tryStatTarget: async () => ({
      size: 10,
      lastModified: staleMtime,
      type: "file",
    }),
    tryStatSource: async () => {
      sourceStatCalls += 1;
      return {
        size: 10,
        lastModified: freshMtime,
        type: "file",
      };
    },
  });

  assert.equal(sourceStatCalls, 1, "must re-stat source before skip check");
  assert.equal(transferCalls, 1, "stale listing must not skip a changed source");
  const child = transfers.find((row) => row.parentTaskId === "root");
  assert.ok(child);
  assert.equal(child?.status, "completed");
  assert.equal(child?.sourceLastModified, freshMtime);
});

test("transferDiscoveredFiles does not skip when source re-stat fails", async () => {
  const transfersRef = { current: [rootTask()] } as MutableRefObject<TransferTask[]>;
  const cancelledTasksRef = { current: new Set<string>() } as MutableRefObject<Set<string>>;
  const activeChildIdsRef = {
    current: new Map<string, Set<string>>(),
  } as MutableRefObject<Map<string, Set<string>>>;
  let transfers = transfersRef.current;
  let transferCalls = 0;
  let transferredTotalBytes: number | undefined;
  let transferredSourceLastModified: number | undefined;
  const mtime = 1_700_000_000_000;

  await transferDiscoveredFiles({
    rootTask: rootTask(),
    files: [{
      name: "a.txt",
      sourcePath: "/source/a.txt",
      targetPath: "/target/a.txt",
      size: 10,
      lastModified: mtime,
    }],
    sourceSftpId: null,
    targetSftpId: null,
    sourceIsLocal: true,
    targetIsLocal: true,
    sourceEncoding: "auto",
    targetEncoding: "auto",
    rootTaskId: "root",
    skipUnchanged: true,
    startEntryIndex: 0,
    cancelledTasksRef,
    activeChildIdsRef,
    transfersRef,
    setTransfers: (updater) => {
      transfers = typeof updater === "function" ? updater(transfers) : updater;
      transfersRef.current = transfers;
    },
    waitWhileTransferPaused: async () => {},
    isPauseLatched: () => false,
    transferFile: async (task) => {
      transferCalls += 1;
      transferredTotalBytes = task.totalBytes;
      transferredSourceLastModified = task.sourceLastModified;
    },
    tryStatTarget: async () => ({
      size: 10,
      lastModified: mtime,
      type: "file",
    }),
    tryStatSource: async () => null,
  });

  assert.equal(transferCalls, 1, "missing fresh source meta must not skip");
  assert.equal(transferredTotalBytes, 0, "must not pass stale listing size after re-stat failure");
  assert.equal(
    transferredSourceLastModified,
    undefined,
    "must clear stale listing mtime after re-stat failure",
  );
});

test("transferDiscoveredFiles skips only when fresh source still matches", async () => {
  const transfersRef = { current: [rootTask()] } as MutableRefObject<TransferTask[]>;
  const cancelledTasksRef = { current: new Set<string>() } as MutableRefObject<Set<string>>;
  const activeChildIdsRef = {
    current: new Map<string, Set<string>>(),
  } as MutableRefObject<Map<string, Set<string>>>;
  let transfers = transfersRef.current;
  let transferCalls = 0;
  const mtime = 1_700_000_000_000;

  await transferDiscoveredFiles({
    rootTask: rootTask(),
    files: [{
      name: "a.txt",
      sourcePath: "/source/a.txt",
      targetPath: "/target/a.txt",
      size: 10,
      lastModified: mtime - 60_000,
    }],
    sourceSftpId: null,
    targetSftpId: null,
    sourceIsLocal: true,
    targetIsLocal: true,
    sourceEncoding: "auto",
    targetEncoding: "auto",
    rootTaskId: "root",
    skipUnchanged: true,
    startEntryIndex: 0,
    cancelledTasksRef,
    activeChildIdsRef,
    transfersRef,
    setTransfers: (updater) => {
      transfers = typeof updater === "function" ? updater(transfers) : updater;
      transfersRef.current = transfers;
    },
    waitWhileTransferPaused: async () => {},
    isPauseLatched: () => false,
    transferFile: async () => {
      transferCalls += 1;
    },
    tryStatTarget: async () => ({
      size: 10,
      lastModified: mtime,
      type: "file",
    }),
    tryStatSource: async () => ({
      size: 10,
      lastModified: mtime,
      type: "file",
    }),
  });

  assert.equal(transferCalls, 0, "matching fresh identity must skip transfer");
  const child = transfers.find((row) => row.parentTaskId === "root");
  assert.equal(child?.status, "completed");
  assert.equal(child?.transferredBytes, 10);
});
