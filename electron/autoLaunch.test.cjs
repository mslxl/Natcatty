const test = require("node:test");
const assert = require("node:assert/strict");

const {
  HIDDEN_LAUNCH_ARG,
  isAutoLaunchSupported,
  resolveEffectiveLoginState,
  getAutoLaunchEnabled,
  setAutoLaunchEnabled,
  wasLaunchedHidden,
  registerHandlers,
} = require("./autoLaunch.cjs");

test("isAutoLaunchSupported is false when running unpackaged (electron .)", () => {
  assert.equal(isAutoLaunchSupported({ defaultApp: true, platform: "win32" }), false);
  assert.equal(isAutoLaunchSupported({ defaultApp: true, platform: "darwin" }), false);
});

test("isAutoLaunchSupported is true on macOS and Windows when packaged", () => {
  assert.equal(isAutoLaunchSupported({ defaultApp: false, platform: "darwin" }), true);
  assert.equal(isAutoLaunchSupported({ defaultApp: false, platform: "win32" }), true);
});

test("isAutoLaunchSupported is false on Linux — Electron's login-item API is a no-op there", () => {
  assert.equal(isAutoLaunchSupported({ defaultApp: false, platform: "linux" }), false);
});

test("resolveEffectiveLoginState prefers Windows executableWillLaunchAtLogin over openAtLogin", () => {
  assert.equal(
    resolveEffectiveLoginState({ openAtLogin: true, executableWillLaunchAtLogin: false }, "win32"),
    false,
    "Task Manager can disable the run-key entry without clearing openAtLogin",
  );
  assert.equal(
    resolveEffectiveLoginState({ openAtLogin: true, executableWillLaunchAtLogin: true }, "win32"),
    true,
  );
});

test("resolveEffectiveLoginState falls back to openAtLogin when the Windows field is absent", () => {
  assert.equal(resolveEffectiveLoginState({ openAtLogin: true }, "win32"), true);
});

test("resolveEffectiveLoginState ignores executableWillLaunchAtLogin on non-Windows platforms", () => {
  assert.equal(
    resolveEffectiveLoginState({ openAtLogin: true, executableWillLaunchAtLogin: false }, "darwin"),
    true,
  );
});

test("getAutoLaunchEnabled reports unsupported without touching app in dev", () => {
  let called = false;
  const app = { getLoginItemSettings: () => { called = true; return { openAtLogin: true }; } };

  const result = getAutoLaunchEnabled({ app, defaultApp: true, platform: "win32" });

  assert.deepEqual(result, { enabled: false, supported: false });
  assert.equal(called, false);
});

test("getAutoLaunchEnabled reports unsupported on Linux without touching app", () => {
  let called = false;
  const app = { getLoginItemSettings: () => { called = true; return { openAtLogin: true }; } };

  const result = getAutoLaunchEnabled({ app, defaultApp: false, platform: "linux" });

  assert.deepEqual(result, { enabled: false, supported: false });
  assert.equal(called, false);
});

test("getAutoLaunchEnabled reflects the current login item state", () => {
  const app = { getLoginItemSettings: () => ({ openAtLogin: true }) };

  const result = getAutoLaunchEnabled({ app, defaultApp: false, platform: "darwin" });

  assert.deepEqual(result, { enabled: true, supported: true });
});

test("getAutoLaunchEnabled reports disabled when Windows Startup Apps has disabled the entry", () => {
  const app = {
    getLoginItemSettings: () => ({ openAtLogin: true, executableWillLaunchAtLogin: false }),
  };

  const result = getAutoLaunchEnabled({ app, defaultApp: false, platform: "win32" });

  assert.deepEqual(result, { enabled: false, supported: true });
});

test("getAutoLaunchEnabled tolerates a throwing app API", () => {
  const app = { getLoginItemSettings: () => { throw new Error("boom"); } };

  const result = getAutoLaunchEnabled({ app, defaultApp: false, platform: "win32" });

  assert.deepEqual(result, { enabled: false, supported: true });
});

test("setAutoLaunchEnabled(true) registers the hidden launch arg", () => {
  let capturedSettings = null;
  const app = {
    setLoginItemSettings: (settings) => { capturedSettings = settings; },
    getLoginItemSettings: () => ({ openAtLogin: true, executableWillLaunchAtLogin: true }),
  };

  const result = setAutoLaunchEnabled(true, {
    app,
    execPath: "C:\\Netcatty\\Netcatty.exe",
    defaultApp: false,
    platform: "win32",
  });

  assert.deepEqual(capturedSettings, {
    openAtLogin: true,
    openAsHidden: true,
    path: "C:\\Netcatty\\Netcatty.exe",
    args: [HIDDEN_LAUNCH_ARG],
  });
  assert.deepEqual(result, { success: true, enabled: true, supported: true });
});

