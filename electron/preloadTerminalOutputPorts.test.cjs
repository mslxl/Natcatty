const assert = require("node:assert/strict");
const test = require("node:test");

const {
  createTerminalOutputPortRegistry,
} = require("./preload/terminalOutputPorts.cjs");

function createFakeIpcRenderer() {
  const handlers = new Map();
  return {
    on(channel, handler) {
      handlers.set(channel, handler);
    },
    emitPort(sessionId, port) {
      handlers.get("netcatty:terminal-output-port")?.({ ports: [port] }, { sessionId });
    },
  };
}

function createFakePort() {
  return {
    closed: false,
    close() {
      this.closed = true;
    },
    emit(data) {
      this.onmessage?.({ data });
    },
  };
}

test("register attaches terminal output ports and delivers port messages", () => {
  const delivered = [];
  const ipcRenderer = createFakeIpcRenderer();
  const registry = createTerminalOutputPortRegistry({
    ipcRenderer,
    deliverToListeners(sessionId, data) {
      delivered.push({ sessionId, data });
    },
  });
  const port = createFakePort();

  registry.register();
  ipcRenderer.emitPort("session-1", port);
  port.emit({ sessionId: "session-1", data: "hello" });

  assert.deepEqual(delivered, [
    { sessionId: "session-1", data: "hello" },
  ]);
});

test("register closes stale replacement ports", () => {
  const ipcRenderer = createFakeIpcRenderer();
  const registry = createTerminalOutputPortRegistry({
    ipcRenderer,
    deliverToListeners() {},
  });
  const stale = createFakePort();
  const next = createFakePort();

  registry.register();
  ipcRenderer.emitPort("session-1", stale);
  ipcRenderer.emitPort("session-1", next);

  assert.equal(stale.closed, true);
  assert.equal(next.closed, false);
});

test("closed sessions drop terminal output port messages", () => {
  const delivered = [];
  const closedTerminalDataSessions = new Set(["session-1"]);
  const ipcRenderer = createFakeIpcRenderer();
  const registry = createTerminalOutputPortRegistry({
    ipcRenderer,
    closedTerminalDataSessions,
    deliverToListeners(sessionId, data) {
      delivered.push({ sessionId, data });
    },
  });
  const port = createFakePort();

  registry.register();
  ipcRenderer.emitPort("session-1", port);
  port.emit({ sessionId: "session-1", data: "late" });

  assert.deepEqual(delivered, []);
});

test("closeSession closes and removes a terminal output port", () => {
  const ipcRenderer = createFakeIpcRenderer();
  const registry = createTerminalOutputPortRegistry({
    ipcRenderer,
    deliverToListeners() {},
  });
  const port = createFakePort();

  registry.register();
  ipcRenderer.emitPort("session-1", port);
  registry.closeSession("session-1");

  assert.equal(port.closed, true);
  assert.equal(registry.hasSessionForTest("session-1"), false);
});
