"use strict";

/**
 * Session-backed SFTP clients (openForSession / terminal reuse) are not
 * ssh2-sftp-client instances. They must still expose pipelined fastPut so
 * uploadLocal / writeSftpBinaryWithProgress do not throw after serial put
 * was removed (#2449 fail-closed alignment).
 */

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const sftpBridge = require("./sftpBridge.cjs");
const tempDirBridge = require("./tempDirBridge.cjs");
const {
  TRANSFER_CHUNK_SIZE,
  UPLOAD_TRANSFER_CONCURRENCY,
} = require("./transferLimits.cjs");

function createSessionChannel(options = {}) {
  const fastPutCalls = [];
  const remoteFiles = new Map();
  const remoteMeta = new Map(); // path -> { mode, isSymlink }
  const chmodCalls = [];
  const channel = {
    // hasSftpChannelApi requires these four methods.
    readdir(_targetPath, callback) {
      callback(null, []);
    },
    mkdir(_targetPath, callback) {
      callback(null);
    },
    unlink(targetPath, callback) {
      remoteFiles.delete(targetPath);
      remoteMeta.delete(targetPath);
      callback(null);
    },
    stat(targetPath, callback) {
      const data = remoteFiles.get(targetPath);
      if (!data) {
        const err = new Error(`ENOENT ${targetPath}`);
        err.code = 2;
        callback(err);
        return;
      }
      const meta = remoteMeta.get(targetPath) || {};
      callback(null, {
        size: data.length,
        mode: meta.mode ?? 0o100644,
        isDirectory: () => false,
        isFile: () => true,
        isSymbolicLink: () => !!meta.isSymlink,
      });
    },
    lstat(targetPath, callback) {
      const meta = remoteMeta.get(targetPath);
      const data = remoteFiles.get(targetPath);
      if (!data && !meta) {
        const err = new Error(`ENOENT ${targetPath}`);
        err.code = 2;
        callback(err);
        return;
      }
      callback(null, {
        size: data ? data.length : 0,
        mode: meta?.isSymlink ? 0o120777 : (meta?.mode ?? 0o100644),
        isDirectory: () => false,
        isFile: () => !meta?.isSymlink,
        isSymbolicLink: () => !!meta?.isSymlink,
      });
    },
    readlink(targetPath, callback) {
      const meta = remoteMeta.get(targetPath);
      if (!meta?.isSymlink) {
        const err = new Error(`ENOENT ${targetPath}`);
        err.code = 2;
        callback(err);
        return;
      }
      callback(null, meta.linkPath || "/missing-target");
    },
    fastPut(localPath, remotePath, opts, callback) {
      fastPutCalls.push({
        localPath,
        remotePath,
        concurrency: opts?.concurrency,
        chunkSize: opts?.chunkSize,
      });
      if (typeof options.onFastPut === "function") {
        const intercept = options.onFastPut(localPath, remotePath);
        if (intercept?.error) {
          queueMicrotask(() => callback(intercept.error));
          return;
        }
      }
      try {
        const data = fs.readFileSync(localPath);
        remoteFiles.set(remotePath, data);
        if (!remoteMeta.has(remotePath)) {
          remoteMeta.set(remotePath, { mode: 0o100644 });
        }
        if (typeof opts?.step === "function") {
          opts.step(data.length, data.length, data.length);
        }
        queueMicrotask(() => callback(null));
      } catch (err) {
        queueMicrotask(() => callback(err));
      }
    },
    rename(from, to, callback) {
      if (!remoteFiles.has(from)) {
        const err = new Error(`ENOENT ${from}`);
        err.code = 2;
        callback(err);
        return;
      }
      const sourceMeta = remoteMeta.get(from);
      remoteFiles.set(to, remoteFiles.get(from));
      remoteFiles.delete(from);
      remoteMeta.set(to, sourceMeta || { mode: 0o100644 });
      remoteMeta.delete(from);
      callback(null);
    },
    chmod(targetPath, mode, callback) {
      chmodCalls.push({ targetPath, mode });
      const prev = remoteMeta.get(targetPath) || {};
      remoteMeta.set(targetPath, { ...prev, mode });
      callback(null);
    },
    end() {},
  };
  return { channel, fastPutCalls, remoteFiles, remoteMeta, chmodCalls };
}

test("downloadSftpToLocal aborts while initial SFTP metadata is stalled", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-sftp-stat-abort-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  let markStatStarted;
  const statStarted = new Promise((resolve) => { markStatStarted = resolve; });
  const client = {
    sftp: createSessionChannel().channel,
    stat() {
      markStatStarted();
      return new Promise(() => {});
    },
  };
  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map([["stalled-stat", client]]),
  });
  const controller = new AbortController();
  const download = sftpBridge.downloadSftpToLocal(null, {
    sftpId: "stalled-stat",
    remotePath: "/tmp/source.bin",
    localPath: path.join(tempRoot, "target.bin"),
    abortSignal: controller.signal,
  });

  await statStarted;
  controller.abort();
  await assert.rejects(
    () => Promise.race([
      download,
      new Promise((_, reject) => setTimeout(() => reject(new Error("cancel timed out")), 500)),
    ]),
    /cancel|abort/i,
  );
});

test("session-backed uploadLocalToSftp uses pipelined fastPut on the raw SFTP channel", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-session-upload-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "payload.bin");
  const payload = Buffer.alloc(48 * 1024, 17);
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles } = createSessionChannel();
  const connection = {
    sftp(callback) {
      callback(null, channel);
    },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-upload", { conn: connection }]]),
    sftpClients,
  });

  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-upload",
    fileProtocol: "sftp",
  });
  assert.equal(opened.ok, true);
  assert.equal(opened.fileProtocol, "sftp");

  // Session-backed wrapper must expose fastPut (not only raw channel).
  const client = sftpClients.get(opened.sftpId);
  assert.equal(typeof client.fastPut, "function");
  assert.equal(client.__netcattySessionBacked, true);

  const result = await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath: "/home/alice/payload.bin",
    encoding: "utf-8",
  });

  assert.equal(result.success, true);
  assert.equal(fastPutCalls.length, 1);
  assert.equal(fastPutCalls[0].concurrency, UPLOAD_TRANSFER_CONCURRENCY);
  assert.equal(fastPutCalls[0].chunkSize, TRANSFER_CHUNK_SIZE);
  assert.notEqual(fastPutCalls[0].localPath, localPath);
  assert.match(path.basename(fastPutCalls[0].localPath), /upload-source-.*snapshot/);
  await assert.rejects(fs.promises.stat(fastPutCalls[0].localPath), { code: "ENOENT" });
  // Final path after staged rename
  assert.ok(remoteFiles.has("/home/alice/payload.bin"));
  assert.deepEqual(remoteFiles.get("/home/alice/payload.bin"), payload);
});

