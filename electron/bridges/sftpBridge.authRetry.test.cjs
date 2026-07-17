// Created: 2026-07-17
// Purpose: Verify SFTP retries KI first after EDR removes keyboard-interactive.

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const Module = require("node:module");

/** Build a minimal renderer sender for SFTP progress and auth prompt IPC. */
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

/** Load the SFTP bridge with ssh2-sftp-client mocked for auth retry scenarios. */
function loadSftpBridgeWithAuthRetryMocks(t, options = {}) {
  const bridgePath = require.resolve("./sftpBridge.cjs");
  const authHelperPath = require.resolve("./sshAuthHelper.cjs");
  const originalLoad = Module._load;
  const realAuthHelper = require(authHelperPath);

  /** Mock SFTP client that simulates password rejection removing KI, then KI retry success. */
  class MockSftpClient extends EventEmitter {
    /** Create a mock ssh2-sftp-client instance with an embedded ssh2 client emitter. */
    constructor() {
      super();
      MockSftpClient.instances.push(this);
      this.sftp = null;
      this.client = new EventEmitter();
      this.client.setMaxListeners = () => {};
      this.client._sock = { setTimeout() {} };
      this.client.connect = (opts) => {
        this.client.connectOpts = opts;
        this.connect(opts);
      };
      this.client.sftp = (cb) => {
        setImmediate(() => cb(null, new EventEmitter()));
      };
      this.client.end = () => {
        this.client.ended = true;
      };
      this.client.destroy = () => {
        this.client.destroyed = true;
      };
    }

    /** Drive the fake server-side auth flow for each connection attempt. */
    connect(opts) {
      const attemptIndex = MockSftpClient.instances.length;
      const offered = [];
      this.authMethodsOffered = offered;
      setImmediate(() => {
        this.client.emit("connect");
        this.client.emit("handshake");
        const offerNext = (methodsLeft, partialSuccess) => {
          let nextMethod;
          opts.authHandler(methodsLeft, partialSuccess, (method) => {
            nextMethod = method;
            offered.push(method);
          });
          return nextMethod;
        };

        offerNext(null, null);
        const firstMethods = options.dynamicAuth
          ? ["publickey", "password", "keyboard-interactive"]
          : ["password", "keyboard-interactive"];
        const first = offerNext(firstMethods, false);
        if (attemptIndex === 1 && options.dynamicAuth) {
          assert.equal(first?.type, "publickey");
          const password = offerNext(["password", "keyboard-interactive"], false);
          assert.equal(password?.type, "password");
          offerNext(["publickey"], false);
          const err = new Error("All configured authentication methods failed");
          err.level = "client-authentication";
          this.client.emit("error", err);
          return;
        }
        if (attemptIndex === 1) {
          assert.equal(first, "password");
          offerNext(["publickey"], false);
          const err = new Error("All configured authentication methods failed");
          err.level = "client-authentication";
          this.client.emit("error", err);
          return;
        }

        if (options.dynamicAuth) {
          assert.equal(first?.type, "publickey");
          const keyboardInteractive = offerNext(["keyboard-interactive"], false);
          assert.equal(keyboardInteractive, "keyboard-interactive");
        } else {
          assert.equal(first, "keyboard-interactive");
        }
        this.client.emit("ready");
      });
    }

    /** Mark the high-level SFTP client as ended. */
    end() {
      this.ended = true;
    }
  }
  MockSftpClient.instances = [];

  Module._load = function patchedLoad(request, parent, isMain) {
    if (request === "ssh2-sftp-client") {
      return MockSftpClient;
    }
    if (request === "./sshAuthHelper.cjs") {
      return {
        ...realAuthHelper,
        findAllDefaultPrivateKeys: async () => options.defaultKeys || [],
        getAvailableAgentSocket: async () => null,
        prepareSystemSshAgentForAuth: async () => null,
      };
    }
    return originalLoad.call(this, request, parent, isMain);
  };

  delete require.cache[bridgePath];
  const bridge = require("./sftpBridge.cjs");

  t.after(() => {
    delete require.cache[bridgePath];
    Module._load = originalLoad;
  });

  return { bridge, MockSftpClient };
}

test("openSftp retries keyboard-interactive first when password rejection removes KI", async (t) => {
  const { bridge, MockSftpClient } = loadSftpBridgeWithAuthRetryMocks(t);
  const sftpClients = new Map();
  bridge.init({ sftpClients, sessions: new Map(), electronModule: {} });

  const result = await bridge.openSftp(
    { sender: makeSender() },
    {
      sessionId: "sftp-edr-mfa",
      hostname: "192.168.9.138",
      port: 22,
      username: "root",
      authMethod: "password",
      password: "saved-login-password",
      sshTcpConnectTimeoutMs: 45_000,
      sshAuthReadyTimeoutMs: 300_000,
    },
  );

  assert.equal(result.sftpId, "sftp-edr-mfa");
  assert.equal(MockSftpClient.instances.length, 2);
  assert.deepEqual(MockSftpClient.instances[0].authMethodsOffered, [
    "none",
    "password",
    false,
  ]);
  assert.deepEqual(MockSftpClient.instances[1].authMethodsOffered, [
    "none",
    "keyboard-interactive",
  ]);
  assert.equal(sftpClients.has("sftp-edr-mfa"), true);
});

test("openSftp retries keyboard-interactive after dynamic auth password rejection removes KI", async (t) => {
  const { bridge, MockSftpClient } = loadSftpBridgeWithAuthRetryMocks(t, {
    dynamicAuth: true,
    defaultKeys: [{
      privateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nmock-default-key\n-----END OPENSSH PRIVATE KEY-----",
      keyPath: "id_ed25519",
      keyName: "id_ed25519",
    }],
  });
  const sftpClients = new Map();
  bridge.init({ sftpClients, sessions: new Map(), electronModule: {} });

  const result = await bridge.openSftp(
    { sender: makeSender() },
    {
      sessionId: "sftp-edr-mfa-auto",
      hostname: "192.168.9.138",
      port: 22,
      username: "root",
      authMethod: "auto",
      password: "saved-login-password",
      sshTcpConnectTimeoutMs: 45_000,
      sshAuthReadyTimeoutMs: 300_000,
    },
  );

  assert.equal(result.sftpId, "sftp-edr-mfa-auto");
  assert.equal(MockSftpClient.instances.length, 2);
  assert.deepEqual(
    MockSftpClient.instances[0].authMethodsOffered.map((method) => (
      method?.type === "publickey" ? "publickey" : method?.type || method
    )),
    ["none", "publickey", "password", false],
  );
  assert.deepEqual(
    MockSftpClient.instances[1].authMethodsOffered.map((method) => (
      method?.type === "publickey" ? "publickey" : method?.type || method
    )),
    ["none", "publickey", "keyboard-interactive"],
  );
  assert.equal(sftpClients.has("sftp-edr-mfa-auto"), true);
});
