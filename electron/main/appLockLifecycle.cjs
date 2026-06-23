"use strict";

function emitAppLockReopen(windows) {
  const seen = new Set();
  for (const win of Array.isArray(windows) ? windows : []) {
    try {
      if (!win || win.isDestroyed?.()) continue;
      const id = win.webContents?.id;
      if (id && seen.has(id)) continue;
      if (id) seen.add(id);
      win.webContents?.send?.("netcatty:app-lock:reopen");
    } catch {
      // ignore
    }
  }
}

function shouldBackgroundLockOnHide(appLockController) {
  return Boolean(appLockController && typeof appLockController.setLocked === "function");
}

function handleActivateWithMainWindow({
  app,
  mainWindow,
  globalShortcutBridge,
  reopenWindows,
}) {
  if (!mainWindow || mainWindow.isDestroyed?.()) return false;

  try {
    globalShortcutBridge?.clearPendingFullscreenHide?.(mainWindow);
  } catch {
    // ignore
  }
  try {
    if (mainWindow.isMinimized?.()) mainWindow.restore?.();
  } catch {
    // ignore
  }
  try {
    mainWindow.show?.();
  } catch {
    // ignore
  }
  try {
    mainWindow.focus?.();
  } catch {
    // ignore
  }
  emitAppLockReopen(reopenWindows);
  try {
    app?.focus?.({ steal: true });
  } catch {
    // ignore
  }
  return true;
}

function shouldCommitQuitWithoutDirtyCheck({
  reachableMainWindows,
  queryableWebContents,
}) {
  const hasReachableMainWindows = Array.isArray(reachableMainWindows) && reachableMainWindows.length > 0;
  if (!hasReachableMainWindows) return true;

  const hasQueryableWebContents = Array.isArray(queryableWebContents) && queryableWebContents.length > 0;
  return !hasQueryableWebContents;
}

async function handleBeforeQuit({
  event,
  mainWindows,
  queryDirtyEditors,
  appLockController,
  windowManager,
  app,
  ipcMain,
  quitConfirmed,
  quitGuardChannelBusy,
  timeoutMs,
  setQuitGuardChannelBusy,
  setQuitConfirmed,
}) {
  if (quitConfirmed) return { committed: false, skipped: "quit-confirmed" };
  if (quitGuardChannelBusy) {
    event?.preventDefault?.();
    return { committed: false, skipped: "busy" };
  }

  const reachableMainWindows = (Array.isArray(mainWindows) ? mainWindows : []).filter((candidate) => (
    candidate && !candidate.isDestroyed?.() &&
    (candidate.isVisible?.() || candidate.isMinimized?.())
  ));
  const queryableWebContents = reachableMainWindows
    .map((candidate) => candidate.webContents)
    .filter((wc) => wc && !wc.isDestroyed?.() && !wc.isCrashed?.());

  if (shouldCommitQuitWithoutDirtyCheck({ reachableMainWindows, queryableWebContents })) {
    if (shouldBackgroundLockOnHide(appLockController)) {
      appLockController.setLocked("background");
    }
    windowManager?.setIsQuitting?.(true);
    setQuitConfirmed?.(true);
    app?.quit?.();
    return { committed: true, skipped: "fast-path" };
  }

  setQuitGuardChannelBusy?.(true);
  event?.preventDefault?.();

  try {
    const dirtyResults = await Promise.all(
      queryableWebContents.map((wc) => queryDirtyEditors(wc, timeoutMs, { ipcMain })),
    );
    setQuitGuardChannelBusy?.(false);
    const hasDirty = dirtyResults.some(Boolean);
    if (!hasDirty) {
      if (shouldBackgroundLockOnHide(appLockController)) {
        appLockController.setLocked("background");
      }
      windowManager?.setIsQuitting?.(true);
      setQuitConfirmed?.(true);
      app?.quit?.();
      return { committed: true, skipped: null };
    }

    if (windowManager?.isQuittingForUpdate?.()) {
      windowManager.setQuittingForUpdate?.(false);
    }
    return { committed: false, skipped: "dirty" };
  } catch {
    setQuitGuardChannelBusy?.(false);
    if (shouldBackgroundLockOnHide(appLockController)) {
      appLockController.setLocked("background");
    }
    windowManager?.setIsQuitting?.(true);
    setQuitConfirmed?.(true);
    app?.quit?.();
    return { committed: true, skipped: "error" };
  }
}

module.exports = {
  emitAppLockReopen,
  handleActivateWithMainWindow,
  handleBeforeQuit,
  shouldBackgroundLockOnHide,
  shouldCommitQuitWithoutDirtyCheck,
};