test("session-backed writeSftpBinaryWithProgress uses pipelined fastPut", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-session-write-progress-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });
  // ensureTempDir may be required by getTempFilePath
  if (typeof tempDirBridge.ensureTempDir === "function") {
    tempDirBridge.ensureTempDir();
  }

  const payload = Buffer.alloc(40 * 1024, 29);
  const { channel, fastPutCalls, remoteFiles } = createSessionChannel();
  const connection = {
    sftp(callback) {
      callback(null, channel);
    },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: {
      webContents: {
        fromId: () => ({ send() {} }),
      },
    },
    sessions: new Map([["session-write", { conn: connection }]]),
    sftpClients,
  });

  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-write",
    fileProtocol: "sftp",
  });

  let progressError = null;
  const result = await sftpBridge.writeSftpBinaryWithProgress(
    { sender: { id: 1 } },
    {
      sftpId: opened.sftpId,
      path: "/home/alice/mem.bin",
      content: payload,
      transferId: "mem-upload-1",
      encoding: "utf-8",
      onProgress() {},
      onComplete() {},
      onError(message) {
        progressError = message;
      },
    },
  );

  assert.equal(progressError, null, progressError);
  assert.equal(result.success, true, result.error || progressError || "upload failed");
  assert.equal(fastPutCalls.length, 1);
  assert.equal(fastPutCalls[0].concurrency, UPLOAD_TRANSFER_CONCURRENCY);
  assert.equal(fastPutCalls[0].chunkSize, TRANSFER_CHUNK_SIZE);
  assert.match(path.basename(fastPutCalls[0].localPath), /sftp-upload-/);
  assert.doesNotMatch(fastPutCalls[0].localPath, /upload-source-/);
  await assert.rejects(fs.promises.stat(fastPutCalls[0].localPath), { code: "ENOENT" });
  // New destinations stage to a remote .part path, then rename into place.
  assert.match(fastPutCalls[0].remotePath, /\.netcatty-upload-.*\.part$/);
  assert.notEqual(fastPutCalls[0].remotePath, "/home/alice/mem.bin");
  assert.ok(remoteFiles.has("/home/alice/mem.bin"));
  assert.deepEqual(remoteFiles.get("/home/alice/mem.bin"), payload);
});

test("SCP writeSftpBinaryWithProgress uses the shared staged transaction", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-write-progress-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const payload = Buffer.from("new executable payload");
  const finalPath = "/目录/工具";
  const remoteFiles = new Map([[finalPath, Buffer.from("old")]]);
  const remoteModes = new Map([[finalPath, 0o755]]);
  const uploadPaths = [];
  const chmodCalls = [];
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return {
        type: "file",
        isDirectory: false,
        size: remoteFiles.get(remotePath).length,
        mode: remoteModes.get(remotePath) ?? 0o644,
        permissions: (remoteModes.get(remotePath) ?? 0o644) === 0o755
          ? "rwxr-xr-x"
          : "rw-r--r--",
      };
    },
    async uploadFile(localPath, remotePath, options = {}) {
      uploadPaths.push(remotePath);
      const contents = await fs.promises.readFile(localPath);
      remoteFiles.set(remotePath, contents);
      remoteModes.set(remotePath, 0o644);
      options.onProgress?.(contents.length, contents.length);
    },
    async chmod(remotePath, mode) {
      chmodCalls.push({ remotePath, mode });
      remoteModes.set(remotePath, mode);
    },
    async rename(fromPath, toPath) {
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
      remoteModes.set(toPath, remoteModes.get(fromPath));
      remoteModes.delete(fromPath);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
      remoteModes.delete(remotePath);
    },
  };
  const sftpClients = new Map([["scp-memory", {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  }]]);
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => ({ send() {} }) } },
    sessions: new Map(),
    sftpClients,
  });

  const result = await sftpBridge.writeSftpBinaryWithProgress(
    { sender: { id: 1 } },
    {
      sftpId: "scp-memory",
      path: finalPath,
      content: payload,
      transferId: "scp-memory-upload",
      onProgress() {},
      onComplete() {},
    },
  );

  assert.equal(result.success, true);
  assert.equal(uploadPaths.length, 1);
  assert.match(uploadPaths[0], /\.netcatty-upload-.*\.part$/);
  assert.equal(typeof uploadPaths[0], "string");
  assert.match(uploadPaths[0], /^\/目录\/\.netcatty-upload-/);
  assert.notEqual(uploadPaths[0], finalPath);
  assert.deepEqual(remoteFiles.get(finalPath), payload);
  assert.equal(remoteModes.get(finalPath), 0o755);
  assert.deepEqual(chmodCalls, [{ remotePath: uploadPaths[0], mode: 0o755 }]);
});

test("SCP buffer upload creates a new remote file with mode 0644 under restrictive umask", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-buffer-mode-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const payload = Buffer.from("ordinary buffer payload");
  const finalPath = "/tmp/new-buffer.bin";
  const remoteFiles = new Map();
  const remoteModes = new Map();
  let uploadedSourcePath = null;
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return {
        type: "file",
        isDirectory: false,
        size: remoteFiles.get(remotePath).length,
        mode: remoteModes.get(remotePath),
      };
    },
    async uploadFile(localPath, remotePath, options = {}) {
      uploadedSourcePath = localPath;
      const contents = await fs.promises.readFile(localPath);
      remoteFiles.set(remotePath, contents);
      remoteModes.set(remotePath, (await fs.promises.stat(localPath)).mode & 0o777);
      options.onProgress?.(contents.length, contents.length);
    },
    async rename(fromPath, toPath) {
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
      remoteModes.set(toPath, remoteModes.get(fromPath));
      remoteModes.delete(fromPath);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
      remoteModes.delete(remotePath);
    },
    async chmod(remotePath, mode) {
      remoteModes.set(remotePath, mode);
    },
  };
  const sftpClients = new Map([["scp-buffer-mode", {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  }]]);
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => ({ send() {} }) } },
    sessions: new Map(),
    sftpClients,
  });

  const previousUmask = process.umask(0o077);
  let result;
  try {
    result = await sftpBridge.writeSftpBinaryWithProgress(
      { sender: { id: 1 } },
      {
        sftpId: "scp-buffer-mode",
        path: finalPath,
        content: payload,
        transferId: "scp-buffer-mode-upload",
        onProgress() {},
        onComplete() {},
      },
    );
  } finally {
    process.umask(previousUmask);
  }

  assert.equal(result.success, true);
  assert.match(path.basename(uploadedSourcePath), /sftp-upload-/);
  assert.doesNotMatch(uploadedSourcePath, /upload-source-/);
  assert.deepEqual(remoteFiles.get(finalPath), payload);
  assert.equal(remoteModes.get(finalPath), 0o644);
  await assert.rejects(fs.promises.stat(uploadedSourcePath), { code: "ENOENT" });
});

