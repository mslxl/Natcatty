"use strict";

const path = require("node:path");
const { randomUUID } = require("node:crypto");
const { createScriptRuntime } = require("../scripts/scriptRuntime.cjs");
const { stepsToJavaScript } = require("../scripts/scriptCodegen.cjs");
const {
  appendSessionOutput,
  getOrCreateBuffer,
  removeSessionBuffer,
} = require("../scripts/sessionOutputBuffer.cjs");
const { shellPromptPatterns } = require("../scripts/shellPromptPatterns.cjs");
const { addTerminalDataTap } = require("../bridges/emitTerminalSessionData.cjs");
const sessionLogStreamManager = require("./sessionLogStreamManager.cjs");

let sessions = null;
let electronModule = null;
let terminalBridge = null;
let terminalWorkerManager = null;
let getMainWindow = null;

/** @type {Map<string, object>} */
const runs = new Map();
/** @type {Map<string, object>} */
const recordings = new Map();
/** @type {Map<string, { resolve, reject, type, runId?: string }>} */
const pendingDialogs = new Map();
/** @type {Map<string, { resolve, reject, sessionId, runId?: string }>} */
const pendingScreenSnapshots = new Map();
/** @type {Map<string, symbol>} */
const scriptLogTokens = new Map();
/** @type {Map<string, Promise<void>>} */
const sessionRunChains = new Map();
/** @type {Map<string, { abort: (reason?: Error) => void }>} */
const runAbortControls = new Map();
/** @type {Map<string, { connected?: boolean, hostname?: string, username?: string }>} */
const rendererSessionMetaById = new Map();

function enqueueSessionRun(sessionId, task) {
  const previous = sessionRunChains.get(sessionId) || Promise.resolve();
  const next = previous
    .catch(() => {})
    .then(() => task());
  const settled = next.then(() => {}, () => {});
  sessionRunChains.set(sessionId, settled);
  settled.finally(() => {
    if (sessionRunChains.get(sessionId) === settled) {
      sessionRunChains.delete(sessionId);
    }
  });
  return next;
}

function init(deps) {
  sessions = deps.sessions;
  electronModule = deps.electronModule;
  terminalBridge = deps.terminalBridge;
  terminalWorkerManager = deps.terminalWorkerManager || null;
  getMainWindow = deps.getMainWindow;

  addTerminalDataTap((sessionId, data) => {
    appendSessionOutput(sessionId, data);
  });
  terminalWorkerManager?.addOutputTap?.((sessionId, data) => {
    appendSessionOutput(sessionId, data);
  });
}

function broadcastRuns() {
  const win = getMainWindow?.();
  if (!win?.webContents) return;
  win.webContents.send("netcatty:script:runs-updated", {
    runs: Array.from(runs.values()).map(serializeRun),
  });
}

function serializeRun(run) {
  const now = Date.now();
  const elapsedMs = run.endedAt
    ? run.endedAt - run.startedAt
    : Math.max(0, now - run.startedAt);
  return {
    runId: run.runId,
    scriptId: run.scriptId,
    scriptLabel: run.scriptLabel,
    sessionId: run.sessionId,
    status: run.status,
    startedAt: run.startedAt,
    endedAt: run.endedAt,
    currentStep: run.currentStep,
    stepIndex: run.stepIndex,
    totalSteps: run.totalSteps,
    progressMode: run.progressMode || "activity",
    activityLabel: run.activityLabel,
    progressLabel: run.progressLabel,
    progressCurrent: run.progressCurrent,
    progressTotal: run.progressTotal,
    elapsedMs,
    waitingFor: run.waitingFor,
    logs: run.logs.slice(-200),
    error: run.error,
  };
}

function isWorkerSessionOpen(sessionId) {
  return Boolean(sessionId && terminalWorkerManager?.hasOpenSession?.(sessionId));
}

function rememberRendererSessionMeta(sessionId, meta) {
  if (!sessionId || !meta || typeof meta !== "object") return;
  const prev = rendererSessionMetaById.get(sessionId) || {};
  rendererSessionMetaById.set(sessionId, { ...prev, ...meta });
}

function hasActiveOutputBuffer(sessionId) {
  try {
    return Boolean(String(getOrCreateBuffer(sessionId).getText() || "").trim());
  } catch {
    return false;
  }
}

function isSessionConnected(sessionId) {
  const session = sessions?.get(sessionId);
  if (session) {
    return session.status !== "disconnected";
  }
  const rendererMeta = rendererSessionMetaById.get(sessionId);
  return Boolean(
    rendererMeta?.connected
    || isWorkerSessionOpen(sessionId)
    || hasActiveOutputBuffer(sessionId),
  );
}

