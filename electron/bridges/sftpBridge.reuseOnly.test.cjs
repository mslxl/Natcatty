const test = require("node:test");
const assert = require("node:assert/strict");
const Module = require("node:module");

function loadOpenConnectionWithSftpClient(SftpClient) {
  const openConnectionPath = require.resolve("./sftpBridge/openConnection.cjs");
  delete require.cache[openConnectionPath];

  const originalLoad = Module._load;
  Module._load = function patchedLoad(request, parent, isMain) {
    if (request === "ssh2-sftp-client") {
      return SftpClient;
    }
    return originalLoad.call(this, request, parent, isMain);
  };

  try {
    delete require.cache[openConnectionPath];
    return require("./sftpBridge/openConnection.cjs");
  } finally {
    Module._load = originalLoad;
  }
}

test("openSftp with reuseOnly throws instead of dialing fresh when source is missing", async () => {
  let dialedFresh = false;
  class TrackingSftpClient {
    constructor() {
      dialedFresh = true;
    }
  }

  const { createOpenConnectionApi } = loadOpenConnectionWithSftpClient(TrackingSftpClient);
  const api = createOpenConnectionApi({
    sessions: new Map(),
    findReusableSession: () => null,
    acquireConnectionRef: () => {},
    createSessionBackedSftpClient: () => {
      throw new Error("should not create reused client");
    },
    requireSftpChannel: async () => {},
    sendSftpProgress: () => {},
    sftpClients: new Map(),
    randomUUID: () => "conn-1",
    findAllDefaultPrivateKeysFromHelper: async () => [],
    getAvailableAgentSocket: async () => null,
    hasUsableProxy: () => false,
  });

  await assert.rejects(
    () => api.openSftp(
      { sender: { id: 1, isDestroyed: () => false, send: () => {} } },
      {
        sessionId: "sftp-1",
        hostname: "example.test",
        username: "alice",
        port: 22,
        sourceSessionId: "missing",
        reuseOnly: true,
      },
    ),
    /not reusable/,
  );
  assert.equal(dialedFresh, false);
});
