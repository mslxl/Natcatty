import assert from "node:assert/strict";
import test from "node:test";

import type { TransferTask } from "../../domain/models";
import { createSftpTransferCenterStore } from "./sftpTransferCenterStore";

const makeTask = (id: string, status: TransferTask["status"] = "transferring"): TransferTask => ({
  id,
  fileName: `${id}.txt`,
  sourcePath: `/source/${id}.txt`,
  targetPath: `/target/${id}.txt`,
  sourceConnectionId: "local",
  targetConnectionId: `remote-${id}`,
  direction: "upload",
  status,
  totalBytes: 10,
  transferredBytes: 2,
  speed: 1,
  startTime: 1,
  isDirectory: false,
  resumable: true,
});

test("store aggregates owner snapshots without duplicating tasks", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [makeTask("a")]);
  store.publishOwner("panel-b", [makeTask("b")]);
  store.publishOwner("panel-a", [{ ...makeTask("a"), transferredBytes: 5 }]);

  assert.deepEqual(store.getSnapshot().tasks.map((task) => [task.id, task.transferredBytes]), [
    ["a", 5],
    ["b", 2],
  ]);
});

test("store routes cancel/retry/prioritize/dismiss to the task owner; pause/resume are process-global", async () => {
  const calls: string[] = [];
  const store = createSftpTransferCenterStore();
  store.registerOwner("panel-a", {
    pause: async (id) => { calls.push(`pause:${id}`); },
    resume: async (id) => { calls.push(`resume:${id}`); },
    cancel: async (id) => { calls.push(`cancel:${id}`); },
    retry: async (id) => { calls.push(`retry:${id}`); },
    prioritize: async (id) => { calls.push(`prioritize:${id}`); },
    dismiss: (id) => calls.push(`dismiss:${id}`),
  });
  store.publishOwner("panel-a", [makeTask("a")]);

  await store.cancel("a");
  await store.retry("a");
  await store.prioritize("a");
  store.dismiss("a");

  // Pause/resume are process-global (not owner controllers).
  assert.deepEqual(calls, [
    "cancel:a",
    "retry:a",
    "prioritize:a",
    "dismiss:a",
  ]);
  assert.ok(!calls.some((c) => c.startsWith("pause:") || c.startsWith("resume:")));
});

test("resume without an owner uses a live backend transfer session when available", async (t) => {
  const resumeCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        resumeTransfer: async (id: string) => {
          resumeCalls.push(id);
          return { success: true };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("orphaned-paused", "paused"),
    direction: "download",
    sourceHostId: "host-a",
    sourceConnectionId: "remote-conn",
    targetConnectionId: "local",
  }]);

  await store.resume("orphaned-paused");

  assert.deepEqual(resumeCalls, ["orphaned-paused"]);
  assert.equal(store.getSnapshot().tasks[0]?.status, "transferring");
  assert.equal(store.getSnapshot().tasks[0]?.error, undefined);
});

test("resume soft-controls without panel when canAdopt is false", async (t) => {
  // Downloads often have only the remote pane open. Soft-resume is process-global
  // (bridge + latch) and must not require canAdopt / both panes.
  const resumeCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        resumeTransfer: async (id: string) => {
          resumeCalls.push(id);
          return { success: true };
        },
      },
    },
  });
  const store = createSftpTransferCenterStore();
  store.registerOwner("panel-a", {
    pause: async () => {},
    resume: async () => {},
    cancel: async () => {},
    retry: async () => {},
    prioritize: async () => {},
    dismiss: () => {},
    canAdopt: () => false,
    canPrepareAdoption: true,
  });
  store.publishOwner("panel-a", [{
    ...makeTask("download-paused", "paused"),
    direction: "download",
    sourceHostId: "host-a",
    sourceConnectionId: "remote-conn",
    targetConnectionId: "local",
  }]);

  await store.resume("download-paused");

  assert.deepEqual(resumeCalls, ["download-paused"]);
  assert.equal(store.getSnapshot().tasks[0]?.status, "transferring");
  assert.equal(store.getSnapshot().tasks[0]?.error, undefined);
});

test("persisted unfinished tasks restore as interrupted without controllers", () => {
  let persisted = "";
  const first = createSftpTransferCenterStore({
    read: () => null,
    write: (value) => { persisted = value; },
  });
  first.publishOwner("panel-a", [makeTask("a")]);

  const restored = createSftpTransferCenterStore({
    read: () => persisted,
    write: () => {},
  });
  assert.equal(restored.getSnapshot().tasks[0]?.status, "interrupted");
  assert.equal(restored.getSnapshot().tasks[0]?.ownerId, "panel-a");
  assert.equal(restored.canControl("a"), true);
});

test("paused source fingerprint patches are persisted for restart", () => {
  let persisted = "";
  const first = createSftpTransferCenterStore({
    read: () => null,
    write: (value) => { persisted = value; },
  });
  first.publishOwner("panel-a", [makeTask("paused-fingerprint", "paused")]);
  first.patchTask("paused-fingerprint", { sourceFingerprint: "sha256:durable" });

  const restored = createSftpTransferCenterStore({
    read: () => persisted,
    write: () => {},
  });
  assert.equal(restored.getSnapshot().tasks[0]?.status, "interrupted");
  assert.equal(restored.getSnapshot().tasks[0]?.sourceFingerprint, "sha256:durable");
});

test("background source fingerprint progress is persisted immediately for restart", () => {
  let persisted = "";
  const first = createSftpTransferCenterStore({
    read: () => null,
    write: (value) => { persisted = value; },
  });
  first.publishOwner("panel-a", [makeTask("background-fingerprint", "paused")]);
  first.ingestBackgroundEvent({
    type: "progress",
    transferId: "background-fingerprint",
    transferred: 2,
    totalBytes: 10,
    speed: 0,
    checkpointBytes: 2,
    sourceFingerprint: "sha256:background-durable",
    lifecycleState: "paused",
  });

  const restored = createSftpTransferCenterStore({
    read: () => persisted,
    write: () => {},
  });
  assert.equal(restored.getSnapshot().tasks[0]?.sourceFingerprint, "sha256:background-durable");
});

test("owner source fingerprint publish is persisted immediately for restart", () => {
  let persisted = "";
  const first = createSftpTransferCenterStore({
    read: () => null,
    write: (value) => { persisted = value; },
  });
  const paused = makeTask("owner-fingerprint", "paused");
  first.publishOwner("panel-a", [paused]);
  first.publishOwner("panel-a", [{
    ...paused,
    sourceFingerprint: "sha256:owner-durable",
  }]);

  const restored = createSftpTransferCenterStore({
    read: () => persisted,
    write: () => {},
  });
  assert.equal(restored.getSnapshot().tasks[0]?.sourceFingerprint, "sha256:owner-durable");
});

test("top-level completion is persisted immediately while child completions stay coalesced", () => {
  let writes = 0;
  let persisted = "";
  const first = createSftpTransferCenterStore({
    read: () => null,
    write: (value) => {
      writes += 1;
      persisted = value;
    },
  });
  const parent = {
    ...makeTask("completed-parent"),
    isDirectory: true,
    progressMode: "files" as const,
  };
  const child = { ...makeTask("completed-child"), parentTaskId: parent.id };
  first.publishOwner("panel-a", [parent, child]);
  first.publishOwner("panel-a", [parent, { ...child, status: "completed", endTime: Date.now() }]);
  assert.equal(writes, 1, "child completion should remain coalesced");
  first.publishOwner("panel-a", [
    { ...parent, status: "completed", endTime: Date.now() },
    { ...child, status: "completed", endTime: Date.now() },
  ]);
  assert.equal(writes, 2, "parent completion should flush immediately");

  const restored = createSftpTransferCenterStore({
    read: () => persisted,
    write: () => {},
  });
  assert.equal(restored.getSnapshot().tasks.find((task) => task.id === parent.id)?.status, "completed");
});

test("orphaned unfinished tasks stay controllable so dead rows can be cancelled", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("gone-panel", [
    makeTask("dead-transferring", "transferring"),
    makeTask("dead-paused", "paused"),
  ]);
  // No owner controller registered — simulates app restart.
  assert.equal(store.canControl("dead-transferring"), true);
  assert.equal(store.canControl("dead-paused"), true);
});

test("pause on an orphaned transferring task demotes it to interrupted", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("gone-panel", [makeTask("stuck", "transferring")]);

  await store.pause("stuck");

  assert.equal(store.getSnapshot().tasks[0]?.status, "interrupted");
  assert.equal(store.getSnapshot().tasks[0]?.reconnectRequired, true);
});

test("orphan pause with a live walk latches paused without cancel/demote", async (t) => {
  const {
    isTransferOrRootPauseLatched,
    isTransferPauseLatched,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("live-dir");
  t.after(() => {
    unregisterTransferWalk("live-dir");
    resetTransferPauseLatchesForTests();
    resetTransferWalkRegistryForTests();
  });

  const cancelCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        // No pauseTransfer → falls to no-bridge branch.
        cancelTransfer: async (id: string) => {
          cancelCalls.push(id);
          return { success: true };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("terminal:closed", [
    {
      ...makeTask("live-dir", "transferring"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 2,
      totalBytes: 10,
    },
    {
      ...makeTask("live-child", "transferring"),
      parentTaskId: "live-dir",
      transferredBytes: 1,
      totalBytes: 5,
    },
  ]);

  await store.pause("live-dir");

  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "live-dir")?.status, "paused");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "live-child")?.status, "paused");
  assert.equal(isTransferPauseLatched("live-dir"), true);
  assert.equal(isTransferOrRootPauseLatched("live-dir", "live-child"), true);
  assert.deepEqual(cancelCalls, [], "must not cancelTransfer a still-running walk");
});

