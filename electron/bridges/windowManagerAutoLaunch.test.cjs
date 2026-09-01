const test = require("node:test");
const assert = require("node:assert/strict");

const { createMainWindowApi } = require("./windowManager/mainWindow.cjs");

class BrowserWindowStub {
  constructor() {
    this.webContents = {
      id: 1,
      on() {},
      once() {},
      isDestroyed() {
        return false;
      },
      isCrashed() {
        return false;
      },
      setIgnoreMenuShortcuts() {},
      setWindowOpenHandler() {},
      openDevTools() {},
      getZoomFactor() {
        return 1;
      },
      setZoomFactor() {},
    };
  }

  on() {}
  once() {}
  isDestroyed() { return false; }
  isMaximized() { return false; }
  isFullScreen() { return false; }
  getBounds() { return { x: 0, y: 0, width: 1400, height: 900 }; }
  setBackgroundColor() {}
  setOpacity() {}
  async loadURL() {}
  close() {}
}

function createApi({ setupDeferredShow, getGlobalShortcutBridge } = {}) {
  return createMainWindowApi({
    mainWindow: null,
    electronApp: null,
    currentTheme: "light",
    isQuitting: false,
    pendingWindowStateWrite: null,
    queuedWindowState: null,
    windowStateCloseRequested: false,
    DEFAULT_WINDOW_WIDTH: 1400,
    DEFAULT_WINDOW_HEIGHT: 900,
    MIN_WINDOW_WIDTH: 1100,
    MIN_WINDOW_HEIGHT: 640,
    V8_CACHE_OPTIONS: "bypassHeatCheck",
    THEME_COLORS: { light: { background: "#fff" } },
    unhealthyWebContentsIds: new Set(),
    rendererReadySeenByWebContentsId: new Set(),
    __dirname,
    URL,
    require,
    console,
    setTimeout,
    clearTimeout,
    getGlobalShortcutBridge: getGlobalShortcutBridge || (() => ({ handleWindowClose: () => false })),
    debugLog() {},
    resolveFrontendBackgroundColor() { return null; },
    loadWindowState() { return null; },
    getDevRendererBaseUrl(url) { return url; },
    getWindowBoundsState() { return null; },
    queueWindowStateSave() {},
    saveWindowStateSync() {},
    setupDeferredShow: setupDeferredShow || (() => {}),
    createExternalOnlyWindowOpenHandler() { return {}; },
    createAppWindowOpenHandler() { return {}; },
    attachOAuthLoadingOverlay() {},
    registerWindowHandlers() {},
    requestWindowCommandClose() { return true; },
    shouldCloseWindowFromInput() { return false; },
    applyWindowOpacityToWindow() {},
    closeSettingsWindow() {},
    hideSettingsWindow() {},
  });
}

async function createWindowWith(api, { startHidden, onRegisterBridge } = {}) {
  return api.createWindow(
    {
      BrowserWindow: BrowserWindowStub,
      nativeTheme: {},
      app: {},
      screen: {},
      shell: {},
      ipcMain: {},
    },
    {
      preload: "/tmp/preload.cjs",
      devServerUrl: "http://localhost:5173",
      isDev: true,
      appIcon: null,
      isMac: false,
      electronDir: __dirname,
      onRegisterBridge,
      startHidden,
    },
  );
}

test("createWindow forwards startHidden to setupDeferredShow", async () => {
  const deferredShowCalls = [];
  const api = createApi({
    setupDeferredShow: (win, options) => { deferredShowCalls.push(options); },
  });

  await createWindowWith(api, { startHidden: true });

  assert.equal(deferredShowCalls.length, 1);
  assert.equal(deferredShowCalls[0].startHidden, true);
});

test("createWindow defaults startHidden to false when omitted", async () => {
  const deferredShowCalls = [];
  const api = createApi({
    setupDeferredShow: (win, options) => { deferredShowCalls.push(options); },
  });

  await createWindowWith(api, {});

  assert.equal(deferredShowCalls.length, 1);
  assert.equal(deferredShowCalls[0].startHidden, false);
});

test("createWindow(startHidden) ensures a tray exists right after bridges register", async () => {
  const callOrder = [];
  const api = createApi({
    getGlobalShortcutBridge: () => ({
      handleWindowClose: () => false,
      createTray: () => { callOrder.push("createTray"); },
    }),
  });

  await createWindowWith(api, {
    startHidden: true,
    onRegisterBridge: () => { callOrder.push("onRegisterBridge"); },
  });

  assert.deepEqual(callOrder, ["onRegisterBridge", "createTray"]);
});

test("createWindow(startHidden) tolerates a tray creation failure without throwing", async () => {
  const api = createApi({
    getGlobalShortcutBridge: () => ({
      handleWindowClose: () => false,
      createTray: () => { throw new Error("electronModule not ready"); },
    }),
  });

  await assert.doesNotReject(() => createWindowWith(api, { startHidden: true }));
});

test("createWindow without startHidden does not force-create a tray", async () => {
  let createTrayCalls = 0;
  const api = createApi({
    getGlobalShortcutBridge: () => ({
      handleWindowClose: () => false,
      createTray: () => { createTrayCalls += 1; },
    }),
  });

  await createWindowWith(api, {});

  assert.equal(createTrayCalls, 0);
});
