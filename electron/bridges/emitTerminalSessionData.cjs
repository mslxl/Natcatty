"use strict";

const {
  writeMirror,
  resizeMirror,
  serializeMirror,
  disposeMirror,
} = require("./terminalHeadlessMirror.cjs");

/**
 * Deliver terminal output to the renderer and optionally mirror it in a
 * headless xterm on the main process for fast hibernate snapshots.
 */
function emitTerminalSessionData(contents, sessionId, data, options = {}) {
  const { cols, rows, mirrorEnabled = true } = options;
  if (mirrorEnabled && data) {
    writeMirror(sessionId, data, cols, rows);
  }
  contents?.send("netcatty:data", { sessionId, data });
}

function registerTerminalMirrorHandlers(ipcMain) {
  ipcMain.handle("netcatty:mirror:snapshot", (_event, payload = {}) => {
    const sessionId = payload.sessionId;
    if (!sessionId) return { snapshot: "", alternateScreen: false };
    return serializeMirror(sessionId);
  });

  ipcMain.handle("netcatty:mirror:resize", (_event, payload = {}) => {
    const { sessionId, cols, rows } = payload;
    if (!sessionId || !cols || !rows) return { ok: false };
    resizeMirror(sessionId, cols, rows);
    return { ok: true };
  });

  ipcMain.handle("netcatty:mirror:dispose", (_event, payload = {}) => {
    const sessionId = payload.sessionId;
    if (!sessionId) return { ok: false };
    disposeMirror(sessionId);
    return { ok: true };
  });
}

module.exports = {
  emitTerminalSessionData,
  registerTerminalMirrorHandlers,
  disposeMirror,
};