test("SCP staged uploads preserve an existing mode 000", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-mode-zero-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const payload = Buffer.from("replacement over mode-zero file");
  const finalPath = "/tmp/locked.bin";
  const remoteFiles = new Map([[finalPath, Buffer.from("old")]]);
  const remoteModes = new Map([[finalPath, 0]]);
  const uploadPaths = [];
  const chmodCalls = [];
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return {
        type: "file",
        isDirectory: false,
        size: remoteFiles.get(remotePath).length,
        mode: remoteModes.get(remotePath) ?? 0o644,
        permissions: remoteModes.get(remotePath) === 0 ? "---------" : "rw-r--r--",
      };
    },
    async uploadFile(localPath, remotePath, options = {}) {
      uploadPaths.push(remotePath);
      const contents = await fs.promises.readFile(localPath);
      remoteFiles.set(remotePath, contents);
      remoteModes.set(remotePath, 0o644);
      options.onProgress?.(contents.length, contents.length);
    },
    async chmod(remotePath, mode) {
      chmodCalls.push({ remotePath, mode });
      remoteModes.set(remotePath, mode);
    },
    async rename(fromPath, toPath) {
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
      remoteModes.set(toPath, remoteModes.get(fromPath));
      remoteModes.delete(fromPath);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
      remoteModes.delete(remotePath);
    },
  };
  const sftpClients = new Map([["scp-mode-zero", {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  }]]);
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => ({ send() {} }) } },
    sessions: new Map(),
    sftpClients,
  });

  const result = await sftpBridge.writeSftpBinaryWithProgress(
    { sender: { id: 1 } },
    {
      sftpId: "scp-mode-zero",
      path: finalPath,
      content: payload,
      transferId: "scp-mode-zero-upload",
      onProgress() {},
      onComplete() {},
    },
  );

  assert.equal(result.success, true, result.error || "upload failed");
  assert.equal(uploadPaths.length, 1);
  assert.match(uploadPaths[0], /\.netcatty-upload-.*\.part$/);
  assert.deepEqual(remoteFiles.get(finalPath), payload);
  assert.deepEqual(chmodCalls, [{ remotePath: uploadPaths[0], mode: 0 }]);
  assert.equal(remoteModes.get(finalPath), 0);
});

test("SCP staged uploads do not treat an unparseable zero mode as mode 000", async () => {
  let chmodCalls = 0;
  const backend = {
    async stat(remotePath) {
      return {
        type: "file",
        isDirectory: false,
        size: String(remotePath).includes(".netcatty-upload-") ? 7 : 3,
        mode: 0,
        // No permissions field: the shell mode could not be parsed.
      };
    },
    async chmod() {
      chmodCalls += 1;
    },
    async rename() {},
    async remove() {},
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };

  const result = await sftpBridge.runRemoteUploadTransaction(
    client,
    "/tmp/local.bin",
    "/tmp/final.bin",
    {
      expectedSize: 7,
      async uploadFile() {},
    },
  );
  assert.deepEqual(result, { staged: true });
  assert.equal(chmodCalls, 0);
});

test("SCP symlink uploads do not compare content size with the link node", async () => {
  let uploadCalls = 0;
  const backend = {
    async stat() {
      return { type: "symlink", isSymbolicLink: true, size: 99 };
    },
    async uploadFile() {
      uploadCalls += 1;
    },
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };

  const result = await sftpBridge.runRemoteUploadTransaction(
    client,
    "/tmp/local.bin",
    "/目录/链接.bin",
    {
      expectedSize: 7,
      async uploadFile(remotePath) {
        assert.equal(remotePath, "/目录/链接.bin");
        uploadCalls += 1;
      },
    },
  );
  assert.deepEqual(result, { staged: false });
  assert.equal(uploadCalls, 1);
});

test("cancelling an SCP memory upload leaves the final destination untouched", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-memory-cancel-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  tempDirBridge.init?.({ getPath: () => tempRoot });

  let uploadStarted;
  const started = new Promise((resolve) => { uploadStarted = resolve; });
  let renameCalls = 0;
  let removedStage = false;
  const backend = {
    async stat() {
      const error = new Error("No such file");
      error.code = "ENOENT";
      throw error;
    },
    async uploadFile(_localPath, _remotePath, options = {}) {
      uploadStarted();
      await new Promise((resolve, reject) => {
        options.transfer.abort = () => reject(new Error("Transfer cancelled"));
      });
    },
    async rename() {
      renameCalls += 1;
    },
    async remove(remotePath) {
      if (String(remotePath).includes(".netcatty-upload-")) removedStage = true;
    },
  };
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => ({ send() {} }) } },
    sessions: new Map(),
    sftpClients: new Map([["scp-memory-cancel", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    }]]),
  });

  const upload = sftpBridge.writeSftpBinaryWithProgress(
    { sender: { id: 1 } },
    {
      sftpId: "scp-memory-cancel",
      path: "/tmp/final.bin",
      content: Buffer.alloc(1024, 1),
      transferId: "scp-memory-cancel-transfer",
      onProgress() {},
    },
  );
  await started;
  await sftpBridge.cancelSftpUpload(null, { transferId: "scp-memory-cancel-transfer" });
  const result = await upload;

  assert.equal(result.cancelled, true);
  assert.equal(renameCalls, 0);
  assert.equal(removedStage, true);
});

test("existing destinations stage then restore mode after rename", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-meta-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "exec.bin");
  const payload = Buffer.from("#!/bin/sh\necho hi\n");
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles, remoteMeta, chmodCalls } = createSessionChannel();
  remoteFiles.set("/usr/local/bin/tool", Buffer.from("old"));
  remoteMeta.set("/usr/local/bin/tool", { mode: 0o100755 });

  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-mode", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-mode",
    fileProtocol: "sftp",
  });

  await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath: "/usr/local/bin/tool",
    encoding: "utf-8",
  });

  assert.equal(fastPutCalls.length, 1);
  assert.match(fastPutCalls[0].remotePath, /\.netcatty-upload-.*\.part$/);
  assert.ok(remoteFiles.has("/usr/local/bin/tool"));
  assert.deepEqual(remoteFiles.get("/usr/local/bin/tool"), payload);
  // Stage+rename replaces the inode; restore prior mode bits afterwards.
  assert.ok(
    chmodCalls.some((c) => String(c.targetPath).includes(".netcatty-upload-") && (c.mode & 0o777) === 0o755),
    `expected mode restore via chmod, got ${JSON.stringify(chmodCalls)}`,
  );
  assert.equal(remoteMeta.get("/usr/local/bin/tool")?.mode & 0o777, 0o755);
});

test("mode restore failure leaves the existing destination untouched", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-mode-fail-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "tool");
  await fs.promises.writeFile(localPath, Buffer.from("new-tool"));
  const { channel, remoteFiles, remoteMeta } = createSessionChannel();
  remoteFiles.set("/usr/local/bin/tool", Buffer.from("old-tool"));
  remoteMeta.set("/usr/local/bin/tool", { mode: 0o100755 });
  channel.chmod = (_targetPath, _mode, callback) => {
    const err = new Error("chmod failed");
    err.code = "EIO";
    callback(err);
  };

  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-mode-fail", { conn: { sftp: (cb) => cb(null, channel) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-mode-fail",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/usr/local/bin/tool",
      encoding: "utf-8",
    }),
    /chmod failed/,
  );
  assert.deepEqual(remoteFiles.get("/usr/local/bin/tool"), Buffer.from("old-tool"));
  assert.equal(
    [...remoteFiles.keys()].some((key) => String(key).includes(".netcatty-upload-")),
    false,
  );
});

