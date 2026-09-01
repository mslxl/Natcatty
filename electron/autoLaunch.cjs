/**
 * Auto Launch - Registers Netcatty to start at system login, hidden to the
 * tray. Thin wrapper around Electron's app.setLoginItemSettings/
 * getLoginItemSettings so main.cjs and the settings IPC handlers share one
 * source of truth.
 *
 * Development runs (`electron .`) are unsupported: process.execPath points
 * at the transient electron.exe, so a login item registered there would
 * break (or silently do nothing) after the dev process exits.
 */

const HIDDEN_LAUNCH_ARG = "--hidden";

function isAutoLaunchSupported({ defaultApp = process.defaultApp } = {}) {
  return !defaultApp;
}

function getAutoLaunchEnabled({ app, defaultApp = process.defaultApp } = {}) {
  if (!isAutoLaunchSupported({ defaultApp })) {
    return { enabled: false, supported: false };
  }
  try {
    const settings = app.getLoginItemSettings();
    return { enabled: Boolean(settings?.openAtLogin), supported: true };
  } catch (err) {
    console.warn("[AutoLaunch] Failed to read login item settings:", err?.message || err);
    return { enabled: false, supported: true };
  }
}

function setAutoLaunchEnabled(enabled, { app, execPath = process.execPath, defaultApp = process.defaultApp } = {}) {
  if (!isAutoLaunchSupported({ defaultApp })) {
    return { success: false, enabled: false, supported: false };
  }
  const wantEnabled = Boolean(enabled);
  try {
    app.setLoginItemSettings({
      openAtLogin: wantEnabled,
      // openAsHidden only applies on macOS; Windows/Linux rely on the
      // --hidden arg below, which main.cjs checks on cold start.
      openAsHidden: wantEnabled,
      path: execPath,
      args: wantEnabled ? [HIDDEN_LAUNCH_ARG] : [],
    });
    const settings = app.getLoginItemSettings();
    return { success: true, enabled: Boolean(settings?.openAtLogin), supported: true };
  } catch (err) {
    console.warn("[AutoLaunch] Failed to update login item settings:", err?.message || err);
    return { success: false, enabled: getAutoLaunchEnabled({ app, defaultApp }).enabled, supported: true };
  }
}

/** True when this process was launched by the OS login item (cold start only). */
function wasLaunchedHidden(argv = process.argv) {
  return Array.isArray(argv) && argv.includes(HIDDEN_LAUNCH_ARG);
}

function registerHandlers(ipcMain, { app }) {
  ipcMain.handle("netcatty:autoLaunch:get", async () => {
    return getAutoLaunchEnabled({ app });
  });

  ipcMain.handle("netcatty:autoLaunch:set", async (_event, { enabled }) => {
    return setAutoLaunchEnabled(enabled, { app });
  });
}

module.exports = {
  HIDDEN_LAUNCH_ARG,
  isAutoLaunchSupported,
  getAutoLaunchEnabled,
  setAutoLaunchEnabled,
  wasLaunchedHidden,
  registerHandlers,
};