test("orphan resume clears full latch tree so walk is not blocked", async (t) => {
  const {
    isTransferOrRootPauseLatched,
    latchTransferPauseTree,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  latchTransferPauseTree("dir-latched", ["c1", "c2"]);
  registerTransferWalk("dir-latched");
  t.after(() => {
    unregisterTransferWalk("dir-latched");
    resetTransferPauseLatchesForTests();
    resetTransferWalkRegistryForTests();
  });

  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        resumeTransfer: async () => ({ success: false, reason: "Transfer is no longer active" }),
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("gone", [
    {
      ...makeTask("dir-latched", "paused"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 1,
      totalBytes: 5,
      speed: 0,
    },
    { ...makeTask("c1", "paused"), parentTaskId: "dir-latched", speed: 0 },
    { ...makeTask("c2", "paused"), parentTaskId: "dir-latched", speed: 0 },
  ]);

  await store.resume("dir-latched");

  assert.equal(isTransferOrRootPauseLatched("dir-latched", "c1"), false);
  assert.equal(isTransferOrRootPauseLatched("dir-latched", "c2"), false);
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir-latched")?.status, "transferring");
});

test("snapshot counts only parent tasks and clearing completed history preserves failures", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [
    makeTask("parent"),
    { ...makeTask("child"), parentTaskId: "parent" },
    makeTask("done", "completed"),
    makeTask("failed", "failed"),
  ]);

  assert.equal(store.getSnapshot().activeCount, 1);
  store.clearTerminal("completed");
  assert.deepEqual(store.getSnapshot().tasks.map((task) => task.id), ["parent", "child", "failed"]);
});

test("background agent transfers are recorded and retained in history", () => {
  const store = createSftpTransferCenterStore();
  const now = Date.now();
  store.ingestBackgroundEvent({
    type: "started",
    transferId: "agent-transfer",
    direction: "upload",
    sourcePath: "/local/report.txt",
    targetPath: "/remote/report.txt",
    startedAt: now - 10,
  });
  assert.equal(store.getSnapshot().tasks[0]?.background, true);
  assert.equal(store.getSnapshot().tasks[0]?.origin, "agent");

  store.ingestBackgroundEvent({ type: "completed", transferId: "agent-transfer", endedAt: now });
  assert.equal(store.getSnapshot().tasks[0]?.status, "completed");
  assert.equal(store.getSnapshot().tasks[0]?.endTime, now);
});

test("main-process progress ingest keeps panel-owned transfers moving without React callbacks", () => {
  const store = createSftpTransferCenterStore();
  // Simulate panel publish then unmount: task remains in store, no live controller needed.
  store.publishOwner("panel-hidden", [{
    ...makeTask("upload-1", "queued"),
    transferredBytes: 0,
    totalBytes: 1000,
  }]);

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "upload-1",
    transferred: 400,
    totalBytes: 1000,
    speed: 200,
    checkpointBytes: 400,
  });

  const task = store.getSnapshot().tasks.find((row) => row.id === "upload-1");
  assert.equal(task?.status, "transferring");
  assert.equal(task?.transferredBytes, 400);
  assert.equal(task?.speed, 200);
  assert.equal(task?.checkpointBytes, 400);

  store.ingestBackgroundEvent({
    type: "completed",
    transferId: "upload-1",
    endedAt: Date.now(),
  });
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "upload-1")?.status, "completed");
});

test("duplicate main-process progress does not notify or persist a second renderer update", async () => {
  let writes = 0;
  let notifications = 0;
  const store = createSftpTransferCenterStore({
    read: () => null,
    write: () => { writes += 1; },
  });
  store.publishOwner("panel-a", [{
    ...makeTask("duplicate-progress"),
    transferredBytes: 5,
    totalBytes: 10,
    speed: 2,
    lifecycleEpoch: 0,
  }]);
  store.subscribe(() => { notifications += 1; });

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "duplicate-progress",
    transferred: 5,
    totalBytes: 10,
    speed: 2,
    lifecycleEpoch: 0,
    lifecycleState: "transferring",
  });
  await new Promise((resolve) => setTimeout(resolve, 300));

  assert.equal(notifications, 0);
  assert.equal(writes, 1);
});

test("owner publish after global progress does not repeat the same renderer update", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("global-first"),
    transferredBytes: 0,
    totalBytes: 10,
    speed: 0,
    lifecycleEpoch: 0,
  }]);
  let notifications = 0;
  store.subscribe(() => { notifications += 1; });

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "global-first",
    transferred: 5,
    totalBytes: 10,
    speed: 2,
    lifecycleEpoch: 0,
    lifecycleState: "transferring",
  });
  store.publishOwner("panel-a", [{
    ...makeTask("global-first"),
    transferredBytes: 5,
    totalBytes: 10,
    speed: 2,
    lifecycleEpoch: 0,
  }]);

  assert.equal(notifications, 1);
});

test("newer backend lifecycle progress reopens a paused row", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("paused-but-moving", "paused"),
    transferredBytes: 50,
    checkpointBytes: 50,
    lifecycleEpoch: 1,
    totalBytes: 100,
    speed: 0,
  }]);

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "paused-but-moving",
    transferred: 75,
    checkpointBytes: 75,
    totalBytes: 100,
    speed: 10,
    lifecycleEpoch: 2,
    lifecycleState: "transferring",
  });

  const task = store.getSnapshot().tasks.find((row) => row.id === "paused-but-moving");
  assert.equal(task?.status, "transferring");
  assert.equal(task?.transferredBytes, 75);
  assert.equal(task?.checkpointBytes, 75);
});

test("older in-flight progress cannot reopen a newer confirmed pause", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("paused-with-late-event", "paused"),
    transferredBytes: 50,
    checkpointBytes: 50,
    lifecycleEpoch: 1,
    totalBytes: 100,
    speed: 0,
  }]);

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "paused-with-late-event",
    transferred: 75,
    checkpointBytes: 75,
    totalBytes: 100,
    speed: 10,
    lifecycleEpoch: 0,
    lifecycleState: "transferring",
  });

  const task = store.getSnapshot().tasks.find((row) => row.id === "paused-with-late-event");
  assert.equal(task?.status, "paused");
  assert.equal(task?.transferredBytes, 50);
});

test("older queued or started events cannot overwrite a newer pause", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("paused-newer", "paused"),
    transferredBytes: 50,
    checkpointBytes: 50,
    lifecycleEpoch: 2,
    totalBytes: 100,
    speed: 0,
  }]);

  store.ingestBackgroundEvent({
    type: "started",
    transferId: "paused-newer",
    lifecycleEpoch: 1,
  });
  store.ingestBackgroundEvent({
    type: "queued",
    transferId: "paused-newer",
  });

  assert.equal(store.getSnapshot().tasks[0]?.status, "paused");
  assert.equal(store.getSnapshot().tasks[0]?.lifecycleEpoch, 2);
});

test("background compressed lifecycle creates and updates the same controllable task in every window", () => {
  const store = createSftpTransferCenterStore();
  store.ingestBackgroundEvent({
    type: "started",
    transferId: "compressed-global",
    fileName: "photos (compressed)",
    sourcePath: "/local/photos",
    targetPath: "/remote/photos",
    direction: "upload",
    totalBytes: 1_000,
    isDirectory: true,
    controlKind: "compressed-upload",
    phase: "compressing",
  });
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "compressed-global",
    transferred: 650,
    totalBytes: 1_000,
    phase: "uploading",
  });

  const task = store.getSnapshot().tasks.find((candidate) => candidate.id === "compressed-global");
  assert.equal(task?.fileName, "photos (compressed)");
  assert.equal(task?.isDirectory, true);
  assert.equal(task?.controlKind, "compressed-upload");
  assert.equal(task?.phase, "uploading");
  assert.equal(task?.transferredBytes, 650);
  assert.equal(task?.totalBytes, 1_000);
});

test("a worker compressed progress event can create the task after the source page closed", () => {
  const store = createSftpTransferCenterStore();
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "worker-compressed",
    fileName: "worker folder (compressed)",
    sourcePath: "/local/worker folder",
    targetPath: "/remote/worker folder",
    direction: "upload",
    transferred: 250,
    totalBytes: 1_000,
    isDirectory: true,
    controlKind: "compressed-upload",
    phase: "compressing",
  });

  const task = store.getSnapshot().tasks[0];
  assert.equal(task?.id, "worker-compressed");
  assert.equal(task?.transferredBytes, 250);
  assert.equal(task?.controlKind, "compressed-upload");
});

test("stale panel publishOwner cannot roll back background progress after tab close", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 100,
    totalBytes: 1000,
    speed: 10,
  }]);

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "upload-1",
    transferred: 700,
    totalBytes: 1000,
    speed: 50,
    checkpointBytes: 700,
  });
  assert.equal(store.getSnapshot().tasks[0]?.transferredBytes, 700);

  // Panel React state still frozen at 100 (unmount / missed setState) — must not clobber.
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 100,
    totalBytes: 1000,
    speed: 0,
    checkpointBytes: 100,
  }]);

  const task = store.getSnapshot().tasks.find((row) => row.id === "upload-1");
  assert.equal(task?.transferredBytes, 700);
  assert.equal(task?.checkpointBytes, 700);
  assert.equal(task?.status, "transferring");
});

test("stale paused panel snapshot cannot hide progress beyond its pause checkpoint", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "paused"),
    transferredBytes: 50,
    checkpointBytes: 50,
    lifecycleEpoch: 1,
    totalBytes: 100,
    speed: 0,
  }]);
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "upload-1",
    transferred: 75,
    checkpointBytes: 75,
    totalBytes: 100,
    speed: 10,
    lifecycleEpoch: 2,
    lifecycleState: "transferring",
  });
  assert.equal(store.getSnapshot().tasks[0]?.status, "transferring");

  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "paused"),
    transferredBytes: 50,
    checkpointBytes: 50,
    lifecycleEpoch: 1,
    totalBytes: 100,
    speed: 0,
  }]);

  const task = store.getSnapshot().tasks[0];
  assert.equal(task?.status, "transferring");
  assert.equal(task?.transferredBytes, 75);
  assert.equal(task?.checkpointBytes, 75);
});

