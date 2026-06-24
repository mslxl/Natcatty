const test = require("node:test");
const assert = require("node:assert/strict");

const {
  createAppLockController,
  createAppLockRuntimeBridge,
} = require("./appLockRuntimeBridge.cjs");
const {
  createAppLockPasswordVerifier,
  createAppLockSettingsStore,
} = require("./appLockSettingsStore.cjs");

function withPatchedTimers(run) {
  const originalSetTimeout = global.setTimeout;
  const originalClearTimeout = global.clearTimeout;
  let nextTimerId = 1;
  const timers = new Map();

  global.setTimeout = (fn, delay = 0, ...args) => {
    const id = nextTimerId++;
    timers.set(id, {
      dueAt: Date.now() + Math.max(0, Number(delay) || 0),
      fn: () => fn(...args),
    });
    return id;
  };

  global.clearTimeout = (id) => {
    timers.delete(id);
  };

  const flushNextTimer = () => {
    const nextEntry = timers.entries().next().value;
    if (!nextEntry) return false;
    const [id, timer] = nextEntry;
    timers.delete(id);
    timer.fn();
    return true;
  };
  const flushDueTimers = () => {
    let flushed = 0;
    for (const [id, timer] of [...timers.entries()]) {
      if (timer.dueAt > Date.now()) continue;
      timers.delete(id);
      timer.fn();
      flushed += 1;
    }
    return flushed;
  };

  const getPendingTimerCount = () => timers.size;

  return Promise.resolve()
    .then(() => run({ flushNextTimer, flushDueTimers, getPendingTimerCount }))
    .finally(() => {
      global.setTimeout = originalSetTimeout;
      global.clearTimeout = originalClearTimeout;
    });
}

function withPatchedDateNow(initialValue, run) {
  const originalDateNow = Date.now;
  let currentValue = initialValue;

  Date.now = () => currentValue;

  return Promise.resolve()
    .then(() =>
      run({
        setNow(nextValue) {
          currentValue = nextValue;
        },
      }),
    )
    .finally(() => {
      Date.now = originalDateNow;
    });
}

test("runtime bridge can initialize locked at startup", () => {
  const bridge = createAppLockRuntimeBridge();
  bridge.initialize({
    locked: true,
    reason: "startup",
    lastActivityAt: 1000,
  });
  const state = bridge.getState();
  assert.equal(state.initialized, true);
  assert.equal(state.locked, true);
  assert.equal(state.reason, "startup");
  assert.equal(state.lastActivityAt, 1000);
});

test("runtime bridge records shared activity timestamps", () => {
  const bridge = createAppLockRuntimeBridge();
  bridge.initialize({ locked: false, reason: null, lastActivityAt: 1000 });
  bridge.recordActivity(2500);
  assert.equal(bridge.getState().lastActivityAt, 2500);
});

test("runtime bridge reschedules the shared idle timer after activity", async () => {
  await withPatchedTimers(async ({ flushNextTimer, getPendingTimerCount }) => {
    await withPatchedDateNow(1000, async ({ setNow }) => {
      const bridge = createAppLockRuntimeBridge();
      const idleLocks = [];

      bridge.initialize({ locked: false, reason: null, lastActivityAt: 1000 });
      bridge.scheduleIdleTimer({
        timeoutMinutes: 1,
        canLock: () => true,
        onIdleLock: (state) => idleLocks.push(state),
      });

      assert.equal(getPendingTimerCount(), 1);

      setNow(30000);
      bridge.recordActivity(30000);
      assert.equal(getPendingTimerCount(), 1);

      setNow(61000);
      assert.equal(flushNextTimer(), true);
      assert.equal(bridge.getState().locked, false);
      assert.equal(idleLocks.length, 0);
      assert.equal(getPendingTimerCount(), 1);

      setNow(90000);
      assert.equal(flushNextTimer(), true);
      assert.equal(bridge.getState().locked, true);
      assert.equal(bridge.getState().reason, "idle");
      assert.equal(idleLocks.length, 1);
      assert.equal(getPendingTimerCount(), 0);
    });
  });
});

