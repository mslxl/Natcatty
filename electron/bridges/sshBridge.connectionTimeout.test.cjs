const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const Module = require("node:module");

function loadBridgeWithMockedSsh2(t) {
  const bridgePath = require.resolve("./sshBridge.cjs");
  const authHelperPath = require.resolve("./sshAuthHelper.cjs");
  const originalLoad = Module._load;

  class MockSSHClient extends EventEmitter {
    constructor() {
      super();
      MockSSHClient.instances.push(this);
      this._sock = {
        timeouts: [],
        setTimeout: (ms) => {
          this._sock.timeouts.push(ms);
        },
      };
      this.connectOpts = null;
      this.destroyed = false;
      this.ended = false;
    }

    connect(opts) {
      this.connectOpts = opts;
      setImmediate(() => {
        this.emit("connect");
        if (MockSSHClient.emitSocketTimeout) this.emit("timeout");
      });
    }

    end() {
      this.ended = true;
    }

    destroy() {
      this.destroyed = true;
    }
  }
  MockSSHClient.instances = [];
  MockSSHClient.emitSocketTimeout = true;

  Module._load = function patchedLoad(request, parent, isMain) {
    if (request === "ssh2") {
      return {
        Client: MockSSHClient,
        utils: { parseKey: () => new Error("no key") },
      };
    }
    return originalLoad.call(this, request, parent, isMain);
  };

  delete require.cache[bridgePath];
  delete require.cache[authHelperPath];
  const bridge = require("./sshBridge.cjs");

  t.after(() => {
    delete require.cache[bridgePath];
    delete require.cache[authHelperPath];
    Module._load = originalLoad;
  });

  return { bridge, MockSSHClient };
}

function makeSender() {
  return {
    id: 1,
    isDestroyed: () => false,
    sent: [],
    send(channel, payload) {
      this.sent.push({ channel, payload });
    },
  };
}

function registerStartHandler(bridge, sessions) {
  bridge.init({ sessions, electronModule: {} });
  const ipcMain = {
    handlers: new Map(),
    handle(channel, handler) {
      this.handlers.set(channel, handler);
    },
    on() {},
  };
  bridge.registerHandlers(ipcMain);
  return ipcMain.handlers.get("netcatty:start");
}

test("SSH start keeps TCP dial and auth readiness as separate timeout phases", async (t) => {
  const { bridge, MockSSHClient } = loadBridgeWithMockedSsh2(t);
  const sessions = new Map();
  const start = registerStartHandler(bridge, sessions);
  const sender = makeSender();

  await assert.rejects(
    () => start(
      { sender },
      {
        sessionId: "ssh-timeout",
        hostname: "192.0.2.10",
        username: "alice",
        port: 22,
        password: "secret",
        knownHosts: [],
      },
    ),
    /Connection timeout to 192\.0\.2\.10/,
  );

  assert.equal(MockSSHClient.instances.length, 1);
  const client = MockSSHClient.instances[0];
  assert.equal(client.connectOpts.timeout, 20_000);
  assert.equal(client.connectOpts.readyTimeout, 0);
  assert.deepEqual(client._sock.timeouts, [0]);
  assert.ok(sender.sent.some((message) => (
    message.channel === "netcatty:chain:progress"
    && message.payload.sessionId === "ssh-timeout"
    && message.payload.status === "tcp-connected"
  )));
  assert.ok(sender.sent.some((message) => (
    message.channel === "netcatty:exit"
    && message.payload.sessionId === "ssh-timeout"
    && message.payload.reason === "timeout"
  )));
});

test("SSH start applies configured TCP and authentication timeouts", async (t) => {
  const { bridge, MockSSHClient } = loadBridgeWithMockedSsh2(t);
  const start = registerStartHandler(bridge, new Map());

  await assert.rejects(
    () => start(
      { sender: makeSender() },
      {
        sessionId: "ssh-custom-timeout",
        hostname: "192.0.2.20",
        username: "alice",
        port: 22,
        password: "secret",
        knownHosts: [],
        sshTcpConnectTimeoutMs: 45_000,
        sshAuthReadyTimeoutMs: 300_000,
      },
    ),
    /Connection timeout to 192\.0\.2\.20/,
  );

  assert.equal(MockSSHClient.instances.length, 1);
  assert.equal(MockSSHClient.instances[0].connectOpts.timeout, 45_000);
  assert.equal(MockSSHClient.instances[0].connectOpts.readyTimeout, 0);
});

test("SSH authentication timeout starts only after TCP connects", async (t) => {
  const { bridge, MockSSHClient } = loadBridgeWithMockedSsh2(t);
  MockSSHClient.emitSocketTimeout = false;
  const start = registerStartHandler(bridge, new Map());
  const startedAt = Date.now();
  let guardTimer;
  const guard = new Promise((_, reject) => {
    guardTimer = setTimeout(
      () => reject(new Error("Authentication timeout test exceeded 3 seconds")),
      3_000,
    );
  });

  try {
    await assert.rejects(
      Promise.race([
        start(
          { sender: makeSender() },
          {
            sessionId: "ssh-auth-timeout",
            hostname: "192.0.2.30",
            username: "alice",
            port: 22,
            password: "secret",
            knownHosts: [],
            sshTcpConnectTimeoutMs: 45_000,
            sshAuthReadyTimeoutMs: 1_000,
          },
        ),
        guard,
      ]),
      /Connection timeout to 192\.0\.2\.30/,
    );
  } finally {
    clearTimeout(guardTimer);
  }

  assert.ok(Date.now() - startedAt >= 900);
  assert.equal(MockSSHClient.instances[0].connectOpts.timeout, 45_000);
});
