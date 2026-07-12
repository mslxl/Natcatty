"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const { Duplex } = require("node:stream");
const Module = require("node:module");

function createSender() {
  return {
    id: 1,
    isDestroyed: () => false,
    send() {},
  };
}

function loadBridgeWithMocks(t, { systemAgent = false } = {}) {
  const originalLoad = Module._load;
  let capturedChainOptions = null;

  class MockSshClient extends EventEmitter {
    connect(options) {
      this.options = options;
      setImmediate(() => this.emit("ready"));
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
        connectThroughChain: async (_event, options) => {
          capturedChainOptions = options;
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
        prepareSystemSshAgentForAuth: async () => ({
          getIdentities(callback) { callback(null, []); },
          sign() {},
        }),
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

  return { bridge, getCapturedChainOptions: () => capturedChainOptions };
}

test("port forwarding routes jump-host keyboard-interactive prompts through the external scope", async (t) => {
  const { bridge, getCapturedChainOptions } = loadBridgeWithMocks(t);
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
  } finally {
    await bridge.stopPortForward(event, { tunnelId: "pf-jump-scope" });
  }
});

test("strict target agent selection keeps default keys available to jump hosts", async (t) => {
  const { bridge, getCapturedChainOptions } = loadBridgeWithMocks(t, { systemAgent: true });
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
      port: 22,
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
  } finally {
    await bridge.stopPortForward(event, { tunnelId: "pf-strict-target" });
  }
});
