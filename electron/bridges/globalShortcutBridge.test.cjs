const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");

function loadBridge() {
  const bridgePath = require.resolve("./globalShortcutBridge.cjs");
  delete require.cache[bridgePath];
  return require("./globalShortcutBridge.cjs");
}

function createElectronStub() {
  class FakeTray {
    constructor() {
      this.handlers = new Map();
    }

    setToolTip() {}
    setContextMenu() {}
    destroy() {}

    on(eventName, handler) {
      this.handlers.set(eventName, handler);
    }
  }

  return {
    Tray: FakeTray,
    Menu: {},
    nativeImage: {
      createFromPath() {
        return {
          resize() {
            return this;
          },
          setTemplateImage() {},
        };
      },
      createEmpty() {
        return {};
      },
    },
    app: {
      getAppPath() {
        return process.cwd();
      },
      quit() {},
    },
  };
}

function createIpcMainStub() {
  const handlers = new Map();
  return {
    handlers,
    handle(channel, handler) {
      handlers.set(channel, handler);
    },
  };
}

class FakeWindow extends EventEmitter {
  constructor({ fullscreen = false } = {}) {
    super();
    this.fullscreen = fullscreen;
    this.hideCalls = 0;
    this.setFullScreenCalls = [];
    this.destroyed = false;
  }

  isDestroyed() {
    return this.destroyed;
  }

  isFullScreen() {
    return this.fullscreen;
  }

  setFullScreen(nextValue) {
    this.setFullScreenCalls.push(nextValue);
    this.fullscreen = nextValue;
  }

  hide() {
    this.hideCalls += 1;
  }
}

function withPlatform(platform, run) {
  const original = Object.getOwnPropertyDescriptor(process, "platform");
  Object.defineProperty(process, "platform", { configurable: true, value: platform });
  try {
    return run();
  } finally {
    Object.defineProperty(process, "platform", original);
  }
}

async function enableCloseToTray(bridge) {
  bridge.init({ electronModule: createElectronStub() });
  const ipcMain = createIpcMainStub();
  bridge.registerHandlers(ipcMain);
  await ipcMain.handlers.get("netcatty:tray:setCloseToTray")(null, { enabled: true });
}

test("handleWindowClose allows normal close when close-to-tray is disabled", () => {
  const bridge = loadBridge();
  const win = new FakeWindow();
  let prevented = false;

  const result = bridge.handleWindowClose({ preventDefault() { prevented = true; } }, win);

  assert.equal(result, false);
  assert.equal(prevented, false);
  assert.equal(win.hideCalls, 0);
});

test("handleWindowClose exits mac fullscreen before hiding to tray", async () => {
  await withPlatform("darwin", async () => {
    const bridge = loadBridge();
    await enableCloseToTray(bridge);

    const win = new FakeWindow({ fullscreen: true });
    let prevented = false;

    const result = bridge.handleWindowClose({ preventDefault() { prevented = true; } }, win);

    assert.equal(result, true);
    assert.equal(prevented, true);
    assert.deepEqual(win.setFullScreenCalls, [false]);
    assert.equal(win.hideCalls, 0);

    win.emit("leave-full-screen");

    assert.equal(win.hideCalls, 1);
  });
});

test("handleWindowClose hides immediately when tray close is used outside fullscreen", async () => {
  await withPlatform("darwin", async () => {
    const bridge = loadBridge();
    await enableCloseToTray(bridge);

    const win = new FakeWindow({ fullscreen: false });
    let prevented = false;

    const result = bridge.handleWindowClose({ preventDefault() { prevented = true; } }, win);

    assert.equal(result, true);
    assert.equal(prevented, true);
    assert.deepEqual(win.setFullScreenCalls, []);
    assert.equal(win.hideCalls, 1);
  });
});