test("stale panel progress cannot move a bar after a newer backend pause", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-paused", "transferring"),
    transferredBytes: 50,
    checkpointBytes: 50,
    totalBytes: 100,
  }]);
  store.ingestBackgroundEvent({
    type: "paused",
    transferId: "upload-paused",
    transferred: 50,
    checkpointBytes: 50,
    lifecycleEpoch: 1,
    lifecycleState: "paused",
  });

  store.publishOwner("panel-a", [{
    ...makeTask("upload-paused", "transferring"),
    transferredBytes: 75,
    checkpointBytes: 75,
    totalBytes: 100,
    speed: 10,
  }]);

  const task = store.getSnapshot().tasks[0];
  assert.equal(task?.status, "paused");
  assert.equal(task?.transferredBytes, 50);
  assert.equal(task?.checkpointBytes, 50);
  assert.equal(task?.speed, 0);
});

test("progress under a paused folder parent cannot re-open the child as transferring", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [
    {
      ...makeTask("folder", "paused"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 1,
      totalBytes: 12,
      speed: 0,
    },
    {
      ...makeTask("child", "paused"),
      parentTaskId: "folder",
      transferredBytes: 6_000_000,
      totalBytes: 58_000_000,
      speed: 0,
      lifecycleEpoch: 1,
    },
  ]);

  // Soft-drain progress without a resume epoch must stay paused (no blink row).
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "child",
    transferred: 7_000_000,
    totalBytes: 58_000_000,
    speed: 50,
    checkpointBytes: 7_000_000,
    lifecycleEpoch: 1,
    lifecycleState: "transferring",
  });

  const child = store.getSnapshot().tasks.find((row) => row.id === "child");
  assert.equal(child?.status, "paused");
  assert.equal(child?.transferredBytes, 6_000_000);
  assert.equal(child?.speed, 0);
  assert.equal(child?.checkpointBytes, 7_000_000);
});

test("publishOwner cannot resurrect a live child under a paused folder parent", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [
    {
      ...makeTask("folder", "paused"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 1,
      totalBytes: 12,
      speed: 0,
    },
    {
      ...makeTask("child", "paused"),
      parentTaskId: "folder",
      transferredBytes: 1_000,
      totalBytes: 10_000,
      speed: 0,
    },
  ]);

  store.publishOwner("panel-a", [
    {
      ...makeTask("folder", "paused"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 1,
      totalBytes: 12,
      speed: 0,
    },
    {
      ...makeTask("child", "transferring"),
      parentTaskId: "folder",
      transferredBytes: 2_000,
      totalBytes: 10_000,
      speed: 40,
    },
  ]);

  const child = store.getSnapshot().tasks.find((row) => row.id === "child");
  assert.equal(child?.status, "paused");
  assert.equal(child?.speed, 0);
});

test("intentional panel pause wins even when soft-drain store bytes are ahead", () => {
  const store = createSftpTransferCenterStore();
  // Background soft-drain advanced the global bar past the panel snapshot.
  store.publishOwner("panel-a", [{
    ...makeTask("folder-parent", "transferring"),
    isDirectory: true,
    progressMode: "files",
    transferredBytes: 3,
    totalBytes: 12,
    checkpointBytes: 3,
  }]);
  store.publishOwner("panel-a", [{
    ...makeTask("child-live", "transferring"),
    parentTaskId: "folder-parent",
    transferredBytes: 4_000_000,
    totalBytes: 45_000_000,
    checkpointBytes: 4_000_000,
    speed: 80,
  }]);
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "child-live",
    transferred: 8_000_000,
    totalBytes: 45_000_000,
    speed: 90,
    checkpointBytes: 8_000_000,
  });
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "child-live")?.transferredBytes, 8_000_000);

  // User hits pause: panel freezes child at a lower snapshot and paints pausing.
  store.publishOwner("panel-a", [
    {
      ...makeTask("folder-parent", "pausing"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 3,
      totalBytes: 12,
      checkpointBytes: 3,
      speed: 0,
    },
    {
      ...makeTask("child-live", "pausing"),
      parentTaskId: "folder-parent",
      transferredBytes: 4_000_000,
      totalBytes: 45_000_000,
      checkpointBytes: 4_000_000,
      speed: 0,
    },
  ]);

  const parent = store.getSnapshot().tasks.find((row) => row.id === "folder-parent");
  const child = store.getSnapshot().tasks.find((row) => row.id === "child-live");
  assert.equal(parent?.status, "pausing", "folder parent pause must stick in the global center");
  assert.equal(child?.status, "pausing", "child pause must not be rejected for higher store bytes");
  assert.equal(child?.transferredBytes, 8_000_000, "keep soft-drain water mark, freeze further motion");
  assert.equal(child?.speed, 0);
});

test("pause/resume are process-global even when a live owner is registered", async (t) => {
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  resetTransferWalkRegistryForTests();
  registerTransferWalk("dir");
  t.after(() => {
    unregisterTransferWalk("dir");
    resetTransferWalkRegistryForTests();
  });

  const store = createSftpTransferCenterStore();
  const controllerCalls: string[] = [];
  let syncCalls = 0;
  store.registerOwner("panel-a", {
    pause: async (id) => { controllerCalls.push(`pause:${id}`); },
    resume: async (id) => { controllerCalls.push(`resume:${id}`); },
    cancel: async () => {},
    retry: async () => {},
    prioritize: async () => {},
    dismiss: () => {},
    ownsTask: () => true,
    syncOwnedTasks: () => { syncCalls += 1; },
  });
  store.publishOwner("panel-a", [{
    ...makeTask("dir", "transferring"),
    isDirectory: true,
    progressMode: "files",
    transferredBytes: 2,
    totalBytes: 10,
  }, {
    ...makeTask("file", "transferring"),
    parentTaskId: "dir",
    transferredBytes: 100,
    totalBytes: 1000,
  }]);

  await store.pause("dir");
  assert.deepEqual(controllerCalls, [], "soft-control must not route pause through React owner");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir")?.status, "paused");
  assert.ok(syncCalls >= 1, "owners still get a sync so local lists can mirror store");

  await store.resume("dir");
  assert.deepEqual(controllerCalls, [], "soft-control must not route resume through React owner");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir")?.status, "transferring");
});

test("pause after terminal close works without any React owner", async () => {
  const {
    isTransferPauseLatched,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("stale-dir");
  const pauseCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  const cleanup = () => {
    unregisterTransferWalk("stale-dir");
    resetTransferPauseLatchesForTests();
    resetTransferWalkRegistryForTests();
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  };
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async (id: string) => {
          pauseCalls.push(id);
          return { success: true, checkpointBytes: 1 };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  // No owner registered — pure process-global control after tab close.
  store.publishOwner("terminal:closed-tab", [{
    ...makeTask("stale-dir", "transferring"),
    isDirectory: true,
    progressMode: "files",
    transferredBytes: 2,
    totalBytes: 12,
  }, {
    ...makeTask("stale-child", "transferring"),
    parentTaskId: "stale-dir",
    transferredBytes: 1,
    totalBytes: 10,
  }]);

  await store.pause("stale-dir");

  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "stale-dir")?.status, "paused");
  assert.equal(isTransferPauseLatched("stale-dir"), true);
  assert.ok(pauseCalls.includes("stale-child"));
  cleanup();
});

test("soft pause checkpoint and source fingerprint persist before pause returns", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async () => ({
          success: true,
          checkpointBytes: 7,
          sourceFingerprint: "sha256:pause-return",
          lifecycleEpoch: 1,
        }),
      },
    },
  });
  let persisted = "";
  const store = createSftpTransferCenterStore({
    read: () => null,
    write: (value) => { persisted = value; },
  });
  store.publishOwner("closed-panel", [{
    ...makeTask("pause-durable", "transferring"),
    transferredBytes: 7,
    totalBytes: 10,
  }]);

  await store.pause("pause-durable");

  const restored = createSftpTransferCenterStore({
    read: () => persisted,
    write: () => {},
  });
  const restoredTask = restored.getSnapshot().tasks[0];
  assert.equal(restoredTask?.status, "interrupted");
  assert.equal(restoredTask?.checkpointBytes, 7);
  assert.equal(restoredTask?.sourceFingerprint, "sha256:pause-return");
});

test("publishOwner still allows explicit restart that resets progress to zero", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 500,
    totalBytes: 1000,
  }]);
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "queued"),
    transferredBytes: 0,
    checkpointBytes: 0,
    totalBytes: 1000,
  }]);
  const task = store.getSnapshot().tasks.find((row) => row.id === "upload-1");
  assert.equal(task?.status, "queued");
  assert.equal(task?.transferredBytes, 0);
});

test("stale panel publishOwner cannot un-complete after background completed", () => {
  const store = createSftpTransferCenterStore();
  const endedAt = Date.now();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 900,
    totalBytes: 1000,
    startTime: endedAt - 1000,
  }]);
  store.ingestBackgroundEvent({
    type: "completed",
    transferId: "upload-1",
    endedAt,
  });
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "upload-1")?.status, "completed");
  assert.equal(store.getSnapshot().activeCount, 0);

  // Late panel snapshot still says transferring (React unmount / dual-writer race).
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 900,
    totalBytes: 1000,
    speed: 50,
    startTime: endedAt - 1000,
  }]);

  const task = store.getSnapshot().tasks.find((row) => row.id === "upload-1");
  assert.equal(task?.status, "completed");
  assert.equal(task?.endTime, endedAt);
  assert.equal(store.getSnapshot().activeCount, 0);
});