test("failed promotion and failed restore preserve both recovery files", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-restore-fail-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "data.bin");
  await fs.promises.writeFile(localPath, Buffer.from("new-data"));
  const { channel, remoteFiles, remoteMeta } = createSessionChannel();
  const finalPath = "/tmp/data.bin";
  remoteFiles.set(finalPath, Buffer.from("old-data"));
  remoteMeta.set(finalPath, { mode: 0o100644 });
  const originalRename = channel.rename.bind(channel);
  let stagePromoteAttempts = 0;
  channel.rename = (from, to, callback) => {
    const fromString = String(from);
    const toString = String(to);
    if (fromString.includes(".netcatty-upload-") && toString === finalPath) {
      stagePromoteAttempts += 1;
      callback(new Error("stage promote failed"));
      return;
    }
    if (fromString.includes(".netcatty-backup-") && toString === finalPath) {
      callback(new Error("backup restore failed"));
      return;
    }
    originalRename(from, to, callback);
  };

  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-restore-fail", { conn: { sftp: (cb) => cb(null, channel) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-restore-fail",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: finalPath,
      encoding: "utf-8",
    }),
    /could not be restored/,
  );
  assert.ok(stagePromoteAttempts >= 2);
  assert.equal(remoteFiles.has(finalPath), false);
  assert.ok(
    [...remoteFiles.entries()].some(([key, value]) => String(key).includes(".netcatty-upload-") && value.equals(Buffer.from("new-data"))),
  );
  assert.ok(
    [...remoteFiles.entries()].some(([key, value]) => String(key).includes(".netcatty-backup-") && value.equals(Buffer.from("old-data"))),
  );
});

test("rename fallback replaces safely and restores the old target on promotion failure", async () => {
  const makeClient = ({ failEveryStagePromotion = false } = {}) => {
    const files = new Map([
      ["/tmp/stage", Buffer.from("new")],
      ["/tmp/final", Buffer.from("old")],
    ]);
    let stagePromotionAttempts = 0;
    const channel = {
      readdir(_path, cb) { cb(null, []); },
      mkdir(_path, cb) { cb(null); },
      unlink(targetPath, cb) { files.delete(targetPath); cb(null); },
      stat(targetPath, cb) {
        if (!files.has(targetPath)) {
          const err = new Error("ENOENT");
          err.code = 2;
          cb(err);
          return;
        }
        cb(null, { size: files.get(targetPath).length, isDirectory: false });
      },
    };
    return {
      files,
      client: {
        sftp: channel,
        async stat(targetPath) {
          return { size: files.get(targetPath)?.length || 0, isDirectory: false };
        },
        async rename(from, to) {
          if (from === "/tmp/stage" && to === "/tmp/final") {
            stagePromotionAttempts += 1;
            if (stagePromotionAttempts === 1 || failEveryStagePromotion) {
              throw new Error("overwrite unsupported");
            }
          }
          if (!files.has(from)) throw new Error("ENOENT");
          files.set(to, files.get(from));
          files.delete(from);
        },
        async delete(targetPath) {
          files.delete(targetPath);
        },
      },
    };
  };

  const successful = makeClient();
  await sftpBridge._renameRemotePathForTests(
    successful.client,
    "/tmp/stage",
    "/tmp/final",
    "/tmp/backup",
  );
  assert.deepEqual(successful.files.get("/tmp/final"), Buffer.from("new"));
  assert.equal(successful.files.has("/tmp/backup"), false);

  const restored = makeClient({ failEveryStagePromotion: true });
  await assert.rejects(
    () => sftpBridge._renameRemotePathForTests(
      restored.client,
      "/tmp/stage",
      "/tmp/final",
      "/tmp/backup",
    ),
    /overwrite unsupported/,
  );
  assert.deepEqual(restored.files.get("/tmp/final"), Buffer.from("old"));
  assert.deepEqual(restored.files.get("/tmp/stage"), Buffer.from("new"));
  assert.equal(restored.files.has("/tmp/backup"), false);

  const encodedStage = Buffer.from([0x81, 0x40]);
  const encodedFinal = Buffer.from([0x81, 0x41]);
  const encodedBackup = Buffer.from([0x81, 0x42]);
  const channel = {
    readdir(_path, cb) { cb(null, []); },
    mkdir(_path, cb) { cb(null); },
    unlink(_path, cb) { cb(null); },
    stat(_path, cb) { cb(null, { size: 1, isDirectory: false }); },
  };
  const encodedClient = {
    sftp: channel,
    async stat() { return { size: 1, isDirectory: false }; },
    async rename(from, to) {
      if (from === encodedFinal && to === encodedBackup) return;
      if (from === encodedStage && to === encodedFinal) throw new Error("promote failed");
      if (from === encodedBackup && to === encodedFinal) throw new Error("restore failed");
    },
    async delete() {},
  };
  await assert.rejects(
    () => sftpBridge._renameRemotePathForTests(
      encodedClient,
      encodedStage,
      encodedFinal,
      encodedBackup,
      {
        stagePath: "/目录/.netcatty-upload-stage.part",
        backupPath: "/目录/.netcatty-backup-file.bak",
        finalPath: "/目录/文件.txt",
      },
    ),
    (error) => {
      assert.match(error.message, /\/目录\/\.netcatty-upload-stage\.part/);
      assert.match(error.message, /\/目录\/\.netcatty-backup-file\.bak/);
      return true;
    },
  );
});

test("SCP upload stops when the destination type cannot be inspected", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-stat-fail-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  const localPath = path.join(tempRoot, "local.bin");
  await fs.promises.writeFile(localPath, Buffer.from("payload"));
  let uploadCalls = 0;
  const backend = {
    async stat() {
      throw new Error("temporary stat failure");
    },
    async uploadFile() {
      uploadCalls += 1;
    },
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map([["scp-stat-fail", client]]),
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: "scp-stat-fail",
      localPath,
      remotePath: "/tmp/remote.bin",
      encoding: "utf-8",
    }),
    /temporary stat failure/,
  );
  assert.equal(uploadCalls, 0);
});

test("SCP upload does not replace a symlink that appears before promotion", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-symlink-race-"));
  t.after(async () => fs.promises.rm(tempRoot, { recursive: true, force: true }));
  const localPath = path.join(tempRoot, "local.bin");
  await fs.promises.writeFile(localPath, Buffer.from("payload"));
  let statCalls = 0;
  let renameCalls = 0;
  let removedStage = false;
  const backend = {
    async stat() {
      statCalls += 1;
      if (statCalls === 1) return { type: "file", isDirectory: false };
      return { type: "symlink", isDirectory: false, isSymbolicLink: true };
    },
    async uploadFile() {},
    async rename() {
      renameCalls += 1;
    },
    async remove(remotePath) {
      if (String(remotePath).includes(".netcatty-upload-")) removedStage = true;
    },
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map([["scp-symlink-race", client]]),
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: "scp-symlink-race",
      localPath,
      remotePath: "/tmp/remote.bin",
      encoding: "utf-8",
    }),
    /changed to a symlink/,
  );
  assert.equal(renameCalls, 0);
  assert.equal(removedStage, true);
});

