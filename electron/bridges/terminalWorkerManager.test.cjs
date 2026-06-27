const assert = require("node:assert/strict");
const EventEmitter = require("node:events");
const test = require("node:test");

const {
  createTerminalWorkerManager,
  isTerminalWorkerEnabled,
} = require("./terminalWorkerManager.cjs");

class FakeChild extends EventEmitter {
  constructor() {
    super();
    this.messages = [];
    this.transferLists = [];
    this.killed = false;
  }

  postMessage(message, transferList) {
    this.messages.push(message);
    this.transferLists.push(transferList || []);
  }

  kill() {
    this.killed = true;
  }
}

test("isTerminalWorkerEnabled defaults on and honors NETCATTY_TERMINAL_WORKER=0", () => {
  assert.equal(isTerminalWorkerEnabled({ env: {} }), true);
  assert.equal(isTerminalWorkerEnabled({ env: { NETCATTY_TERMINAL_WORKER: "0" } }), false);
});

test("request sends a worker command and resolves matching response", async () => {
  const child = new FakeChild();
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", { shell: "/bin/zsh" }, { webContentsId: 7 });
  assert.equal(child.messages.length, 1);
  assert.equal(child.messages[0].kind, "request");
  assert.equal(child.messages[0].channel, "netcatty:local:start");
  assert.deepEqual(child.messages[0].payload, { shell: "/bin/zsh" });
  assert.equal(child.messages[0].webContentsId, 7);

  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "local-1" },
  });

  assert.deepEqual(await promise, { sessionId: "local-1" });
});

test("request opens a terminal output port when a session starts", async () => {
  const child = new FakeChild();
  const opened = [];
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      openSession(sessionId, webContents) {
        opened.push({ sessionId, webContentsId: webContents.id });
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return { id };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });
  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "local-1" },
  });
  await promise;

  assert.deepEqual(opened, [{ sessionId: "local-1", webContentsId: 7 }]);
});

test("request transfers the output port to the worker when available", async () => {
  const child = new FakeChild();
  const outputPort = { label: "worker-output-port" };
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      openSession(sessionId, webContents, options) {
        assert.equal(sessionId, "local-1");
        assert.equal(webContents.id, 7);
        assert.deepEqual(options, { transferToWorker: true });
        return outputPort;
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return { id };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });
  child.emit("message", {
    kind: "output",
    sessionId: "local-1",
    data: "early",
  });
  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "local-1" },
  });
  await promise;

  assert.deepEqual(child.messages[1], {
    kind: "output-port",
    sessionId: "local-1",
    bufferedOutput: ["early"],
  });
  assert.deepEqual(child.transferLists[1], [outputPort]);
});

test("output-port-ready flushes output that arrived during port transfer", async () => {
  const child = new FakeChild();
  const outputPort = { label: "worker-output-port" };
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      openSession() {
        return outputPort;
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return { id };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });
  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "local-1" },
  });
  await promise;
  child.emit("message", {
    kind: "output",
    sessionId: "local-1",
    data: "during-transfer",
  });
  child.emit("message", {
    kind: "output-port-ready",
    sessionId: "local-1",
  });

  assert.deepEqual(child.messages[2], {
    kind: "output-flush",
    sessionId: "local-1",
    chunks: ["during-transfer"],
  });
});

test("falls back to netcatty:data when no output port is available", async () => {
  const child = new FakeChild();
  const sent = [];
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      openSession() {
        return false;
      },
      send() {
        return false;
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return {
            id,
            send(channel, payload) {
              sent.push({ id, channel, payload });
            },
          };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });
  child.emit("message", {
    kind: "output",
    sessionId: "local-1",
    data: "early",
  });
  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "local-1" },
  });
  await promise;

  assert.deepEqual(sent, [
    {
      id: 7,
      channel: "netcatty:data",
      payload: { sessionId: "local-1", data: "early" },
    },
  ]);
});

