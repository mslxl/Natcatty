import assert from "node:assert/strict";
import test from "node:test";

import type { Host, TransferTask } from "../../../domain/models";
import {
  classifyDedicatedResumeEndpoints,
  classifyResumeSourceValidationError,
  findPersistedChildForResumeFile,
  MAX_CONCURRENT_DEDICATED_SESSION_OPENS,
  resetDedicatedSessionOpenGateForTests,
  resumeTransferWithDedicatedSession,
  resolveDirectoryResumeTargetRoot,
  resolveHostForTransferEndpoint,
  shouldSkipCompletedResumeChild,
  withDedicatedSessionOpenSlot,
} from "./dedicatedTransferResume";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";

const host = (id: string, label: string, hostname = label): Host => ({
  id,
  label,
  hostname,
  port: 22,
  username: "root",
  authMethod: "password",
  protocol: "ssh",
} as Host);

test("resolveDirectoryResumeTargetRoot prefers staged replace path", () => {
  assert.equal(
    resolveDirectoryResumeTargetRoot({
      targetPath: "/final/dir",
      stagedTargetPath: "/final/dir.netcatty-abc.part",
      replaceExistingTarget: true,
    }),
    "/final/dir.netcatty-abc.part",
  );
  assert.equal(
    resolveDirectoryResumeTargetRoot({
      targetPath: "/final/dir",
    }),
    "/final/dir",
  );
});

test("findPersistedChildForResumeFile matches staged target paths", () => {
  const staged = "/final/dir.netcatty-abc.part/a.txt";
  const child = {
    id: "c1",
    status: "completed" as const,
    sourcePath: "/src/a.txt",
    targetPath: staged,
    checkpointBytes: 10,
    transferredBytes: 10,
    totalBytes: 10,
  };
  const planRoot = resolveDirectoryResumeTargetRoot({
    targetPath: "/final/dir",
    stagedTargetPath: "/final/dir.netcatty-abc.part",
  });
  const planned = { sourcePath: "/src/a.txt", targetPath: `${planRoot}/a.txt` };
  assert.equal(findPersistedChildForResumeFile([child], planned)?.id, "c1");
  assert.equal(shouldSkipCompletedResumeChild(findPersistedChildForResumeFile([child], planned)), true);
});

test("resolveHostForTransferEndpoint prefers id then label", () => {
  const hosts = [host("id-1", "CI-Build-01", "ci-01.example")];
  assert.equal(resolveHostForTransferEndpoint(hosts, "id-1", "other")?.id, "id-1");
  assert.equal(resolveHostForTransferEndpoint(hosts, "missing", "CI-Build-01")?.id, "id-1");
  assert.equal(resolveHostForTransferEndpoint(hosts, undefined, "ci-01.example")?.id, "id-1");
  assert.equal(resolveHostForTransferEndpoint(hosts, "missing", "gone"), null);
});

test("classifyDedicatedResumeEndpoints detects download, upload, and remote-to-remote", () => {
  assert.deepEqual(classifyDedicatedResumeEndpoints({
    direction: "download",
    sourceHostId: "h1",
    targetConnectionId: "local",
  }), { isDownload: true, isUpload: false, isRemoteToRemote: false });

  assert.deepEqual(classifyDedicatedResumeEndpoints({
    direction: "upload",
    targetHostId: "h1",
    sourceConnectionId: "local",
  }), { isDownload: false, isUpload: true, isRemoteToRemote: false });

  assert.deepEqual(classifyDedicatedResumeEndpoints({
    direction: "remote-to-remote",
    sourceHostId: "a",
    targetHostId: "b",
  }), { isDownload: false, isUpload: false, isRemoteToRemote: true });

  assert.deepEqual(classifyDedicatedResumeEndpoints({
    direction: "download",
    sourceHostId: "a",
    targetHostId: "b",
    sourceConnectionId: "s1",
    targetConnectionId: "s2",
  }), { isDownload: false, isUpload: false, isRemoteToRemote: true });
});