test("SCP upload cancelled during the final target check does not promote", async () => {
  const controller = new AbortController();
  let statCalls = 0;
  let renameCalls = 0;
  let removedStage = false;
  const backend = {
    async stat(remotePath) {
      statCalls += 1;
      if (String(remotePath).includes(".netcatty-upload-")) {
        return { type: "file", isDirectory: false, size: 7 };
      }
      if (statCalls > 2) controller.abort();
      return { type: "file", isDirectory: false, size: 3 };
    },
    async rename() {
      renameCalls += 1;
    },
    async remove(remotePath) {
      if (String(remotePath).includes(".netcatty-upload-")) removedStage = true;
    },
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };

  await assert.rejects(
    () => sftpBridge.runRemoteUploadTransaction(client, "/tmp/local.bin", "/tmp/remote.bin", {
      signal: controller.signal,
      expectedSize: 7,
      async uploadFile() {},
    }),
    /abort/i,
  );
  assert.equal(renameCalls, 0);
  assert.equal(removedStage, true);
});

test("SCP upload aborts while staged size verification is pending", async () => {
  const controller = new AbortController();
  let removedStage = false;
  const backend = {
    async stat(remotePath, options = {}) {
      if (!String(remotePath).includes(".netcatty-upload-")) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      await new Promise((resolve, reject) => {
        if (options.signal?.aborted) {
          reject(new Error("Transfer cancelled"));
          return;
        }
        options.signal?.addEventListener(
          "abort",
          () => reject(new Error("Transfer cancelled")),
          { once: true },
        );
      });
    },
    async remove(remotePath) {
      if (String(remotePath).includes(".netcatty-upload-")) removedStage = true;
    },
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  const upload = sftpBridge.runRemoteUploadTransaction(
    client,
    "/tmp/local.bin",
    "/tmp/remote.bin",
    {
      signal: controller.signal,
      expectedSize: 7,
      async uploadFile() {},
    },
  );
  await new Promise((resolve) => setImmediate(resolve));
  controller.abort();
  await assert.rejects(() => upload, /cancel|abort/i);
  assert.equal(removedStage, true);
});

test("staged basenames stay within the remote NAME_MAX budget", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-long-name-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const longBase = `${"a".repeat(240)}.bin`;
  const localPath = path.join(tempRoot, "payload.bin");
  await fs.promises.writeFile(localPath, Buffer.from("x"));

  const { channel, fastPutCalls, remoteFiles } = createSessionChannel();
  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-long", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-long",
    fileProtocol: "sftp",
  });

  await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath: `/tmp/${longBase}`,
    encoding: "utf-8",
  });

  assert.equal(fastPutCalls.length, 1);
  const stagedBase = path.posix.basename(fastPutCalls[0].remotePath);
  assert.ok(Buffer.byteLength(stagedBase, "utf8") <= 255, stagedBase);
  assert.ok(remoteFiles.has(`/tmp/${longBase}`));
});

test("symlink destinations are written in-place (not replaced by rename)", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-symlink-upload-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "cfg.json");
  const payload = Buffer.from('{"ok":true}');
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles, remoteMeta } = createSessionChannel();
  // Symlink at the destination path; real content elsewhere.
  remoteMeta.set("/etc/app/config.json", { isSymlink: true, mode: 0o120777 });
  remoteFiles.set("/etc/app/config.json", Buffer.from("link-placeholder"));

  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-link", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-link",
    fileProtocol: "sftp",
  });

  await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath: "/etc/app/config.json",
    encoding: "utf-8",
  });

  assert.equal(fastPutCalls.length, 1);
  assert.equal(fastPutCalls[0].remotePath, "/etc/app/config.json");
  assert.deepEqual(remoteFiles.get("/etc/app/config.json"), payload);
});

test("truncated write-in-place uploads never delete the symlink destination", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-symlink-size-failure-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "cfg.json");
  const payload = Buffer.from('{"expected":"full-content"}');
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles, remoteMeta } = createSessionChannel();
  const remotePath = "/etc/app/config.json";
  remoteMeta.set(remotePath, { isSymlink: true, mode: 0o120777 });
  remoteFiles.set(remotePath, Buffer.from("old-target"));
  channel.fastPut = (_localPath, uploadedPath, opts, callback) => {
    fastPutCalls.push({ localPath: _localPath, remotePath: uploadedPath });
    const truncated = payload.subarray(0, 3);
    remoteFiles.set(uploadedPath, truncated);
    opts?.step?.(truncated.length, truncated.length, payload.length);
    queueMicrotask(() => callback(null));
  };

  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-link-size", { conn: { sftp: (callback) => callback(null, channel) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-link-size",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath,
      encoding: "utf-8",
    }),
    /size mismatch/i,
  );
  assert.equal(fastPutCalls.length, 1);
  assert.equal(remoteMeta.get(remotePath)?.isSymlink, true);
  assert.equal(remoteFiles.has(remotePath), true);
});

test("lstat unsupported falls back to stat and preserves an existing destination", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-lstat-fallback-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "cfg.json");
  const payload = Buffer.from('{"fallback":true}');
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles, remoteMeta } = createSessionChannel();
  remoteFiles.set("/etc/app/config.json", Buffer.from("old-target-content"));
  remoteMeta.set("/etc/app/config.json", { isSymlink: true, mode: 0o120777 });
  channel.lstat = (_targetPath, callback) => {
    const err = new Error("SSH_FX_OP_UNSUPPORTED");
    err.code = 8;
    callback(err);
  };

  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-lstat-fallback", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-lstat-fallback",
    fileProtocol: "sftp",
  });

  await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath: "/etc/app/config.json",
    encoding: "utf-8",
  });

  assert.equal(fastPutCalls.length, 1);
  assert.equal(fastPutCalls[0].remotePath, "/etc/app/config.json");
  assert.deepEqual(remoteFiles.get("/etc/app/config.json"), payload);
});

test("new files stage safely when readlink distinguishes them without lstat", async (t) => {
  for (const variant of ["missing-method", "runtime-unsupported"]) {
    const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), `netcatty-new-no-lstat-${variant}-`));
    t.after(async () => {
      await fs.promises.rm(tempRoot, { recursive: true, force: true });
    });
    tempDirBridge.init?.({ getPath: () => tempRoot });

    const localPath = path.join(tempRoot, "new.bin");
    const payload = Buffer.from(`new-${variant}`);
    await fs.promises.writeFile(localPath, payload);
    const { channel, fastPutCalls, remoteFiles } = createSessionChannel();
    if (variant === "missing-method") {
      channel.lstat = undefined;
    } else {
      channel.lstat = (_targetPath, callback) => {
        const err = new Error("SSH_FX_OP_UNSUPPORTED");
        err.code = 8;
        callback(err);
      };
    }

    const sftpClients = new Map();
    sftpBridge.init({
      electronModule: { webContents: { fromId: () => null } },
      sessions: new Map([[`session-${variant}`, { conn: { sftp: (cb) => cb(null, channel) } }]]),
      sftpClients,
    });
    const opened = await sftpBridge.openSftpForSession(null, {
      sessionId: `session-${variant}`,
      fileProtocol: "sftp",
    });

    await sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: `/tmp/${variant}.bin`,
      encoding: "utf-8",
    });
    assert.equal(fastPutCalls.length, 1);
    assert.match(fastPutCalls[0].remotePath, /\.netcatty-upload-.*\.part$/);
    assert.deepEqual(remoteFiles.get(`/tmp/${variant}.bin`), payload);
  }
});

