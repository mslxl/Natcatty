"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const { readFileSync } = require("node:fs");
const { Duplex } = require("node:stream");
const Module = require("node:module");
const keyboardInteractiveHandler = require("./keyboardInteractiveHandler.cjs");

function createSender(onSend = () => {}) {
  return {
    id: 1,
    isDestroyed: () => false,
    send: onSend,
  };
}

function loadBridgeWithMocks(t, { systemAgent = false, chainError = null } = {}) {
  const originalLoad = Module._load;
  let capturedChainOptions = null;
  let capturedConnectOptions = null;
  let connectedClient = null;
  let capturedSystemAgentOptions = null;

  class MockSshClient extends EventEmitter {
    constructor() {
      super();
      this.socketTimeouts = [];
      this._sock = {
        setTimeout: (value) => this.socketTimeouts.push(value),
      };
    }

    connect(options) {
      this.options = options;
      connectedClient = this;
      capturedConnectOptions = options;
      setImmediate(() => {
        this.emit("connect");
        this.emit("ready");
      });
    }

    forwardOut(_srcIP, _srcPort, _dstHost, _dstPort, callback) {
      callback(null, new Duplex({
        read() {},
        write(_chunk, _encoding, done) {
          done();
        },
      }));
    }

    end() {
      this.emit("close");
    }
  }

  Module._load = function patchedLoad(request, parent, isMain) {
    if (request === "ssh2") {
      return {
        Client: MockSshClient,
        utils: {
          parseKey: () => null,
        },
      };
    }
    if (request === "./sshBridge.cjs") {
      return {
        buildAlgorithms: () => ({}),
        connectThroughChain: async (_event, options, _jumpHosts, _hostname, _port, sessionId) => {
          capturedChainOptions = options;
          if (chainError) {
            const requestId = keyboardInteractiveHandler.generateRequestId("jump-host");
            keyboardInteractiveHandler.storeRequest(
              requestId,
              () => {},
              _event.sender.id,
              sessionId,
              _event.sender,
            );
            throw chainError;
          }
          return {
            socket: new Duplex({
              read() {},
              write(_chunk, _encoding, done) {
                done();
              },
            }),
            connections: [],
          };
        },
      };
    }
    if (request === "./sshAuthHelper.cjs" && systemAgent) {
      const helper = originalLoad.call(this, request, parent, isMain);
      return {
        ...helper,
        findAllDefaultPrivateKeys: async () => [{
          keyName: "id_ed25519",
          keyPath: "/home/alice/.ssh/id_ed25519",
          privateKey: "PRIVATE KEY",
        }],
        prepareSystemSshAgentForAuth: async (options) => {
          capturedSystemAgentOptions = options;
          return {
            getIdentities(callback) { callback(null, []); },
            sign() {},
          };
        },
      };
    }
    return originalLoad.call(this, request, parent, isMain);
  };

  const bridgePath = require.resolve("./portForwardingBridge.cjs");
  delete require.cache[bridgePath];
  const bridge = require("./portForwardingBridge.cjs");

  t.after(() => {
    Module._load = originalLoad;
    delete require.cache[bridgePath];
  });

  return {
    bridge,
    getCapturedChainOptions: () => capturedChainOptions,
    getCapturedConnectOptions: () => capturedConnectOptions,
    getConnectedClient: () => connectedClient,
    getCapturedSystemAgentOptions: () => capturedSystemAgentOptions,
  };
}