test("runtime bridge does not schedule idle timer when timeout is disabled", async () => {
  await withPatchedTimers(async ({ getPendingTimerCount }) => {
    const bridge = createAppLockRuntimeBridge();

    bridge.initialize({ locked: false, reason: null, lastActivityAt: 1000 });
    bridge.scheduleIdleTimer({
      timeoutMinutes: 0,
      canLock: () => true,
      onIdleLock: () => {
        throw new Error("idle lock should not run");
      },
    });

    assert.equal(getPendingTimerCount(), 0);
    assert.equal(bridge.getState().locked, false);
  });
});

test("runtime bridge notifies subscribers on lock state changes", () => {
  const bridge = createAppLockRuntimeBridge();
  const snapshots = [];
  const unsubscribe = bridge.subscribe((state) => {
    snapshots.push(state);
  });

  bridge.initialize({ locked: false, reason: null, lastActivityAt: 1000 });
  bridge.lock("manual");
  bridge.unlock();
  unsubscribe();
  bridge.lock("manual");

  assert.deepEqual(
    snapshots.map((state) => [state.locked, state.reason]),
    [
      [false, null],
      [true, "manual"],
      [false, null],
    ],
  );
});

test("runtime bridge clearIdleTimer cancels pending idle locks", async () => {
  await withPatchedTimers(async ({ flushNextTimer, getPendingTimerCount }) => {
    const bridge = createAppLockRuntimeBridge();
    let locked = false;

    bridge.initialize({ locked: false, reason: null, lastActivityAt: 1000 });
    bridge.scheduleIdleTimer({
      timeoutMinutes: 1,
      canLock: () => true,
      onIdleLock: () => {
        locked = true;
      },
    });

    assert.equal(getPendingTimerCount(), 1);
    bridge.clearIdleTimer();
    assert.equal(getPendingTimerCount(), 0);
    assert.equal(flushNextTimer(), false);
    assert.equal(locked, false);
  });
});

test("runtime bridge unreferences the shared idle timer handle when supported", () => {
  const originalSetTimeout = global.setTimeout;
  const originalClearTimeout = global.clearTimeout;
  let unrefCalled = false;

  global.setTimeout = (fn) => {
    void fn;
    return {
      unref() {
        unrefCalled = true;
      },
    };
  };
  global.clearTimeout = () => {};

  try {
    const bridge = createAppLockRuntimeBridge();
    bridge.initialize({ locked: false, reason: null, lastActivityAt: 1000 });
    bridge.scheduleIdleTimer({
      timeoutMinutes: 1,
      canLock: () => true,
      onIdleLock: () => {},
    });

    assert.equal(unrefCalled, true);
  } finally {
    global.setTimeout = originalSetTimeout;
    global.clearTimeout = originalClearTimeout;
  }
});

function createWindowCollector(name) {
  const sent = [];
  return {
    name,
    sent,
    isDestroyed() {
      return false;
    },
    webContents: {
      id: `${name}-${Math.random()}`,
      isDestroyed() {
        return false;
      },
      send(channel, payload) {
        sent.push([channel, payload]);
      },
    },
  };
}

function createIpcMainHarness() {
  const handlers = new Map();
  return {
    handlers,
    handle(channel, handler) {
      handlers.set(channel, handler);
    },
  };
}

