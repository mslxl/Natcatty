/**
 * Update Bridge - Handles auto updates via electron-updater
 */

let electronModule = null;
let autoUpdater;
let autoUpdaterLoaded = false;
let listenersAttached = false;

const updateStatus = {
  status: "idle",
  supported: false,
  updateInfo: null,
  progress: null,
  error: null,
};

function loadAutoUpdater() {
  if (autoUpdaterLoaded) return autoUpdater;
  autoUpdaterLoaded = true;
  try {
    autoUpdater = require("electron-updater").autoUpdater;
  } catch (err) {
    console.warn("[UpdateBridge] electron-updater not available:", err?.message || err);
    autoUpdater = null;
  }
  return autoUpdater;
}

function isSupported() {
  return !!loadAutoUpdater() && !!electronModule?.app?.isPackaged;
}

function normalizeReleaseNotes(notes) {
  if (!notes) return "";
  if (typeof notes === "string") return notes;
  if (Array.isArray(notes)) {
    return notes
      .map((entry) => entry?.note || "")
      .filter(Boolean)
      .join("\n");
  }
  return "";
}

function mapUpdateInfo(info) {
  if (!info) return null;
  return {
    version: info.version || "",
    releaseName: info.releaseName || info.version || "",
    releaseNotes: normalizeReleaseNotes(info.releaseNotes),
    releaseDate: info.releaseDate || "",
  };
}

function mapProgress(progress) {
  if (!progress) return null;
  return {
    percent: typeof progress.percent === "number" ? progress.percent : 0,
    transferred: typeof progress.transferred === "number" ? progress.transferred : 0,
    total: typeof progress.total === "number" ? progress.total : 0,
    bytesPerSecond: typeof progress.bytesPerSecond === "number" ? progress.bytesPerSecond : 0,
  };
}

function broadcastStatus() {
  const { BrowserWindow } = electronModule || {};
  if (!BrowserWindow?.getAllWindows) return;
  const windows = BrowserWindow.getAllWindows();
  windows.forEach((win) => {
    if (!win || win.isDestroyed()) return;
    win.webContents?.send?.("netcatty:update:status", updateStatus);
  });
}

function setStatus(next) {
  updateStatus.status = next.status ?? updateStatus.status;
  updateStatus.updateInfo = next.updateInfo ?? updateStatus.updateInfo;
  updateStatus.progress = next.progress ?? updateStatus.progress;
  updateStatus.error = next.error ?? updateStatus.error;
  updateStatus.supported = isSupported();
  broadcastStatus();
}

function attachListeners() {
  if (!isSupported() || listenersAttached) return;
  const updater = loadAutoUpdater();
  if (!updater) return;
  listenersAttached = true;

  updater.on("checking-for-update", () => {
    setStatus({ status: "checking", error: null });
  });

  updater.on("update-available", (info) => {
    setStatus({ status: "available", updateInfo: mapUpdateInfo(info), error: null });
  });

  updater.on("update-not-available", (info) => {
    setStatus({ status: "not-available", updateInfo: mapUpdateInfo(info), error: null });
  });

  updater.on("download-progress", (progress) => {
    setStatus({ status: "downloading", progress: mapProgress(progress), error: null });
  });

  updater.on("update-downloaded", (info) => {
    setStatus({ status: "downloaded", updateInfo: mapUpdateInfo(info), progress: null, error: null });
  });

  updater.on("error", (err) => {
    setStatus({
      status: "error",
      error: err?.message || String(err),
    });
  });
}

function init(deps) {
  electronModule = deps?.electronModule || null;
  updateStatus.supported = isSupported();
}

function registerHandlers(ipcMain) {
  if (!ipcMain) return;
  const updater = loadAutoUpdater();
  if (updater) {
    updater.autoDownload = false;
  }
  attachListeners();

  ipcMain.handle("netcatty:update:getStatus", async () => {
    updateStatus.supported = isSupported();
    return { ...updateStatus };
  });

  ipcMain.handle("netcatty:update:check", async () => {
    if (!isSupported()) {
      return { supported: false, error: "Auto updates are unavailable in this build." };
    }
    attachListeners();
    try {
      setStatus({ status: "checking", error: null });
      const result = await updater.checkForUpdates();
      const info = mapUpdateInfo(result?.updateInfo);
      if (info) {
        updateStatus.updateInfo = info;
      }
      return { supported: true, updateInfo: info };
    } catch (err) {
      const message = err?.message || String(err);
      setStatus({ status: "error", error: message });
      return { supported: true, error: message };
    }
  });

  ipcMain.handle("netcatty:update:download", async () => {
    if (!isSupported()) {
      return { supported: false, error: "Auto updates are unavailable in this build." };
    }
    if (updateStatus.status === "downloading" || updateStatus.status === "downloaded") {
      return { supported: true };
    }
    attachListeners();
    try {
      if (!updateStatus.updateInfo) {
        await updater.checkForUpdates();
      }
      await updater.downloadUpdate();
      return { supported: true };
    } catch (err) {
      const message = err?.message || String(err);
      setStatus({ status: "error", error: message });
      return { supported: true, error: message };
    }
  });

  ipcMain.handle("netcatty:update:install", async () => {
    if (!isSupported()) {
      return { supported: false, error: "Auto updates are unavailable in this build." };
    }
    try {
      // quitAndInstall exits the app on success; return below is only for errors.
      updater.quitAndInstall();
      return { supported: true };
    } catch (err) {
      const message = err?.message || String(err);
      setStatus({ status: "error", error: message });
      return { supported: true, error: message };
    }
  });
}

module.exports = {
  init,
  registerHandlers,
};