test("stale panel publishOwner cannot un-cancel after background cancelled", () => {
  const store = createSftpTransferCenterStore();
  const endedAt = Date.now();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 100,
    totalBytes: 1000,
    startTime: endedAt - 1000,
  }]);
  store.ingestBackgroundEvent({
    type: "cancelled",
    transferId: "upload-1",
    endedAt,
  });
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "upload-1")?.status, "cancelled");

  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 150,
    totalBytes: 1000,
    startTime: endedAt - 1000,
  }]);

  const task = store.getSnapshot().tasks.find((row) => row.id === "upload-1");
  assert.equal(task?.status, "cancelled");
  assert.equal(task?.endTime, endedAt);
  assert.equal(store.getSnapshot().activeCount, 0);
});

test("orphan pause latches process-global pause without a panel controller", async () => {
  const {
    isTransferPauseLatched,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  resetTransferPauseLatchesForTests();
  const store = createSftpTransferCenterStore();
  const started = Date.now();
  store.publishOwner("gone-panel", [{
    ...makeTask("live-1", "transferring"),
    transferredBytes: 10,
    totalBytes: 100,
    startTime: started,
  }]);
  // No registerOwner — simulates tab close / panel unmount.
  // pauseTransfer is unavailable in pure unit tests → demote path still latches.
  await store.pause("live-1");
  assert.equal(isTransferPauseLatched("live-1"), true, "pause must latch even without a panel owner");
  const task = store.getSnapshot().tasks.find((row) => row.id === "live-1");
  assert.ok(task);
  assert.ok(
    task!.status === "paused" || task!.status === "interrupted" || task!.status === "pausing",
    `expected paused-like status, got ${task!.status}`,
  );
  resetTransferPauseLatchesForTests();
});

test("orphan pause retries while the backend stream is still arming", async (t) => {
  const pauseCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async (id: string) => {
          pauseCalls.push(id);
          return pauseCalls.length === 1
            ? { success: false, reason: "This transfer cannot be paused yet" }
            : { success: true, checkpointBytes: 42 };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("arming-upload", "transferring"),
    transferredBytes: 20,
    totalBytes: 100,
  }]);

  await store.pause("arming-upload");

  assert.deepEqual(pauseCalls, ["arming-upload", "arming-upload"]);
  const task = store.getSnapshot().tasks.find((candidate) => candidate.id === "arming-upload");
  assert.equal(task?.status, "paused");
  assert.equal(task?.checkpointBytes, 42);
  assert.equal(task?.pauseUnavailableReason, undefined);
});

test("orphan compressed upload pause uses the compression job and reports deferred pause honestly", async (t) => {
  const compressedCalls: string[] = [];
  const streamCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseCompressedUpload: async (id: string) => {
          compressedCalls.push(id);
          return { success: true, deferred: true };
        },
        pauseTransfer: async (id: string) => {
          streamCalls.push(id);
          return { success: true };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("compressed-1", "transferring"),
    fileName: "photos (compressed)",
    isDirectory: true,
    phase: "extracting",
    controlKind: "compressed-upload",
  } as TransferTask]);

  await store.pause("compressed-1");

  assert.deepEqual(compressedCalls, ["compressed-1"]);
  assert.deepEqual(streamCalls, []);
  assert.equal(store.getSnapshot().tasks[0]?.status, "pausing");
  assert.equal(store.getSnapshot().tasks[0]?.pauseUnavailableReason, undefined);
});

test("orphan compressed upload resume and cancel keep using the compression job", async (t) => {
  const calls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        resumeCompressedUpload: async (id: string) => {
          calls.push(`resume:${id}`);
          return { success: true };
        },
        cancelCompressedUpload: async (id: string) => {
          calls.push(`cancel:${id}`);
          return { success: true };
        },
        resumeTransfer: async (id: string) => {
          calls.push(`wrong-resume:${id}`);
          return { success: true };
        },
        cancelTransfer: async (id: string) => {
          calls.push(`wrong-cancel:${id}`);
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("compressed-2", "paused"),
    fileName: "archive (compressed)",
    isDirectory: true,
    phase: "compressing",
    controlKind: "compressed-upload",
  } as TransferTask]);

  await store.resume("compressed-2");
  assert.equal(store.getSnapshot().tasks[0]?.status, "transferring");
  await store.cancel("compressed-2");

  assert.deepEqual(calls, ["resume:compressed-2", "cancel:compressed-2"]);
  assert.equal(store.getSnapshot().tasks[0]?.status, "cancelled");
});

test("orphan cancel marks process-global cancel so surviving walks stop", async () => {
  const {
    isTransferCancelledFlag,
    resetTransferCancelLatchesForTests,
  } = await import("./sftp/transferCancelLatch");
  resetTransferCancelLatchesForTests();
  const store = createSftpTransferCenterStore();
  const started = Date.now();
  store.publishOwner("gone-panel", [{
    ...makeTask("walk-1", "transferring"),
    transferredBytes: 10,
    totalBytes: 100,
    startTime: started,
  }]);
  await store.cancel("walk-1");
  assert.equal(isTransferCancelledFlag("walk-1"), true);
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "walk-1")?.status, "cancelled");
  resetTransferCancelLatchesForTests();
});

test("orphan resume releases process-global pause latch without a panel owner", async (t) => {
  const {
    isTransferPauseLatched,
    latchTransferPause,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  // Live walk: soft-unlatch only (do not fall through to prepareAdopter).
  registerTransferWalk("paused-1");
  t.after(() => {
    unregisterTransferWalk("paused-1");
    resetTransferPauseLatchesForTests();
    resetTransferWalkRegistryForTests();
  });
  const store = createSftpTransferCenterStore();
  const started = Date.now();
  store.publishOwner("gone-panel", [{
    ...makeTask("paused-1", "paused"),
    transferredBytes: 10,
    totalBytes: 100,
    checkpointBytes: 10,
    startTime: started,
  }]);
  // Simulate a walk that outlived the panel and is still latched.
  latchTransferPause("paused-1");
  assert.equal(isTransferPauseLatched("paused-1"), true);
  await store.resume("paused-1");
  assert.equal(
    isTransferPauseLatched("paused-1"),
    false,
    "resume without a panel owner must unlatch so surviving walks continue",
  );
});

test("failed rows may reopen via publishOwner for same-id checkpoint resume", () => {
  // failed is not sticky: resumeTransfer paints transferring with preserved checkpoint.
  const store = createSftpTransferCenterStore();
  const endedAt = Date.now();
  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 100,
    totalBytes: 1000,
    checkpointBytes: 100,
    startTime: endedAt - 1000,
  }]);
  store.ingestBackgroundEvent({
    type: "failed",
    transferId: "upload-1",
    endedAt,
    error: "disk full",
  });
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "upload-1")?.status, "failed");

  store.publishOwner("panel-a", [{
    ...makeTask("upload-1", "transferring"),
    transferredBytes: 100,
    totalBytes: 1000,
    checkpointBytes: 100,
    startTime: endedAt - 1000,
  }]);
  const task = store.getSnapshot().tasks.find((row) => row.id === "upload-1");
  assert.equal(task?.status, "transferring");
  assert.equal(task?.checkpointBytes, 100);
});

test("clearing terminal history asks each owner to clean transfer artifacts", () => {
  const dismissed: string[] = [];
  const store = createSftpTransferCenterStore();
  store.registerOwner("panel-a", {
    pause: async () => {}, resume: async () => {}, cancel: async () => {}, retry: async () => {}, prioritize: async () => {},
    dismiss: (id) => { dismissed.push(id); },
  });
  store.publishOwner("panel-a", [makeTask("done", "completed"), makeTask("failed", "failed")]);

  store.clearTerminal("completed");

  assert.deepEqual(dismissed, ["done"]);
  assert.deepEqual(store.getSnapshot().tasks.map((task) => task.id), ["failed"]);
});

test("failed reauthentication leaves a paused transfer requiring attention with the failure reason", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      dispatchEvent(event: CustomEvent<{ reportFailure?: (error: string) => void }>) {
        event.detail.reportFailure?.("Authentication failed");
        return true;
      },
    },
  });

  const store = createSftpTransferCenterStore();
  // Original panel is gone — resume must open/authenticate a new one.
  // A preparer is present but adoption never becomes ready because auth fails.
  store.registerOwner("visible-preparer", {
    pause: async () => {}, resume: async () => {}, cancel: async () => {}, retry: async () => {}, prioritize: async () => {},
    dismiss: () => {},
    canAdopt: () => false,
    canPrepareAdoption: true,
    adopt: async () => {},
  });
  store.publishOwner("closed-panel", [{
    ...makeTask("paused", "paused"),
    sourceConnectionId: "closed",
    sourceHostId: "host-a",
  }]);

  await store.resume("paused");

  assert.equal(store.getSnapshot().tasks[0]?.status, "attention");
  assert.equal(store.getSnapshot().tasks[0]?.error, "Authentication failed");
});

test("background events do not resurrect a cancelled agent transfer", () => {
  const store = createSftpTransferCenterStore();
  store.ingestBackgroundEvent({
    type: "queued",
    transferId: "agent-1",
    direction: "download",
    sourcePath: "/r/a",
    targetPath: "/l/a",
    startedAt: Date.now(),
  });
  store.ingestBackgroundEvent({ type: "cancelled", transferId: "agent-1", endedAt: Date.now() });
  assert.equal(store.getSnapshot().tasks[0]?.status, "cancelled");

  store.ingestBackgroundEvent({ type: "started", transferId: "agent-1" });
  store.ingestBackgroundEvent({ type: "progress", transferId: "agent-1", transferred: 50, totalBytes: 100, speed: 1 });
  store.ingestBackgroundEvent({ type: "completed", transferId: "agent-1", endedAt: Date.now() });

  assert.equal(store.getSnapshot().tasks[0]?.status, "cancelled");
});

