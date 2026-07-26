import assert from "node:assert/strict";
import test from "node:test";

import type { TransferTask } from "../../../domain/models";
import {
  softPauseTransfer,
  softResumeTransfer,
  type TransferControlHost,
} from "./globalSftpTransferControl";
import {
  isTransferPauseLatched,
  resetTransferPauseLatchesForTests,
} from "./transferPauseLatch";
import {
  registerTransferWalk,
  resetTransferWalkRegistryForTests,
  unregisterTransferWalk,
} from "./transferWalkRegistry";

function makeTask(id: string, status: TransferTask["status"] = "transferring"): TransferTask {
  return {
    id,
    fileName: `${id}.bin`,
    sourcePath: `/src/${id}`,
    targetPath: `/dst/${id}`,
    sourceConnectionId: "local",
    targetConnectionId: "remote",
    direction: "upload",
    status,
    totalBytes: 100,
    transferredBytes: 10,
    speed: 1,
    startTime: 1,
    isDirectory: false,
    resumable: true,
  };
}

function createHost(initial: TransferTask[], bridge?: TransferControlHost["getBridge"]): {
  host: TransferControlHost;
  getTasks: () => TransferTask[];
} {
  let tasks = initial.map((t) => ({ ...t }));
  return {
    getTasks: () => tasks,
    host: {
      getTasks: () => tasks,
      setTasks: (next) => { tasks = next; },
      getBridge: bridge ?? (() => undefined),
    },
  };
}

test("softPauseTransfer latches and paints paused for a live directory walk without a panel", async () => {
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("dir");
  const { host, getTasks } = createHost([
    { ...makeTask("dir"), isDirectory: true, progressMode: "files", totalBytes: 5, transferredBytes: 1 },
    { ...makeTask("c1"), parentTaskId: "dir" },
  ]);

  const outcome = await softPauseTransfer(host, "dir");
  assert.equal(outcome, "paused");
  assert.equal(getTasks().find((t) => t.id === "dir")?.status, "paused");
  assert.equal(isTransferPauseLatched("dir"), true);

  unregisterTransferWalk("dir");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
});

test("softResumeTransfer with live walk paints transferring without requiring bridge success", async () => {
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("dir");
  const { host, getTasks } = createHost([
    { ...makeTask("dir", "paused"), isDirectory: true, progressMode: "files", totalBytes: 5, transferredBytes: 1, speed: 0 },
  ]);

  const handled = await softResumeTransfer(host, "dir");
  assert.equal(handled, true);
  assert.equal(getTasks().find((t) => t.id === "dir")?.status, "transferring");
  assert.equal(isTransferPauseLatched("dir"), false);

  unregisterTransferWalk("dir");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
});

test("single-file softResume with live walk + bridge resume fail returns false (no false transferring)", async () => {
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("file-1");
  const { host, getTasks } = createHost(
    [{ ...makeTask("file-1", "paused"), transferredBytes: 10, speed: 0 }],
    () => ({
      resumeTransfer: async () => ({ success: false, reason: "not active" }),
      pauseTransfer: async () => ({ success: true, checkpointBytes: 10, lifecycleEpoch: 1 }),
    }),
  );

  const handled = await softResumeTransfer(host, "file-1");
  assert.equal(handled, false, "must not soft-succeed when every bridge resume fails");
  // Must not paint transferring — hard reconnect path must remain available.
  assert.notEqual(getTasks().find((t) => t.id === "file-1")?.status, "transferring");

  unregisterTransferWalk("file-1");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
});

test("directory softResume with live walk and no bridge success still rejoins", async () => {
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("dir-2");
  const { host, getTasks } = createHost(
    [{ ...makeTask("dir-2", "paused"), isDirectory: true, progressMode: "files", totalBytes: 3, transferredBytes: 1, speed: 0 }],
    () => ({
      resumeTransfer: async () => ({ success: false, reason: "not active" }),
    }),
  );

  const handled = await softResumeTransfer(host, "dir-2");
  assert.equal(handled, true);
  assert.equal(getTasks().find((t) => t.id === "dir-2")?.status, "transferring");
  // lifecycleEpoch cleared so child stream progress is accepted
  assert.equal(getTasks().find((t) => t.id === "dir-2")?.lifecycleEpoch, undefined);

  unregisterTransferWalk("dir-2");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
});

test("soft pause/resume stamps bridge lifecycleEpoch so later progress is not stale-dropped", async () => {
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  let bridgeEpoch = 0;
  const { host, getTasks } = createHost(
    [makeTask("stream", "transferring")],
    () => ({
      pauseTransfer: async () => {
        bridgeEpoch += 1;
        return { success: true, checkpointBytes: 10, lifecycleEpoch: bridgeEpoch };
      },
      resumeTransfer: async () => {
        bridgeEpoch += 1;
        return { success: true, lifecycleEpoch: bridgeEpoch };
      },
    }),
  );

  // Double soft-pause (second hits already-paused — still returns bridge epoch).
  await softPauseTransfer(host, "stream");
  await softPauseTransfer(host, "stream");
  const pausedEpoch = getTasks().find((t) => t.id === "stream")?.lifecycleEpoch;
  assert.ok(typeof pausedEpoch === "number" && pausedEpoch > 0);

  const handled = await softResumeTransfer(host, "stream");
  assert.equal(handled, true);
  const resumed = getTasks().find((t) => t.id === "stream");
  assert.equal(resumed?.status, "transferring");
  // Bridge-aligned: must equal last resume bridge epoch, not control-plane bumps.
  assert.equal(resumed?.lifecycleEpoch, bridgeEpoch);

  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
});

test("directory softResume stamps bridge epoch only on successIds; queued siblings clear epoch", async () => {
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
  registerTransferWalk("folder-mix");
  const { host, getTasks } = createHost(
    [
      { ...makeTask("folder-mix", "paused"), isDirectory: true, progressMode: "files", totalBytes: 3, transferredBytes: 1, speed: 0, lifecycleEpoch: 9 },
      { ...makeTask("live-child", "paused"), parentTaskId: "folder-mix", transferredBytes: 50, speed: 0, lifecycleEpoch: 9 },
      { ...makeTask("queued-child", "queued"), parentTaskId: "folder-mix", transferredBytes: 0, speed: 0, lifecycleEpoch: 9 },
    ],
    () => ({
      resumeTransfer: async (id: string) => {
        if (id === "live-child") return { success: true, lifecycleEpoch: 4 };
        return { success: false, reason: "not active" };
      },
    }),
  );

  const handled = await softResumeTransfer(host, "folder-mix");
  assert.equal(handled, true);
  const parent = getTasks().find((t) => t.id === "folder-mix");
  const live = getTasks().find((t) => t.id === "live-child");
  const queued = getTasks().find((t) => t.id === "queued-child");
  assert.equal(parent?.status, "transferring");
  assert.equal(parent?.lifecycleEpoch, 4, "parent gets max of successful child bridge epochs");
  assert.equal(live?.status, "transferring");
  assert.equal(live?.lifecycleEpoch, 4, "resumed child keeps its bridge epoch");
  assert.equal(queued?.status, "queued");
  assert.equal(queued?.lifecycleEpoch, undefined, "non-resumed sibling must not inherit parent resume epoch");

  unregisterTransferWalk("folder-mix");
  resetTransferPauseLatchesForTests();
  resetTransferWalkRegistryForTests();
});
