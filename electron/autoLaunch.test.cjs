const test = require("node:test");
const assert = require("node:assert/strict");

const {
  HIDDEN_LAUNCH_ARG,
  isAutoLaunchSupported,
  getAutoLaunchEnabled,
  setAutoLaunchEnabled,
  wasLaunchedHidden,
  registerHandlers,
} = require("./autoLaunch.cjs");

test("isAutoLaunchSupported is false when running unpackaged (electron .)", () => {
  assert.equal(isAutoLaunchSupported({ defaultApp: true }), false);
  assert.equal(isAutoLaunchSupported({ defaultApp: false }), true);
});

test("getAutoLaunchEnabled reports unsupported without touching app in dev", () => {
  let called = false;
  const app = { getLoginItemSettings: () => { called = true; return { openAtLogin: true }; } };

  const result = getAutoLaunchEnabled({ app, defaultApp: true });

  assert.deepEqual(result, { enabled: false, supported: false });
  assert.equal(called, false);
});

test("getAutoLaunchEnabled reflects the current login item state", () => {
  const app = { getLoginItemSettings: () => ({ openAtLogin: true }) };

  const result = getAutoLaunchEnabled({ app, defaultApp: false });

  assert.deepEqual(result, { enabled: true, supported: true });
});

test("getAutoLaunchEnabled tolerates a throwing app API", () => {
  const app = { getLoginItemSettings: () => { throw new Error("boom"); } };

  const result = getAutoLaunchEnabled({ app, defaultApp: false });

  assert.deepEqual(result, { enabled: false, supported: true });
});

test("setAutoLaunchEnabled(true) registers the hidden launch arg", () => {
  let capturedSettings = null;
  const app = {
    setLoginItemSettings: (settings) => { capturedSettings = settings; },
    getLoginItemSettings: () => ({ openAtLogin: true }),
  };

  const result = setAutoLaunchEnabled(true, { app, execPath: "C:\\Netcatty\\Netcatty.exe", defaultApp: false });

  assert.deepEqual(capturedSettings, {
    openAtLogin: true,
    openAsHidden: true,
    path: "C:\\Netcatty\\Netcatty.exe",
    args: [HIDDEN_LAUNCH_ARG],
  });
  assert.deepEqual(result, { success: true, enabled: true, supported: true });
});

test("setAutoLaunchEnabled(false) clears the hidden launch arg", () => {
  let capturedSettings = null;
  const app = {
    setLoginItemSettings: (settings) => { capturedSettings = settings; },
    getLoginItemSettings: () => ({ openAtLogin: false }),
  };

  const result = setAutoLaunchEnabled(false, { app, defaultApp: false });

  assert.deepEqual(capturedSettings.args, []);
  assert.equal(capturedSettings.openAtLogin, false);
  assert.deepEqual(result, { success: true, enabled: false, supported: true });
});

test("setAutoLaunchEnabled is a no-op in dev and does not call the app API", () => {
  let called = false;
  const app = { setLoginItemSettings: () => { called = true; } };

  const result = setAutoLaunchEnabled(true, { app, defaultApp: true });

  assert.equal(called, false);
  assert.deepEqual(result, { success: false, enabled: false, supported: false });
});

test("setAutoLaunchEnabled surfaces failures without throwing", () => {
  const app = {
    setLoginItemSettings: () => { throw new Error("registry locked"); },
    getLoginItemSettings: () => ({ openAtLogin: false }),
  };

  const result = setAutoLaunchEnabled(true, { app, defaultApp: false });

  assert.equal(result.success, false);
  assert.equal(result.supported, true);
});

test("wasLaunchedHidden detects the --hidden cold-start flag", () => {
  assert.equal(wasLaunchedHidden(["node", "main.js", "--hidden"]), true);
  assert.equal(wasLaunchedHidden(["node", "main.js"]), false);
  assert.equal(wasLaunchedHidden(undefined), false);
});

test("registerHandlers wires get/set IPC channels", async () => {
  const handlers = new Map();
  const ipcMain = { handle: (channel, fn) => handlers.set(channel, fn) };
  const app = {
    getLoginItemSettings: () => ({ openAtLogin: false }),
    setLoginItemSettings: () => {},
  };

  registerHandlers(ipcMain, { app });

  assert.ok(handlers.has("netcatty:autoLaunch:get"));
  assert.ok(handlers.has("netcatty:autoLaunch:set"));

  const getResult = await handlers.get("netcatty:autoLaunch:get")();
  assert.deepEqual(getResult, { enabled: false, supported: true });

  app.getLoginItemSettings = () => ({ openAtLogin: true });
  const setResult = await handlers.get("netcatty:autoLaunch:set")(null, { enabled: true });
  assert.deepEqual(setResult, { success: true, enabled: true, supported: true });
});