test("no-lstat fallback writes through a broken symlink instead of replacing it", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-broken-link-no-lstat-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });
  const localPath = path.join(tempRoot, "payload.bin");
  await fs.promises.writeFile(localPath, Buffer.from("new-target"));

  const created = createSessionChannel();
  const remotePath = "/tmp/broken-link";
  created.remoteMeta.set(remotePath, { isSymlink: true, mode: 0o120777 });
  created.channel.lstat = undefined;
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-broken-link", { conn: { sftp: (cb) => cb(null, created.channel) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-broken-link",
    fileProtocol: "sftp",
  });

  await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath,
    encoding: "utf-8",
  });
  assert.equal(created.fastPutCalls.length, 1);
  assert.equal(created.fastPutCalls[0].remotePath, remotePath);
  assert.equal(created.remoteMeta.get(remotePath)?.isSymlink, true);
});

test("upload stops when neither lstat nor readlink can classify a missing target", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-no-link-inspection-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });
  const localPath = path.join(tempRoot, "payload.bin");
  await fs.promises.writeFile(localPath, Buffer.from("payload"));

  const created = createSessionChannel();
  created.channel.lstat = undefined;
  created.channel.readlink = undefined;
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-no-link-inspection", {
      conn: { sftp: (callback) => callback(null, created.channel) },
    }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-no-link-inspection",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/tmp/new.bin",
      encoding: "utf-8",
    }),
    /cannot safely distinguish/i,
  );
  assert.equal(created.fastPutCalls.length, 0);
  assert.equal(created.remoteFiles.has("/tmp/new.bin"), false);
});

test("permission failure during final target recheck never falls back to direct write", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-recheck-permission-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "data.bin");
  await fs.promises.writeFile(localPath, Buffer.from("new-data"));
  const created = createSessionChannel();
  created.remoteFiles.set("/tmp/data.bin", Buffer.from("old-data"));
  created.remoteMeta.set("/tmp/data.bin", { mode: 0o100644 });
  const originalLstat = created.channel.lstat.bind(created.channel);
  let lstatCalls = 0;
  created.channel.lstat = (targetPath, callback) => {
    lstatCalls += 1;
    if (lstatCalls >= 2 && String(targetPath) === "/tmp/data.bin") {
      const err = new Error("Permission denied");
      err.code = "EACCES";
      callback(err);
      return;
    }
    originalLstat(targetPath, callback);
  };

  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-recheck-permission", { conn: { sftp: (cb) => cb(null, created.channel) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-recheck-permission",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/tmp/data.bin",
      encoding: "utf-8",
    }),
    /Permission denied/,
  );
  assert.equal(created.fastPutCalls.length, 1);
  assert.match(created.fastPutCalls[0].remotePath, /\.netcatty-upload-/);
  assert.deepEqual(created.remoteFiles.get("/tmp/data.bin"), Buffer.from("old-data"));
});

test("staged SFTP upload stops if the destination becomes a symlink", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-sftp-symlink-race-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "cfg.json");
  await fs.promises.writeFile(localPath, Buffer.from("new-content"));
  let channelRef = null;
  const created = createSessionChannel({
    onFastPut(_local, remotePath) {
      if (String(remotePath).includes(".netcatty-upload-")) {
        created.remoteMeta.set("/etc/app/config.json", { isSymlink: true, mode: 0o120777 });
      }
      return null;
    },
  });
  channelRef = created.channel;
  created.remoteFiles.set("/etc/app/config.json", Buffer.from("old-content"));
  created.remoteMeta.set("/etc/app/config.json", { mode: 0o100644 });

  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-symlink-race", { conn: { sftp: (cb) => cb(null, channelRef) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-symlink-race",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/etc/app/config.json",
      encoding: "utf-8",
    }),
    /changed to a symlink/,
  );
  assert.deepEqual(created.remoteFiles.get("/etc/app/config.json"), Buffer.from("old-content"));
  assert.equal(
    [...created.remoteFiles.keys()].some((key) => String(key).includes(".netcatty-upload-")),
    false,
  );
});

test("no-lstat staged upload stops if a broken symlink appears before promotion", async (t) => {
  for (const variant of ["missing-method", "runtime-unsupported"]) {
    const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), `netcatty-late-broken-link-${variant}-`));
    t.after(async () => {
      await fs.promises.rm(tempRoot, { recursive: true, force: true });
    });
    tempDirBridge.init?.({ getPath: () => tempRoot });
    const localPath = path.join(tempRoot, "payload.bin");
    await fs.promises.writeFile(localPath, Buffer.from("new-content"));
    const remotePath = `/tmp/${variant}.bin`;
    const created = createSessionChannel({
      onFastPut(_local, fastPutPath) {
        if (String(fastPutPath).includes(".netcatty-upload-")) {
          created.remoteMeta.set(remotePath, { isSymlink: true, mode: 0o120777 });
        }
        return null;
      },
    });
    if (variant === "missing-method") {
      created.channel.lstat = undefined;
    } else {
      created.channel.lstat = (_targetPath, callback) => {
        const error = new Error("SSH_FX_OP_UNSUPPORTED");
        error.code = 8;
        callback(error);
      };
    }
    const sftpClients = new Map();
    sftpBridge.init({
      electronModule: { webContents: { fromId: () => null } },
      sessions: new Map([[`session-${variant}`, {
        conn: { sftp: (callback) => callback(null, created.channel) },
      }]]),
      sftpClients,
    });
    const opened = await sftpBridge.openSftpForSession(null, {
      sessionId: `session-${variant}`,
      fileProtocol: "sftp",
    });

    await assert.rejects(
      () => sftpBridge.uploadLocalToSftp(null, {
        sftpId: opened.sftpId,
        localPath,
        remotePath,
        encoding: "utf-8",
      }),
      /changed to a symlink/,
    );
    assert.equal(created.remoteMeta.get(remotePath)?.isSymlink, true);
    assert.equal(created.remoteFiles.has(remotePath), false);
    assert.equal(
      [...created.remoteFiles.keys()].some((key) => String(key).includes(".netcatty-upload-")),
      false,
    );
  }
});

test("abort during staged mode setup prevents promotion", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-sftp-mode-abort-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "tool");
  await fs.promises.writeFile(localPath, Buffer.from("new-tool"));
  const controller = new AbortController();
  const { channel, remoteFiles, remoteMeta } = createSessionChannel();
  remoteFiles.set("/usr/local/bin/tool", Buffer.from("old-tool"));
  remoteMeta.set("/usr/local/bin/tool", { mode: 0o100755 });
  const originalChmod = channel.chmod.bind(channel);
  channel.chmod = (targetPath, mode, callback) => {
    controller.abort();
    originalChmod(targetPath, mode, callback);
  };

  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-mode-abort", { conn: { sftp: (cb) => cb(null, channel) } }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-mode-abort",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/usr/local/bin/tool",
      encoding: "utf-8",
      abortSignal: controller.signal,
    }),
    /abort|cancel/i,
  );
  assert.deepEqual(remoteFiles.get("/usr/local/bin/tool"), Buffer.from("old-tool"));
});