test("orphaned resume prefers a dedicated SFTP session without a panel owner", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("dedicated", "interrupted"),
    direction: "download",
    sourceHostId: "host-a",
    sourceHostLabel: "CI-Build-01",
    targetConnectionId: "local",
    checkpointBytes: 100,
    reconnectRequired: true,
  }]);

  let sawTaskId = "";
  store.setDedicatedResumeHandler(async (task) => {
    sawTaskId = task.id;
    store.patchTask(task.id, {
      status: "transferring",
      transferredBytes: 100,
      speed: 10,
    });
    return { success: true };
  });

  await store.resume("dedicated");

  assert.equal(sawTaskId, "dedicated");
  assert.equal(store.getSnapshot().tasks[0]?.status, "completed");
  assert.equal(store.getSnapshot().tasks[0]?.reconnectRequired, false);
});

test("force-quit continue skips dead soft-resume and uses dedicated handler", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  let softResumeCalls = 0;
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        clearPendingTransferCancel: async () => ({ success: true }),
        resumeTransfer: async () => {
          softResumeCalls += 1;
          return { success: true }; // would be wrong to honor after force-quit
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("after-quit", "interrupted"),
    direction: "upload",
    sourceConnectionId: "local",
    targetHostId: "host-a",
    targetHostLabel: "box-a",
    checkpointBytes: 20,
    transferredBytes: 20,
    totalBytes: 100,
    reconnectRequired: true,
  }]);

  let sawDedicated = false;
  store.setDedicatedResumeHandler(async (task) => {
    sawDedicated = true;
    store.patchTask(task.id, {
      status: "transferring",
      transferredBytes: 40,
      checkpointBytes: 40,
      reconnectRequired: false,
      ownerId: "dedicated-resume",
    });
    return { success: true };
  });

  await store.resume("after-quit");
  assert.equal(softResumeCalls, 0, "must not soft-resume a dead post-quit transfer");
  assert.equal(sawDedicated, true);
  assert.equal(store.getSnapshot().tasks[0]?.status, "completed");
  assert.equal(store.getSnapshot().tasks[0]?.ownerId, "dedicated-resume");
});

test("restart resume waits for the dedicated handler to become ready and keeps progress live", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        clearPendingTransferCancel: async () => ({ success: true }),
        resumeTransfer: async () => ({ success: false, reason: "Transfer is no longer active" }),
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("startup-race", "interrupted"),
    direction: "upload",
    sourceConnectionId: "local",
    targetHostId: "host-a",
    targetHostLabel: "box-a",
    checkpointBytes: 20,
    transferredBytes: 20,
    totalBytes: 100,
    reconnectRequired: true,
  }]);

  let releaseTransfer!: () => void;
  const transferFinished = new Promise<void>((resolve) => { releaseTransfer = resolve; });
  let handlerStarted!: () => void;
  const sawHandlerStart = new Promise<void>((resolve) => { handlerStarted = resolve; });

  const resume = store.resume("startup-race");
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(store.getSnapshot().tasks[0]?.status, "pending");

  store.setDedicatedResumeHandler(async (task) => {
    store.patchTask(task.id, {
      status: "transferring",
      transferredBytes: 35,
      checkpointBytes: 35,
      reconnectRequired: false,
    });
    handlerStarted();
    await transferFinished;
    return { success: true };
  });

  await sawHandlerStart;
  assert.equal(store.getSnapshot().tasks[0]?.status, "transferring");
  assert.equal(store.getSnapshot().tasks[0]?.transferredBytes, 35);

  releaseTransfer();
  await resume;
  assert.equal(store.getSnapshot().tasks[0]?.status, "completed");
});

test("directory resume uses dedicated handler and rehomes children", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [
    {
      ...makeTask("dir-parent", "interrupted"),
      isDirectory: true,
      progressMode: "files",
      direction: "download",
      sourceHostId: "host-a",
      sourceHostLabel: "CI-Build-01",
      targetConnectionId: "local",
      totalBytes: 2,
      transferredBytes: 1,
      reconnectRequired: true,
    },
    {
      ...makeTask("dir-child-done", "completed"),
      parentTaskId: "dir-parent",
      sourcePath: "/r/a",
      targetPath: "/l/a",
    },
    {
      ...makeTask("dir-child-open", "interrupted"),
      parentTaskId: "dir-parent",
      sourcePath: "/r/b",
      targetPath: "/l/b",
      checkpointBytes: 50,
      reconnectRequired: true,
    },
  ]);

  let sawDirectory = false;
  store.setDedicatedResumeHandler(async (task) => {
    sawDirectory = !!task.isDirectory;
    store.upsertTasks([{
      ...makeTask("dir-child-open", "completed"),
      parentTaskId: "dir-parent",
      ownerId: "dedicated-resume",
      sourcePath: "/r/b",
      targetPath: "/l/b",
    }]);
    return { success: true };
  });

  await store.resume("dir-parent");

  assert.equal(sawDirectory, true);
  const snapshot = store.getSnapshot().tasks;
  const parent = snapshot.find((task) => task.id === "dir-parent");
  const children = snapshot.filter((task) => task.parentTaskId === "dir-parent");
  assert.equal(parent?.status, "completed");
  assert.equal(parent?.ownerId, "dedicated-resume");
  assert.ok(children.every((child) => child.ownerId === "dedicated-resume"));
  assert.equal(children.find((child) => child.id === "dir-child-done")?.status, "completed");
});

test("upsertTasks refuses new children under a cancelled directory parent", () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("dedicated-resume", [{
    ...makeTask("dir", "cancelled"),
    isDirectory: true,
    ownerId: "dedicated-resume",
  }]);
  store.upsertTasks([{
    ...makeTask("late-child", "transferring"),
    parentTaskId: "dir",
    ownerId: "dedicated-resume",
  }]);
  assert.equal(store.getSnapshot().tasks.some((task) => task.id === "late-child"), false);
});

test("pause on dedicated directory parent freezes unfinished children", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("dedicated-resume", [
    {
      ...makeTask("dir", "transferring"),
      isDirectory: true,
      ownerId: "dedicated-resume",
      direction: "download",
      sourceHostId: "host-a",
      targetConnectionId: "local",
      totalBytes: 2,
      transferredBytes: 0,
    },
    {
      ...makeTask("c1", "transferring"),
      parentTaskId: "dir",
      ownerId: "dedicated-resume",
    },
  ]);

  // No live bridge pause — directory soft-pause latches + paints paused without
  // cancel demotion (cancel would kill a still-running dedicated walk).
  await store.pause("dir");

  const snapshot = store.getSnapshot().tasks;
  assert.equal(snapshot.find((task) => task.id === "dir")?.status, "paused");
  assert.equal(snapshot.find((task) => task.id === "c1")?.status, "paused");
});

test("dedicated directory resume after soft-pause winds down then startFresh (no dead transferring)", async (t) => {
  const store = createSftpTransferCenterStore();
  let resumeCalls = 0;
  const cancelCalls: string[] = [];
  // Models bridge cancel when the child is no longer in activeTransfers: the
  // pendingCancel latch sticks until clearPendingTransferCancel runs.
  const pendingCancel = new Set<string>();
  const clearPendingCalls: string[] = [];
  let firstRunStarted: (() => void) | null = null;
  const firstRunBlocked = new Promise<void>((resolve) => { firstRunStarted = resolve; });
  let releaseFirstRun: (() => void) | null = null;
  const firstRunHold = new Promise<void>((resolve) => { releaseFirstRun = resolve; });

  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        cancelTransfer: async (id: string) => {
          cancelCalls.push(id);
          // Sticky latch when not active (real bridge behavior for missing handle).
          pendingCancel.add(id);
          return { success: true };
        },
        clearPendingTransferCancel: async (id: string) => {
          clearPendingCalls.push(id);
          pendingCancel.delete(id);
          return { success: true };
        },
        resumeTransfer: async () => ({ success: false, reason: "Transfer is no longer active" }),
        pauseTransfer: async () => ({ success: true, checkpointBytes: 1 }),
      },
    },
  });

  store.publishOwner("dedicated-resume", [{
    ...makeTask("dir", "interrupted"),
    isDirectory: true,
    ownerId: "dedicated-resume",
    direction: "download",
    sourceHostId: "host-a",
    targetConnectionId: "local",
    totalBytes: 2,
    transferredBytes: 0,
    reconnectRequired: true,
  }, {
    ...makeTask("c1", "interrupted"),
    parentTaskId: "dir",
    ownerId: "dedicated-resume",
    reconnectRequired: true,
  }]);

  store.setDedicatedResumeHandler(async () => {
    resumeCalls += 1;
    if (resumeCalls === 1) {
      // Leave reconnectRequired so pause is not skipped only after live transfer.
      store.patchTask("dir", { status: "transferring", reconnectRequired: false });
      store.upsertTasks([{
        ...makeTask("c1", "transferring"),
        parentTaskId: "dir",
        ownerId: "dedicated-resume",
        reconnectRequired: false,
      }]);
      firstRunStarted?.();
      await firstRunHold;
      return { success: false, error: "Transfer cancelled" };
    }
    // startFresh reuses the same child transfer id — latch must be clear or
    // startStreamTransfer would immediately cancel (production failure mode).
    assert.equal(
      pendingCancel.has("c1"),
      false,
      "child pendingCancel latch must be cleared before startFresh reuses c1",
    );
    assert.ok(clearPendingCalls.includes("c1"), "must call clearPendingTransferCancel for c1");
    return { success: true };
  });

  // Start first dedicated run (held in resumeInvocations).
  const first = store.resume("dir");
  await firstRunBlocked;
  assert.equal(store.getSnapshot().tasks.find((task) => task.id === "dir")?.status, "transferring");

  // Soft-pause paints paused under dedicated-resume.
  await store.pause("dir");
  assert.equal(store.getSnapshot().tasks.find((task) => task.id === "dir")?.status, "paused");

  // Resume must cancel soft-paused children, clear sticky latches, await wind-down, then startFresh.
  const second = store.resume("dir");
  // Allow the held first run to settle after cancel wind-down begins.
  releaseFirstRun?.();
  await second;
  await first.catch(() => {});

  assert.ok(cancelCalls.includes("c1"), "must cancel soft-paused children before startFresh");
  assert.equal(resumeCalls, 2, "must startFresh after wind-down");
  assert.equal(pendingCancel.has("c1"), false, "child latch must stay clear after startFresh");
  assert.equal(store.getSnapshot().tasks.find((task) => task.id === "dir")?.status, "completed");
});

