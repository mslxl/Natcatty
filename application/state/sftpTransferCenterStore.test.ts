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

test("store routes controls to the task owner", async () => {
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

  await store.pause("a");
  await store.resume("a");
  await store.cancel("a");
  await store.retry("a");
  await store.prioritize("a");
  store.dismiss("a");

  assert.deepEqual(calls, [
    "pause:a",
    "resume:a",
    "cancel:a",
    "retry:a",
    "prioritize:a",
    "dismiss:a",
  ]);
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

test("resume still uses the live owner when canAdopt is false", async () => {
  // Downloads often have only the remote pane open, so canAdopt (which wants
  // both endpoints) returns false. Pause/resume must still unpause the live
  // backend transfer through the owning panel instead of failing with
  // "server no longer exists".
  const calls: string[] = [];
  const store = createSftpTransferCenterStore();
  store.registerOwner("panel-a", {
    pause: async (id) => { calls.push(`pause:${id}`); },
    resume: async (id) => { calls.push(`resume:${id}`); },
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

  assert.deepEqual(calls, ["resume:download-paused"]);
  assert.equal(store.getSnapshot().tasks[0]?.status, "paused");
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