test("directory resume skips completed children and matches by path", () => {
  const children = [
    {
      id: "c1",
      status: "completed" as const,
      sourcePath: "/remote/a.txt",
      targetPath: "/local/a.txt",
      checkpointBytes: 10,
      transferredBytes: 10,
      totalBytes: 10,
    },
    {
      id: "c2",
      status: "interrupted" as const,
      sourcePath: "/remote/b.txt",
      targetPath: "/local/b.txt",
      checkpointBytes: 4,
      transferredBytes: 4,
      totalBytes: 20,
    },
  ];

  assert.equal(
    shouldSkipCompletedResumeChild(findPersistedChildForResumeFile(children, {
      sourcePath: "/remote/a.txt",
      targetPath: "/local/a.txt",
    })),
    true,
  );
  assert.equal(
    shouldSkipCompletedResumeChild(findPersistedChildForResumeFile(children, {
      sourcePath: "/remote/b.txt",
      targetPath: "/local/b.txt",
    })),
    false,
  );
  assert.equal(
    findPersistedChildForResumeFile(children, {
      sourcePath: "/remote/b.txt",
      targetPath: "/local/b.txt",
    })?.checkpointBytes,
    4,
  );
  assert.equal(
    findPersistedChildForResumeFile(children, {
      sourcePath: "/remote/missing.txt",
      targetPath: "/local/missing.txt",
    }),
    null,
  );
  // Ambiguous OR match must not win: two children share sourcePath different targets.
  const ambiguous = [
    { id: "x", status: "interrupted" as const, sourcePath: "/s/a", targetPath: "/t/1", checkpointBytes: 1, transferredBytes: 1, totalBytes: 10 },
    { id: "y", status: "interrupted" as const, sourcePath: "/s/a", targetPath: "/t/2", checkpointBytes: 2, transferredBytes: 2, totalBytes: 10 },
  ];
  assert.equal(
    findPersistedChildForResumeFile(ambiguous, { sourcePath: "/s/a", targetPath: "/t/other" }),
    null,
  );
  assert.equal(
    findPersistedChildForResumeFile(ambiguous, { sourcePath: "/s/a", targetPath: "/t/2" })?.id,
    "y",
  );
});

test("directory parent classification stays distinct from single-file downloads", () => {
  const parent = {
    direction: "download" as const,
    sourceHostId: "h1",
    targetConnectionId: "local",
    isDirectory: true,
  } satisfies Pick<TransferTask, "direction" | "sourceHostId" | "targetConnectionId" | "isDirectory">;
  assert.equal(parent.isDirectory, true);
  assert.equal(classifyDedicatedResumeEndpoints(parent).isDownload, true);
});

test("classifyResumeSourceValidationError maps size vs modified vs fatal", () => {
  assert.equal(classifyResumeSourceValidationError(null).kind, "ok");
  assert.equal(classifyResumeSourceValidationError("Source size changed while the transfer was paused").kind, "restart");
  assert.equal(classifyResumeSourceValidationError("Saved checkpoint is beyond the current source size").kind, "restart");
  assert.equal(classifyResumeSourceValidationError("Source was modified while the transfer was paused").kind, "modified");
  assert.equal(classifyResumeSourceValidationError("Source is unavailable").kind, "fatal");
});

test("dedicated session open gate limits concurrent dials", async () => {
  resetDedicatedSessionOpenGateForTests();
  assert.equal(MAX_CONCURRENT_DEDICATED_SESSION_OPENS, 2);

  let inFlight = 0;
  let peak = 0;
  const work = async () => {
    inFlight += 1;
    peak = Math.max(peak, inFlight);
    await new Promise((resolve) => setTimeout(resolve, 30));
    inFlight -= 1;
  };

  await Promise.all([
    withDedicatedSessionOpenSlot(work),
    withDedicatedSessionOpenSlot(work),
    withDedicatedSessionOpenSlot(work),
    withDedicatedSessionOpenSlot(work),
  ]);

  assert.ok(peak <= MAX_CONCURRENT_DEDICATED_SESSION_OPENS, `peak open slots ${peak}`);
  resetDedicatedSessionOpenGateForTests();
});

