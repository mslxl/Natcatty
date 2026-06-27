"use strict";

const {
  execViaPty,
  execViaChannel,
  execViaRawPty,
} = require("../bridges/ai/ptyExec.cjs");
const { getFreshIdlePrompt } = require("../bridges/ai/shellUtils.cjs");

function cancelPtyExecsForSession(activePtyExecs, chatSessionId) {
  if (!chatSessionId) return;
  for (const [marker, entry] of activePtyExecs) {
    if (entry.chatSessionId !== chatSessionId) continue;
    try {
      if (typeof entry.cancel === "function") entry.cancel();
      else entry.cleanup?.();
    } catch {
      // Ignore cancellation races while the worker session is shutting down.
    }
    activePtyExecs.delete(marker);
  }
}

function createWorkerAiExecHandler({ sessions, activePtyExecs = new Map() }) {
  return async function handleWorkerAiExec(event, payload = {}) {
    const {
      sessionId,
      command,
      chatSessionId,
      commandTimeoutMs,
      sessionMeta,
      enforceWallTimeout,
    } = payload;
    const session = sessions?.get(sessionId);
    if (!session) {
      return { ok: false, error: "Session not found" };
    }

    const meta = sessionMeta || {};
    const sessionProtocol = session.protocol || session.type || meta.protocol || "";
    const isSshOrSerial = sessionProtocol === "ssh" || sessionProtocol === "serial";
    const isNetworkDevice = (meta.deviceType === "network" && isSshOrSerial) || sessionProtocol === "serial";
    const timeoutMs = Number.isFinite(commandTimeoutMs) ? commandTimeoutMs : 60000;

    if ((session.protocol === "local" || session.type === "local") && session.shellKind === "unknown") {
      return {
        ok: false,
        error: "AI execution is not supported for this local shell executable. Configure the local terminal to use bash/zsh/sh, fish, PowerShell/pwsh, or cmd.exe.",
      };
    }

    const ptyStream = session.stream || session.pty || session.proc;

    if (isNetworkDevice && ptyStream && typeof ptyStream.write === "function") {
      return execViaRawPty(ptyStream, command, {
        timeoutMs,
        trackForCancellation: activePtyExecs,
        chatSessionId,
        encoding: sessionProtocol === "serial" ? (session.serialEncoding || "utf8") : "utf8",
      });
    }

    if (ptyStream && typeof ptyStream.write === "function") {
      return execViaPty(ptyStream, command, {
        stripMarkers: true,
        trackForCancellation: activePtyExecs,
        timeoutMs,
        shellKind: session.shellKind,
        chatSessionId,
        expectedPrompt: getFreshIdlePrompt(session),
        typedInput: true,
        echoCommand: (rawCommand) => {
          event?.sender?.send?.("netcatty:data", {
            sessionId,
            data: `${rawCommand}\r\n`,
            syntheticEcho: true,
          });
        },
        enforceWallTimeout: enforceWallTimeout === true,
      });
    }

    if (isNetworkDevice) {
      return { ok: false, error: "Network device session has no writable PTY stream for command execution" };
    }

    const sshClient = session.sshClient || session.conn;
    if (sshClient && typeof sshClient.exec === "function") {
      return execViaChannel(sshClient, command, {
        timeoutMs,
        trackForCancellation: activePtyExecs,
        chatSessionId,
      });
    }

    if (session.protocol === "serial" && session.serialPort && typeof session.serialPort.write === "function") {
      if (session.ymodemActive || session.zmodemSentry?.isActive?.()) {
        return { ok: false, error: "Serial file transfer is already in progress" };
      }
      return execViaRawPty(session.serialPort, command, {
        timeoutMs,
        trackForCancellation: activePtyExecs,
        chatSessionId,
        encoding: session.serialEncoding || "utf8",
      });
    }

    return { ok: false, error: "No terminal stream or SSH client available for this session" };
  };
}

function registerWorkerAiExecHandlers(ipcMain, { sessions }) {
  const activePtyExecs = new Map();
  ipcMain.handle("netcatty:ai:exec", createWorkerAiExecHandler({
    sessions,
    activePtyExecs,
  }));
  ipcMain.on("netcatty:ai:catty:cancel", (_event, payload = {}) => {
    cancelPtyExecsForSession(activePtyExecs, payload.chatSessionId);
  });
}

module.exports = {
  cancelPtyExecsForSession,
  createWorkerAiExecHandler,
  registerWorkerAiExecHandlers,
};