async function createControllerHarness() {
  const settingsStore = createAppLockSettingsStore({
    filePath: "/tmp/app-lock-settings.json",
    readFile: async () => {
      const err = new Error("ENOENT");
      err.code = "ENOENT";
      throw err;
    },
    writeFile: async () => {},
  });
  await settingsStore.load();

  const runtimeBridge = createAppLockRuntimeBridge();
  runtimeBridge.initialize({
    locked: true,
    reason: "startup",
    lastActivityAt: 1000,
  });

  const mainWindowA = createWindowCollector("main-a");
  const mainWindowB = createWindowCollector("main-b");
  const settingsWindow = createWindowCollector("settings");
  const trayPanelWindow = createWindowCollector("tray");
  const popupWindowA = createWindowCollector("popup-a");
  const popupWindowB = createWindowCollector("popup-b");
  const systemAuthCalls = {
    status: 0,
    unlock: 0,
  };
  const systemAuthBridge = {
    async getStatus() {
      systemAuthCalls.status += 1;
      return {
        supported: true,
        available: true,
        platform: "darwin",
        label: "Touch ID",
        reason: null,
      };
    },
    async requestUnlock() {
      systemAuthCalls.unlock += 1;
      return { ok: true };
    },
  };

  const controller = createAppLockController({
    settingsStore,
    runtimeBridge,
    systemAuthBridge,
    getMainWindows: () => [mainWindowA, mainWindowB],
    getSettingsWindow: () => settingsWindow,
    getTrayPanelWindow: () => trayPanelWindow,
    getTerminalPopupWindows: () => [popupWindowA, popupWindowB],
  });

  return {
    controller,
    runtimeBridge,
    settingsStore,
    systemAuthBridge,
    systemAuthCalls,
    windows: [
      mainWindowA,
      mainWindowB,
      settingsWindow,
      trayPanelWindow,
      popupWindowA,
      popupWindowB,
    ],
  };
}

test("system unlock setting confirms with system auth instead of current password when enabling", async () => {
  const { controller, systemAuthCalls } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();

  const saved = await controller.setSystemUnlockEnabled({ enabled: true });
  assert.equal(saved.systemUnlockEnabled, true);
  assert.equal(systemAuthCalls.unlock, 1);
});

test("system unlock auto prompt setting does not request system auth when already enabled", async () => {
  const { controller, systemAuthCalls } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();

  await controller.setSystemUnlockEnabled({ enabled: true });
  assert.equal(systemAuthCalls.unlock, 1);

  const enabledAutoPrompt = await controller.setSystemUnlockEnabled({
    enabled: true,
    autoPromptEnabled: true,
  });
  assert.equal(enabledAutoPrompt.systemUnlockEnabled, true);
  assert.equal(enabledAutoPrompt.systemUnlockAutoPromptEnabled, true);
  assert.equal(systemAuthCalls.unlock, 1);

  const disabledAutoPrompt = await controller.setSystemUnlockEnabled({
    enabled: true,
    autoPromptEnabled: false,
  });
  assert.equal(disabledAutoPrompt.systemUnlockEnabled, true);
  assert.equal(disabledAutoPrompt.systemUnlockAutoPromptEnabled, false);
  assert.equal(systemAuthCalls.unlock, 1);
});

test("system unlock setting cannot be disabled without password while locked", async () => {
  const { controller } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();
  await controller.setSystemUnlockEnabled({ enabled: true });
  controller.setLocked("manual");

  assert.deepEqual(
    await controller.setSystemUnlockEnabled({ enabled: false }),
    { ok: false, error: "locked" },
  );

  const saved = await controller.setSystemUnlockEnabled({ enabled: false, currentPassword: "alpha" });
  assert.equal(saved.systemUnlockEnabled, false);
});

test("system unlock succeeds only when enabled and locked", async () => {
  const { controller, runtimeBridge, systemAuthCalls } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();

  assert.deepEqual(await controller.requestSystemUnlock(), { ok: false, error: "disabled" });
  await controller.setSystemUnlockEnabled({ enabled: true });
  assert.deepEqual(await controller.requestSystemUnlock(), { ok: true });
  assert.equal(runtimeBridge.getState().locked, false);
  assert.equal(systemAuthCalls.unlock, 2);
  assert.deepEqual(await controller.requestSystemUnlock(), { ok: false, error: "not-locked" });
});

test("system unlock cancellation preserves locked runtime state", async () => {
  const { controller, runtimeBridge, systemAuthBridge } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();
  await controller.setSystemUnlockEnabled({ enabled: true, currentPassword: "alpha" });
  controller.setLocked("manual");
  systemAuthBridge.requestUnlock = async () => ({ ok: false, error: "cancelled" });

  assert.deepEqual(await controller.requestSystemUnlock(), { ok: false, error: "cancelled" });
  assert.equal(runtimeBridge.getState().locked, true);
});