test("parent-dir permission on staged path falls back to in-place for new files", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-perm-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "data.bin");
  const payload = Buffer.from("new-content");
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles } = createSessionChannel({
    onFastPut(_local, remotePath) {
      if (String(remotePath).includes(".netcatty-upload-")) {
        const err = new Error("Permission denied");
        err.code = 3;
        return { error: err };
      }
      return null;
    },
  });
  // Destination does not exist → staging is attempted first.

  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-perm", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-perm",
    fileProtocol: "sftp",
  });

  await sftpBridge.uploadLocalToSftp(null, {
    sftpId: opened.sftpId,
    localPath,
    remotePath: "/ro-dir/file.bin",
    encoding: "utf-8",
  });

  assert.ok(fastPutCalls.length >= 2, "expected staged attempt then in-place");
  assert.match(fastPutCalls[0].remotePath, /\.netcatty-upload-.*\.part$/);
  assert.equal(fastPutCalls[fastPutCalls.length - 1].remotePath, "/ro-dir/file.bin");
  assert.deepEqual(remoteFiles.get("/ro-dir/file.bin"), payload);
});

test("no-lstat new upload aborted during staged size verify leaves no final", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-late-abort-promote-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "payload.bin");
  const payload = Buffer.from("late-abort-payload");
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls, remoteFiles } = createSessionChannel();
  channel.lstat = undefined;
  const controller = new AbortController();
  let renameCalled = false;
  const origStat = channel.stat.bind(channel);
  const origRename = channel.rename.bind(channel);
  channel.stat = (targetPath, callback) => {
    if (String(targetPath).includes(".netcatty-upload-")) {
      // Abort while size verification is in flight so the post-stat check must
      // block promotion (throwIfAborted after await client.stat).
      controller.abort();
      queueMicrotask(() => origStat(targetPath, callback));
      return;
    }
    return origStat(targetPath, callback);
  };
  channel.rename = (from, to, callback) => {
    renameCalled = true;
    return origRename(from, to, callback);
  };

  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-late-abort", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-late-abort",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/tmp/late-abort.bin",
      encoding: "utf-8",
      abortSignal: controller.signal,
    }),
    /abort|cancel/i,
  );

  assert.equal(fastPutCalls.length, 1);
  assert.match(fastPutCalls[0].remotePath, /\.netcatty-upload-.*\.part$/);
  assert.equal(renameCalled, false);
  assert.equal(remoteFiles.has("/tmp/late-abort.bin"), false);
});

test("size-mismatch on path containing 'access' does not fall back to in-place", async (t) => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-access-name-"));
  t.after(async () => {
    await fs.promises.rm(tempRoot, { recursive: true, force: true });
  });
  tempDirBridge.init?.({ getPath: () => tempRoot });

  const localPath = path.join(tempRoot, "payload.bin");
  const payload = Buffer.from("twelve-bytes"); // 12 bytes
  await fs.promises.writeFile(localPath, payload);

  const { channel, fastPutCalls } = createSessionChannel();
  // Make staged-path stat report a wrong size so size-verify throws a message
  // containing the path word "access" — must NOT be treated as permission.
  const origStat = channel.stat.bind(channel);
  channel.stat = (targetPath, callback) => {
    if (String(targetPath).includes(".netcatty-upload-")) {
      callback(null, {
        size: 1,
        mode: 0o100644,
        isDirectory: () => false,
        isFile: () => true,
        isSymbolicLink: () => false,
      });
      return;
    }
    return origStat(targetPath, callback);
  };

  const connection = {
    sftp(callback) { callback(null, channel); },
  };
  const sftpClients = new Map();
  sftpBridge.init({
    electronModule: { webContents: { fromId: () => null } },
    sessions: new Map([["session-access", { conn: connection }]]),
    sftpClients,
  });
  const opened = await sftpBridge.openSftpForSession(null, {
    sessionId: "session-access",
    fileProtocol: "sftp",
  });

  await assert.rejects(
    () => sftpBridge.uploadLocalToSftp(null, {
      sftpId: opened.sftpId,
      localPath,
      remotePath: "/tmp/access-denied-name.bin",
      encoding: "utf-8",
    }),
    /size mismatch/i,
  );
  // Only the staged attempt — no in-place fallback write to the final path.
  assert.equal(fastPutCalls.length, 1);
  assert.match(fastPutCalls[0].remotePath, /\.netcatty-upload-.*\.part$/);
});

test("pipelinedUploadLocalFile aborts in-flight fastPut when AbortSignal fires", async () => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-abort-fastput-"));
  const localPath = path.join(tempRoot, "abort.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(64 * 1024, 3));

  let ended = false;
  let fastPutStarted = false;
  const channel = {
    readdir(_p, cb) { cb(null, []); },
    mkdir(_p, cb) { cb(null); },
    unlink(_p, cb) { cb(null); },
    stat(_p, cb) {
      const err = new Error("ENOENT");
      err.code = 2;
      cb(err);
    },
    fastPut(_local, _remote, _opts, callback) {
      fastPutStarted = true;
      // Stay pending until end() cancels the transfer.
      this._pendingCallback = callback;
    },
    end() {
      ended = true;
      const cb = this._pendingCallback;
      this._pendingCallback = null;
      if (typeof cb === "function") {
        const err = new Error("SFTP channel closed");
        queueMicrotask(() => cb(err));
      }
    },
  };
  const bareClient = {
    __netcattySessionBacked: true,
    sftp: null,
    client: {
      sftp(cb) {
        cb(null, channel);
      },
    },
  };

  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map(),
  });

  const controller = new AbortController();
  const uploadPromise = sftpBridge.pipelinedUploadLocalFile(
    bareClient,
    localPath,
    "/tmp/abort-out.bin",
    {
      concurrency: UPLOAD_TRANSFER_CONCURRENCY,
      chunkSize: TRANSFER_CHUNK_SIZE,
      signal: controller.signal,
    },
  );

  // Allow fastPut to start, then abort.
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(fastPutStarted, true);
  controller.abort();

  await assert.rejects(uploadPromise, /abort|cancel/i);
  assert.equal(ended, true);

  await fs.promises.rm(tempRoot, { recursive: true, force: true });
});