function getSessionMeta(sessionId) {
  const session = sessions?.get(sessionId);
  const rendererMeta = rendererSessionMetaById.get(sessionId);
  if (session) {
    return {
      connected: session.status !== "disconnected",
      hostname: session.hostname || session.hostLabel || rendererMeta?.hostname || "",
      username: session.username || rendererMeta?.username || "",
    };
  }
  if (isSessionConnected(sessionId)) {
    return {
      connected: true,
      hostname: rendererMeta?.hostname || "",
      username: rendererMeta?.username || "",
    };
  }
  return { connected: false, hostname: "", username: "" };
}

function notifyScriptSessionInput(sessionId, data) {
  const session = sessions?.get(sessionId);
  const webContents = session?.webContentsId
    ? electronModule.webContents.fromId(session.webContentsId)
    : getMainWindow?.()?.webContents;
  webContents?.send("netcatty:script:session-input", { sessionId, data });
}

function writeToSession(sessionId, data, options = {}) {
  const payload = {
    sessionId,
    data,
    automated: options.automated !== false,
  };
  const webContentsId = getMainWindow?.()?.webContents?.id;
  if (terminalWorkerManager) {
    // Mirror input-based log rewrites into the main-process stream manager
    // (see the netcatty:write forwarder in terminalBridge.registerHandlers);
    // the real write handler runs in the terminal worker process.
    sessionLogStreamManager.registerSudoAutofillInput(sessionId, data);
    terminalWorkerManager.send("netcatty:write", payload, { webContentsId });
  } else {
    terminalBridge?.writeToSession?.(
      { sender: getMainWindow?.()?.webContents },
      payload,
    );
  }
  if (options.automated !== false && data && data !== "\x03") {
    // Sending a command means later waits must observe post-input output, not
    // the startup viewport / preserved prompt that was seeded for #1960.
    getOrCreateBuffer(sessionId).invalidateStartupSeed();
    notifyScriptSessionInput(sessionId, data);
  }
}

function bufferFallbackSnapshot(sessionId) {
  return {
    rows: 24,
    cols: 80,
    currentRow: 0,
    lines: getOrCreateBuffer(sessionId).getText().split("\n"),
    // Not a real terminal viewport — full script buffer / scrollback only.
    source: "buffer-fallback",
  };
}

function isViewportSnapshot(snapshot) {
  return snapshot?.source !== "buffer-fallback";
}

function defaultDialogValue(type) {
  if (type === "confirm") return false;
  if (type === "prompt") return "";
  if (type === "form") return {};
  if (type === "waitForTimeout") return "abort";
  return undefined;
}

function settlePendingRunRequests(runId, options = {}) {
  const reject = options.reject === true;
  const reason = options.reason || new Error("Stopped by user");
  for (const [requestId, pending] of pendingScreenSnapshots.entries()) {
    if (pending.runId !== runId) continue;
    pendingScreenSnapshots.delete(requestId);
    if (reject) {
      pending.reject(reason);
    } else {
      pending.resolve(bufferFallbackSnapshot(pending.sessionId));
    }
  }
  for (const [requestId, pending] of pendingDialogs.entries()) {
    if (pending.runId !== runId) continue;
    pendingDialogs.delete(requestId);
    if (reject) {
      pending.reject(reason);
    } else {
      pending.resolve(defaultDialogValue(pending.type));
    }
  }
}

async function requestScreenSnapshot(sessionId, runId) {
  const session = sessions?.get(sessionId);
  const webContents = session?.webContentsId
    ? electronModule.webContents.fromId(session.webContentsId)
    : getMainWindow?.()?.webContents;
  if (!webContents) {
    return bufferFallbackSnapshot(sessionId);
  }

  const requestId = randomUUID();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pendingScreenSnapshots.delete(requestId);
      resolve(bufferFallbackSnapshot(sessionId));
    }, 3000);
    pendingScreenSnapshots.set(requestId, {
      sessionId,
      runId,
      resolve: (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      reject: (err) => {
        clearTimeout(timer);
        reject(err);
      },
    });
    webContents.send("netcatty:script:screen-snapshot-request", { requestId, sessionId });
  });
}

