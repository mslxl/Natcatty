const test = require("node:test");
const assert = require("node:assert/strict");

const {
  emitAppLockReopen,
  handleActivateWithMainWindow,
  handleBeforeQuit,
  shouldBackgroundLockOnHide,
  shouldCommitQuitWithoutDirtyCheck,
} = require("./appLockLifecycle.cjs");

test("shouldBackgroundLockOnHide locks only when app lock controller exists", () => {
  assert.equal(shouldBackgroundLockOnHide({ setLocked: () => {} }), true);
  assert.equal(shouldBackgroundLockOnHide(null), false);
});

test("shouldCommitQuitWithoutDirtyCheck commits when no reachable main windows exist", () => {
  assert.equal(
    shouldCommitQuitWithoutDirtyCheck({
      reachableMainWindows: [],
      queryableWebContents: [{ id: 1 }],
    }),
    true,
  );
});

test("shouldCommitQuitWithoutDirtyCheck commits when no queryable webContents exist", () => {
  assert.equal(
    shouldCommitQuitWithoutDirtyCheck({
      reachableMainWindows: [{ id: 1 }],
      queryableWebContents: [],
    }),
    true,
  );
});

test("shouldCommitQuitWithoutDirtyCheck waits for dirty check when reachable renderers exist", () => {
  assert.equal(
    shouldCommitQuitWithoutDirtyCheck({
      reachableMainWindows: [{ id: 1 }],
      queryableWebContents: [{ id: 1 }],
    }),
    false,
  );
});

test("emitAppLockReopen sends reopen once per live unique webContents", () => {
  const sent = [];
  const sharedWebContents = {
    id: 7,
    send(channel) {
      sent.push(channel);
    },
  };
  const windows = [
    null,
    { isDestroyed: () => true, webContents: sharedWebContents },
    { isDestroyed: () => false, webContents: sharedWebContents },
    { isDestroyed: () => false, webContents: sharedWebContents },
    {
      isDestroyed: () => false,
      webContents: {
        id: 8,
        send(channel) {
          sent.push(channel);
        },
      },
    },
  ];

  emitAppLockReopen(windows);

  assert.deepEqual(sent, [
    "netcatty:app-lock:reopen",
    "netcatty:app-lock:reopen",
  ]);
});

test("handleActivateWithMainWindow shows and focuses the main window, then emits reopen", () => {
  const calls = [];
  const mainWindow = {
    isDestroyed: () => false,
    isMinimized: () => true,
    restore() {
      calls.push("restore");
    },
    show() {
      calls.push("show");
    },
    focus() {
      calls.push("focus");
    },
    webContents: {
      id: 99,
      send(channel) {
        calls.push(`send:${channel}`);
      },
    },
  };
  const app = {
    focus() {
      calls.push("app.focus");
    },
  };
  const globalShortcutBridge = {
    clearPendingFullscreenHide(win) {
      calls.push(`clear:${win === mainWindow}`);
    },
  };

  const handled = handleActivateWithMainWindow({
    app,
    mainWindow,
    globalShortcutBridge,
    reopenWindows: [mainWindow],
  });

  assert.equal(handled, true);
  assert.deepEqual(calls, [
    "clear:true",
    "restore",
    "show",
    "focus",
    "send:netcatty:app-lock:reopen",
    "app.focus",
  ]);
});

test("handleActivateWithMainWindow refuses crashed main windows so activate can recreate them", () => {
  const calls = [];
  const mainWindow = {
    isDestroyed: () => false,
    destroy() {
      calls.push("destroy");
    },
    webContents: {
      isCrashed: () => true,
      id: 99,
      send(channel) {
        calls.push(`send:${channel}`);
      },
    },
  };

  const handled = handleActivateWithMainWindow({
    app: {
      focus() {
        calls.push("app.focus");
      },
    },
    mainWindow,
    globalShortcutBridge: {
      clearPendingFullscreenHide() {
        calls.push("clear");
      },
    },
    reopenWindows: [mainWindow],
  });

  assert.equal(handled, false);
  assert.deepEqual(calls, ["destroy"]);
});

test("handleBeforeQuit commits quit after clean dirty-editor check and locks background", async () => {
  const calls = [];
  const mainWindow = {
    isDestroyed: () => false,
    isVisible: () => true,
    isMinimized: () => false,
    webContents: {
      isDestroyed: () => false,
      isCrashed: () => false,
      id: 1,
    },
  };
  const event = {
    preventDefault() {
      calls.push("preventDefault");
    },
  };

  await handleBeforeQuit({
    event,
    mainWindows: [mainWindow],
    queryDirtyEditors: async () => false,
    appLockController: {
      setLocked(reason) {
        calls.push(`lock:${reason}`);
      },
    },
    windowManager: {
      setIsQuitting(value) {
        calls.push(`setIsQuitting:${value}`);
      },
      isQuittingForUpdate() {
        return false;
      },
    },
    app: {
      quit() {
        calls.push("app.quit");
      },
    },
    ipcMain: {},
    quitConfirmed: false,
    quitGuardChannelBusy: false,
    timeoutMs: 10,
    setQuitGuardChannelBusy(value) {
      calls.push(`quitGuardBusy:${value}`);
    },
    setQuitConfirmed(value) {
      calls.push(`quitConfirmed:${value}`);
    },
  });

  assert.deepEqual(calls, [
    "quitGuardBusy:true",
    "preventDefault",
    "quitGuardBusy:false",
    "lock:background",
    "setIsQuitting:true",
    "quitConfirmed:true",
    "app.quit",
  ]);
});

test("handleBeforeQuit cancels quit without locking when dirty editors exist", async () => {
  const calls = [];
  const mainWindow = {
    isDestroyed: () => false,
    isVisible: () => true,
    isMinimized: () => false,
    webContents: {
      isDestroyed: () => false,
      isCrashed: () => false,
      id: 1,
    },
  };
  const event = {
    preventDefault() {
      calls.push("preventDefault");
    },
  };

  await handleBeforeQuit({
    event,
    mainWindows: [mainWindow],
    queryDirtyEditors: async () => true,
    appLockController: {
      setLocked(reason) {
        calls.push(`lock:${reason}`);
      },
    },
    windowManager: {
      setIsQuitting(value) {
        calls.push(`setIsQuitting:${value}`);
      },
      isQuittingForUpdate() {
        return true;
      },
      setQuittingForUpdate(value) {
        calls.push(`setQuittingForUpdate:${value}`);
      },
    },
    app: {
      quit() {
        calls.push("app.quit");
      },
    },
    ipcMain: {},
    quitConfirmed: false,
    quitGuardChannelBusy: false,
    timeoutMs: 10,
    setQuitGuardChannelBusy(value) {
      calls.push(`quitGuardBusy:${value}`);
    },
    setQuitConfirmed(value) {
      calls.push(`quitConfirmed:${value}`);
    },
  });

  assert.deepEqual(calls, [
    "quitGuardBusy:true",
    "preventDefault",
    "quitGuardBusy:false",
    "setQuittingForUpdate:false",
  ]);
});
