"use strict";

const {
  logTerminalInterruptDebug,
  normalizeTrace,
} = require("../bridges/terminalInterruptDiagnostics.cjs");

function createIpcMainHarness() {
  const handlers = new Map();
  const listeners = new Map();
  return {
    handlers,
    listeners,
    handle(channel, handler) {
      handlers.set(channel, handler);
    },
    on(channel, listener) {
      listeners.set(channel, listener);
    },
  };
}

function normalizeMessageEvent(eventOrMessage) {
  if (eventOrMessage && typeof eventOrMessage === "object" && "data" in eventOrMessage) {
    return {
      message: eventOrMessage.data,
      ports: eventOrMessage.ports || [],
    };
  }
  return {
    message: eventOrMessage,
    ports: eventOrMessage?.ports || [],
  };
}

function createOutputPortRegistry(parentPort) {
  const outputPorts = new Map();

  function closeSession(sessionId) {
    const port = outputPorts.get(sessionId);
    if (!port) return;
    outputPorts.delete(sessionId);
    try {
      port.close?.();
    } catch {
      // Ignore close races while tearing down a worker-owned output port.
    }
  }

  function post(sessionId, data) {
    const port = outputPorts.get(sessionId);
    if (!port) return false;
    try {
      port.postMessage({ sessionId, data });
      return true;
    } catch {
      closeSession(sessionId);
      return false;
    }
  }

  function open(sessionId, port, bufferedOutput = []) {
    if (!sessionId || !port) return;
    closeSession(sessionId);
    outputPorts.set(sessionId, port);
    try {
      port.start?.();
    } catch {
      // Some Electron MessagePort implementations do not require start().
    }
    for (const chunk of bufferedOutput || []) {
      post(sessionId, chunk);
    }
    parentPort.postMessage({ kind: "output-port-ready", sessionId });
  }

  function flush(sessionId, chunks = []) {
    for (const chunk of chunks || []) {
      if (!post(sessionId, chunk)) {
        parentPort.postMessage({ kind: "output", sessionId, data: chunk });
      }
    }
  }

  return {
    open,
    post,
    flush,
    closeSession,
  };
}

function createSender(parentPort, webContentsId, outputPorts) {
  return {
    id: webContentsId,
    isDestroyed() {
      return false;
    },
    send(channel, payload) {
      if (channel === "netcatty:data") {
        if (outputPorts?.post?.(payload?.sessionId, payload?.data)) {
          return;
        }
        parentPort.postMessage({
          kind: "output",
          sessionId: payload?.sessionId,
          data: payload?.data,
        });
        return;
      }
      if (channel === "netcatty:exit" && payload?.sessionId) {
        outputPorts?.closeSession?.(payload.sessionId);
      }
      parentPort.postMessage({
        kind: "renderer-event",
        webContentsId,
        channel,
        payload,
      });
    },
  };
}

function createTerminalWorkerRuntime(options = {}) {
  const {
    parentPort,
    registerBridges,
  } = options;
  const ipcMain = createIpcMainHarness();
  let started = false;
  const outputPorts = createOutputPortRegistry(parentPort);

  async function handleRequest(message) {
    const handler = ipcMain.handlers.get(message.channel);
    if (!handler) {
      parentPort.postMessage({
        kind: "response",
        requestId: message.requestId,
        error: `No terminal worker handler registered for ${message.channel}`,
      });
      return;
    }
    try {
      const result = await handler({
        sender: createSender(parentPort, message.webContentsId, outputPorts),
      }, message.payload);
      parentPort.postMessage({
        kind: "response",
        requestId: message.requestId,
        result,
      });
    } catch (err) {
      parentPort.postMessage({
        kind: "response",
        requestId: message.requestId,
        error: err?.message || String(err),
      });
    }
  }

  function handleSend(message) {
    const listener = ipcMain.listeners.get(message.channel);
    if (!listener) return;
    if (message.channel === "netcatty:interrupt") {
      const trace = normalizeTrace(message.payload);
      logTerminalInterruptDebug("worker-received-send", {
        channel: message.channel,
        webContentsId: message.webContentsId,
      }, trace);
    }
    if (message.channel === "netcatty:close" && message.payload?.sessionId) {
      outputPorts.closeSession(message.payload.sessionId);
    }
    listener({
      sender: createSender(parentPort, message.webContentsId, outputPorts),
    }, message.payload);
  }

  function handleMessage(eventOrMessage) {
    const { message, ports } = normalizeMessageEvent(eventOrMessage);
    if (message?.kind === "output-port") {
      outputPorts.open(message.sessionId, ports?.[0], message.bufferedOutput);
      return;
    }
    if (message?.kind === "output-flush") {
      outputPorts.flush(message.sessionId, message.chunks);
      return;
    }
    if (message?.kind === "close-output-port") {
      outputPorts.closeSession(message.sessionId);
      return;
    }
    if (message?.kind === "request") {
      void handleRequest(message);
      return;
    }
    if (message?.kind === "send") {
      handleSend(message);
    }
  }

  function start() {
    if (started) return;
    started = true;
    registerBridges?.(ipcMain);
    parentPort.on("message", handleMessage);
  }

  return {
    start,
    ipcMain,
    createSender(webContentsId) {
      return createSender(parentPort, webContentsId, outputPorts);
    },
  };
}

module.exports = {
  createTerminalWorkerRuntime,
  createOutputPortRegistry,
};
