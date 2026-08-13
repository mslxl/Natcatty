const assert = require("node:assert/strict");
const test = require("node:test");

const { createFileOpsApi } = require("./fileOps.cjs");

test("home discovery accepts a virtual SFTP root when SSH exec is unavailable", async () => {
  const channel = {};
  const listed = [];
  const api = createFileOpsApi({
    sftpClients: new Map([["jumpserver", { sftp: channel }]]),
    throwIfAborted() {},
    requireSftpChannel: async () => channel,
    realpathAsync: async (resolvedChannel, remotePath) => {
      assert.equal(resolvedChannel, channel);
      assert.equal(remotePath, ".");
      return "/";
    },
    readdirAsync: async (resolvedChannel, remotePath) => {
      listed.push([resolvedChannel, remotePath]);
      return [{ filename: "data" }];
    },
  });

  const result = await api.getSftpHomeDir(null, { sftpId: "jumpserver" });

  assert.deepEqual(result, { success: true, homeDir: "/" });
  assert.deepEqual(listed, [[channel, "/"]]);
});

test("home discovery rejects non-listable root so candidate probing can run", async () => {
  const channel = {};
  const api = createFileOpsApi({
    sftpClients: new Map([["restricted", { sftp: channel }]]),
    throwIfAborted() {},
    requireSftpChannel: async () => channel,
    realpathAsync: async () => "/",
    readdirAsync: async () => {
      const error = new Error("Permission denied");
      error.code = "EACCES";
      throw error;
    },
  });

  const result = await api.getSftpHomeDir(null, { sftpId: "restricted" });

  assert.equal(result.success, false);
  assert.match(result.error || "", /Could not determine home directory/);
});

test("home discovery still accepts non-root realpath without listing", async () => {
  const channel = {};
  let readdirCalls = 0;
  const api = createFileOpsApi({
    sftpClients: new Map([["normal", { sftp: channel }]]),
    throwIfAborted() {},
    requireSftpChannel: async () => channel,
    realpathAsync: async () => "/home/deploy",
    readdirAsync: async () => {
      readdirCalls += 1;
      return [];
    },
  });

  const result = await api.getSftpHomeDir(null, { sftpId: "normal" });

  assert.deepEqual(result, { success: true, homeDir: "/home/deploy" });
  assert.equal(readdirCalls, 0);
});

test("statSftp follows symlinks and reports target size for resume sizing", async () => {
  const channel = { stat() {}, lstat() {} };
  let lstatCalls = 0;
  let statCalls = 0;
  const api = createFileOpsApi({
    sftpClients: new Map([["sftp-1", { sftp: channel }]]),
    path: require("node:path"),
    requireSftpChannel: async () => channel,
    resolveEncodingForRequest: () => "utf-8",
    encodePath: (remotePath) => remotePath,
    lstatAsync: async () => {
      lstatCalls += 1;
      return {
        size: 11,
        mode: 0o120777,
        mtime: 10,
        isDirectory: () => false,
        isSymbolicLink: () => true,
      };
    },
    statAsync: async () => {
      statCalls += 1;
      return {
        size: 42,
        mode: 0o100644,
        mtime: 20,
        isDirectory: () => false,
        isSymbolicLink: () => false,
      };
    },
    statResultFromAttrs: (attrs) => ({
      size: attrs.size,
      modifyTime: attrs.mtime * 1000,
      mode: attrs.mode,
      isDirectory: attrs.isDirectory(),
      isSymbolicLink: attrs.isSymbolicLink(),
    }),
  });

  const result = await api.statSftp(null, {
    sftpId: "sftp-1",
    path: "/usr/local/bin/tool",
  });

  assert.equal(statCalls, 1);
  assert.equal(lstatCalls, 0, "shared stat must follow for resume/sizing");
  assert.equal(result.type, "file");
  assert.equal(result.size, 42);
});

test("lstatSftp classifies symlinks without following the target", async () => {
  const channel = { lstat() {} };
  let lstatCalls = 0;
  let statCalls = 0;
  const api = createFileOpsApi({
    sftpClients: new Map([["sftp-1", { sftp: channel }]]),
    path: require("node:path"),
    requireSftpChannel: async () => channel,
    resolveEncodingForRequest: () => "utf-8",
    encodePath: (remotePath) => remotePath,
    lstatAsync: async () => {
      lstatCalls += 1;
      return {
        size: 11,
        mode: 0o120777,
        mtime: 10,
        isDirectory: () => false,
        isSymbolicLink: () => true,
      };
    },
    statAsync: async () => {
      statCalls += 1;
      return {
        size: 42,
        mode: 0o100644,
        mtime: 20,
        isDirectory: () => false,
        isSymbolicLink: () => false,
      };
    },
    statResultFromAttrs: (attrs) => ({
      size: attrs.size,
      modifyTime: attrs.mtime * 1000,
      mode: attrs.mode,
      isDirectory: attrs.isDirectory(),
      isSymbolicLink: attrs.isSymbolicLink(),
    }),
  });

  const result = await api.lstatSftp(null, {
    sftpId: "sftp-1",
    path: "/usr/local/bin/tool",
  });

  assert.equal(lstatCalls, 1);
  assert.equal(statCalls, 0, "must not follow the symlink with STAT");
  assert.equal(result.type, "symlink");
  assert.equal(result.size, 11);
});