test("resume refuses when another active transfer already owns the same path", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [
    {
      ...makeTask("live", "transferring"),
      sourcePath: "/root/sing-box",
      targetPath: "/Users/me/Desktop/sing-box",
      fileName: "sing-box",
      direction: "download",
      sourceConnectionId: "remote-live",
      targetConnectionId: "local",
    },
    {
      ...makeTask("stale", "interrupted"),
      sourcePath: "/root/other",
      targetPath: "/Users/me/Desktop/sing-box",
      fileName: "sing-box",
      direction: "download",
      sourceConnectionId: "remote-stale",
      targetConnectionId: "local",
      reconnectRequired: true,
    },
  ]);
  store.setDedicatedResumeHandler(async () => {
    throw new Error("dedicated resume must not run when path is busy");
  });

  await store.resume("stale");

  const stale = store.getSnapshot().tasks.find((task) => task.id === "stale");
  assert.equal(stale?.status, "attention");
  assert.match(stale?.error ?? "", /already in progress/i);
});

test("orphan soft-resume with a live walk does not start dedicated resume", async (t) => {
  const { registerTransferWalk, unregisterTransferWalk, resetTransferWalkRegistryForTests } =
    await import("./sftp/transferWalkRegistry");
  resetTransferWalkRegistryForTests();
  registerTransferWalk("dir-alive");
  t.after(() => {
    unregisterTransferWalk("dir-alive");
    resetTransferWalkRegistryForTests();
  });

  const resumeCalls: string[] = [];
  let dedicatedCalls = 0;
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        // Between files: no active child streams — bridge resume misses.
        resumeTransfer: async (id: string) => {
          resumeCalls.push(id);
          return { success: false, reason: "Transfer is no longer active" };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.setDedicatedResumeHandler(async () => {
    dedicatedCalls += 1;
    return { success: false, error: "should not dedicated-resume a live walk" };
  });
  store.publishOwner("terminal:closed-tab", [{
    ...makeTask("dir-alive", "paused"),
    isDirectory: true,
    progressMode: "files",
    transferredBytes: 3,
    totalBytes: 12,
    speed: 0,
  }, {
    ...makeTask("child-done", "completed"),
    parentTaskId: "dir-alive",
  }]);

  await store.resume("dir-alive");

  assert.equal(dedicatedCalls, 0, "must not start a second dedicated walk while processTransfer is alive");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir-alive")?.status, "transferring");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir-alive")?.reconnectRequired, false);
});

test("async single-file soft-drain does not re-pause after immediate resume", async (t) => {
  let resolveFilePause!: (value: { success: boolean; checkpointBytes?: number }) => void;
  const filePauseGate = new Promise<{ success: boolean; checkpointBytes?: number }>((resolve) => {
    resolveFilePause = resolve;
  });
  const resumeCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async (id: string) => {
          if (id === "file-slow") return filePauseGate;
          return { success: true, checkpointBytes: 1 };
        },
        resumeTransfer: async (id: string) => {
          resumeCalls.push(id);
          return { success: true };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("gone-panel", [{
    ...makeTask("file-slow", "transferring"),
    transferredBytes: 10,
    totalBytes: 100,
    checkpointBytes: 10,
  }]);

  // Pause returns after soft-drain await for single-file (not fire-and-forget).
  // Hold the bridge pause so Resume can win mid-drain.
  const pausePromise = store.pause("file-slow");
  // Yield so pause paints pausing and enters soft-drain.
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "file-slow")?.status, "pausing");

  await store.resume("file-slow");
  // Soft-drain finally completes after resume.
  resolveFilePause({ success: true, checkpointBytes: 20 });
  await pausePromise;
  await new Promise((resolve) => setTimeout(resolve, 10));

  const file = store.getSnapshot().tasks.find((row) => row.id === "file-slow");
  assert.equal(file?.status, "transferring", "late single-file soft-drain must not re-paint paused");
  assert.ok(resumeCalls.includes("file-slow"), "must resumeTransfer after superseded single-file pause");
  const {
    isTransferPauseLatched,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  assert.equal(isTransferPauseLatched("file-slow"), false, "latches must be clear after resume wins");
  resetTransferPauseLatchesForTests();
});

test("async folder soft-drain does not re-pause after immediate resume", async (t) => {
  let resolveChildPause!: (value: { success: boolean; checkpointBytes?: number }) => void;
  const childPauseGate = new Promise<{ success: boolean; checkpointBytes?: number }>((resolve) => {
    resolveChildPause = resolve;
  });
  const resumeCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async (id: string) => {
          if (id === "child-slow") return childPauseGate;
          return { success: true, checkpointBytes: 1 };
        },
        resumeTransfer: async (id: string) => {
          resumeCalls.push(id);
          return { success: true };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("gone-panel", [
    {
      ...makeTask("dir-race", "transferring"),
      isDirectory: true,
      progressMode: "files",
      transferredBytes: 0,
      totalBytes: 2,
    },
    {
      ...makeTask("child-slow", "transferring"),
      parentTaskId: "dir-race",
      transferredBytes: 10,
      totalBytes: 100,
    },
  ]);

  // Pause returns immediately (async soft-drain).
  await store.pause("dir-race");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir-race")?.status, "paused");

  // User hits resume before soft-drain finishes — full store resume (bumps epoch
  // + releases latch tree), which is what the UI does.
  await store.resume("dir-race");
  assert.equal(store.getSnapshot().tasks.find((row) => row.id === "dir-race")?.status, "transferring");
  // Soft-drain pause finally resolves after resume.
  resolveChildPause({ success: true, checkpointBytes: 10 });
  await new Promise((resolve) => setTimeout(resolve, 30));

  const dir = store.getSnapshot().tasks.find((row) => row.id === "dir-race");
  const child = store.getSnapshot().tasks.find((row) => row.id === "child-slow");
  assert.equal(dir?.status, "transferring", "late soft-drain must not re-paint parent paused");
  assert.equal(child?.status, "transferring", "late soft-drain must not re-paint child paused");
  // Bridge pause that landed after resume must be undone.
  assert.ok(resumeCalls.includes("child-slow"), "must resumeTransfer the child after superseded pause");
});

test("orphan directory pause stays latched even when some children hard-miss pause", async (t) => {
  const pauseCalls: string[] = [];
  const resumeCalls: string[] = [];
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async (id: string) => {
          pauseCalls.push(id);
          if (id === "c-ok") return { success: true, checkpointBytes: 4 };
          return { success: false, reason: "This transfer cannot be paused safely" };
        },
        resumeTransfer: async (id: string) => {
          resumeCalls.push(id);
          return { success: true };
        },
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("dedicated-resume", [
    {
      ...makeTask("dir", "transferring"),
      isDirectory: true,
      ownerId: "dedicated-resume",
      direction: "download",
      sourceHostId: "host-a",
      targetConnectionId: "local",
      totalBytes: 2,
      transferredBytes: 0,
    },
    {
      ...makeTask("c-ok", "transferring"),
      parentTaskId: "dir",
      ownerId: "dedicated-resume",
    },
    {
      ...makeTask("c-fail", "transferring"),
      parentTaskId: "dir",
      ownerId: "dedicated-resume",
    },
  ]);

  await store.pause("dir");

  // Folder pause is async: UI is paused immediately; soft-drain may still be running.
  const dir = store.getSnapshot().tasks.find((task) => task.id === "dir");
  assert.equal(dir?.status, "paused", "folder parent must paint paused without waiting on children");
  assert.equal(store.getSnapshot().tasks.find((task) => task.id === "c-ok")?.status, "paused");
  assert.equal(store.getSnapshot().tasks.find((task) => task.id === "c-fail")?.status, "paused");
  // Soft-drain is fire-and-forget — give it a tick to hit the bridge.
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.deepEqual(pauseCalls.sort(), ["c-fail", "c-ok"]);
  // Latch-first: do not bridge-resume partial successes (that re-opened the queue).
  assert.deepEqual(resumeCalls, []);
  assert.equal(
    store.getSnapshot().tasks.find((task) => task.id === "dir")?.status,
    "paused",
    "hard-miss soft-drain must not demote folder out of paused",
  );
});

test("dedicated resume source-changed marks attention and can reset checkpoint", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("src-changed", "interrupted"),
    direction: "download",
    sourceHostId: "host-a",
    targetConnectionId: "local",
    checkpointBytes: 40,
    transferredBytes: 40,
    totalBytes: 100,
    reconnectRequired: true,
  }]);

  store.setDedicatedResumeHandler(async () => ({
    success: false,
    needsAttention: true,
    resetCheckpoint: true,
    error: "Source was modified while the transfer was paused",
  }));

  await store.resume("src-changed");

  const task = store.getSnapshot().tasks[0];
  assert.equal(task?.status, "attention");
  assert.equal(task?.retryable, true);
  assert.equal(task?.checkpointBytes, 0);
  assert.equal(task?.transferredBytes, 0);
  assert.match(task?.error ?? "", /modified/i);
});