function showDialog(type, message, defaultValue, extras = {}, runId) {
  const win = getMainWindow?.();
  const webContents = win?.webContents;
  if (!webContents) {
    if (type === "confirm") return Promise.resolve(false);
    if (type === "prompt") return Promise.resolve(defaultValue || "");
    if (type === "form") return Promise.resolve({});
    if (type === "waitForTimeout") return Promise.resolve("abort");
    return Promise.resolve(undefined);
  }
  const requestId = randomUUID();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pendingDialogs.delete(requestId);
      reject(new Error("Dialog timed out"));
    }, 120000);
    pendingDialogs.set(requestId, {
      type,
      runId,
      resolve: (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      reject: (err) => {
        clearTimeout(timer);
        reject(err);
      },
    });
    webContents.send("netcatty:script:dialog-request", {
      requestId,
      type,
      message,
      defaultValue,
      ...extras,
    });
  });
}

function showWaitForTimeoutDialog(pattern, timeoutMs, runId) {
  return showDialog(
    "waitForTimeout",
    `Timed out waiting for "${pattern}" after ${timeoutMs}ms`,
    undefined,
    { pattern, timeoutMs },
    runId,
  );
}

async function stopScriptSessionLog(sessionId) {
  const token = scriptLogTokens.get(sessionId);
  if (!token) return;
  scriptLogTokens.delete(sessionId);
  await sessionLogStreamManager.stopStream(sessionId, token);
}

async function syncOutputBufferFromSnapshot(sessionId, runId, isAborted = () => false) {
  const buffer = getOrCreateBuffer(sessionId);
  const syncStartText = buffer.getText();
  try {
    const snapshot = await requestScreenSnapshot(sessionId, runId);
    if (isAborted()) return;
    const screenText = (snapshot.lines || []).join("\n");

    // Buffer-fallback snapshots are the full script output/scrollback, not the
    // visible viewport. Seeding them as waitable would rematch scrolled-off
    // text (#1821). Keep the consumed baseline for that path — but only through
    // the pre-sync length so bytes that arrived during the await stay fresh
    // (same trailingFresh idea as the viewport path).
    if (!isViewportSnapshot(snapshot) || !screenText) {
      if (isAborted()) return;
      buffer.markOutputConsumedThrough(syncStartText.length, {
        preserveTailPatterns: shellPromptPatterns(),
      });
      return;
    }

    // Seed the script buffer from the current visible screen so waits can match
    // text already on screen (e.g. bastion "SSH资源" near the top of a long menu).
    // Marking the whole buffer consumed made those waits time out (#1960).
    // If live output arrived during the snapshot IPC round-trip (BEL keepalives,
    // etc.), keep that trailing fresh data after the viewport instead of
    // discarding the snapshot.
    const currentText = buffer.getText();
    const trailingFresh = currentText.startsWith(syncStartText)
      ? currentText.slice(syncStartText.length)
      : "";
    if (isAborted()) return;
    buffer.replaceWithVisibleScreen(screenText, trailingFresh, syncStartText);
  } catch {
    if (isAborted()) return;
    // Snapshot failed: baseline existing buffer so waits require new output.
    if (buffer.getText() === syncStartText) {
      buffer.markOutputConsumedThrough(syncStartText.length, {
        preserveTailPatterns: shellPromptPatterns(),
      });
    }
  }
}

