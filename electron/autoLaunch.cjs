/**
 * Auto Launch - Registers Netcatty to start at system login, hidden to the
 * tray. Thin wrapper around Electron's app.setLoginItemSettings/
 * getLoginItemSettings so main.cjs and the settings IPC handlers share one
 * source of truth.
 *
 * Development runs (`electron .`) are unsupported: process.execPath points
 * at the transient electron.exe, so a login item registered there would
 * break (or silently do nothing) after the dev process exits.
 *
 * Platform support is further limited to macOS and Windows: Electron's
 * login-item API is a no-op on Linux, which ships as AppImage/deb/rpm/pacman
 * with no first-party autostart hook, so reporting it as supported there
 * would show an enabled toggle that does nothing.
 */

const HIDDEN_LAUNCH_ARG = "--hidden";

function isAutoLaunchSupported({ defaultApp = process.defaultApp, platform = process.platform } = {}) {
  if (defaultApp) return false;
  return platform === "darwin" || platform === "win32";
}

/**
 * Resolve the OS's effective auto-launch state, not just whether a
 * registration exists. Windows can retain the run-key entry while the user
 * disables it via Task Manager's Startup Apps UI; Electron surfaces that as
 * executableWillLaunchAtLogin, distinct from openAtLogin (registration only).
 */
function resolveEffectiveLoginState(settings, platform) {
  if (platform === "win32" && typeof settings?.executableWillLaunchAtLogin === "boolean") {
    return settings.executableWillLaunchAtLogin;
  }
  return Boolean(settings?.openAtLogin);
}

/**
 * Electron's getLoginItemSettings() only reports openAtLogin/
 * executableWillLaunchAtLogin for the specific path+args combination you
 * ask about — it does not mean "is anything registered for this app". Our
 * code only ever registers a login item with args:[HIDDEN_LAUNCH_ARG], so
 * every read must query that exact combination (matching what
 * setAutoLaunchEnabled writes) or Windows reports a false negative.
 */
function buildLoginItemQueryOptions(execPath) {
  return { path: execPath, args: [HIDDEN_LAUNCH_ARG] };
}

function getAutoLaunchEnabled({
  app,
  execPath = process.execPath,
  defaultApp = process.defaultApp,
  platform = process.platform,
} = {}) {
  if (!isAutoLaunchSupported({ defaultApp, platform })) {
    return { enabled: false, supported: false };
  }
  try {
    const settings = app.getLoginItemSettings(buildLoginItemQueryOptions(execPath));
    return { enabled: resolveEffectiveLoginState(settings, platform), supported: true };
  } catch (err) {
    console.warn("[AutoLaunch] Failed to read login item settings:", err?.message || err);
    return { enabled: false, supported: true };
  }
}

function setAutoLaunchEnabled(enabled, {
  app,
  execPath = process.execPath,
  defaultApp = process.defaultApp,
  platform = process.platform,
} = {}) {
  if (!isAutoLaunchSupported({ defaultApp, platform })) {
    return { success: false, enabled: false, supported: false };
  }
  const wantEnabled = Boolean(enabled);
  try {
    app.setLoginItemSettings({
      openAtLogin: wantEnabled,
      // openAsHidden only applies on macOS App Store builds; Windows relies
      // on the --hidden arg below, which main.cjs checks on cold start.
      openAsHidden: wantEnabled,
      path: execPath,
      args: wantEnabled ? [HIDDEN_LAUNCH_ARG] : [],
    });
    const settings = app.getLoginItemSettings(buildLoginItemQueryOptions(execPath));
    return { success: true, enabled: resolveEffectiveLoginState(settings, platform), supported: true };
  } catch (err) {
    console.warn("[AutoLaunch] Failed to update login item settings:", err?.message || err);
    return { success: false, enabled: getAutoLaunchEnabled({ app, execPath, defaultApp, platform }).enabled, supported: true };
  }
}

/**
 * True when this process was launched by the OS login item (cold start
 * only). Windows relies on the --hidden arg (openAsHidden is a macOS-only
 * setting); macOS never puts args from setLoginItemSettings() into argv for
 * a login launch, so it must be detected via
 * getLoginItemSettings().wasOpenedAsHidden instead.
 */
function wasLaunchedHidden({ argv = process.argv, app, platform = process.platform } = {}) {
  if (Array.isArray(argv) && argv.includes(HIDDEN_LAUNCH_ARG)) return true;
  if (platform !== "darwin" || typeof app?.getLoginItemSettings !== "function") return false;
  try {
    return Boolean(app.getLoginItemSettings().wasOpenedAsHidden);
  } catch (err) {
    console.warn("[AutoLaunch] Failed to read macOS hidden login-item state:", err?.message || err);
    return false;
  }
}

function registerHandlers(ipcMain, { app, platform = process.platform }) {
  ipcMain.handle("netcatty:autoLaunch:get", async () => {
    return getAutoLaunchEnabled({ app, platform });
  });

  ipcMain.handle("netcatty:autoLaunch:set", async (_event, { enabled }) => {
    return setAutoLaunchEnabled(enabled, { app, platform });
  });
}

module.exports = {
  HIDDEN_LAUNCH_ARG,
  isAutoLaunchSupported,
  resolveEffectiveLoginState,
  buildLoginItemQueryOptions,
  getAutoLaunchEnabled,
  setAutoLaunchEnabled,
  wasLaunchedHidden,
  registerHandlers,
};