test("reconnectRequired resume skips a retained panel that cannot adopt", async () => {
  const store = createSftpTransferCenterStore();
  const ownerCalls: string[] = [];
  store.registerOwner("stale-panel", {
    pause: async () => {},
    resume: async (id) => { ownerCalls.push(`resume:${id}`); },
    cancel: async () => {},
    retry: async () => {},
    prioritize: async () => {},
    dismiss: () => {},
    canAdopt: () => false,
  });
  store.publishOwner("stale-panel", [{
    ...makeTask("stuck", "attention"),
    direction: "download",
    sourceHostId: "host-a",
    sourceHostLabel: "CI-Build-01",
    targetConnectionId: "local",
    reconnectRequired: true,
    error: "Reconnect the source and target before resuming",
  }]);

  let dedicated = false;
  store.setDedicatedResumeHandler(async () => {
    dedicated = true;
    return { success: true };
  });

  await store.resume("stuck");

  assert.equal(dedicated, true);
  assert.deepEqual(ownerCalls, []);
  assert.equal(store.getSnapshot().tasks[0]?.status, "completed");
});

test("resume marks orphaned tasks pending while reconnecting", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      dispatchEvent: () => true,
      netcatty: {
        resumeTransfer: async () => ({ success: false }),
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("reconnect-me", "interrupted"),
    sourceHostId: "host-a",
    reconnectRequired: true,
  }]);

  const resumePromise = store.resume("reconnect-me");
  // Status flips to pending before the long prepare wait finishes.
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(store.getSnapshot().tasks[0]?.status, "pending");
  assert.equal(store.getSnapshot().tasks[0]?.reconnectRequired, true);

  // Unblock prepare loop by cancelling.
  await store.cancel("reconnect-me");
  await resumePromise;
  assert.equal(store.getSnapshot().tasks[0]?.status, "cancelled");
});

test("resume waits for a transfer panel that becomes visible after the click", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { dispatchEvent: () => true },
  });

  const calls: string[] = [];
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("waiting", "paused"),
    sourceHostId: "host-a",
  }]);

  const resumePromise = store.resume("waiting");
  setTimeout(() => {
    store.registerOwner("visible-panel", {
      pause: async () => {},
      resume: async (id) => { calls.push(`resume:${id}`); },
      cancel: async () => {}, retry: async () => {}, prioritize: async () => {}, dismiss: () => {},
      canAdopt: () => true,
      canPrepareAdoption: true,
      adopt: async (task) => { calls.push(`adopt:${task.id}`); },
    });
  }, 10);

  await resumePromise;

  assert.deepEqual(calls, ["adopt:waiting"]);
  assert.equal(store.getSnapshot().tasks[0]?.ownerId, "visible-panel");
});

test("an interrupted task without its old controller can still be cancelled", async () => {
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [makeTask("interrupted", "interrupted")]);

  await store.cancel("interrupted");

  assert.equal(store.getSnapshot().tasks[0]?.status, "cancelled");
});

test("concurrent resume clicks adopt a task only once", async () => {
  let adoptCount = 0;
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("resume-once", "interrupted"),
    sourceHostId: "host-a",
  }]);
  store.registerOwner("visible-panel", {
    pause: async () => {}, resume: async () => {}, cancel: async () => {}, retry: async () => {}, prioritize: async () => {}, dismiss: () => {},
    canAdopt: () => true,
    adopt: async () => { adoptCount += 1; },
  });

  await Promise.all([store.resume("resume-once"), store.resume("resume-once")]);

  assert.equal(adoptCount, 1);
});

test("cancelling while resume waits prevents later adoption", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { dispatchEvent: () => true },
  });

  let adoptCount = 0;
  const store = createSftpTransferCenterStore();
  store.publishOwner("closed-panel", [{
    ...makeTask("cancel-waiting", "paused"),
    sourceHostId: "host-a",
  }]);

  const resumePromise = store.resume("cancel-waiting");
  setTimeout(() => { void store.cancel("cancel-waiting"); }, 10);
  setTimeout(() => {
    store.registerOwner("visible-panel", {
      pause: async () => {}, resume: async () => {}, cancel: async () => {}, retry: async () => {}, prioritize: async () => {}, dismiss: () => {},
      canAdopt: () => true,
      canPrepareAdoption: true,
      adopt: async () => { adoptCount += 1; },
    });
  }, 20);

  await resumePromise;

  assert.equal(adoptCount, 0);
  assert.equal(store.getSnapshot().tasks[0]?.status, "cancelled");
});

test("patchTask freezes transferredBytes while paused or latched (runtime soft-drain)", async () => {
  const {
    latchTransferPause,
    releaseTransferPause,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  resetTransferPauseLatchesForTests();
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("live", "transferring"),
    transferredBytes: 1000,
    totalBytes: 10_000,
    checkpointBytes: 1000,
    lifecycleEpoch: 1,
  }]);
  // User pause paints + latches (soft-control).
  latchTransferPause("live");
  store.patchTask("live", { status: "paused", speed: 0, lifecycleEpoch: 2 });
  assert.equal(store.getSnapshot().tasks[0]?.status, "paused");

  // Soft-drain still reports higher bytes via runtime writer.
  store.patchTask("live", {
    transferredBytes: 9000,
    speed: 50,
    checkpointBytes: 9000,
  });
  const afterDrain = store.getSnapshot().tasks[0];
  assert.equal(afterDrain?.status, "paused");
  assert.equal(afterDrain?.transferredBytes, 1000, "visible bar must freeze after pause");
  assert.equal(afterDrain?.speed, 0);
  assert.equal(afterDrain?.checkpointBytes, 9000, "durable checkpoint may still advance");

  releaseTransferPause("live");
  // Explicit resume with epoch may reopen and accept progress.
  store.patchTask("live", {
    status: "transferring",
    lifecycleEpoch: 3,
    transferredBytes: 1500,
    speed: 10,
  });
  assert.equal(store.getSnapshot().tasks[0]?.status, "transferring");
  assert.equal(store.getSnapshot().tasks[0]?.transferredBytes, 1500);
  resetTransferPauseLatchesForTests();
});

test("publishOwner while latched does not raise transferredBytes after pause", async () => {
  const {
    latchTransferPauseTree,
    resetTransferPauseLatchesForTests,
  } = await import("./sftp/transferPauseLatch");
  resetTransferPauseLatchesForTests();
  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("file", "transferring"),
    transferredBytes: 5000,
    totalBytes: 20_000,
    lifecycleEpoch: 1,
  }]);
  latchTransferPauseTree("file", []);
  store.publishOwner("panel-a", [{
    ...makeTask("file", "paused"),
    transferredBytes: 5000,
    totalBytes: 20_000,
    speed: 0,
    lifecycleEpoch: 2,
  }]);
  // Soft-drain panel snapshot tries to push the bar.
  store.publishOwner("panel-a", [{
    ...makeTask("file", "paused"),
    transferredBytes: 18_000,
    totalBytes: 20_000,
    speed: 0,
    lifecycleEpoch: 2,
  }]);
  assert.equal(
    store.getSnapshot().tasks[0]?.transferredBytes,
    5000,
    "latched paused row must not take soft-drain higher bytes from panel",
  );
  resetTransferPauseLatchesForTests();
});

test("soft-resume failure demotes and uses dedicated handler even with a live owner", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        resumeTransfer: async () => ({ success: false, reason: "not active" }),
        clearPendingTransferCancel: async () => {},
      },
    },
  });

  const store = createSftpTransferCenterStore();
  let dedicated = 0;
  store.setDedicatedResumeHandler(async (task) => {
    dedicated += 1;
    store.patchTask(task.id, {
      status: "completed",
      ownerId: "dedicated-resume",
      transferredBytes: task.totalBytes || 10,
      reconnectRequired: false,
    });
    return { success: true };
  });
  // Live owner present but cannot soft-resume a dead stream.
  store.registerOwner("panel-a", {
    pause: async () => {},
    resume: async () => {},
    cancel: async () => {},
    retry: async () => {},
    prioritize: async () => {},
    dismiss: () => {},
    ownsTask: () => true,
    canAdopt: () => false,
  });
  store.publishOwner("panel-a", [{
    ...makeTask("dead-soft", "paused"),
    sourceHostId: "host-a",
    transferredBytes: 4,
    checkpointBytes: 4,
  }]);

  await store.resume("dead-soft");
  assert.equal(dedicated, 1, "must not silent-return when soft fails with owner present");
  assert.equal(store.getSnapshot().tasks[0]?.status, "completed");
});

test("single-file soft-resume bridge-fail with live walk goes hard reconnect (not stuck transferring)", async (t) => {
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  const { resetTransferPauseLatchesForTests } = await import("./sftp/transferPauseLatch");
  resetTransferWalkRegistryForTests();
  resetTransferPauseLatchesForTests();
  registerTransferWalk("stuck-file");
  t.after(() => {
    unregisterTransferWalk("stuck-file");
    resetTransferWalkRegistryForTests();
    resetTransferPauseLatchesForTests();
  });

  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async () => ({ success: true, checkpointBytes: 10, lifecycleEpoch: 1 }),
        resumeTransfer: async () => ({ success: false, reason: "not active" }),
        clearPendingTransferCancel: async () => {},
      },
    },
  });

  const store = createSftpTransferCenterStore();
  let dedicated = 0;
  store.setDedicatedResumeHandler(async (task) => {
    dedicated += 1;
    store.patchTask(task.id, {
      status: "completed",
      ownerId: "dedicated-resume",
      transferredBytes: 100,
      reconnectRequired: false,
    });
    return { success: true };
  });
  store.publishOwner("panel-a", [{
    ...makeTask("stuck-file", "transferring"),
    sourceHostId: "host-a",
    transferredBytes: 10,
    totalBytes: 100,
  }]);

  await store.pause("stuck-file");
  assert.equal(store.getSnapshot().tasks[0]?.status, "paused");

  await store.resume("stuck-file");
  assert.equal(dedicated, 1, "hard reconnect must run when soft bridge resume fails");
  const row = store.getSnapshot().tasks[0];
  assert.equal(row?.status, "completed");
  assert.notEqual(row?.status, "transferring");
});