test("single-file restart resume continues from checkpoint without page callbacks", async (t) => {
  resetDedicatedSessionOpenGateForTests();
  const previousLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "2", setItem: () => {}, removeItem: () => {} },
  });
  t.after(() => {
    if (previousLocalStorage) Object.defineProperty(globalThis, "localStorage", previousLocalStorage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  });
  const originalGet = netcattyBridge.get;
  let startOptions: Record<string, unknown> | undefined;
  (netcattyBridge as { get: () => unknown }).get = () => ({
    openSftp: async () => "dedicated-sftp",
    closeSftp: async () => {},
    statLocal: async () => ({ size: 100, lastModified: 123 }),
    startStreamTransfer: async function (options: Record<string, unknown>) {
      assert.equal(arguments.length, 1);
      startOptions = options;
      return { transferId: "file-restart" };
    },
  });
  try {
    const result = await resumeTransferWithDedicatedSession({
      id: "file-restart",
      fileName: "file.bin",
      sourcePath: "/local/file.bin",
      targetPath: "/remote/file.bin",
      sourceConnectionId: "local",
      targetConnectionId: "old-sftp",
      targetHostId: "h1",
      targetHostLabel: "box",
      direction: "upload",
      status: "interrupted",
      totalBytes: 100,
      transferredBytes: 20,
      checkpointBytes: 20,
      speed: 0,
      startTime: 1,
      isDirectory: false,
      reconnectRequired: true,
    }, {
      hosts: [host("h1", "box", "1.2.3.4")],
      keys: [],
      identities: [],
    });

    assert.equal(result.success, true, result.error);
    assert.equal(startOptions?.checkpointBytes, 20);
    assert.equal(startOptions?.skipAdmission, true);
  } finally {
    (netcattyBridge as { get: typeof originalGet }).get = originalGet;
    resetDedicatedSessionOpenGateForTests();
  }
});

test("folder restart resume reports file-count progress without child page callbacks", async (t) => {
  resetDedicatedSessionOpenGateForTests();
  const previousLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "2", setItem: () => {}, removeItem: () => {} },
  });
  t.after(() => {
    if (previousLocalStorage) Object.defineProperty(globalThis, "localStorage", previousLocalStorage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  });
  const originalGet = netcattyBridge.get;
  const parentProgress: number[] = [];
  const childUpdates: TransferTask[] = [];
  (netcattyBridge as { get: () => unknown }).get = () => ({
    openSftp: async () => "dedicated-sftp",
    closeSftp: async () => {},
    listLocalTree: async () => [
      { localPath: "/local/folder/a.bin", relativePath: "a.bin", type: "file", size: 10, lastModified: 1 },
      { localPath: "/local/folder/b.bin", relativePath: "b.bin", type: "file", size: 20, lastModified: 2 },
    ],
    mkdirSftp: async () => {},
    statLocal: async (path: string) => ({ size: path.endsWith("a.bin") ? 10 : 20 }),
    startStreamTransfer: async function (options: { transferId: string; totalBytes?: number }) {
      assert.equal(arguments.length, 1);
      return { transferId: options.transferId };
    },
  });
  try {
    const result = await resumeTransferWithDedicatedSession({
      id: "folder-restart",
      fileName: "folder",
      sourcePath: "/local/folder",
      targetPath: "/remote/folder",
      sourceConnectionId: "local",
      targetConnectionId: "old-sftp",
      targetHostId: "h1",
      targetHostLabel: "box",
      direction: "upload",
      status: "interrupted",
      totalBytes: 2,
      transferredBytes: 0,
      checkpointBytes: 0,
      speed: 0,
      startTime: 1,
      isDirectory: true,
      progressMode: "files",
      reconnectRequired: true,
    }, {
      hosts: [host("h1", "box", "1.2.3.4")],
      keys: [],
      identities: [],
    }, (value) => parentProgress.push(value.transferred), {
      children: [],
      onChildUpdate: (child) => childUpdates.push(child),
    });

    assert.equal(result.success, true, result.error);
    assert.equal(parentProgress.at(-1), 2);
    assert.ok(childUpdates.some((child) => child.status === "transferring"));
    assert.equal(childUpdates.filter((child) => child.status === "completed").length, 2);
  } finally {
    (netcattyBridge as { get: typeof originalGet }).get = originalGet;
    resetDedicatedSessionOpenGateForTests();
  }
});
