"use strict";

const { trackEmitted } = require("./terminalFlowAck.cjs");

let getSession = null;

function configureTerminalSessionDataEmitter(options = {}) {
  getSession = typeof options.getSession === "function" ? options.getSession : null;
}

function emitTerminalSessionData(contents, sessionId, data) {
  if (getSession && sessionId && data) {
    const session = getSession(sessionId);
    if (session) {
      trackEmitted(session, typeof data === "string" ? data.length : 0);
    }
  }
  contents?.send("netcatty:data", { sessionId, data });
}

module.exports = {
  configureTerminalSessionDataEmitter,
  emitTerminalSessionData,
};