test("port forwarding routes jump-host keyboard-interactive prompts through the external scope", async (t) => {
  const {
    bridge,
    getCapturedChainOptions,
    getCapturedConnectOptions,
    getConnectedClient,
  } = loadBridgeWithMocks(t);
  const event = { sender: createSender() };

  try {
    const knownHosts = [{
      id: "kh-jump",
      hostname: "jump.internal",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "trusted-jump-fingerprint",
    }];
    const result = await bridge.startPortForward(event, {
      tunnelId: "pf-jump-scope",
      type: "local",
      localPort: 0,
      bindAddress: "127.0.0.1",
      remoteHost: "127.0.0.1",
      remotePort: 3306,
      hostname: "db.internal",
      port: 22,
      username: "dbuser",
      password: "target-password",
      sshTcpConnectTimeoutMs: 45_000,
      sshAuthReadyTimeoutMs: 300_000,
      knownHosts,
      jumpHosts: [{
        hostname: "jump.internal",
        port: 22,
        username: "jumpuser",
        password: "jump-password",
      }],
    });

    assert.equal(result.success, true);
    assert.equal(getCapturedChainOptions()?._keyboardInteractiveScope, "external");
    assert.equal(getCapturedChainOptions()?.knownHosts, knownHosts);
    assert.equal(getCapturedChainOptions()?.sshTcpConnectTimeoutMs, 45_000);
    assert.equal(getCapturedChainOptions()?.sshAuthReadyTimeoutMs, 300_000);
    assert.equal(getCapturedConnectOptions()?.timeout, 45_000);
    assert.equal(getCapturedConnectOptions()?.readyTimeout, 0);
    assert.deepEqual(getConnectedClient()?.socketTimeouts, [0]);
  } finally {
    await bridge.stopPortForward(event, { tunnelId: "pf-jump-scope" });
  }
});

test("port forwarding forwards target hostId to keyboard-interactive prompts", () => {
  const source = readFileSync(require.resolve("./portForwardingBridge.cjs"), "utf8");
  assert.match(source, /hostname,\s*hostId,\s*port = 22,/);
  assert.match(
    source,
    /conn\.on\("keyboard-interactive", createKeyboardInteractiveHandler\(\{\s*sender,\s*sessionId: tunnelId,\s*hostId,/,
  );
});

test("jump-host startup failures clear pending keyboard-interactive prompts", async (t) => {
  const sent = [];
  const { bridge } = loadBridgeWithMocks(t, { chainError: new Error("jump auth timeout") });
  const event = { sender: createSender((channel, payload) => sent.push({ channel, payload })) };

  await assert.rejects(
    bridge.startPortForward(event, {
      tunnelId: "pf-jump-failure",
      type: "local",
      localPort: 0,
      bindAddress: "127.0.0.1",
      remoteHost: "127.0.0.1",
      remotePort: 3306,
      hostname: "db.internal",
      username: "dbuser",
      jumpHosts: [{ hostname: "jump.internal", username: "jumpuser" }],
    }),
    /jump auth timeout/,
  );

  assert.equal(
    Array.from(keyboardInteractiveHandler.getRequests().values())
      .some((request) => request.sessionId === "pf-jump-failure"),
    false,
  );
  assert.equal(
    sent.some(({ channel, payload }) =>
      channel === "netcatty:keyboard-interactive-cancelled" &&
      payload.sessionId === "pf-jump-failure" &&
      payload.reason === "connection-ended"),
    true,
  );
});

test("strict target agent selection keeps default keys available to jump hosts", async (t) => {
  const { bridge, getCapturedChainOptions, getCapturedSystemAgentOptions } = loadBridgeWithMocks(t, { systemAgent: true });
  const event = { sender: createSender() };

  try {
    const result = await bridge.startPortForward(event, {
      tunnelId: "pf-strict-target",
      type: "local",
      localPort: 0,
      bindAddress: "127.0.0.1",
      remoteHost: "127.0.0.1",
      remotePort: 3306,
      hostname: "db.internal",
      port: 2222,
      username: "dbuser",
      useSshAgent: true,
      identitiesOnly: true,
      jumpHosts: [{
        hostname: "jump.internal",
        port: 22,
        username: "jumpuser",
      }],
    });

    assert.equal(result.success, true);
    assert.deepEqual(getCapturedChainOptions()?._defaultKeys, [{
      keyName: "id_ed25519",
      keyPath: "/home/alice/.ssh/id_ed25519",
      privateKey: "PRIVATE KEY",
    }]);
    assert.equal(getCapturedSystemAgentOptions()?.port, 2222);
  } finally {
    await bridge.stopPortForward(event, { tunnelId: "pf-strict-target" });
  }
});
