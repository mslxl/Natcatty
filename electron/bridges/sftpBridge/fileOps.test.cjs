const assert = require("node:assert/strict");
const test = require("node:test");

const { createFileOpsApi } = require("./fileOps.cjs");

test("home discovery accepts a virtual SFTP root when SSH exec is unavailable", async () => {
  const channel = {};
  const api = createFileOpsApi({
    sftpClients: new Map([["jumpserver", { sftp: channel }]]),
    throwIfAborted() {},
    requireSftpChannel: async () => channel,
    realpathAsync: async (resolvedChannel, remotePath) => {
      assert.equal(resolvedChannel, channel);
      assert.equal(remotePath, ".");
      return "/";
    },
  });

  const result = await api.getSftpHomeDir(null, { sftpId: "jumpserver" });

  assert.deepEqual(result, { success: true, homeDir: "/" });
});