test("setAutoLaunchEnabled(true) reports disabled when Windows Startup Apps blocks it", () => {
  const app = {
    setLoginItemSettings: () => {},
    getLoginItemSettings: () => ({ openAtLogin: true, executableWillLaunchAtLogin: false }),
  };

  const result = setAutoLaunchEnabled(true, { app, defaultApp: false, platform: "win32" });

  assert.deepEqual(result, { success: true, enabled: false, supported: true });
});

test("setAutoLaunchEnabled(false) clears the hidden launch arg", () => {
  let capturedSettings = null;
  const app = {
    setLoginItemSettings: (settings) => { capturedSettings = settings; },
    getLoginItemSettings: () => ({ openAtLogin: false }),
  };

  const result = setAutoLaunchEnabled(false, { app, defaultApp: false, platform: "win32" });

  assert.deepEqual(capturedSettings.args, []);
  assert.equal(capturedSettings.openAtLogin, false);
  assert.deepEqual(result, { success: true, enabled: false, supported: true });
});

test("setAutoLaunchEnabled is a no-op in dev and does not call the app API", () => {
  let called = false;
  const app = { setLoginItemSettings: () => { called = true; } };

  const result = setAutoLaunchEnabled(true, { app, defaultApp: true, platform: "win32" });

  assert.equal(called, false);
  assert.deepEqual(result, { success: false, enabled: false, supported: false });
});

test("setAutoLaunchEnabled is a no-op on Linux and does not call the app API", () => {
  let called = false;
  const app = { setLoginItemSettings: () => { called = true; } };

  const result = setAutoLaunchEnabled(true, { app, defaultApp: false, platform: "linux" });

  assert.equal(called, false);
  assert.deepEqual(result, { success: false, enabled: false, supported: false });
});

test("setAutoLaunchEnabled surfaces failures without throwing", () => {
  const app = {
    setLoginItemSettings: () => { throw new Error("registry locked"); },
    getLoginItemSettings: () => ({ openAtLogin: false }),
  };

  const result = setAutoLaunchEnabled(true, { app, defaultApp: false, platform: "win32" });

  assert.equal(result.success, false);
  assert.equal(result.supported, true);
});

test("wasLaunchedHidden detects the --hidden cold-start flag", () => {
  assert.equal(wasLaunchedHidden({ argv: ["node", "main.js", "--hidden"], platform: "win32" }), true);
  assert.equal(wasLaunchedHidden({ argv: ["node", "main.js"], platform: "win32" }), false);
  assert.equal(wasLaunchedHidden({ argv: undefined, platform: "win32" }), false);
});

test("wasLaunchedHidden detects a macOS hidden login-item launch via wasOpenedAsHidden", () => {
  const app = { getLoginItemSettings: () => ({ wasOpenedAsHidden: true }) };

  const result = wasLaunchedHidden({ argv: ["node", "main.js"], app, platform: "darwin" });

  assert.equal(result, true, "macOS login launches never carry setLoginItemSettings() args in argv");
});

test("wasLaunchedHidden does not consult macOS login-item state on other platforms", () => {
  let called = false;
  const app = { getLoginItemSettings: () => { called = true; return { wasOpenedAsHidden: true }; } };

  const result = wasLaunchedHidden({ argv: ["node", "main.js"], app, platform: "win32" });

  assert.equal(result, false);
  assert.equal(called, false);
});

test("wasLaunchedHidden tolerates a throwing macOS login-item lookup", () => {
  const app = { getLoginItemSettings: () => { throw new Error("boom"); } };

  const result = wasLaunchedHidden({ argv: [], app, platform: "darwin" });

  assert.equal(result, false);
});

test("registerHandlers wires get/set IPC channels", async () => {
  const handlers = new Map();
  const ipcMain = { handle: (channel, fn) => handlers.set(channel, fn) };
  const app = {
    getLoginItemSettings: () => ({ openAtLogin: false }),
    setLoginItemSettings: () => {},
  };

  registerHandlers(ipcMain, { app, platform: "win32" });

  assert.ok(handlers.has("netcatty:autoLaunch:get"));
  assert.ok(handlers.has("netcatty:autoLaunch:set"));

  const getResult = await handlers.get("netcatty:autoLaunch:get")();
  assert.deepEqual(getResult, { enabled: false, supported: true });

  app.getLoginItemSettings = () => ({ openAtLogin: true, executableWillLaunchAtLogin: true });
  const setResult = await handlers.get("netcatty:autoLaunch:set")(null, { enabled: true });
  assert.deepEqual(setResult, { success: true, enabled: true, supported: true });
});

test("registerHandlers respects the real process.platform when no override is given", async () => {
  const handlers = new Map();
  const ipcMain = { handle: (channel, fn) => handlers.set(channel, fn) };
  const app = { getLoginItemSettings: () => ({ openAtLogin: false }) };

  registerHandlers(ipcMain, { app });
  const result = await handlers.get("netcatty:autoLaunch:get")();

  assert.equal(
    result.supported,
    process.platform === "darwin" || process.platform === "win32",
  );
});