test("send posts fire-and-forget control commands to the worker", () => {
  const child = new FakeChild();
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  manager.send("netcatty:interrupt", { sessionId: "session-1" }, { webContentsId: 7 });

  assert.deepEqual(child.messages, [
    {
      kind: "send",
      channel: "netcatty:interrupt",
      payload: { sessionId: "session-1" },
      webContentsId: 7,
    },
  ]);
});

test("worker output is routed through the dedicated terminal output channel", () => {
  const child = new FakeChild();
  const routed = [];
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      send(sessionId, data) {
        routed.push({ sessionId, data });
        return true;
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  manager.ensureStarted();
  child.emit("message", {
    kind: "output",
    sessionId: "session-1",
    data: "hello",
  });

  assert.deepEqual(routed, [{ sessionId: "session-1", data: "hello" }]);
});

test("worker buffers early output until the output port is opened", async () => {
  const child = new FakeChild();
  const routed = [];
  let opened = false;
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      openSession() {
        opened = true;
      },
      send(sessionId, data) {
        if (!opened) return false;
        routed.push({ sessionId, data });
        return true;
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return { id };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });
  child.emit("message", {
    kind: "output",
    sessionId: "session-1",
    data: "hello",
  });
  assert.deepEqual(routed, []);

  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "session-1" },
  });
  await promise;

  assert.deepEqual(routed, [{ sessionId: "session-1", data: "hello" }]);
});

test("close immediately clears the output route and drops pending output", async () => {
  const child = new FakeChild();
  const closed = [];
  const routed = [];
  let opened = false;
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      openSession() {
        opened = true;
      },
      send(sessionId, data) {
        if (!opened) return false;
        routed.push({ sessionId, data });
        return true;
      },
      closeSession(sessionId) {
        closed.push(sessionId);
        opened = false;
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return { id };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });
  child.emit("message", {
    kind: "output",
    sessionId: "session-1",
    data: "old",
  });
  manager.send("netcatty:close", { sessionId: "session-1" }, { webContentsId: 7 });
  child.emit("message", {
    kind: "response",
    requestId: child.messages[0].requestId,
    result: { sessionId: "session-1" },
  });
  await promise;
  child.emit("message", {
    kind: "output",
    sessionId: "session-1",
    data: "late",
  });

  assert.deepEqual(closed, ["session-1"]);
  assert.deepEqual(routed, []);
});

test("worker renderer events are forwarded to their original webContents", () => {
  const child = new FakeChild();
  const forwarded = [];
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    electronModule: {
      webContents: {
        fromId(id) {
          return {
            send(channel, payload) {
              forwarded.push({ id, channel, payload });
            },
          };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  manager.ensureStarted();
  child.emit("message", {
    kind: "renderer-event",
    webContentsId: 7,
    channel: "netcatty:exit",
    payload: { sessionId: "session-1" },
  });

  assert.deepEqual(forwarded, [
    { id: 7, channel: "netcatty:exit", payload: { sessionId: "session-1" } },
  ]);
});

test("worker exit events close the session output route", () => {
  const child = new FakeChild();
  const closed = [];
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      closeSession(sessionId) {
        closed.push(sessionId);
      },
    },
    electronModule: {
      webContents: {
        fromId() {
          return { send() {} };
        },
      },
    },
    workerScriptPath: "/worker.cjs",
  });

  manager.ensureStarted();
  child.emit("message", {
    kind: "renderer-event",
    webContentsId: 7,
    channel: "netcatty:exit",
    payload: { sessionId: "session-1" },
  });

  assert.deepEqual(closed, ["session-1"]);
});

test("worker exit rejects pending requests and closes output routes", async () => {
  const child = new FakeChild();
  const closed = [];
  const manager = createTerminalWorkerManager({
    utilityProcess: {
      fork() {
        return child;
      },
    },
    terminalOutputChannel: {
      closeAll() {
        closed.push("all");
      },
    },
    workerScriptPath: "/worker.cjs",
  });
  const promise = manager.request("netcatty:local:start", {}, { webContentsId: 7 });

  child.emit("exit", 1);

  await assert.rejects(promise, /Terminal worker exited/);
  assert.deepEqual(closed, ["all"]);
});