async function runScriptOnSession({
  runId,
  scriptId,
  scriptLabel,
  sessionId,
  content,
  permissionMode = "auto",
  sessionMeta,
}) {
  rememberRendererSessionMeta(sessionId, sessionMeta);
  const run = {
    runId,
    scriptId,
    scriptLabel,
    sessionId,
    status: "running",
    startedAt: Date.now(),
    endedAt: undefined,
    currentStep: undefined,
    stepIndex: 0,
    totalSteps: undefined,
    progressMode: "activity",
    activityLabel: undefined,
    progressLabel: undefined,
    progressCurrent: undefined,
    progressTotal: undefined,
    waitingFor: undefined,
    logs: [],
    paused: false,
    aborted: false,
  };
  runs.set(runId, run);
  broadcastRuns();

  let abortRun = () => {};
  const abortPromise = new Promise((_, reject) => {
    abortRun = (reason) => reject(reason || new Error("Script stopped"));
  });
  abortPromise.catch(() => {});
  runAbortControls.set(runId, { abort: abortRun });

  const runtime = createScriptRuntime({
    sessionId,
    runId,
    appVersion: electronModule?.app?.getVersion?.(),
    appendLog: (id, message) => {
      const entry = runs.get(id);
      if (!entry || entry.aborted || entry.endedAt) return;
      entry.logs.push({ at: Date.now(), message });
      broadcastRuns();
    },
    writeToSession: (sid, data, options) => {
      if (run.aborted || run.endedAt) return;
      writeToSession(sid, data, options);
    },
    getOutputBuffer: getOrCreateBuffer,
    getScreenSnapshot: (sid) => requestScreenSnapshot(sid, runId),
    getSessionMeta,
    showDialog: (type, message, defaultValue, extras) => showDialog(type, message, defaultValue, extras, runId),
    showWaitForTimeoutDialog: (pattern, timeoutMs) => showWaitForTimeoutDialog(pattern, timeoutMs, runId),
    disconnectSession: async (sid) => {
      if (terminalWorkerManager) {
        terminalWorkerManager.send("netcatty:close", { sessionId: sid });
      } else {
        terminalBridge?.closeSession?.({ sender: {} }, { sessionId: sid });
      }
    },
    startSessionLog: async (sid, logPath) => {
      const session = sessions?.get(sid);
      const defaultDir = electronModule?.app?.getPath?.("documents") || process.cwd();
      const filePath = logPath
        ? path.resolve(String(logPath))
        : path.join(defaultDir, `netcatty-script-${Date.now()}.log`);
      const result = sessionLogStreamManager.startStreamToFile(sid, {
        filePath,
        hostLabel: session?.hostname || session?.hostLabel || "script",
        format: "raw",
        stopRequiresToken: true,
      });
      if (!result.ok) {
        throw new Error(result.error || "Failed to start script log");
      }
      scriptLogTokens.set(sid, result.token);
    },
    stopSessionLog: async (sid) => {
      await stopScriptSessionLog(sid);
    },
    isPaused: () => Boolean(runs.get(runId)?.paused),
    isAborted: () => Boolean(runs.get(runId)?.aborted),
    permissionMode,
    startedAt: run.startedAt,
    onStatusChange: (id, patch) => {
      const entry = runs.get(id);
      if (!entry || entry.aborted || entry.endedAt) return;
      Object.assign(entry, patch);
      broadcastRuns();
    },
  });

  try {
    const operationPromise = (async () => {
      await syncOutputBufferFromSnapshot(sessionId, runId, () => run.aborted);
      await runtime.execute(content);
    })();
    operationPromise.catch(() => {});
    await Promise.race([
      operationPromise,
      abortPromise,
    ]);
    if (run.aborted) {
      run.status = "failed";
      run.error = run.error || "Stopped by user";
    } else {
      run.status = "completed";
    }
    run.endedAt = Date.now();
    run.progressMode = "activity";
    run.progressLabel = undefined;
    run.progressCurrent = undefined;
    run.progressTotal = undefined;
  } catch (err) {
    run.status = "failed";
    run.endedAt = Date.now();
    run.progressMode = "activity";
    run.progressLabel = undefined;
    run.progressCurrent = undefined;
    run.progressTotal = undefined;
    run.error = err?.message || String(err);
    run.logs.push({ at: Date.now(), message: run.error });
  } finally {
    settlePendingRunRequests(runId, {
      reject: run.aborted,
      reason: new Error("Stopped by user"),
    });
    await stopScriptSessionLog(sessionId);
    runAbortControls.delete(runId);
    broadcastRuns();
  }
}

async function handleScriptRun(_event, payload = {}) {
  const {
    scriptId,
    scriptLabel,
    content,
    sessionId,
    sessionIds,
    mode = "parallel",
    permissionMode = "auto",
  } = payload;
  const targets = Array.isArray(sessionIds) && sessionIds.length > 0
    ? sessionIds
    : sessionId
      ? [sessionId]
      : [];
  if (targets.length === 0) {
    throw new Error("No target session for script run");
  }
  if (!content || !String(content).trim()) {
    throw new Error("Script content is empty");
  }

  const runIds = [];
  const queueRun = (sid) => {
    const runId = randomUUID();
    runIds.push(runId);
    return enqueueSessionRun(sid, () => runScriptOnSession({
      runId,
      scriptId,
      scriptLabel,
      sessionId: sid,
      content,
      permissionMode,
      sessionMeta: payload.sessionMeta,
    }));
  };

  if (mode === "sequential") {
    for (const sid of targets) {
      await queueRun(sid);
    }
  } else {
    await Promise.all(targets.map((sid) => queueRun(sid)));
  }

  return { runIds, runId: runIds[0] };
}

