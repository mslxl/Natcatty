"use strict";

const { trackEmitted } = require("./terminalFlowAck.cjs");

let getSession = null;
let outputChannel = null;

function configureTerminalSessionDataEmitter(options = {}) {
  getSession = typeof options.getSession === "function" ? options.getSession : null;
  outputChannel = options.outputChannel || null;
}

function emitTerminalSessionData(contents, sessionId, data) {
  if (getSession && sessionId && data) {
    const session = getSession(sessionId);
    if (session) {
      trackEmitted(session, typeof data === "string" ? data.length : 0);
    }
  }
  if (outputChannel?.send?.(sessionId, data)) return;
  contents?.send("netcatty:data", { sessionId, data });
}

module.exports = {
  configureTerminalSessionDataEmitter,
  emitTerminalSessionData,
};