test("ingestBackgroundEvent progress advances after soft pause then soft resume (bridge epochs)", async (t) => {
  const { resetTransferPauseLatchesForTests } = await import("./sftp/transferPauseLatch");
  const { resetTransferControlEpochsForTests } = await import("./sftp/transferControlEpoch");
  resetTransferPauseLatchesForTests();
  resetTransferControlEpochsForTests();

  let bridgeEpoch = 0;
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
    resetTransferPauseLatchesForTests();
    resetTransferControlEpochsForTests();
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async () => {
          // First pause bumps; second already-paused returns same epoch (bridge behavior).
          if (bridgeEpoch === 0) bridgeEpoch = 1;
          return { success: true, checkpointBytes: 10, lifecycleEpoch: bridgeEpoch };
        },
        resumeTransfer: async () => {
          bridgeEpoch += 1;
          return { success: true, lifecycleEpoch: bridgeEpoch };
        },
        clearPendingTransferCancel: async () => {},
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [{
    ...makeTask("prog", "transferring"),
    transferredBytes: 10,
    totalBytes: 100,
    speed: 5,
  }]);

  // Double soft-pause then resume (reproduces control-plane vs bridge epoch skew).
  await store.pause("prog");
  await store.pause("prog");
  await store.resume("prog");

  const afterResume = store.getSnapshot().tasks.find((t) => t.id === "prog");
  assert.equal(afterResume?.status, "transferring");
  assert.equal(afterResume?.lifecycleEpoch, bridgeEpoch, "store must track bridge epoch after soft resume");

  // Bridge-shaped progress at bridge epoch must advance the bar (not stale-dropped).
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "prog",
    transferred: 70,
    totalBytes: 100,
    speed: 20,
    lifecycleEpoch: bridgeEpoch,
    lifecycleState: "transferring",
  });
  const afterProgress = store.getSnapshot().tasks.find((t) => t.id === "prog");
  assert.equal(afterProgress?.transferredBytes, 70, "progress after soft resume must move the bar");
  assert.equal(afterProgress?.status, "transferring");
  assert.ok((afterProgress?.speed ?? 0) > 0);
});

test("directory soft resume then new/queued child progress at bridge epoch 0 advances (no dual-epoch freeze)", async (t) => {
  const {
    registerTransferWalk,
    unregisterTransferWalk,
    resetTransferWalkRegistryForTests,
  } = await import("./sftp/transferWalkRegistry");
  const { resetTransferPauseLatchesForTests } = await import("./sftp/transferPauseLatch");
  const { resetTransferControlEpochsForTests } = await import("./sftp/transferControlEpoch");
  resetTransferWalkRegistryForTests();
  resetTransferPauseLatchesForTests();
  resetTransferControlEpochsForTests();
  registerTransferWalk("dir-prog");
  t.after(() => {
    unregisterTransferWalk("dir-prog");
    resetTransferWalkRegistryForTests();
    resetTransferPauseLatchesForTests();
    resetTransferControlEpochsForTests();
  });

  let liveEpoch = 0;
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        pauseTransfer: async (id: string) => {
          if (id === "live-child") {
            liveEpoch = Math.max(liveEpoch, 1);
            return { success: true, checkpointBytes: 50, lifecycleEpoch: liveEpoch };
          }
          return { success: false, reason: "not active" };
        },
        resumeTransfer: async (id: string) => {
          if (id === "live-child") {
            liveEpoch += 1;
            return { success: true, lifecycleEpoch: liveEpoch };
          }
          return { success: false, reason: "not active" };
        },
        clearPendingTransferCancel: async () => {},
      },
    },
  });

  const store = createSftpTransferCenterStore();
  store.publishOwner("panel-a", [
    {
      ...makeTask("dir-prog", "transferring"),
      isDirectory: true,
      progressMode: "files",
      totalBytes: 3,
      transferredBytes: 1,
      ownerId: "panel-a",
    },
    {
      ...makeTask("live-child", "transferring"),
      parentTaskId: "dir-prog",
      transferredBytes: 50,
      totalBytes: 200,
      ownerId: "panel-a",
    },
    {
      ...makeTask("queued-child", "queued"),
      parentTaskId: "dir-prog",
      transferredBytes: 0,
      totalBytes: 100,
      ownerId: "panel-a",
    },
  ]);

  await store.pause("dir-prog");
  await store.resume("dir-prog");

  const afterResume = store.getSnapshot().tasks;
  assert.equal(afterResume.find((t) => t.id === "live-child")?.lifecycleEpoch, liveEpoch);
  assert.equal(
    afterResume.find((t) => t.id === "queued-child")?.lifecycleEpoch,
    undefined,
    "queued sibling must not inherit live-child resume epoch",
  );

  // Simulate next file arming at bridge epoch 0 (startStreamTransfer default).
  store.upsertTasks([{
    ...makeTask("new-child", "transferring"),
    parentTaskId: "dir-prog",
    transferredBytes: 0,
    totalBytes: 80,
    lifecycleEpoch: undefined,
    ownerId: "panel-a",
  }]);

  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "new-child",
    transferred: 40,
    totalBytes: 80,
    speed: 12,
    lifecycleEpoch: 0,
    lifecycleState: "transferring",
  });
  const newChild = store.getSnapshot().tasks.find((t) => t.id === "new-child");
  assert.equal(newChild?.transferredBytes, 40, "new child progress at bridge epoch 0 must advance");
  assert.equal(newChild?.status, "transferring");

  // Queued sibling also starts a stream at epoch 0 after resume.
  store.ingestBackgroundEvent({
    type: "progress",
    transferId: "queued-child",
    transferred: 25,
    totalBytes: 100,
    speed: 8,
    lifecycleEpoch: 0,
    lifecycleState: "transferring",
  });
  // Soft resume left queued status; progress with transferring lifecycle should open bar.
  const queued = store.getSnapshot().tasks.find((t) => t.id === "queued-child");
  assert.equal(queued?.transferredBytes, 25, "queued sibling progress at epoch 0 must not be stale-dropped");
});

test("large directory progress coalesces synchronous persistence work", async () => {
  let writes = 0;
  let writtenCharacters = 0;
  const store = createSftpTransferCenterStore({
    read: () => null,
    write(value) {
      writes += 1;
      writtenCharacters += value.length;
    },
  });
  const parent = {
    ...makeTask("large-folder"),
    isDirectory: true,
    progressMode: "files" as const,
    totalBytes: 2_000,
    transferredBytes: 0,
  };
  let tasks: TransferTask[] = [
    parent,
    ...Array.from({ length: 2_000 }, (_, index) => ({
      ...makeTask(`large-child-${index}`, index < 1_000 ? "completed" : "queued"),
      parentTaskId: parent.id,
      startTime: index + 2,
    })),
  ];

  store.publishOwner("panel-a", tasks);
  const oneSnapshotCharacters = writtenCharacters;
  for (let tick = 1; tick <= 24; tick += 1) {
    tasks = tasks.map((task) => task.id === parent.id
      ? { ...task, transferredBytes: 1_000 + tick }
      : task);
    store.publishOwner("panel-a", tasks);
  }

  await new Promise((resolve) => setTimeout(resolve, 300));
  assert.ok(writes <= 3, `expected at most 3 storage writes, got ${writes}`);
  assert.ok(
    writtenCharacters <= oneSnapshotCharacters * 3,
    `expected bounded persistence volume, wrote ${writtenCharacters} characters for a ${oneSnapshotCharacters}-character snapshot`,
  );
});

test("completed directory history is pruned with one owner update and no reentrant dismiss storm", () => {
  const store = createSftpTransferCenterStore();
  const now = Date.now();
  const parent = {
    ...makeTask("finished-folder"),
    isDirectory: true,
    progressMode: "files" as const,
    totalBytes: 250,
    transferredBytes: 250,
  };
  let ownerTasks: TransferTask[] = [
    parent,
    ...Array.from({ length: 250 }, (_, index) => ({
      ...makeTask(`finished-child-${index}`, "completed"),
      parentTaskId: parent.id,
      startTime: index + 2,
      endTime: now - index,
    })),
  ];
  let individualDismisses = 0;
  let batchDismisses = 0;
  let publishDepth = 0;
  let maxPublishDepth = 0;
  const republish = () => {
    publishDepth += 1;
    maxPublishDepth = Math.max(maxPublishDepth, publishDepth);
    store.publishOwner("panel-a", ownerTasks);
    publishDepth -= 1;
  };
  const controls = {
    pause: async () => {},
    resume: async () => {},
    cancel: async () => {},
    retry: async () => {},
    prioritize: async () => {},
    dismiss(id: string) {
      individualDismisses += 1;
      ownerTasks = ownerTasks.filter((task) => task.id !== id && task.parentTaskId !== id);
      republish();
    },
    dismissMany(ids: readonly string[]) {
      batchDismisses += 1;
      const removing = new Set(ids);
      ownerTasks = ownerTasks.filter((task) => !removing.has(task.id) && !removing.has(task.parentTaskId ?? ""));
      republish();
    },
  };
  store.registerOwner("panel-a", controls);

  republish();
  ownerTasks = ownerTasks.map((task) => task.id === parent.id
    ? { ...task, status: "completed", endTime: now }
    : task);
  republish();

  assert.equal(individualDismisses, 0);
  assert.equal(batchDismisses, 1);
  assert.equal(maxPublishDepth, 2);
  assert.ok(store.getSnapshot().tasks.length <= 200);
});

test("storage exhaustion cannot escape into the renderer update path", () => {
  const store = createSftpTransferCenterStore({
    read: () => null,
    write() {
      throw new DOMException("quota exceeded", "QuotaExceededError");
    },
  });

  assert.doesNotThrow(() => {
    store.publishOwner("panel-a", [makeTask("quota-safe")]);
  });
  assert.equal(store.getSnapshot().tasks[0]?.id, "quota-safe");
});
