const {
  canLockFromSettings,
  createAppLockPasswordVerifier,
  normalizeAppLockTimeoutMinutes,
  verifyAppLockPassword,
} = require("./appLockSettingsStore.cjs");

function normalizeReason(reason) {
  return typeof reason === "string" && reason.trim() !== "" ? reason : null;
}

function cloneState(state) {
  return {
    initialized: state.initialized === true,
    locked: state.locked === true,
    reason: normalizeReason(state.reason),
    version: state.version,
    lastLockedAt: typeof state.lastLockedAt === "number" ? state.lastLockedAt : null,
    lastUnlockedAt: typeof state.lastUnlockedAt === "number" ? state.lastUnlockedAt : null,
    lastActivityAt: typeof state.lastActivityAt === "number" ? state.lastActivityAt : null,
  };
}

function createAppLockRuntimeBridge() {
  let state = {
    initialized: false,
    locked: false,
    reason: null,
    version: 0,
    lastLockedAt: null,
    lastUnlockedAt: null,
    lastActivityAt: null,
  };

  const listeners = new Set();
  let idleTimerId = null;
  let idleTimerConfig = null;

  function getState() {
    return cloneState(state);
  }

  function notify() {
    const snapshot = getState();
    listeners.forEach((listener) => {
      try {
        listener(snapshot);
      } catch {
        // ignore subscriber failures
      }
    });
  }

  function clearScheduledTimerOnly() {
    if (idleTimerId !== null) {
      clearTimeout(idleTimerId);
      idleTimerId = null;
    }
  }

  function getTimeoutMs(timeoutMinutes) {
    if (!Number.isFinite(timeoutMinutes) || timeoutMinutes <= 0) return null;
    return timeoutMinutes * 60_000;
  }

  function rescheduleIdleTimer() {
    clearScheduledTimerOnly();

    if (!idleTimerConfig || state.initialized !== true || state.locked === true) {
      return;
    }

    const timeoutMs = getTimeoutMs(idleTimerConfig.timeoutMinutes);
    if (timeoutMs === null || typeof state.lastActivityAt !== "number") {
      return;
    }

    const elapsedMs = Math.max(0, Date.now() - state.lastActivityAt);
    const delayMs = Math.max(0, timeoutMs - elapsedMs);

    idleTimerId = setTimeout(() => {
      idleTimerId = null;

      if (!idleTimerConfig || state.initialized !== true || state.locked === true) {
        return;
      }

      const currentTimeoutMs = getTimeoutMs(idleTimerConfig.timeoutMinutes);
      if (currentTimeoutMs === null || typeof state.lastActivityAt !== "number") {
        return;
      }

      const currentElapsedMs = Math.max(0, Date.now() - state.lastActivityAt);
      if (currentElapsedMs < currentTimeoutMs) {
        rescheduleIdleTimer();
        return;
      }

      if (!idleTimerConfig.canLock()) {
        clearIdleTimer();
        return;
      }

      const nextState = lock("idle");
      try {
        idleTimerConfig.onIdleLock(nextState);
      } catch {
        // ignore callback failures
      }
    }, delayMs);
    if (idleTimerId && typeof idleTimerId.unref === "function") {
      idleTimerId.unref();
    }
  }

  function applyStatePatch(patch, { notifyListeners = true } = {}) {
    state = {
      ...state,
      ...patch,
      version: state.version + 1,
    };
    rescheduleIdleTimer();
    if (notifyListeners) {
      notify();
    }
    return getState();
  }

  function initialize(nextState) {
    const now = Date.now();
    const locked = nextState?.locked === true;
    return applyStatePatch({
      initialized: true,
      locked,
      reason: locked ? normalizeReason(nextState?.reason) || "startup" : null,
      lastLockedAt: locked
        ? (typeof nextState?.lastLockedAt === "number" ? nextState.lastLockedAt : now)
        : null,
      lastUnlockedAt: locked
        ? null
        : (typeof nextState?.lastUnlockedAt === "number" ? nextState.lastUnlockedAt : null),
      lastActivityAt: typeof nextState?.lastActivityAt === "number" ? nextState.lastActivityAt : now,
    });
  }

  function lock(reason = "manual") {
    const nextReason = normalizeReason(reason) || "manual";
    if (state.initialized === true && state.locked === true && state.reason === nextReason) {
      return getState();
    }

    return applyStatePatch({
      initialized: true,
      locked: true,
      reason: nextReason,
      lastLockedAt: Date.now(),
    });
  }

  function unlock() {
    const now = Date.now();
    if (state.initialized === true && state.locked === false && state.reason === null) {
      return getState();
    }

    return applyStatePatch({
      initialized: true,
      locked: false,
      reason: null,
      lastUnlockedAt: now,
      lastActivityAt: now,
    });
  }

  function recordActivity(timestamp = Date.now()) {
    if (state.initialized !== true || state.locked === true) {
      return getState();
    }
    if (typeof timestamp !== "number" || !Number.isFinite(timestamp)) {
      return getState();
    }
    if (state.lastActivityAt === timestamp) {
      return getState();
    }

    return applyStatePatch(
      {
        lastActivityAt: timestamp,
      },
      { notifyListeners: false },
    );
  }

  function subscribe(listener) {
    if (typeof listener !== "function") {
      return () => {};
    }

    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }

  function scheduleIdleTimer({ timeoutMinutes, canLock, onIdleLock }) {
    idleTimerConfig = {
      timeoutMinutes,
      canLock: typeof canLock === "function" ? canLock : () => true,
      onIdleLock: typeof onIdleLock === "function" ? onIdleLock : () => {},
    };
    rescheduleIdleTimer();
  }

  function clearIdleTimer() {
    idleTimerConfig = null;
    clearScheduledTimerOnly();
  }

  return {
    initialize,
    getState,
    lock,
    unlock,
    recordActivity,
    subscribe,
    scheduleIdleTimer,
    clearIdleTimer,
  };
}