test("shared-channel fastPut cancel force-settles when callback stalls", async () => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-shared-abort-bound-"));
  const localPath = path.join(tempRoot, "stall.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(8 * 1024, 5));

  let ended = false;
  let unlinkedPath = null;
  const channel = {
    readdir(_p, cb) { cb(null, []); },
    mkdir(_p, cb) { cb(null); },
    unlink(targetPath, cb) {
      unlinkedPath = targetPath;
      cb(null);
    },
    stat(_p, cb) {
      const err = new Error("ENOENT");
      err.code = 2;
      cb(err);
    },
    // Never invoke the callback — simulates a stalled shared-channel fastPut.
    fastPut() {},
    end() {
      ended = true;
    },
  };
  // No client.sftp() for a second channel → acquireUpload uses shared channel.
  const sharedOnlyClient = {
    __netcattySudoMode: true,
    sftp: channel,
    client: null,
  };

  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map(),
  });

  const controller = new AbortController();
  const uploadPromise = sftpBridge.pipelinedUploadLocalFile(
    sharedOnlyClient,
    localPath,
    "/tmp/stall-out.bin",
    {
      concurrency: UPLOAD_TRANSFER_CONCURRENCY,
      chunkSize: TRANSFER_CHUNK_SIZE,
      signal: controller.signal,
    },
  );

  await new Promise((r) => setImmediate(r));
  controller.abort();

  const started = Date.now();
  await assert.rejects(uploadPromise, /abort|cancel/i);
  const elapsed = Date.now() - started;
  // Must settle via the 2s force-finish path, not hang forever.
  assert.ok(elapsed < 5000, `cancel took too long: ${elapsed}ms`);
  // Shared channel must not be ended (would kill browse/sudo session).
  assert.equal(ended, false);
  // Final destinations must not be unlinked on shared-channel force-settle.
  assert.equal(unlinkedPath, null);

  await fs.promises.rm(tempRoot, { recursive: true, force: true });
});

test("shared-channel force-settle unlinks explicitly generated stage paths", async () => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-shared-stage-unlink-"));
  const localPath = path.join(tempRoot, "stall.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(4 * 1024, 9));

  let unlinkedPath = null;
  const channel = {
    readdir(_p, cb) { cb(null, []); },
    mkdir(_p, cb) { cb(null); },
    unlink(targetPath, cb) {
      unlinkedPath = targetPath;
      cb(null);
    },
    stat(_p, cb) {
      const err = new Error("ENOENT");
      err.code = 2;
      cb(err);
    },
    fastPut() {},
    end() {},
  };
  const sharedOnlyClient = {
    __netcattySudoMode: true,
    sftp: channel,
    client: null,
  };

  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map(),
  });

  const controller = new AbortController();
  const stagedPath = "/tmp/.netcatty-upload-deadbeef-stall.bin.part";
  const uploadPromise = sftpBridge.pipelinedUploadLocalFile(
    sharedOnlyClient,
    localPath,
    stagedPath,
    {
      concurrency: UPLOAD_TRANSFER_CONCURRENCY,
      chunkSize: TRANSFER_CHUNK_SIZE,
      signal: controller.signal,
      generatedStagePath: true,
    },
  );

  await new Promise((r) => setImmediate(r));
  controller.abort();
  await assert.rejects(uploadPromise, /abort|cancel/i);
  assert.equal(unlinkedPath, stagedPath);

  await fs.promises.rm(tempRoot, { recursive: true, force: true });
});

test("shared-channel cancel never unlinks a caller path that resembles a stage", async () => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-shared-stage-lookalike-"));
  const localPath = path.join(tempRoot, "stall.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(4 * 1024, 11));

  let unlinkedPath = null;
  const channel = {
    readdir(_p, cb) { cb(null, []); },
    mkdir(_p, cb) { cb(null); },
    unlink(targetPath, cb) {
      unlinkedPath = targetPath;
      cb(null);
    },
    stat(_p, cb) {
      const err = new Error("ENOENT");
      err.code = 2;
      cb(err);
    },
    fastPut() {},
    end() {},
  };
  const sharedOnlyClient = {
    __netcattySudoMode: true,
    sftp: channel,
    client: null,
  };

  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map(),
  });

  const controller = new AbortController();
  const callerPath = "/tmp/.netcatty-upload-deadbeef-user-file.part";
  const uploadPromise = sftpBridge.pipelinedUploadLocalFile(
    sharedOnlyClient,
    localPath,
    callerPath,
    {
      concurrency: UPLOAD_TRANSFER_CONCURRENCY,
      chunkSize: TRANSFER_CHUNK_SIZE,
      signal: controller.signal,
    },
  );

  await new Promise((r) => setImmediate(r));
  controller.abort();
  await assert.rejects(uploadPromise, /abort|cancel/i);
  assert.equal(unlinkedPath, null);

  await fs.promises.rm(tempRoot, { recursive: true, force: true });
});

test("shared-channel fastPut error force-settles when callback stalls", async () => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-shared-error-bound-"));
  const localPath = path.join(tempRoot, "err.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(4 * 1024, 7));

  let ended = false;
  const channel = new EventEmitter();
  Object.assign(channel, {
    readdir(_p, cb) { cb(null, []); },
    mkdir(_p, cb) { cb(null); },
    unlink(_p, cb) { cb(null); },
    stat(_p, cb) {
      const err = new Error("ENOENT");
      err.code = 2;
      cb(err);
    },
    // Emit channel error and never invoke the fastPut callback.
    fastPut() {
      queueMicrotask(() => channel.emit("error", new Error("channel failed")));
    },
    end() {
      ended = true;
    },
  });
  const sharedOnlyClient = {
    __netcattySudoMode: true,
    sftp: channel,
    client: null,
  };

  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map(),
  });

  const uploadPromise = sftpBridge.pipelinedUploadLocalFile(
    sharedOnlyClient,
    localPath,
    "/tmp/err-out.bin",
    {
      concurrency: UPLOAD_TRANSFER_CONCURRENCY,
      chunkSize: TRANSFER_CHUNK_SIZE,
    },
  );

  const started = Date.now();
  await assert.rejects(uploadPromise, /channel failed|SFTP channel/i);
  const elapsed = Date.now() - started;
  assert.ok(elapsed < 5000, `error settle took too long: ${elapsed}ms`);
  assert.equal(ended, false);

  await fs.promises.rm(tempRoot, { recursive: true, force: true });
});

test("pipelinedUploadLocalFile falls back to raw sftp.fastPut when client.fastPut is missing", async () => {
  const tempRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-raw-fastput-"));
  const localPath = path.join(tempRoot, "raw.bin");
  await fs.promises.writeFile(localPath, Buffer.from("hello-raw"));

  let sawRawFastPut = false;
  const channel = {
    readdir(_p, cb) { cb(null, []); },
    mkdir(_p, cb) { cb(null); },
    unlink(_p, cb) { cb(null); },
    stat(_p, cb) {
      const err = new Error("ENOENT");
      err.code = 2;
      cb(err);
    },
    fastPut(local, remote, _opts, callback) {
      sawRawFastPut = local === localPath && remote === "/tmp/out.bin";
      queueMicrotask(() => callback(null));
    },
  };
  // Bare client with only raw channel.fastPut (no high-level client.fastPut).
  const bareClient = {
    sftp: channel,
    client: {
      sftp(cb) {
        cb(null, channel);
      },
    },
  };

  sftpBridge.init({
    electronModule: {},
    sessions: new Map(),
    sftpClients: new Map(),
  });

  await sftpBridge.pipelinedUploadLocalFile(bareClient, localPath, "/tmp/out.bin", {
    concurrency: UPLOAD_TRANSFER_CONCURRENCY,
    chunkSize: TRANSFER_CHUNK_SIZE,
  });
  assert.equal(sawRawFastPut, true);

  await fs.promises.rm(tempRoot, { recursive: true, force: true });
});