function handleScriptStop(_event, payload = {}) {
  const run = runs.get(payload.runId);
  if (!run) return { ok: false };
  run.aborted = true;
  run.paused = false;
  run.status = "failed";
  run.error = "Stopped by user";
  run.endedAt = Date.now();
  run.waitingFor = undefined;
  getOrCreateBuffer(run.sessionId).abortWaiters("Stopped by user");
  settlePendingRunRequests(run.runId, {
    reject: false,
    reason: new Error("Stopped by user"),
  });
  runAbortControls.get(run.runId)?.abort(new Error("Stopped by user"));
  broadcastRuns();
  return { ok: true };
}

function handleScriptPause(_event, payload = {}) {
  const run = runs.get(payload.runId);
  if (!run) return { ok: false };
  run.paused = true;
  run.status = "paused";
  broadcastRuns();
  return { ok: true };
}

function handleScriptResume(_event, payload = {}) {
  const run = runs.get(payload.runId);
  if (!run) return { ok: false };
  run.paused = false;
  run.status = "running";
  broadcastRuns();
  return { ok: true };
}

function handleScriptGetRuns(_event, payload = {}) {
  const all = Array.from(runs.values()).map(serializeRun);
  if (payload.sessionId) {
    return all.filter((run) => run.sessionId === payload.sessionId);
  }
  return all;
}

function handleScriptDialogResponse(_event, payload = {}) {
  const pending = pendingDialogs.get(payload.requestId);
  if (!pending) return { ok: false };
  pendingDialogs.delete(payload.requestId);
  if (payload.cancelled) {
    pending.reject(new Error("Dialog cancelled"));
    return { ok: true };
  }
  if (pending.type === "confirm") {
    pending.resolve(Boolean(payload.value));
  } else if (pending.type === "prompt") {
    pending.resolve(typeof payload.value === "string" ? payload.value : "");
  } else if (pending.type === "form") {
    pending.resolve(payload.value && typeof payload.value === "object" ? payload.value : {});
  } else if (pending.type === "waitForTimeout") {
    pending.resolve(typeof payload.value === "string" ? payload.value : "abort");
  } else {
    pending.resolve(undefined);
  }
  return { ok: true };
}

function handleScriptScreenSnapshotResponse(_event, payload = {}) {
  const pending = pendingScreenSnapshots.get(payload.requestId);
  if (!pending) return { ok: false };
  pendingScreenSnapshots.delete(payload.requestId);
  pending.resolve(payload.snapshot || bufferFallbackSnapshot(pending.sessionId));
  return { ok: true };
}

function handleRecordingStart(_event, payload = {}) {
  const { sessionId } = payload;
  if (!sessionId) throw new Error("sessionId required");
  recordings.set(sessionId, {
    sessionId,
    startedAt: Date.now(),
    steps: [],
    lastTimestamp: Date.now(),
  });
  return { ok: true };
}

function handleRecordingStop(_event, payload = {}) {
  const { sessionId } = payload;
  const recording = recordings.get(sessionId);
  if (!recording) {
    return { steps: [], code: "" };
  }
  recordings.delete(sessionId);
  const code = stepsToJavaScript(recording.steps, new Date(recording.startedAt).toISOString());
  return { steps: recording.steps, code };
}

function handleRecordingAppendStep(_event, payload = {}) {
  const { sessionId, step } = payload;
  const recording = recordings.get(sessionId);
  if (!recording || !step) return { ok: false };
  const now = Date.now();
  const gap = now - recording.lastTimestamp;
  if (gap > 1000 && step.type === "send") {
    recording.steps.push({ type: "sleep", value: gap });
  }
  recording.steps.push(step);
  recording.lastTimestamp = now;
  return { ok: true };
}

function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:script:run", handleScriptRun);
  ipcMain.handle("netcatty:script:stop", handleScriptStop);
  ipcMain.handle("netcatty:script:pause", handleScriptPause);
  ipcMain.handle("netcatty:script:resume", handleScriptResume);
  ipcMain.handle("netcatty:script:get-runs", handleScriptGetRuns);
  ipcMain.handle("netcatty:script:dialog-response", handleScriptDialogResponse);
  ipcMain.handle("netcatty:script:screen-snapshot-response", handleScriptScreenSnapshotResponse);
  ipcMain.handle("netcatty:script:recording:start", handleRecordingStart);
  ipcMain.handle("netcatty:script:recording:stop", handleRecordingStop);
  ipcMain.handle("netcatty:script:recording:append-step", handleRecordingAppendStep);
}

module.exports = {
  init,
  registerHandlers,
  appendSessionOutput,
  removeSessionBuffer,
};