test("system unlock IPC handlers are registered", async () => {
  const { controller } = await createControllerHarness();
  const ipcMain = createIpcMainHarness();
  controller.registerHandlers(ipcMain);

  assert.equal(ipcMain.handlers.has("netcatty:appLock:getSystemUnlockStatus"), true);
  assert.equal(ipcMain.handlers.has("netcatty:appLock:setSystemUnlockEnabled"), true);
  assert.equal(ipcMain.handlers.has("netcatty:appLock:requestSystemUnlock"), true);
  assert.equal(
    (await ipcMain.handlers.get("netcatty:appLock:getSystemUnlockStatus")()).label,
    "Touch ID",
  );
});

test("unlock request verifies against the latest persisted password verifier", async () => {
  const { controller } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();
  assert.deepEqual(await controller.requestUnlock("alpha"), {
    ok: true,
  });
  controller.setLocked("manual");

  await controller.requestPasswordChange({
    currentPassword: "alpha",
    nextPassword: "bravo",
  });
  controller.setLocked("manual");

  assert.deepEqual(await controller.requestUnlock("alpha"), {
    ok: false,
    error: "incorrect",
  });
  assert.deepEqual(await controller.requestUnlock("bravo"), {
    ok: true,
  });
});

test("stale renderer cannot overwrite the latest verifier with a whole-object settings write", async () => {
  const { controller, settingsStore } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();

  const staleSnapshot = settingsStore.getSnapshot();
  assert.equal(staleSnapshot.enabled, true);
  assert.equal(typeof staleSnapshot.passwordVerifier?.hash, "string");

  await controller.requestPasswordChange({
    currentPassword: "alpha",
    nextPassword: "bravo",
  });

  const freshSnapshot = settingsStore.getSnapshot();
  assert.equal(freshSnapshot.enabled, true);
  assert.notEqual(freshSnapshot.passwordVerifier?.hash, staleSnapshot.passwordVerifier?.hash);

  assert.deepEqual(
    await controller.requestDisable("alpha"),
    { ok: false, error: "incorrect" },
  );

  assert.deepEqual(
    await controller.requestDisable("bravo"),
    {
      enabled: false,
      timeoutMinutes: freshSnapshot.timeoutMinutes,
      systemUnlockEnabled: false,
      systemUnlockAutoPromptEnabled: false,
      passwordVerifier: null,
    },
  );
});

test("runtime state broadcast fans out to all main windows, settings, tray panel, and every popup window", async () => {
  const { controller, windows } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();

  const state = controller.setLocked("manual");
  for (const win of windows) {
    const runtimeMessages = win.sent.filter(([channel]) => channel === "netcatty:appLock:runtimeStateChanged");
    assert.equal(runtimeMessages.length, 1, `${win.name} should receive one runtime broadcast`);
    assert.deepEqual(runtimeMessages[0][1], state);
  }
});

test("unlocking an enabled app schedules the shared idle timer in controller flow", async () => {
  await withPatchedTimers(async ({ getPendingTimerCount }) => {
    const { controller, runtimeBridge } = await createControllerHarness();

    await controller.requestPasswordChange({ nextPassword: "alpha" });
    assert.equal(getPendingTimerCount(), 0);

    await controller.requestEnable();
    assert.equal(getPendingTimerCount(), 0);

    await controller.requestUnlock("alpha");
    assert.equal(getPendingTimerCount(), 1);
    controller.syncIdleTimer();
    await controller.requestDisable("alpha");
    runtimeBridge.clearIdleTimer();
  });
});

test("unlock and activity keep the shared idle timer armed", async () => {
  await withPatchedTimers(async ({ getPendingTimerCount }) => {
    await withPatchedDateNow(1000, async ({ setNow }) => {
      const { controller, runtimeBridge } = await createControllerHarness();

      await controller.requestPasswordChange({ nextPassword: "alpha" });
      await controller.requestEnable();
      controller.setLocked("manual");
      assert.equal(getPendingTimerCount(), 0);

      await controller.requestUnlock("alpha");
      assert.equal(getPendingTimerCount(), 1);

      setNow(5000);
      controller.reportActivity(5000);
      assert.equal(getPendingTimerCount(), 1);
      await controller.requestDisable("alpha");
      runtimeBridge.clearIdleTimer();
    });
  });
});