module.exports = {
  createAppLockController,
  createAppLockRuntimeBridge,
};

function createAppLockController({
  settingsStore,
  runtimeBridge,
  systemAuthBridge = null,
  getMainWindows = () => [],
  getSettingsWindow = () => null,
  getTrayPanelWindow = () => null,
  getTerminalPopupWindows = () => [],
}) {
  if (!settingsStore || typeof settingsStore.getSnapshot !== "function" || typeof settingsStore.save !== "function") {
    throw new Error("createAppLockController requires a settingsStore");
  }
  if (!runtimeBridge || typeof runtimeBridge.getState !== "function") {
    throw new Error("createAppLockController requires a runtimeBridge");
  }

  function syncIdleTimer() {
    const settings = getSettings();
    if (!canLockFromSettings(settings)) {
      runtimeBridge.clearIdleTimer?.();
      return;
    }

    runtimeBridge.scheduleIdleTimer?.({
      timeoutMinutes: settings.timeoutMinutes,
      canLock: () => canLockFromSettings(getSettings()),
      onIdleLock: (nextState) => {
        broadcast("netcatty:appLock:runtimeStateChanged", nextState);
      },
    });
  }

  function getWindowsForBroadcast() {
    const windows = [
      ...(Array.isArray(getMainWindows()) ? getMainWindows() : []),
      getSettingsWindow(),
      getTrayPanelWindow(),
      ...(Array.isArray(getTerminalPopupWindows()) ? getTerminalPopupWindows() : []),
    ];

    const seen = new Set();
    return windows.filter((win) => {
      if (!win || typeof win.isDestroyed !== "function" || win.isDestroyed()) return false;
      if (!win.webContents || typeof win.webContents.isDestroyed !== "function" || win.webContents.isDestroyed()) {
        return false;
      }

      const id = win.webContents.id || win;
      if (seen.has(id)) return false;
      seen.add(id);
      return true;
    });
  }

  function broadcast(channel, payload) {
    for (const win of getWindowsForBroadcast()) {
      try {
        win.webContents.send(channel, payload);
      } catch {
        // ignore disposed windows during broadcast
      }
    }
  }

  function getSettings() {
    return settingsStore.getSnapshot();
  }

  function getRuntimeState() {
    return runtimeBridge.getState();
  }

  async function getSystemAuthStatusOnly() {
    if (!systemAuthBridge || typeof systemAuthBridge.getStatus !== "function") {
      return {
        supported: false,
        available: false,
        platform: "unsupported",
        label: null,
        reason: null,
      };
    }
    try {
      return await systemAuthBridge.getStatus();
    } catch {
      return {
        supported: false,
        available: false,
        platform: "unsupported",
        label: null,
        reason: "failed",
      };
    }
  }

  async function getSystemUnlockStatus() {
    const status = await getSystemAuthStatusOnly();
    const settings = getSettings();
    const canLock = canLockFromSettings(settings);
    return {
      supported: status.supported === true,
      available: status.available === true && canLock,
      enabled: settings.systemUnlockEnabled === true && canLock,
      platform: status.platform || "unsupported",
      label: status.label || null,
      reason: status.reason || null,
    };
  }

  async function saveSettings(nextSettings) {
    const saved = await settingsStore.save(nextSettings);
    syncIdleTimer();
    broadcast("netcatty:appLock:settingsChanged", saved);
    return saved;
  }

  async function requestEnable() {
    const current = getSettings();
    if (!current.passwordVerifier) {
      return current;
    }
    return saveSettings({
      ...current,
      enabled: true,
    });
  }

  async function requestDisable(currentPassword) {
    const current = getSettings();
    if (current.passwordVerifier) {
      if (!currentPassword) {
        return { ok: false, error: "empty-current" };
      }
      const verified = await verifyAppLockPassword(currentPassword, current.passwordVerifier);
      if (!verified) {
        return { ok: false, error: "incorrect" };
      }
    }

    const saved = await saveSettings({
      enabled: false,
      timeoutMinutes: current.timeoutMinutes,
      passwordVerifier: null,
    });
    const runtimeState = runtimeBridge.unlock();
    syncIdleTimer();
    broadcast("netcatty:appLock:runtimeStateChanged", runtimeState);
    return saved;
  }

  async function requestReset(currentPassword) {
    const current = getSettings();
    const verified = await verifyCurrentPassword(current, currentPassword);
    if (verified !== true) return verified;

    const saved = await saveSettings({
      enabled: false,
      timeoutMinutes: current.timeoutMinutes,
      passwordVerifier: null,
    });
    const runtimeState = runtimeBridge.unlock();
    syncIdleTimer();
    broadcast("netcatty:appLock:runtimeStateChanged", runtimeState);
    return saved;
  }

  async function requestPasswordChange(input = {}) {
    const current = getSettings();
    const nextPassword = typeof input.nextPassword === "string" ? input.nextPassword : "";
    const currentPassword = typeof input.currentPassword === "string" ? input.currentPassword : "";

    if (nextPassword.trim() === "") {
      return { ok: false, error: "empty-next" };
    }
    if (current.passwordVerifier) {
      if (!currentPassword) {
        return { ok: false, error: "empty-current" };
      }
      const verified = await verifyAppLockPassword(currentPassword, current.passwordVerifier);
      if (!verified) {
        return { ok: false, error: "incorrect" };
      }
    }

    const passwordVerifier = await createAppLockPasswordVerifier(nextPassword);
    return saveSettings({
      ...current,
      enabled: current.enabled || !current.passwordVerifier,
      passwordVerifier,
    });
  }

  async function setTimeoutMinutes(timeoutMinutes) {
    const current = getSettings();
    return saveSettings({
      ...current,
      timeoutMinutes: normalizeAppLockTimeoutMinutes(timeoutMinutes),
    });
  }

  async function verifyCurrentPassword(current, currentPassword) {
    if (!current.passwordVerifier) return true;
    if (!currentPassword) return { ok: false, error: "empty-current" };
    const verified = await verifyAppLockPassword(currentPassword, current.passwordVerifier);
    if (!verified) return { ok: false, error: "incorrect" };
    return true;
  }

  async function setSystemUnlockEnabled(input = {}) {
    const enabled = input?.enabled === true;
    const currentPassword = typeof input?.currentPassword === "string" ? input.currentPassword : "";
    const current = getSettings();
    if (!canLockFromSettings(current)) {
      return { ok: false, error: "unavailable" };
    }

    if (!enabled) {
      if (runtimeBridge.getState().locked === true && !currentPassword) {
        return { ok: false, error: "locked" };
      }
      if (currentPassword) {
        const verified = await verifyCurrentPassword(current, currentPassword);
        if (verified !== true) return verified;
      }
      return saveSettings({
        ...current,
        systemUnlockEnabled: false,
      });
    }

    const verified = await verifyCurrentPassword(current, currentPassword);
    if (verified !== true) return verified;

    const status = await getSystemAuthStatusOnly();
    if (status.supported !== true) return { ok: false, error: "unsupported" };
    if (status.available !== true) return { ok: false, error: "unavailable" };

    return saveSettings({
      ...current,
      systemUnlockEnabled: true,
    });
  }

  function setLocked(reason) {
    if (!canLockFromSettings(getSettings())) {
      return getRuntimeState();
    }
    const nextState = runtimeBridge.lock(reason);
    syncIdleTimer();
    broadcast("netcatty:appLock:runtimeStateChanged", nextState);
    return nextState;
  }

  async function requestUnlock(password) {
    const current = getSettings();
    if (!canLockFromSettings(current)) {
      const nextState = runtimeBridge.unlock();
      syncIdleTimer();
      broadcast("netcatty:appLock:runtimeStateChanged", nextState);
      return { ok: true };
    }
    if (!password) {
      return { ok: false, error: "empty" };
    }

    const verified = await verifyAppLockPassword(password, current.passwordVerifier);
    if (!verified) {
      return { ok: false, error: "incorrect" };
    }

    const nextState = runtimeBridge.unlock();
    syncIdleTimer();
    broadcast("netcatty:appLock:runtimeStateChanged", nextState);
    return { ok: true };
  }

  async function requestSystemUnlock() {
    const current = getSettings();
    if (!canLockFromSettings(current)) return { ok: false, error: "unavailable" };
    if (current.systemUnlockEnabled !== true) return { ok: false, error: "disabled" };
    if (runtimeBridge.getState().locked !== true) return { ok: false, error: "not-locked" };

    const status = await getSystemAuthStatusOnly();
    if (status.supported !== true) return { ok: false, error: "unsupported" };
    if (status.available !== true) return { ok: false, error: "unavailable" };
    if (!systemAuthBridge || typeof systemAuthBridge.requestUnlock !== "function") {
      return { ok: false, error: "unsupported" };
    }

    const result = await systemAuthBridge.requestUnlock();
    if (!result || result.ok !== true) {
      return {
        ok: false,
        error: result?.error || "failed",
      };
    }

    const nextState = runtimeBridge.unlock();
    syncIdleTimer();
    broadcast("netcatty:appLock:runtimeStateChanged", nextState);
    return { ok: true };
  }

  function reportActivity(timestamp = Date.now()) {
    const nextState = runtimeBridge.recordActivity(timestamp);
    syncIdleTimer();
    return nextState;
  }

  function registerHandlers(ipcMain) {
    ipcMain.handle("netcatty:appLock:getRuntimeState", () => getRuntimeState());
    ipcMain.handle("netcatty:appLock:getSettings", () => getSettings());
    ipcMain.handle("netcatty:appLock:setTimeoutMinutes", (_event, timeoutMinutes) =>
      setTimeoutMinutes(timeoutMinutes));
    ipcMain.handle("netcatty:appLock:requestEnable", () => requestEnable());
    ipcMain.handle("netcatty:appLock:requestDisable", (_event, currentPassword) =>
      requestDisable(currentPassword));
    ipcMain.handle("netcatty:appLock:requestReset", (_event, currentPassword) =>
      requestReset(currentPassword));
    ipcMain.handle("netcatty:appLock:requestPasswordChange", (_event, input) =>
      requestPasswordChange(input));
    ipcMain.handle("netcatty:appLock:setLocked", (_event, reason) => setLocked(reason));
    ipcMain.handle("netcatty:appLock:requestUnlock", (_event, password) =>
      requestUnlock(password));
    ipcMain.handle("netcatty:appLock:getSystemUnlockStatus", () =>
      getSystemUnlockStatus());
    ipcMain.handle("netcatty:appLock:setSystemUnlockEnabled", (_event, input) =>
      setSystemUnlockEnabled(input));
    ipcMain.handle("netcatty:appLock:requestSystemUnlock", () =>
      requestSystemUnlock());
    ipcMain.handle("netcatty:appLock:reportActivity", () => reportActivity());
  }

  return {
    getSettings,
    getRuntimeState,
    requestEnable,
    requestDisable,
    requestReset,
    requestPasswordChange,
    setTimeoutMinutes,
    setLocked,
    requestUnlock,
    getSystemUnlockStatus,
    setSystemUnlockEnabled,
    requestSystemUnlock,
    reportActivity,
    registerHandlers,
    syncIdleTimer,
  };
}