test("activity reported from any window postpones the shared idle lock", async () => {
  await withPatchedTimers(async ({ flushDueTimers, getPendingTimerCount }) => {
    await withPatchedDateNow(1000, async ({ setNow }) => {
      const { controller, runtimeBridge } = await createControllerHarness();

      await controller.requestPasswordChange({ nextPassword: "alpha" });
      await controller.setTimeoutMinutes(1);
      await controller.requestEnable();
      controller.setLocked("manual");
      await controller.requestUnlock("alpha");
      assert.equal(getPendingTimerCount(), 1);

      setNow(30000);
      controller.reportActivity(30000);
      assert.equal(runtimeBridge.getState().lastActivityAt, 30000);
      assert.equal(getPendingTimerCount(), 1);

      setNow(61000);
      assert.equal(flushDueTimers(), 0);
      assert.equal(runtimeBridge.getState().locked, false);
      assert.equal(getPendingTimerCount(), 1);

      setNow(90000);
      assert.equal(flushDueTimers(), 1);
      assert.equal(runtimeBridge.getState().locked, true);
      assert.equal(runtimeBridge.getState().reason, "idle");
      assert.equal(getPendingTimerCount(), 0);

      runtimeBridge.clearIdleTimer();
    });
  });
});

test("disabling app lock clears the shared idle timer", async () => {
  await withPatchedTimers(async ({ getPendingTimerCount }) => {
    const { controller, runtimeBridge } = await createControllerHarness();

    await controller.requestPasswordChange({ nextPassword: "alpha" });
    await controller.requestEnable();
    await controller.requestUnlock("alpha");
    assert.equal(getPendingTimerCount(), 1);

    await controller.requestDisable("alpha");
    assert.equal(getPendingTimerCount(), 0);
    runtimeBridge.clearIdleTimer();
  });
});

test("disabling app lock removes the saved password verifier", async () => {
  const { controller } = await createControllerHarness();

  const saved = await controller.requestDisable("alpha");

  assert.equal(saved.enabled, false);
  assert.equal(saved.passwordVerifier, null);
});

test("resetting app lock requires the current password before clearing the verifier", async () => {
  const { controller, runtimeBridge } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();
  controller.setLocked("manual");

  assert.deepEqual(
    await controller.requestReset(),
    { ok: false, error: "empty-current" },
  );
  assert.deepEqual(
    await controller.requestReset("wrong"),
    { ok: false, error: "incorrect" },
  );
  assert.equal(controller.getSettings().passwordVerifier !== null, true);
  assert.equal(runtimeBridge.getState().locked, true);
});

test("resetting app lock clears the verifier, unlocks runtime, and broadcasts settings and runtime", async () => {
  const { controller, runtimeBridge, windows } = await createControllerHarness();
  await controller.requestPasswordChange({ nextPassword: "alpha" });
  await controller.requestEnable();
  controller.setLocked("manual");
  for (const win of windows) {
    win.sent.length = 0;
  }

  const saved = await controller.requestReset("alpha");

  assert.equal(saved.enabled, false);
  assert.equal(saved.passwordVerifier, null);
  assert.equal(runtimeBridge.getState().locked, false);
  assert.equal(runtimeBridge.getState().reason, null);
  for (const win of windows) {
    const settingsMessages = win.sent.filter(([channel]) => channel === "netcatty:appLock:settingsChanged");
    const runtimeMessages = win.sent.filter(([channel]) => channel === "netcatty:appLock:runtimeStateChanged");
    assert.equal(settingsMessages.length, 1, `${win.name} should receive one settings broadcast`);
    assert.equal(runtimeMessages.length, 1, `${win.name} should receive one runtime broadcast`);
    assert.equal(settingsMessages[0][1].passwordVerifier, null);
    assert.equal(runtimeMessages[0][1].locked, false);
  }
});

test("creating the first app lock password enables app lock", async () => {
  const { controller } = await createControllerHarness({
    enabled: false,
    passwordVerifier: null,
  });

  const saved = await controller.requestPasswordChange({
    nextPassword: "first secret",
  });

  assert.equal(saved.enabled, true);
  assert.equal(typeof saved.passwordVerifier?.hash, "string");
});
