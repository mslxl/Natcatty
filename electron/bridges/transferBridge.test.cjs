const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");
const Module = require("node:module");
const { EventEmitter } = require("node:events");
const { PassThrough, Readable, Writable } = require("node:stream");

const transferBridge = require("./transferBridge.cjs");
const sftpBridge = require("./sftpBridge.cjs");
const tempDirBridge = require("./tempDirBridge.cjs");
const {
  DOWNLOAD_TRANSFER_CONCURRENCY,
  TRANSFER_CHUNK_SIZE,
  UPLOAD_TRANSFER_CONCURRENCY,
} = require("./transferLimits.cjs");

function createSender() {
  return {
    sent: [],
    send(channel, payload) {
      this.sent.push({ channel, payload });
    },
  };
}

async function waitUntil(predicate, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (!predicate() && Date.now() < deadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  return predicate();
}

function createFastSftp(overrides) {
  const sftp = new EventEmitter();
  sftp.readdir = (_path, callback) => callback(null, []);
  sftp.stat = (_path, callback) => callback(null, { size: 1024 * 1024 });
  sftp.lstat = (_path, callback) => {
    const error = new Error("ENOENT");
    error.code = 2;
    callback(error);
  };
  sftp.mkdir = (_path, callback) => callback(null);
  sftp.unlink = (_path, callback) => callback(null);
  sftp.end = () => {};
  Object.assign(sftp, overrides);
  return sftp;
}

test("SFTP upload ignores cancellation after remote promotion is committed", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-sftp-commit-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.from("payload");
  await fs.promises.writeFile(localPath, payload);
  const targetPath = "/tmp/payload.bin";
  const remoteFiles = new Map([[targetPath, Buffer.from("old payload")]]);

  let releaseRename;
  let markRenameStarted;
  const renameStarted = new Promise((resolve) => { markRenameStarted = resolve; });
  let directRenameAttempted = false;
  const fastSftp = createFastSftp({
    lstat(remotePath, callback) {
      const key = String(remotePath);
      if (!remoteFiles.has(key)) {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
        return;
      }
      callback(null, {
        size: remoteFiles.get(key).length,
        mode: 0o100644,
        isDirectory: () => false,
        isSymbolicLink: () => false,
      });
    },
    open(remotePath, _flags, callback) {
      const key = String(remotePath);
      remoteFiles.set(key, Buffer.alloc(payload.length));
      callback(null, Buffer.from(key));
    },
    write(handle, buffer, offset, length, position, callback) {
      const key = handle.toString();
      buffer.copy(remoteFiles.get(key), position, offset, offset + length);
      setImmediate(() => callback(null));
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    __netcattySudoMode: true,
    sftp: fastSftp,
    stat: async (remotePath) => {
      const key = String(remotePath);
      if (!remoteFiles.has(key)) {
        const error = new Error("ENOENT");
        error.code = 2;
        throw error;
      }
      return { size: remoteFiles.get(key).length, isDirectory: false };
    },
    chmod: async () => {},
    delete: async (remotePath) => { remoteFiles.delete(String(remotePath)); },
    async rename(fromPath, toPath) {
      const from = String(fromPath);
      const to = String(toPath);
      if (from.includes(".netcatty-upload-") && to === targetPath && !directRenameAttempted) {
        directRenameAttempted = true;
        throw new Error("atomic replace unavailable");
      }
      if (from.includes(".netcatty-upload-") && to === targetPath) {
        markRenameStarted();
        await new Promise((resolve) => { releaseRename = resolve; });
      }
      remoteFiles.set(to, remoteFiles.get(from));
      remoteFiles.delete(from);
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });
  const sender = createSender();
  const transferId = "sftp-commit-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  await renameStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  releaseRename();
  const result = await running;

  assert.equal(result.error, undefined);
  assert.deepEqual(remoteFiles.get(targetPath), payload);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")), false);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-backup-")), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), false);
});

test("SCP upload ignores cancellation after remote promotion is committed", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-commit-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.from("payload");
  await fs.promises.writeFile(localPath, payload);
  const targetPath = "/tmp/payload.bin";
  const remoteFiles = new Map([[targetPath, Buffer.from("old payload")]]);

  let releaseRename;
  let markRenameStarted;
  const renameStarted = new Promise((resolve) => { markRenameStarted = resolve; });
  const backend = {
    async mkdir() {},
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return { type: "file", isDirectory: false, size: remoteFiles.get(remotePath).length };
    },
    async uploadFile(localSourcePath, remotePath, options = {}) {
      remoteFiles.set(remotePath, await fs.promises.readFile(localSourcePath));
      options.onProgress?.(payload.length, payload.length);
    },
    async rename(fromPath, toPath) {
      if (fromPath === targetPath) {
        markRenameStarted();
        await new Promise((resolve) => { releaseRename = resolve; });
      }
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
    },
    async remove(remotePath) { remoteFiles.delete(remotePath); },
    async chmod() {},
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });
  const sender = createSender();
  const transferId = "scp-commit-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  await renameStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  releaseRename();
  const result = await running;

  assert.equal(result.error, undefined);
  assert.deepEqual(remoteFiles.get(targetPath), payload);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")), false);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-backup-")), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), false);
});

test("SCP upload rejects transient source bytes before touching the old target", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-source-change-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.alloc(16 * 1024, 57);
  await fs.promises.writeFile(localPath, payload);
  const targetPath = "/tmp/payload.bin";
  const oldTarget = Buffer.from("old payload");
  const remoteFiles = new Map([[targetPath, oldTarget]]);
  const removed = [];
  let uploadCalls = 0;
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return { type: "file", isDirectory: false, size: remoteFiles.get(remotePath).length };
    },
    async uploadFile(_sourcePath, _remotePath, options) {
      uploadCalls += 1;
      const opened = options.openReadStream();
      for await (const _chunk of opened.stream) {
        // Consume the verified stream. The injected transient bytes must fail
        // before the staged upload can be promoted.
      }
    },
    async remove(remotePath) {
      removed.push(remotePath);
      remoteFiles.delete(remotePath);
    },
    async rename() {
      throw new Error("promotion must not run");
    },
    async chmod() {},
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const realOpen = fs.promises.open.bind(fs.promises);
  let sourceReadOpens = 0;
  let transientReadInjected = false;
  fs.promises.open = async (p, flags, ...args) => {
    const handle = await realOpen(p, flags, ...args);
    if (path.resolve(String(p)) !== path.resolve(localPath) || !String(flags).includes("r")) {
      return handle;
    }
    sourceReadOpens += 1;
    if (sourceReadOpens !== 3) return handle;
    const realRead = handle.read.bind(handle);
    handle.read = async (buffer, offset, length, position) => {
      const result = await realRead(buffer, offset, length, position);
      if (!transientReadInjected && result.bytesRead > 0) {
        buffer.fill(58, offset, offset + result.bytesRead);
        transientReadInjected = true;
      }
      return result;
    };
    return handle;
  };
  t.after(() => {
    fs.promises.open = realOpen;
  });

  const transferId = "scp-source-change";
  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  const snapshotPath = tempDirBridge.getTransferTempFilePath(
    `upload-source-${digestId}`,
    "snapshot.bin",
  );
  assert.equal(transientReadInjected, true);
  assert.match(result.error || "", /source content changed/i);
  assert.equal(uploadCalls, 1);
  assert.deepEqual(remoteFiles.get(targetPath), oldTarget);
  assert.ok(removed.some((remotePath) => remotePath.includes(".netcatty-upload-")));
  assert.equal(fs.existsSync(digestPath), false);
  assert.equal(fs.existsSync(snapshotPath), false);
});

test("SCP staged streaming preserves executable mode without a local snapshot", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-executable-mode-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "tool.sh");
  const payload = Buffer.from("#!/bin/sh\necho ok\n");
  await fs.promises.writeFile(localPath, payload);
  await fs.promises.chmod(localPath, 0o755);
  const targetPath = "/usr/local/bin/tool.sh";
  const remoteFiles = new Map();
  let uploadedMode = null;
  let uploadedSourcePath = null;
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return { type: "file", isDirectory: false, size: remoteFiles.get(remotePath).length };
    },
    async uploadFile(sourcePath, remotePath, options) {
      uploadedSourcePath = sourcePath;
      uploadedMode = (await fs.promises.stat(sourcePath)).mode & 0o777;
      assert.equal(options.fileSize, payload.length);
      const opened = options.openReadStream();
      const chunks = [];
      for await (const chunk of opened.stream) chunks.push(chunk);
      remoteFiles.set(remotePath, Buffer.concat(chunks));
    },
    async rename(fromPath, toPath) {
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
    },
    async chmod() {},
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId: "scp-executable-mode",
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  assert.equal(result.error, undefined);
  assert.equal(uploadedSourcePath, localPath);
  assert.equal(uploadedMode, 0o755);
  assert.deepEqual(remoteFiles.get(targetPath), payload);
  const digestId = crypto.createHash("sha256")
    .update("scp-executable-mode")
    .digest("hex")
    .slice(0, 16);
  const snapshotPath = tempDirBridge.getTransferTempFilePath(
    `upload-source-${digestId}`,
    "snapshot.bin",
  );
  assert.equal(fs.existsSync(snapshotPath), false);
});

test("SCP staged upload rechecks a deleted destination after mode setup", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-mode-target-race-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.from("uploaded-data");
  await fs.promises.writeFile(localPath, payload);
  const targetPath = "/tmp/payload.bin";
  const remoteFiles = new Map([[targetPath, Buffer.from("old-state")]]);
  const remoteMeta = new Map([[targetPath, { mode: 0o100755, modifyTime: 1 }]]);
  let renameCalls = 0;
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      const meta = remoteMeta.get(remotePath) || { mode: 0o100644, modifyTime: 0 };
      return {
        type: "file",
        isDirectory: false,
        size: remoteFiles.get(remotePath).length,
        mode: meta.mode,
        permissions: "rwxr-xr-x",
        modifyTime: meta.modifyTime,
      };
    },
    async uploadFile(_sourcePath, remotePath, options) {
      const opened = options.openReadStream();
      const chunks = [];
      for await (const chunk of opened.stream) chunks.push(chunk);
      remoteFiles.set(remotePath, Buffer.concat(chunks));
      remoteMeta.set(remotePath, { mode: 0o100644, modifyTime: 0 });
    },
    async chmod(remotePath) {
      if (remotePath.includes(".netcatty-upload-")) {
        remoteFiles.delete(targetPath);
        remoteMeta.delete(targetPath);
      }
    },
    async rename(fromPath, toPath) {
      renameCalls += 1;
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
      remoteMeta.delete(remotePath);
    },
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId: "scp-mode-target-race",
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  assert.match(result.error || "", /destination disappeared during upload/i);
  assert.equal(renameCalls, 0);
  assert.equal(remoteFiles.has(targetPath), false);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")), false);
});

test("SCP staged upload does not promote when only AbortSignal cancels final verification", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-scp-final-verify-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.alloc(16 * 1024, 91);
  await fs.promises.writeFile(localPath, payload);
  const targetPath = "/tmp/payload.bin";
  const remoteFiles = new Map();
  let renameCalls = 0;
  const backend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) {
        const error = new Error("No such file");
        error.code = "ENOENT";
        throw error;
      }
      return { type: "file", isDirectory: false, size: remoteFiles.get(remotePath).length };
    },
    async uploadFile(_sourcePath, remotePath, options) {
      const opened = options.openReadStream();
      const chunks = [];
      for await (const chunk of opened.stream) chunks.push(chunk);
      remoteFiles.set(remotePath, Buffer.concat(chunks));
    },
    async rename(fromPath, toPath) {
      renameCalls += 1;
      remoteFiles.set(toPath, remoteFiles.get(fromPath));
      remoteFiles.delete(fromPath);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
    },
    async chmod() {},
  };
  const client = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: backend,
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const realOpen = fs.promises.open.bind(fs.promises);
  let sourceReadOpens = 0;
  let releaseFinalVerify;
  let markFinalVerifyStarted;
  const finalVerifyStarted = new Promise((resolve) => { markFinalVerifyStarted = resolve; });
  fs.promises.open = async (p, flags, ...args) => {
    const handle = await realOpen(p, flags, ...args);
    if (path.resolve(String(p)) !== path.resolve(localPath) || !String(flags).includes("r")) {
      return handle;
    }
    sourceReadOpens += 1;
    if (sourceReadOpens !== 4) return handle;
    const realRead = handle.read.bind(handle);
    handle.read = async (...readArgs) => {
      markFinalVerifyStarted();
      await new Promise((resolve) => { releaseFinalVerify = resolve; });
      return realRead(...readArgs);
    };
    return handle;
  };
  t.after(() => {
    fs.promises.open = realOpen;
  });

  const controller = new AbortController();
  const transferId = "scp-final-verify-cancel";
  const running = transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
    abortSignal: controller.signal,
  });

  await finalVerifyStarted;
  controller.abort();
  releaseFinalVerify();
  const result = await running;

  assert.match(result.error || "", /cancel/i);
  assert.equal(renameCalls, 0);
  assert.equal(remoteFiles.has(targetPath), false);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")), false);
});

test("in-place upload ignores cancellation during final size verification", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-in-place-commit-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.from("payload");
  await fs.promises.writeFile(localPath, payload);
  const targetPath = "/tmp/link.bin";
  let remote = Buffer.from("old payload");
  let statCalls = 0;
  let releaseFinalStat;
  let markFinalStatStarted;
  const finalStatStarted = new Promise((resolve) => { markFinalStatStarted = resolve; });
  const fastSftp = createFastSftp({
    lstat(remotePath, callback) {
      if (String(remotePath).includes(".netcatty-backup-")) {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
        return;
      }
      callback(null, {
        size: remote.length,
        mode: 0o120777,
        isDirectory: () => false,
        isSymbolicLink: () => true,
      });
    },
    open(_remotePath, _flags, callback) {
      remote = Buffer.alloc(payload.length);
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      buffer.copy(remote, position, offset, offset + length);
      setImmediate(() => callback(null));
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    __netcattySudoMode: true,
    sftp: fastSftp,
    async stat(remotePath) {
      if (String(remotePath).includes(".netcatty-backup-")) {
        const error = new Error("ENOENT");
        error.code = "ENOENT";
        throw error;
      }
      statCalls += 1;
      if (statCalls === 1) {
        markFinalStatStarted();
        await new Promise((resolve) => { releaseFinalStat = resolve; });
      }
      return { size: remote.length };
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });
  const sender = createSender();
  const transferId = "in-place-commit-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: localPath,
    targetPath,
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  await finalStatStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  releaseFinalStat();
  const result = await running;

  assert.equal(result.error, undefined);
  assert.deepEqual(remote, payload);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), false);
  assert.equal(sender.sent.some((entry) => (
    entry.channel === "netcatty:transfer:progress"
    && entry.payload.transferred === payload.length
    && entry.payload.totalBytes === payload.length
  )), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), true);
});

test("in-place concurrent upload ignores cancellation while the remote handle closes", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-in-place-close-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.from("payload");
  await fs.promises.writeFile(localPath, payload);
  let remote = Buffer.from("old payload");
  let releaseClose;
  let markCloseStarted;
  const closeStarted = new Promise((resolve) => { markCloseStarted = resolve; });
  const fastSftp = createFastSftp({
    lstat(remotePath, callback) {
      if (String(remotePath).includes(".netcatty-backup-")) {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
        return;
      }
      callback(null, {
        size: remote.length,
        mode: 0o120777,
        isDirectory: () => false,
        isSymbolicLink: () => true,
      });
    },
    open(_remotePath, _flags, callback) {
      remote = Buffer.alloc(payload.length);
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      buffer.copy(remote, position, offset, offset + length);
      setImmediate(() => callback(null));
    },
    close(_handle, callback) {
      markCloseStarted();
      releaseClose = () => callback(null);
    },
  });
  const client = {
    __netcattySudoMode: true,
    sftp: fastSftp,
    stat: async (remotePath) => {
      if (String(remotePath).includes(".netcatty-backup-")) {
        const error = new Error("ENOENT");
        error.code = "ENOENT";
        throw error;
      }
      return { size: remote.length };
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });
  const sender = createSender();
  const transferId = "in-place-close-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: localPath,
    targetPath: "/tmp/link.bin",
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  await closeStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  releaseClose();
  const result = await running;

  assert.equal(result.error, undefined);
  assert.deepEqual(remote, payload);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), true);
});

test("SFTP download cancellation settles while initial metadata is stalled", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-download-stat-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  let markStatStarted;
  const statStarted = new Promise((resolve) => { markStatStarted = resolve; });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      markStatStarted();
      return new Promise(() => {});
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });
  const sender = createSender();
  const transferId = "download-stat-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: "/tmp/source.bin",
    targetPath: path.join(tempDir, "target.bin"),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    resumable: true,
  });

  await statStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => setTimeout(() => reject(new Error("cancel timed out")), 500)),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("SFTP download cancellation settles while source snapshot metadata is stalled", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-download-snapshot-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  let statCalls = 0;
  let markSnapshotStarted;
  const snapshotStarted = new Promise((resolve) => { markSnapshotStarted = resolve; });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      statCalls += 1;
      if (statCalls === 1) return Promise.resolve({ size: 1024 });
      markSnapshotStarted();
      return new Promise(() => {});
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });
  const sender = createSender();
  const transferId = "download-snapshot-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: "/tmp/source.bin",
    targetPath: path.join(tempDir, "target.bin"),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    resumable: true,
  });

  await snapshotStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => setTimeout(() => reject(new Error("cancel timed out")), 500)),
  ]);

  assert.equal(statCalls, 2);
  assert.match(result.error || "", /cancel/i);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("SFTP upload cancellation settles while remote directory setup is stalled", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-upload-dir-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "source.bin");
  await fs.promises.writeFile(localPath, Buffer.from("payload"));
  let releaseStat;
  let statCalls = 0;
  let markStatStarted;
  const statStarted = new Promise((resolve) => { markStatStarted = resolve; });
  const createdDirectories = [];
  const stalledSftp = createFastSftp({
    stat(_remotePath, callback) {
      statCalls += 1;
      if (statCalls > 1) {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
        return;
      }
      releaseStat = () => {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
      };
      markStatStarted();
    },
    mkdir(remotePath, callback) {
      createdDirectories.push(String(remotePath));
      callback(null);
    },
  });
  const client = {
    sftp: stalledSftp,
  };
  const clients = new Map([["target", client]]);
  sftpBridge.init({ sftpClients: clients, sessions: new Map(), electronModule: {} });
  t.after(() => {
    sftpBridge.init({ sftpClients: new Map(), sessions: new Map(), electronModule: {} });
  });
  transferBridge.init({ sftpClients: clients });
  const sender = createSender();
  const transferId = "upload-directory-cancel";
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: localPath,
    targetPath: "/stalled/directory/target.bin",
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    resumable: false,
  });

  await statStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => setTimeout(() => reject(new Error("cancel timed out")), 500)),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
  assert.deepEqual(createdDirectories, []);
  releaseStat();
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.deepEqual(createdDirectories, []);
});

test("SFTP upload cancellation waits for an in-flight remote mkdir to settle", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-upload-mkdir-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "source.bin");
  await fs.promises.writeFile(localPath, Buffer.from("payload"));
  let releaseMkdir;
  let markMkdirStarted;
  const mkdirStarted = new Promise((resolve) => { markMkdirStarted = resolve; });
  const createdDirectories = [];
  const stalledSftp = createFastSftp({
    stat(_remotePath, callback) {
      const error = new Error("ENOENT");
      error.code = 2;
      callback(error);
    },
    mkdir(remotePath, callback) {
      const directory = String(remotePath);
      markMkdirStarted();
      releaseMkdir = () => {
        createdDirectories.push(directory);
        callback(null);
      };
    },
  });
  const client = { sftp: stalledSftp };
  const clients = new Map([["target", client]]);
  sftpBridge.init({ sftpClients: clients, sessions: new Map(), electronModule: {} });
  t.after(() => {
    sftpBridge.init({ sftpClients: new Map(), sessions: new Map(), electronModule: {} });
  });
  transferBridge.init({ sftpClients: clients });
  const sender = createSender();
  const transferId = "upload-mkdir-cancel";
  let settled = false;
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: localPath,
    targetPath: "/stalled/directory/target.bin",
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    resumable: false,
  }).finally(() => { settled = true; });

  await mkdirStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(settled, false);
  assert.deepEqual(createdDirectories, []);

  releaseMkdir();
  const result = await running;
  assert.match(result.error || "", /cancel/i);
  assert.deepEqual(createdDirectories, ["/stalled"]);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("cancelling one SFTP channel waiter does not cancel the shared reopen", async () => {
  let openCalls = 0;
  let releaseChannel;
  const reopenedChannel = createFastSftp({});
  const client = {
    sftp: null,
    client: {
      sftp(callback) {
        openCalls += 1;
        releaseChannel = () => callback(null, reopenedChannel);
      },
    },
  };
  const firstController = new AbortController();
  const secondController = new AbortController();
  const firstWait = sftpBridge.requireSftpChannel(client, { signal: firstController.signal });
  const secondWait = sftpBridge.requireSftpChannel(client, { signal: secondController.signal });

  firstController.abort(new Error("first waiter cancelled"));
  await assert.rejects(() => firstWait, /first waiter cancelled/i);
  assert.equal(openCalls, 1);
  releaseChannel();

  assert.equal(await secondWait, reopenedChannel);
  assert.equal(client.sftp, reopenedChannel);
  assert.equal(openCalls, 1);
});

test("shared SFTP channel reopen preserves the requested timeout", async () => {
  const client = {
    sftp: null,
    client: {
      sftp() {},
    },
  };
  const startedAt = Date.now();

  await assert.rejects(
    () => sftpBridge.requireSftpChannel(client, { timeoutMs: 10 }),
    /session lost/i,
  );

  assert.ok(Date.now() - startedAt < 500);
  assert.equal(client._reopeningPromise, null);
});

test("server-to-server resume cancellation settles while source identity verification is stalled", async (t) => {
  const transferId = `server-prefix-cancel-${crypto.randomUUID()}`;
  const sourcePath = "/source/payload.bin";
  const tempPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(sourcePath));
  await fs.promises.writeFile(tempPath, Buffer.from("abcd"));
  t.after(async () => fs.promises.rm(tempPath, { force: true }));
  let markPrefixStarted;
  const prefixStarted = new Promise((resolve) => { markPrefixStarted = resolve; });
  const sourceClient = {
    sftp: createFastSftp({
      createReadStream() {
        markPrefixStarted();
        return new PassThrough();
      },
    }),
  };
  const targetClient = { sftp: createFastSftp({}) };
  transferBridge.init({
    sftpClients: new Map([["source", sourceClient], ["target", targetClient]]),
  });
  const sender = createSender();
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath,
    targetPath: "/target/payload.bin",
    sourceType: "sftp",
    targetType: "sftp",
    sourceSftpId: "source",
    targetSftpId: "target",
    totalBytes: 8,
    resumable: true,
    sameHost: false,
    resumeStage: "download",
    checkpointBytes: 4,
    downloadCheckpointBytes: 4,
    sourceFingerprint: `sha256:${"0".repeat(64)}`,
  });

  await prefixStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => setTimeout(() => reject(new Error("cancel timed out")), 500)),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("resumable SFTP uploads use the configured per-file request concurrency", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // Need more than one fanout wave so the concurrency cap is observable.
  const fileSize = UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2;
  const localPath = path.join(tempDir, "large.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(fileSize));

  let activeWrites = 0;
  let observedConcurrency = 0;
  let observedChunkSize = 0;
  const pendingWrites = [];
  let holdWrites = true;
  const fastSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "w");
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, _position, callback) {
      activeWrites += 1;
      observedConcurrency = Math.max(observedConcurrency, activeWrites);
      observedChunkSize = Math.max(observedChunkSize, length);
      if (holdWrites) {
        pendingWrites.push(() => {
          activeWrites -= 1;
          callback(null);
        });
        return;
      }
      setImmediate(() => {
        activeWrites -= 1;
        callback(null);
      });
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: fileSize });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-large",
      sourcePath: localPath,
      targetPath: "/tmp/large.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      resumable: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (pendingWrites.length < UPLOAD_TRANSFER_CONCURRENCY && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(pendingWrites.length, UPLOAD_TRANSFER_CONCURRENCY);
  assert.equal(observedConcurrency, UPLOAD_TRANSFER_CONCURRENCY);
  assert.equal(observedChunkSize, TRANSFER_CHUNK_SIZE);
  holdWrites = false;
  for (const complete of pendingWrites.splice(0)) complete();
  const result = await running;
  assert.equal(result.error, undefined);
});

test("fast resumable uploads pause only after in-flight ranges are durable", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-fast-upload-pause-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // First in-flight wave must be smaller than the file so pause can land mid-transfer.
  const payload = Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2, 13);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  const pendingWrites = [];
  let holdWrites = true;
  let durableBytes = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      const complete = () => {
        durableBytes = Math.max(durableBytes, position + length);
        callback(null);
      };
      if (holdWrites) pendingWrites.push(complete);
      else setImmediate(complete);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: durableBytes });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-fast-paused",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (pendingWrites.length < UPLOAD_TRANSFER_CONCURRENCY && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(pendingWrites.length, UPLOAD_TRANSFER_CONCURRENCY);

  const pausing = transferBridge.pauseTransfer(null, { transferId: "upload-fast-paused" });
  holdWrites = false;
  for (const complete of pendingWrites.splice(0)) complete();
  const paused = await pausing;
  assert.equal(paused.success, true);
  assert.equal(paused.checkpointBytes, UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE);

  {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "upload-fast-paused" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  assert.equal((await running).error, undefined);
  assert.equal(durableBytes, payload.length);
});

test("pause soft-drains concurrent ranges but resume waits before truncating", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-pause-soft-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // Several MB so concurrent fanout stays saturated while we hold writes.
  const payload = Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 4, 65);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let holdWrites = true;
  const pendingWrites = [];
  let durableBytes = 0;
  const truncateCalls = [];
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      const complete = () => {
        durableBytes = Math.max(durableBytes, position + length);
        callback(null);
      };
      if (holdWrites) pendingWrites.push({ position, complete });
      else setImmediate(complete);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: durableBytes });
    },
    truncate(_remotePath, size) {
      truncateCalls.push(size);
      durableBytes = size;
      return Promise.resolve();
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-soft-pause",
      sourcePath: localPath,
      targetPath: "/tmp/upload-soft.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (pendingWrites.length < UPLOAD_TRANSFER_CONCURRENCY && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(pendingWrites.length >= 1, "expected in-flight concurrent writes");

  const started = Date.now();
  // Hold writes so active ranges never drain — soft-drain must still resolve.
  const paused = await transferBridge.pauseTransfer(null, { transferId: "upload-soft-pause" });
  const elapsed = Date.now() - started;
  assert.equal(paused.success, true);
  // Soft drain is PAUSE_RANGE_DRAIN_MS (~50ms); allow headroom without full drain.
  assert.ok(elapsed < 1500, `soft pause took too long: ${elapsed}ms`);

  const outOfOrderWrite = pendingWrites.pop();
  assert.ok(outOfOrderWrite?.position > 0, "expected a range beyond the contiguous checkpoint");
  outOfOrderWrite.complete();
  await new Promise((resolve) => setImmediate(resolve));

  let resumeSettled = false;
  const resuming = transferBridge.resumeTransfer(null, { transferId: "upload-soft-pause" })
    .then((result) => {
      resumeSettled = true;
      return result;
    });
  await new Promise((resolve) => setTimeout(resolve, 500));
  const truncatedBeforeDrain = truncateCalls.length > 0;
  const resumedBeforeDrain = resumeSettled;

  holdWrites = false;
  for (const { complete } of pendingWrites.splice(0)) complete();
  const resumeResult = await resuming;
  assert.equal(resumeResult.success, true);
  assert.ok(Number.isFinite(resumeResult.lifecycleEpoch));
  assert.equal((await running).error, undefined);
  assert.equal(truncatedBeforeDrain, false, "must not truncate while range writes are active");
  assert.equal(resumedBeforeDrain, false, "resume must wait for active range writes to settle");
  assert.equal(durableBytes, payload.length);
});

test("cancelling resume during soft-drain does not truncate the staged file", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-resume-cancel-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2, 66);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let holdWrites = true;
  const pendingWrites = [];
  const truncateCalls = [];
  let durableBytes = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      const complete = () => {
        durableBytes = Math.max(durableBytes, position + length);
        callback(null);
      };
      if (holdWrites) pendingWrites.push(complete);
      else setImmediate(complete);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: durableBytes });
    },
    truncate(_remotePath, size) {
      truncateCalls.push(size);
      durableBytes = size;
      return Promise.resolve();
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const transferId = "upload-resume-cancel";
  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload-resume-cancel.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (pendingWrites.length < UPLOAD_TRANSFER_CONCURRENCY && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(pendingWrites.length, UPLOAD_TRANSFER_CONCURRENCY);
  assert.equal((await transferBridge.pauseTransfer(null, { transferId })).success, true);

  const resuming = transferBridge.resumeTransfer(null, { transferId });
  await transferBridge.cancelTransfer(null, { transferId });
  holdWrites = false;
  for (const complete of pendingWrites.splice(0)) complete();

  const transferResult = await running;
  const resumeResult = await resuming;
  assert.match(transferResult.error || "", /cancelled/i);
  assert.deepEqual(resumeResult, {
    success: false,
    reason: "Transfer is no longer active",
  });
  assert.deepEqual(truncateCalls, []);
});

test("resuming while a fast pause is pending settles the pause request", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-pause-resume-race-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(32 * 1024, 67);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let finishWrite = null;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      finishWrite = () => callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "pause-resume-race",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );
  const deadline = Date.now() + 1000;
  while (!finishWrite && Date.now() < deadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(typeof finishWrite, "function");

  const pausing = transferBridge.pauseTransfer(null, { transferId: "pause-resume-race" });
  {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "pause-resume-race" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  assert.deepEqual(await pausing, {
    success: false,
    reason: "Pause was superseded by resume",
  });

  finishWrite();
  assert.equal((await running).error, undefined);
});

test("pause acknowledges quickly then publishes a full source identity", async (t) => {
  // Pause acknowledgement must not wait for full-file SHA-256. The background
  // identity is nevertheless a complete digest so a later resume cannot mix
  // bytes from different source versions.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-late-pause-race-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2, 71);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  const pendingWrites = [];
  let holdWrites = true;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      if (holdWrites) pendingWrites.push(callback);
      else setImmediate(() => callback(null));
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  let running;
  {
    const sender = createSender();
    running = transferBridge.startTransfer(
      { sender },
      {
        transferId: "late-pause-race",
        sourcePath: localPath,
        targetPath: "/tmp/upload.bin",
        sourceType: "local",
        targetType: "sftp",
        targetSftpId: "target",
        totalBytes: payload.length,
        resumable: true,
      },
    );
    t.after(async () => {
      await transferBridge.cancelTransfer(null, { transferId: "late-pause-race" });
      await running?.catch(() => {});
      transferBridge.clearPendingCancel("late-pause-race");
    });
    const writeDeadline = Date.now() + 1000;
    while (pendingWrites.length < UPLOAD_TRANSFER_CONCURRENCY && Date.now() < writeDeadline) {
      await new Promise((resolve) => setImmediate(resolve));
    }
    assert.equal(pendingWrites.length, UPLOAD_TRANSFER_CONCURRENCY);

    const pauseStarted = Date.now();
    const pausing = transferBridge.pauseTransfer(null, { transferId: "late-pause-race" });
    holdWrites = false;
    for (const callback of pendingWrites.splice(0)) callback(null);
    const paused = await pausing;
    const pauseElapsed = Date.now() - pauseStarted;
    assert.equal(paused.success, true);
    assert.ok(pauseElapsed < 1500, `pause blocked too long: ${pauseElapsed}ms`);
    const fingerprintPublished = await waitUntil(() => sender.sent.some((entry) => (
      entry.channel === "netcatty:transfer:progress"
      && /^sha256:[a-f0-9]{64}$/.test(entry.payload.sourceFingerprint || "")
    )));
    assert.equal(fingerprintPublished, true, "full pause identity must be published after pause acknowledgement");
    const fingerprintEntry = sender.sent.findLast((entry) => (
        entry.channel === "netcatty:transfer:progress"
        && /^sha256:[a-f0-9]{64}$/.test(entry.payload.sourceFingerprint || "")
    ));
    assert.equal(
      fingerprintEntry?.payload.sourceFingerprint,
      `sha256:${crypto.createHash("sha256").update(payload).digest("hex")}`,
    );

    {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "late-pause-race" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  }

  assert.equal((await running).error, undefined);
});

test("remote pause identity rejects a same-size source rewrite", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-modifytime-id-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.from("abcdef");
  const modifyTimeMs = 1_700_000_000_000;
  let currentPayload = payload;
  const source = new PassThrough();
  let readStreamCalls = 0;
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        readStreamCalls += 1;
        return readStreamCalls === 1 ? source : Readable.from(currentPayload);
      },
    }),
    stat() {
      return Promise.resolve({ size: currentPayload.length, modifyTime: modifyTimeMs });
    },
    client: { sftp(callback) { callback(new Error("isolated channel unavailable")); } },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const targetPath = path.join(tempDir, "target.bin");
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "download-modifytime-id",
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );
  t.after(async () => {
    source.destroy();
    await transferBridge.cancelTransfer(null, { transferId: "download-modifytime-id" });
    await running.catch(() => {});
    transferBridge.clearPendingCancel("download-modifytime-id");
  });

  const readyDeadline = Date.now() + 1000;
  while (source.listenerCount("data") === 0 && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(source.listenerCount("data") > 0);
  source.write(payload.subarray(0, 3));
  await new Promise((resolve) => setImmediate(resolve));

  const paused = await transferBridge.pauseTransfer(null, { transferId: "download-modifytime-id" });
  assert.equal(paused.success, true);
  const digest = crypto.createHash("sha256").update(payload).digest("hex");
  // Remote download fingerprints are versioned with planned prefix coverage.
  const expectedFingerprint = `sha256:p${payload.length}:${digest}`;
  const fingerprintPublished = await waitUntil(() => sender.sent.some((entry) => (
    entry.channel === "netcatty:transfer:progress"
    && entry.payload.sourceFingerprint === expectedFingerprint
  )));
  assert.equal(fingerprintPublished, true, "remote pause identity must be published after pause acknowledgement");
  const fingerprintEntry = sender.sent.findLast((entry) => (
    entry.channel === "netcatty:transfer:progress"
    && entry.payload.sourceFingerprint === expectedFingerprint
  ));
  const sourceFingerprint = fingerprintEntry?.payload.sourceFingerprint;
  assert.equal(sourceFingerprint, expectedFingerprint);

  await transferBridge.cancelTransfer(null, { transferId: "download-modifytime-id" });
  assert.match((await running).error || "", /cancel/i);

  currentPayload = Buffer.from(payload);
  currentPayload[4] = 90;
  const restarted = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-modifytime-id-resume",
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
      checkpointBytes: 3,
      sourceFingerprint,
    },
  );
  assert.match(restarted.error || "", /source file has changed/i);
});

test("legacy sampled identity restarts safely instead of resuming mixed content", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-meta-rewrite-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const fileSize = 512 * 1024;
  const checkpoint = 400 * 1024;
  // Between mid sample (~240–272 KiB) and tail sample (~480–512 KiB).
  const rewriteAt = 300 * 1024;
  const modifyTimeMs = 1_700_000_000_000;
  const original = Buffer.alloc(fileSize, 17);
  const rewritten = Buffer.from(original);
  rewritten.fill(99, rewriteAt, rewriteAt + TRANSFER_CHUNK_SIZE);

  const transferId = "meta-rewrite-resume";
  const targetPath = path.join(tempDir, "target.bin");
  const stagePath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  await fs.promises.mkdir(path.dirname(stagePath), { recursive: true });
  await fs.promises.writeFile(stagePath, original.subarray(0, checkpoint));

  const client = {
    sftp: createFastSftp({
      createReadStream(_remotePath, options = {}) {
        const start = Number.isFinite(options.start) ? options.start : 0;
        const end = Number.isFinite(options.end) ? options.end : rewritten.length - 1;
        return Readable.from(Buffer.from(rewritten.subarray(start, end + 1)));
      },
    }),
    stat() {
      return Promise.resolve({ size: fileSize, modifyTime: modifyTimeMs });
    },
    client: { sftp(callback) { callback(new Error("isolated channel unavailable")); } },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: fileSize,
      resumable: true,
      checkpointBytes: checkpoint,
      sourceFingerprint: `meta:${fileSize}:${modifyTimeMs}`,
    },
  );
  assert.equal(result.error, undefined);
  assert.deepEqual(await fs.promises.readFile(targetPath), rewritten);
});

test("resume rejects a rewrite beyond the first 256 KiB of the saved prefix", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-full-prefix-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const transferId = `full-prefix-${crypto.randomUUID()}`;
  const sourcePath = path.join(tempDir, "source.bin");
  const targetPath = path.join(tempDir, "target.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, "target.bin");
  const fileSize = 768 * 1024;
  const checkpoint = 400 * 1024;
  const original = Buffer.alloc(fileSize, 17);
  const current = Buffer.from(original);
  current.fill(99, 300 * 1024, 332 * 1024);
  await fs.promises.writeFile(sourcePath, current);
  await fs.promises.writeFile(stagedPath, original.subarray(0, checkpoint));
  t.after(async () => { await fs.promises.unlink(stagedPath).catch(() => {}); });

  transferBridge.init({ sftpClients: new Map() });
  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "local",
    targetType: "local",
    totalBytes: fileSize,
    resumable: true,
    checkpointBytes: checkpoint,
    sourceFingerprint: `sha256:${crypto.createHash("sha256").update(current).digest("hex")}`,
  });

  assert.match(result.error || "", /saved content does not match/i);
  assert.equal(fs.existsSync(targetPath), false);
});

test("local resume verification reports progress and destroys both readers on cancellation", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-verify-cancel-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const transferId = `verify-cancel-${crypto.randomUUID()}`;
  const sourcePath = path.join(tempDir, "source.bin");
  const targetPath = path.join(tempDir, "target.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, "target.bin");
  const checkpoint = 512 * 1024;
  const verifyBytes = checkpoint;
  const payload = Buffer.alloc(checkpoint + 1, 31);
  await fs.promises.writeFile(sourcePath, payload);
  await fs.promises.writeFile(stagedPath, payload.subarray(0, checkpoint));
  t.after(async () => { await fs.promises.unlink(stagedPath).catch(() => {}); });

  const sourceFingerprint = `sha256:${crypto.createHash("sha256").update(payload).digest("hex")}`;
  const originalCreateReadStream = fs.createReadStream;
  const verificationStreams = [];
  fs.createReadStream = (filePath, options = {}) => {
    if (
      (filePath === sourcePath || filePath === stagedPath)
      && options.start === 0
      && options.end === verifyBytes - 1
    ) {
      const stream = new PassThrough();
      verificationStreams.push(stream);
      return stream;
    }
    return originalCreateReadStream(filePath, options);
  };
  t.after(() => { fs.createReadStream = originalCreateReadStream; });

  transferBridge.init({ sftpClients: new Map() });
  const sender = createSender();
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "local",
    targetType: "local",
    totalBytes: payload.length,
    resumable: true,
    checkpointBytes: checkpoint,
    sourceFingerprint,
  });
  t.after(async () => {
    await transferBridge.cancelTransfer(null, { transferId });
    await running.catch(() => {});
    transferBridge.clearPendingCancel(transferId);
  });

  assert.equal(await waitUntil(() => verificationStreams.length === 2), true);
  await new Promise((resolve) => setTimeout(resolve, 220));
  for (const stream of verificationStreams) stream.write(Buffer.alloc(256 * 1024, 31));
  assert.equal(await waitUntil(() => sender.sent.some((entry) => (
    entry.channel === "netcatty:transfer:progress"
    && entry.payload.phase === "verifying"
    && entry.payload.speed > 0
  ))), true, "resume verification progress must be visible");
  const verifyingEvent = sender.sent.findLast((entry) => entry.payload?.phase === "verifying");
  assert.equal(verifyingEvent?.payload.transferred, checkpoint);
  assert.equal(verifyingEvent?.payload.checkpointBytes, checkpoint);

  await transferBridge.cancelTransfer(null, { transferId });
  const result = await running;
  assert.match(result.error || "", /cancel/i);
  assert.equal(verificationStreams.every((stream) => stream.destroyed), true);
});

test("remote resume verification destroys the SFTP reader on cancellation", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-remote-verify-cancel-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const transferId = `remote-verify-cancel-${crypto.randomUUID()}`;
  const targetPath = path.join(tempDir, "target.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, "target.bin");
  const checkpoint = 512 * 1024;
  const verifyBytes = checkpoint;
  const payload = Buffer.alloc(checkpoint + 1, 47);
  await fs.promises.writeFile(stagedPath, payload.subarray(0, checkpoint));
  t.after(async () => { await fs.promises.unlink(stagedPath).catch(() => {}); });

  const sourceFingerprint = `sha256:${crypto.createHash("sha256").update(payload).digest("hex")}`;
  let verificationStream;
  const sftp = createFastSftp({
    createReadStream(_remotePath, options = {}) {
      const start = Number(options.start) || 0;
      const end = Number.isFinite(options.end) ? options.end : payload.length - 1;
      if (start === 0 && end === verifyBytes - 1) {
        verificationStream = new PassThrough();
        return verificationStream;
      }
      return Readable.from(payload.subarray(start, end + 1));
    },
  });
  const client = {
    sftp,
    stat: async () => ({ size: payload.length }),
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath: "/tmp/source.bin",
    targetPath,
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes: payload.length,
    resumable: true,
    checkpointBytes: checkpoint,
    sourceFingerprint,
  });
  t.after(async () => {
    await transferBridge.cancelTransfer(null, { transferId });
    await running.catch(() => {});
    transferBridge.clearPendingCancel(transferId);
  });

  assert.equal(await waitUntil(() => verificationStream !== undefined), true);
  await new Promise((resolve) => setTimeout(resolve, 220));
  verificationStream.write(Buffer.alloc(256 * 1024, 47));
  assert.equal(await waitUntil(() => sender.sent.some((entry) => (
    entry.channel === "netcatty:transfer:progress"
    && entry.payload.phase === "verifying"
    && entry.payload.speed > 0
  ))), true);
  await transferBridge.cancelTransfer(null, { transferId });
  const result = await running;
  assert.match(result.error || "", /cancel/i);
  assert.equal(verificationStream.destroyed, true);
});

test("remote resume verification cancellation does not wait for SFTP channel reopen", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-remote-verify-reopen-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const transferId = `remote-verify-reopen-${crypto.randomUUID()}`;
  const targetPath = path.join(tempDir, "target.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, "target.bin");
  const checkpoint = 64 * 1024;
  const totalBytes = checkpoint + 1;
  await fs.promises.writeFile(stagedPath, Buffer.alloc(checkpoint, 19));
  t.after(async () => { await fs.promises.unlink(stagedPath).catch(() => {}); });

  let reopenStarted = false;
  let finishReopen;
  const client = {
    sftp: null,
    stat: async () => ({ size: totalBytes }),
    client: {
      sftp(callback) {
        reopenStarted = true;
        finishReopen = () => callback(null, createFastSftp({}));
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const running = transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath: "/tmp/source.bin",
    targetPath,
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes,
    resumable: true,
    checkpointBytes: checkpoint,
    sourceFingerprint: `sha256:${"0".repeat(64)}`,
  });
  t.after(async () => {
    await transferBridge.cancelTransfer(null, { transferId });
    await running.catch(() => {});
    transferBridge.clearPendingCancel(transferId);
  });

  assert.equal(await waitUntil(() => reopenStarted), true);
  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => setTimeout(() => reject(new Error("cancel timed out")), 250)),
  ]);
  assert.match(result.error || "", /cancel/i);
  finishReopen?.();
  assert.equal(await waitUntil(() => client._reopeningPromise === null), true);
});

test("failed resumable upload opens close their isolated channel", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-open-fail-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(32 * 1024, 23));
  let endedChannels = 0;
  let hadOpenErrorListener = false;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      hadOpenErrorListener = fastSftp.listenerCount("error") > 0;
      const error = new Error("permission denied");
      if (hadOpenErrorListener) fastSftp.emit("error", error);
      callback(error);
    },
    end() {
      endedChannels += 1;
    },
  });
  const client = {
    // Stream fallback also rejects so the transfer still fails after cleanup.
    sftp: createFastSftp({
      createWriteStream() {
        const writeStream = new Writable({
          write(_chunk, _encoding, callback) {
            callback(new Error("permission denied"));
          },
        });
        queueMicrotask(() => writeStream.destroy(new Error("permission denied")));
        return writeStream;
      },
    }),
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-open-fail",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: 32 * 1024,
      resumable: true,
    },
  );

  assert.match(result.error || "", /permission denied/);
  assert.equal(hadOpenErrorListener, true);
  // concurrent-isolated + fastPut-isolated each open/end their own channel.
  assert.ok(endedChannels >= 1, `expected isolated channels to end, got ${endedChannels}`);
});

test("failed digest baseline opens do not acquire an isolated channel", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-local-open-fail-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(32 * 1024, 71));
  let endedChannels = 0;
  const fastSftp = createFastSftp({
    end() {
      endedChannels += 1;
    },
  });
  const client = {
    sftp: createFastSftp({}),
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const originalOpen = fs.promises.open;
  let sourceOpens = 0;
  let sourceCloses = 0;
  fs.promises.open = async (filePath, ...args) => {
    if (String(filePath).includes("ranges.sha256.part")) {
      throw new Error("upload digest baseline unavailable");
    }
    const handle = await originalOpen(filePath, ...args);
    if (path.resolve(String(filePath)) === path.resolve(localPath)) {
      sourceOpens += 1;
      const realClose = handle.close.bind(handle);
      handle.close = async () => {
        sourceCloses += 1;
        return realClose();
      };
    }
    return handle;
  };
  let result;
  try {
    result = await transferBridge.startTransfer(
      { sender: createSender() },
      {
        transferId: "upload-local-open-fail",
        sourcePath: localPath,
        targetPath: "/tmp/upload.bin",
        sourceType: "local",
        targetType: "sftp",
        targetSftpId: "target",
        totalBytes: 32 * 1024,
        resumable: true,
      },
    );
  } finally {
    fs.promises.open = originalOpen;
  }

  assert.match(result.error || "", /upload digest baseline unavailable/);
  assert.equal(endedChannels, 0);
  assert.equal(sourceCloses, sourceOpens);
  const digestId = crypto.createHash("sha256")
    .update("upload-local-open-fail")
    .digest("hex")
    .slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  assert.equal(fs.existsSync(digestPath), false);
});

test("digest capacity check reclaims a crashed baseline before measuring space", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-digest-reclaim-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const transferId = "upload-digest-reclaim";
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(TRANSFER_CHUNK_SIZE, 41));
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  await fs.promises.mkdir(path.dirname(digestPath), { recursive: true });
  await fs.promises.writeFile(digestPath, Buffer.alloc(32, 9));

  let staleAbsentAtCheck = false;
  const originalStatfs = fs.promises.statfs;
  fs.promises.statfs = async () => {
    staleAbsentAtCheck = !fs.existsSync(digestPath);
    return { bavail: staleAbsentAtCheck ? 1n : 0n, bsize: 32n };
  };
  const client = {
    sftp: createFastSftp({}),
    delete: async () => {},
    client: { sftp: (callback) => callback(new Error("stop after baseline")) },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });
  let result;
  try {
    result = await transferBridge.startTransfer(
      { sender: createSender() },
      {
        transferId,
        sourcePath: localPath,
        targetPath: "/tmp/upload.bin",
        sourceType: "local",
        targetType: "sftp",
        targetSftpId: "target",
        totalBytes: TRANSFER_CHUNK_SIZE,
        resumable: true,
      },
    );
  } finally {
    fs.promises.statfs = originalStatfs;
    await fs.promises.rm(digestPath, { force: true });
  }
  assert.equal(staleAbsentAtCheck, true);
  assert.doesNotMatch(result.error || "", /not enough.*temporary storage/i);
});

test("digest baseline cancellation removes the sidecar before opening remote upload", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-digest-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const transferId = "upload-digest-cancel";
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2, 53));
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  t.after(async () => fs.promises.rm(digestPath, { force: true }));
  let remoteChannelOpens = 0;
  const client = {
    sftp: createFastSftp({}),
    delete: async () => {},
    client: {
      sftp(callback) {
        remoteChannelOpens += 1;
        callback(new Error("remote must not open"));
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const originalOpen = fs.promises.open;
  let cancellationTriggered = false;
  fs.promises.open = async (filePath, flags, ...args) => {
    const handle = await originalOpen(filePath, flags, ...args);
    if (String(filePath) !== localPath || flags !== "r" || cancellationTriggered) return handle;
    return {
      async read(...readArgs) {
        cancellationTriggered = true;
        await transferBridge.cancelTransfer(null, { transferId });
        return handle.read(...readArgs);
      },
      close: () => handle.close(),
    };
  };
  let result;
  try {
    result = await transferBridge.startTransfer(
      { sender: createSender() },
      {
        transferId,
        sourcePath: localPath,
        targetPath: "/tmp/upload.bin",
        sourceType: "local",
        targetType: "sftp",
        targetSftpId: "target",
        totalBytes: UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2,
        resumable: true,
      },
    );
  } finally {
    fs.promises.open = originalOpen;
  }
  assert.equal(cancellationTriggered, true);
  assert.match(result.error || "", /cancel/i);
  assert.equal(remoteChannelOpens, 0);
  assert.equal(fs.existsSync(digestPath), false);
});

test("failed local open for resumable upload still ends the isolated channel", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-local-open-fail-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // Create the source so preflight can proceed, then delete it as soon as the
  // isolated channel opens so concurrent isolated upload fails on local open.
  const localPath = path.join(tempDir, "source.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(32 * 1024, 7));
  let endedChannels = 0;
  let remoteOpenAttempts = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      remoteOpenAttempts += 1;
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
    end() {
      endedChannels += 1;
    },
  });
  const client = {
    sftp: createFastSftp({
      createWriteStream() {
        // Stream fallback after range-path failure — keep it error-safe.
        const writeStream = new Writable({
          write(_chunk, _encoding, callback) {
            callback(new Error("stream fallback after missing local open"));
          },
        });
        return writeStream;
      },
    }),
    client: {
      sftp(callback) {
        fs.promises.rm(localPath, { force: true }).finally(() => {
          callback(null, fastSftp);
        });
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-local-open-fail",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: 32 * 1024,
      resumable: true,
      skipAdmission: true,
    },
  );

  assert.ok(result.error, "expected transfer to fail when local source disappears");
  // Critical: isolated channel must not leak when local open fails first.
  assert.ok(endedChannels >= 1, `expected isolated channel end, got ${endedChannels}`);
  assert.equal(remoteOpenAttempts, 0);
});

test("cancel during stalled resumable upload OPEN ends the isolated channel", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-open-stall-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(32 * 1024, 29));
  let endedChannels = 0;
  let releaseOpen = null;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      releaseOpen = () => callback(new Error("channel closed during open"));
    },
    end() {
      endedChannels += 1;
      // Simulate ssh2 failing the pending OPEN when the channel ends.
      releaseOpen?.();
      releaseOpen = null;
    },
  });
  const client = {
    sftp: createFastSftp({
      createWriteStream() {
        const writeStream = new Writable({
          write(_chunk, _encoding, callback) {
            callback(new Error("should not stream-fallback after cancel"));
          },
        });
        return writeStream;
      },
    }),
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "upload-open-stall-cancel",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: 32 * 1024,
      resumable: true,
      skipAdmission: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (!releaseOpen && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(releaseOpen, "expected remote OPEN to stall");

  await transferBridge.cancelTransfer(null, { transferId: "upload-open-stall-cancel" });
  const result = await running;
  assert.match(result.error || "", /cancel|closed/i);
  assert.ok(endedChannels >= 1, `expected isolated channel end on cancel, got ${endedChannels}`);
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"));
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"), false);
});

test("resumable SFTP uploads fail closed when pipelined strategies fail (no serial stream)", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-fail-closed-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.from("complete fallback upload");
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let endedChannels = 0;
  let createWriteStreamCalls = 0;
  let deleteCalls = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(new Error("server rejected random-access writes"));
    },
    end() {
      endedChannels += 1;
    },
  });
  const client = {
    sftp: createFastSftp({
      open(_remotePath, _flags, callback) {
        callback(new Error("server rejected random-access writes"));
      },
      createWriteStream() {
        createWriteStreamCalls += 1;
        throw new Error("serial WriteStream must not run");
      },
    }),
    stat() {
      return Promise.resolve({ size: 0 });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      deleteCalls += 1;
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-fail-closed",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.match(result.error || "", /pipelined upload failed|rejected random-access/i);
  assert.equal(createWriteStreamCalls, 0);
  assert.ok(endedChannels >= 1, `expected isolated channels to end, got ${endedChannels}`);
  assert.equal(deleteCalls, 0, "a resumable upload error must preserve its staged prefix");
});

test("resumable concurrent uploads reject a source rewritten mid-transfer", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-source-change-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // Keep the source within one request so every range check has completed
  // before the rewrite; only the final whole-source verification can catch it.
  const payload = Buffer.alloc(16 * 1024, 41);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  // Freeze metadata views so a same-size rewrite is invisible to mtime/ctime
  // checks; promotion must still fail via the digest revalidation.
  const frozenSource = await fs.promises.stat(localPath);
  const realStat = fs.promises.stat.bind(fs.promises);
  const realOpen = fs.promises.open.bind(fs.promises);
  fs.promises.stat = async (p, ...args) => {
    if (path.resolve(String(p)) === path.resolve(localPath)) return frozenSource;
    return realStat(p, ...args);
  };
  fs.promises.open = async (p, flags, ...args) => {
    const handle = await realOpen(p, flags, ...args);
    if (path.resolve(String(p)) === path.resolve(localPath) && String(flags).includes("r")) {
      handle.stat = async () => frozenSource;
    }
    return handle;
  };
  t.after(() => {
    fs.promises.stat = realStat;
    fs.promises.open = realOpen;
  });
  let rewritten = false;
  let promoted = false;
  let stagedDeleted = false;
  let remoteBytes = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      remoteBytes = Math.max(remoteBytes, position + length);
      if (!rewritten && position === 0) {
        rewritten = true;
        fs.writeFileSync(localPath, Buffer.alloc(payload.length, 42));
      }
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: remoteBytes });
    },
    rename() {
      promoted = true;
      return Promise.resolve();
    },
    delete() {
      stagedDeleted = true;
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-source-change",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(rewritten, true);
  assert.match(result.error || "", /source|content|changed|fingerprint|mismatch/i);
  assert.equal(promoted, false);
  assert.equal(stagedDeleted, true);
});

test("assertSourceMetadataUnchanged ignores ctime drift when content is verified separately", () => {
  const initial = {
    size: 100,
    mtimeMs: 1,
    ctimeMs: 1,
    mtime: 0.001,
    ctime: 0.001,
    ino: 42,
  };
  const drifted = {
    ...initial,
    ctimeMs: 999,
    ctime: 0.999,
  };
  // Without a separate content proof, timestamp drift is a hard fail (download path).
  assert.throws(
    () => transferBridge._assertSourceMetadataUnchangedForTests(initial, drifted, 100),
    /source content changed/i,
  );
  // With digest / per-range verification, macOS xattr ctime bumps must not abort.
  assert.doesNotThrow(() => transferBridge._assertSourceMetadataUnchangedForTests(
    initial,
    drifted,
    100,
    { contentVerifiedSeparately: true },
  ));
  // Size still fails hard even when content is verified separately.
  assert.throws(
    () => transferBridge._assertSourceMetadataUnchangedForTests(
      initial,
      { ...drifted, size: 99 },
      100,
      { contentVerifiedSeparately: true },
    ),
    /source size changed/i,
  );
  // Append-only growth is rejected unless the download snapshot opts in.
  assert.throws(
    () => transferBridge._assertSourceMetadataUnchangedForTests(
      initial,
      { ...initial, size: 150, mtimeMs: 2 },
      100,
    ),
    /source size changed/i,
  );
  // Growth without a separate prefix proof is still rejected (rewrite+grow risk).
  assert.throws(
    () => transferBridge._assertSourceMetadataUnchangedForTests(
      initial,
      { ...initial, size: 150, mtimeMs: 2, ctimeMs: 2 },
      100,
      { allowSourceGrowth: true },
    ),
    /source content changed/i,
  );
  assert.doesNotThrow(() => transferBridge._assertSourceMetadataUnchangedForTests(
    initial,
    { ...initial, size: 150, mtimeMs: 2, ctimeMs: 2 },
    100,
    { allowSourceGrowth: true, contentVerifiedSeparately: true },
  ));
  // Growth still cannot cover a shrink.
  assert.throws(
    () => transferBridge._assertSourceMetadataUnchangedForTests(
      initial,
      { ...initial, size: 80 },
      100,
      { allowSourceGrowth: true, contentVerifiedSeparately: true },
    ),
    /source size changed/i,
  );
});

test("resumable upload succeeds when only source ctime drifts (pause/resume false positive)", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-ctime-drift-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // Reproduce the user-facing finish-path false positive: content bytes are
  // stable (digest matches) but ctime moved — common on macOS after long
  // pause/resume cycles (Spotlight / quarantine / xattr).
  const payload = Buffer.alloc(16 * 1024, 43);
  const localPath = path.join(tempDir, "ChatGPT.dmg");
  await fs.promises.writeFile(localPath, payload);
  const baseline = await fs.promises.stat(localPath);
  let allowCtimeDrift = false;
  const driftedStat = () => {
    if (!allowCtimeDrift) return baseline;
    return {
      ...baseline,
      ctimeMs: baseline.ctimeMs + 60_000,
      ctime: (baseline.ctimeMs + 60_000) / 1000,
      // mtime intentionally stable — content not rewritten.
    };
  };
  const realStat = fs.promises.stat.bind(fs.promises);
  const realOpen = fs.promises.open.bind(fs.promises);
  fs.promises.stat = async (p, ...args) => {
    if (path.resolve(String(p)) === path.resolve(localPath)) return driftedStat();
    return realStat(p, ...args);
  };
  fs.promises.open = async (p, flags, ...args) => {
    const handle = await realOpen(p, flags, ...args);
    if (path.resolve(String(p)) === path.resolve(localPath) && String(flags).includes("r")) {
      handle.stat = async () => driftedStat();
    }
    return handle;
  };
  t.after(() => {
    fs.promises.stat = realStat;
    fs.promises.open = realOpen;
  });

  let remoteBytes = 0;
  let promoted = false;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      remoteBytes = Math.max(remoteBytes, position + length);
      // After the first remote WRITE, simulate metadata-only drift as if the
      // transfer had been pause/resumed for a long wall-clock time.
      allowCtimeDrift = true;
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: remoteBytes });
    },
    rename() {
      promoted = true;
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-ctime-drift-ok",
      sourcePath: localPath,
      targetPath: "/tmp/ChatGPT.dmg",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined, `expected success, got: ${result.error}`);
  assert.equal(allowCtimeDrift, true);
  assert.equal(promoted, true);
  assert.equal(remoteBytes, payload.length);
});

test("non-resumable shared range uploads reject a same-size source rewrite", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-nonresume-source-change-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // One request ensures the rewrite happens after every range has already
  // passed its pre-WRITE digest check. Only the final digest scan can catch it.
  const payload = Buffer.alloc(16 * 1024, 51);
  const localPath = path.join(tempDir, "upload.bin");
  const digestId = crypto.createHash("sha256")
    .update("upload-nonresume-source-change")
    .digest("hex")
    .slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  await fs.promises.writeFile(localPath, payload);
  const frozenSource = await fs.promises.stat(localPath);
  const realStat = fs.promises.stat.bind(fs.promises);
  const realOpen = fs.promises.open.bind(fs.promises);
  fs.promises.stat = async (p, ...args) => {
    if (path.resolve(String(p)) === path.resolve(localPath)) return frozenSource;
    return realStat(p, ...args);
  };
  fs.promises.open = async (p, flags, ...args) => {
    const handle = await realOpen(p, flags, ...args);
    if (path.resolve(String(p)) === path.resolve(localPath) && String(flags).includes("r")) {
      handle.stat = async () => frozenSource;
    }
    return handle;
  };
  t.after(() => {
    fs.promises.stat = realStat;
    fs.promises.open = realOpen;
  });
  let rewritten = false;
  let remoteBytes = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      remoteBytes = Math.max(remoteBytes, position + length);
      if (!rewritten) {
        rewritten = true;
        fs.writeFileSync(localPath, Buffer.alloc(payload.length, 52));
      }
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat: async () => ({ size: remoteBytes }),
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-nonresume-source-change",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: false,
    },
  );

  assert.equal(rewritten, true);
  assert.match(result.error || "", /source content changed/i);
  assert.equal(fs.existsSync(digestPath), false);
});

test("non-resumable isolated upload rejects a transient source rewrite before promotion", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-isolated-source-change-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(16 * 1024, 53);
  const localPath = path.join(tempDir, "upload.bin");
  const targetPath = "/tmp/upload.bin";
  const oldTarget = Buffer.from("old target");
  const remoteFiles = new Map([[targetPath, oldTarget]]);
  await fs.promises.writeFile(localPath, payload);

  // The first two source opens build and verify the baseline. On the upload
  // open, simulate a same-size transient rewrite whose bytes are read once,
  // while the path itself has already returned to the original content before
  // the final whole-file scan. Per-range verification must still reject it.
  const realOpen = fs.promises.open.bind(fs.promises);
  let sourceReadOpens = 0;
  let transientReadInjected = false;
  fs.promises.open = async (p, flags, ...args) => {
    const handle = await realOpen(p, flags, ...args);
    if (path.resolve(String(p)) !== path.resolve(localPath) || !String(flags).includes("r")) {
      return handle;
    }
    sourceReadOpens += 1;
    if (sourceReadOpens !== 3) return handle;
    const realRead = handle.read.bind(handle);
    handle.read = async (buffer, offset, length, position) => {
      const result = await realRead(buffer, offset, length, position);
      if (!transientReadInjected && result.bytesRead > 0) {
        buffer.fill(54, offset, offset + result.bytesRead);
        transientReadInjected = true;
      }
      return result;
    };
    return handle;
  };
  t.after(() => {
    fs.promises.open = realOpen;
  });

  let promoted = false;
  let stagedDeleted = false;
  let fastPutCalls = 0;
  let writeCalls = 0;
  const sharedSftp = createFastSftp({
    lstat(remotePath, callback) {
      const key = String(remotePath);
      if (!remoteFiles.has(key)) {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
        return;
      }
      callback(null, {
        size: remoteFiles.get(key).length,
        mode: 0o100644,
        isDirectory: () => false,
        isSymbolicLink: () => false,
      });
    },
  });
  const fastSftp = createFastSftp({
    open(remotePath, _flags, callback) {
      const key = String(remotePath);
      remoteFiles.set(key, Buffer.alloc(payload.length));
      callback(null, Buffer.from(key));
    },
    write(handle, buffer, offset, length, position, callback) {
      writeCalls += 1;
      buffer.copy(remoteFiles.get(handle.toString()), position, offset, offset + length);
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
    fastPut(_sourcePath, _remotePath, _options, callback) {
      fastPutCalls += 1;
      callback(null);
    },
  });
  const client = {
    sftp: sharedSftp,
    stat(remotePath) {
      const value = remoteFiles.get(String(remotePath));
      if (!value) {
        const error = new Error("ENOENT");
        error.code = 2;
        return Promise.reject(error);
      }
      return Promise.resolve({ size: value.length });
    },
    rename(sourcePath, destinationPath) {
      promoted = true;
      const source = String(sourcePath);
      const destination = String(destinationPath);
      remoteFiles.set(destination, remoteFiles.get(source));
      remoteFiles.delete(source);
      return Promise.resolve();
    },
    delete(remotePath) {
      const key = String(remotePath);
      stagedDeleted = stagedDeleted || key !== targetPath;
      remoteFiles.delete(key);
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-isolated-source-change",
      sourcePath: localPath,
      targetPath,
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: false,
    },
  );

  assert.equal(transientReadInjected, true);
  assert.match(result.error || "", /source content changed/i);
  assert.equal(writeCalls, 0);
  assert.equal(fastPutCalls, 0);
  assert.equal(promoted, false);
  assert.equal(stagedDeleted, true);
  assert.deepEqual(remoteFiles.get(targetPath), oldTarget);
});

test("fastPut fallback uploads an immutable snapshot while the source changes and recovers", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-fastput-snapshot-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(16 * 1024, 55);
  const replacement = Buffer.alloc(payload.length, 56);
  const localPath = path.join(tempDir, "upload.bin");
  const targetPath = "/tmp/upload.bin";
  const remoteFiles = new Map([[targetPath, Buffer.from("old target")]]);
  await fs.promises.writeFile(localPath, payload);
  const frozenSource = await fs.promises.stat(localPath);
  const realStat = fs.promises.stat.bind(fs.promises);
  fs.promises.stat = async (p, ...args) => {
    if (path.resolve(String(p)) === path.resolve(localPath)) return frozenSource;
    return realStat(p, ...args);
  };
  t.after(() => {
    fs.promises.stat = realStat;
  });

  let fastPutSourcePath = null;
  const sharedSftp = createFastSftp({
    lstat(remotePath, callback) {
      const value = remoteFiles.get(String(remotePath));
      if (!value) {
        const error = new Error("ENOENT");
        error.code = 2;
        callback(error);
        return;
      }
      callback(null, {
        size: value.length,
        mode: 0o100644,
        isDirectory: () => false,
        isSymbolicLink: () => false,
      });
    },
  });
  const fastSftp = createFastSftp({
    fastPut(sourcePath, remotePath, options, callback) {
      fastPutSourcePath = sourcePath;
      const uploaded = fs.readFileSync(sourcePath);
      fs.writeFileSync(localPath, replacement);
      fs.writeFileSync(localPath, payload);
      remoteFiles.set(String(remotePath), uploaded);
      options.step?.(uploaded.length, uploaded.length, uploaded.length);
      queueMicrotask(() => callback(null));
    },
  });
  const client = {
    sftp: sharedSftp,
    stat: async (remotePath) => ({ size: remoteFiles.get(String(remotePath))?.length || 0 }),
    chmod: async () => {},
    rename(sourcePath, destinationPath) {
      const source = String(sourcePath);
      const destination = String(destinationPath);
      remoteFiles.set(destination, remoteFiles.get(source));
      remoteFiles.delete(source);
      return Promise.resolve();
    },
    delete(remotePath) {
      remoteFiles.delete(String(remotePath));
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-fastput-snapshot",
      sourcePath: localPath,
      targetPath,
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: false,
    },
  );

  assert.equal(result.error, undefined);
  assert.notEqual(fastPutSourcePath, localPath);
  await assert.rejects(fs.promises.stat(fastPutSourcePath), { code: "ENOENT" });
  assert.deepEqual(remoteFiles.get(targetPath), payload);
});

test("failed snapshot open closes verification handles and removes temporary files", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-snapshot-open-fail-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const localPath = path.join(tempDir, "upload.bin");
  const payload = Buffer.alloc(16 * 1024, 59);
  await fs.promises.writeFile(localPath, payload);
  const transferId = "upload-snapshot-open-fail";
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  const snapshotPath = tempDirBridge.getTransferTempFilePath(
    `upload-source-${digestId}`,
    "snapshot.bin",
  );

  let fastPutCalls = 0;
  const fastSftp = createFastSftp({
    fastPut(_sourcePath, _remotePath, _options, callback) {
      fastPutCalls += 1;
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    delete: async () => {},
    client: { sftp: (callback) => callback(null, fastSftp) },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const realOpen = fs.promises.open.bind(fs.promises);
  let trackedOpens = 0;
  let trackedCloses = 0;
  fs.promises.open = async (filePath, flags, ...args) => {
    if (path.resolve(String(filePath)) === path.resolve(snapshotPath) && String(flags).includes("w")) {
      throw new Error("upload snapshot unavailable");
    }
    const handle = await realOpen(filePath, flags, ...args);
    const resolved = path.resolve(String(filePath));
    if (resolved === path.resolve(localPath) || resolved === path.resolve(digestPath)) {
      trackedOpens += 1;
      const realClose = handle.close.bind(handle);
      handle.close = async () => {
        trackedCloses += 1;
        return realClose();
      };
    }
    return handle;
  };
  t.after(() => {
    fs.promises.open = realOpen;
  });

  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath: localPath,
    targetPath: "/tmp/upload.bin",
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: false,
  });

  assert.match(result.error || "", /upload snapshot unavailable/i);
  assert.equal(fastPutCalls, 0);
  assert.equal(trackedCloses, trackedOpens);
  assert.equal(fs.existsSync(digestPath), false);
  assert.equal(fs.existsSync(snapshotPath), false);
});

test("non-resumable shared range uploads remove their temporary digest after success", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-nonresume-cleanup-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const transferId = "upload-nonresume-cleanup";
  const payload = Buffer.alloc(TRANSFER_CHUNK_SIZE * 2, 61);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  let remoteBytes = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      remoteBytes = Math.max(remoteBytes, position + length);
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  transferBridge.init({
    sftpClients: new Map([["target", {
      __netcattySudoMode: true,
      sftp: sharedSftp,
      stat: async () => ({ size: remoteBytes }),
      rename: async () => {},
      delete: async () => {},
    }]]),
  });

  const sender = createSender();
  const result = await transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: false,
    },
  );

  assert.equal(result.error, undefined);
  assert.equal(fs.existsSync(digestPath), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), true);
});

test("non-resumable shared range uploads reject a source that grows and remove their digest", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-nonresume-growth-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const transferId = "upload-nonresume-growth";
  const payload = Buffer.alloc(TRANSFER_CHUNK_SIZE * 2, 62);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  let sourceGrew = false;
  let remoteBytes = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      remoteBytes = Math.max(remoteBytes, position + length);
      if (!sourceGrew) {
        sourceGrew = true;
        fs.appendFileSync(localPath, Buffer.from([1]));
      }
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  transferBridge.init({
    sftpClients: new Map([["target", {
      __netcattySudoMode: true,
      sftp: sharedSftp,
      stat: async () => ({ size: remoteBytes }),
    }]]),
  });

  const sender = createSender();
  const result = await transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: false,
    },
  );

  assert.equal(sourceGrew, true);
  assert.match(result.error || "", /source size changed/i);
  assert.equal(fs.existsSync(digestPath), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("non-resumable digest creation cancellation removes the temporary digest", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-nonresume-digest-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const transferId = "upload-nonresume-digest-cancel";
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(
    localPath,
    Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2, 63),
  );
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  let remoteOpenAttempts = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      remoteOpenAttempts += 1;
      callback(new Error("remote must not open"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      callback(new Error("remote must not write"));
    },
  });
  transferBridge.init({
    sftpClients: new Map([["target", {
      __netcattySudoMode: true,
      sftp: sharedSftp,
      stat: async () => ({ size: 0 }),
    }]]),
  });

  const originalOpen = fs.promises.open;
  let cancellationTriggered = false;
  let result;
  try {
    fs.promises.open = async (filePath, flags, ...args) => {
      const handle = await originalOpen(filePath, flags, ...args);
      if (String(filePath) !== localPath || flags !== "r" || cancellationTriggered) return handle;
      return {
        async read(...readArgs) {
          cancellationTriggered = true;
          await transferBridge.cancelTransfer(null, { transferId });
          return handle.read(...readArgs);
        },
        stat: () => handle.stat(),
        close: () => handle.close(),
      };
    };
    result = await transferBridge.startTransfer(
      { sender: createSender() },
      {
        transferId,
        sourcePath: localPath,
        targetPath: "/tmp/upload.bin",
        sourceType: "local",
        targetType: "sftp",
        targetSftpId: "target",
        totalBytes: UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2,
        resumable: false,
      },
    );
  } finally {
    fs.promises.open = originalOpen;
  }

  assert.equal(cancellationTriggered, true);
  assert.match(result.error || "", /cancel/i);
  assert.equal(remoteOpenAttempts, 0);
  assert.equal(fs.existsSync(digestPath), false);
});

test("non-resumable shared range cancellation drains writes and removes the temporary digest", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-nonresume-write-cancel-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const transferId = "upload-nonresume-write-cancel";
  const payload = Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE * 2, 64);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  const digestId = crypto.createHash("sha256").update(transferId).digest("hex").slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  const pendingWrites = [];
  const sharedSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      pendingWrites.push(callback);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  transferBridge.init({
    sftpClients: new Map([["target", {
      __netcattySudoMode: true,
      sftp: sharedSftp,
      stat: async () => ({ size: 0 }),
    }]]),
  });

  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: false,
    },
  );
  const readyDeadline = Date.now() + 2000;
  while (
    pendingWrites.length < UPLOAD_TRANSFER_CONCURRENCY
    && Date.now() < readyDeadline
  ) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(pendingWrites.length, UPLOAD_TRANSFER_CONCURRENCY);
  let transferSettled = false;
  void running.finally(() => {
    transferSettled = true;
  });
  await transferBridge.cancelTransfer(null, { transferId });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(transferSettled, false);
  assert.equal(fs.existsSync(digestPath), true);

  const firstWrite = pendingWrites.shift();
  assert.ok(pendingWrites.length > 0);
  firstWrite(new Error("write cancelled"));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(transferSettled, false);
  assert.equal(fs.existsSync(digestPath), true);

  const finalWrite = pendingWrites.pop();
  for (const callback of pendingWrites.splice(0)) callback(new Error("write cancelled"));
  await new Promise((resolve) => setTimeout(resolve, 2200));
  assert.equal(transferSettled, false);
  assert.equal(fs.existsSync(digestPath), true);

  finalWrite(new Error("write cancelled"));
  const result = await running;

  assert.match(result.error || "", /cancel|write cancelled/i);
  assert.equal(fs.existsSync(digestPath), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), true);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"), false);
});

test("resumable fast uploads fail closed when isolated channel errors (no serial stream)", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-channel-error-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(32 * 1024, 31);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let hadErrorListener = false;
  let createWriteStreamCalls = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      hadErrorListener = fastSftp.listenerCount("error") > 0;
      const error = new Error("isolated channel failed");
      queueMicrotask(() => {
        if (hadErrorListener) fastSftp.emit("error", error);
        callback(error);
      });
    },
    close(_handle, callback) {
      callback(null);
    },
    end() {},
  });
  const client = {
    sftp: createFastSftp({
      open(_remotePath, _flags, callback) {
        callback(new Error("shared open also fails"));
      },
      createWriteStream() {
        createWriteStreamCalls += 1;
        throw new Error("serial WriteStream must not run");
      },
    }),
    stat() {
      return Promise.resolve({ size: 0 });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-channel-error",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.match(result.error || "", /pipelined upload failed|isolated channel failed/i);
  assert.equal(hadErrorListener, true);
  assert.equal(createWriteStreamCalls, 0);
});

test("resumable concurrent range failure does not complete via serial stream", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-sparse-fail-closed-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(3 * 32 * 1024, 41);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let secondWriteCallback = null;
  let thirdWriteCompleted = false;
  let secondWriteFailureScheduled = false;
  const scheduleSecondWriteFailure = () => {
    if (secondWriteFailureScheduled || !thirdWriteCompleted || !secondWriteCallback) return;
    secondWriteFailureScheduled = true;
    const callback = secondWriteCallback;
    secondWriteCallback = null;
    queueMicrotask(() => callback(new Error("second range failed")));
  };
  let createWriteStreamCalls = 0;
  let remoteBytes = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      if (position === 32 * 1024) {
        secondWriteCallback = callback;
        scheduleSecondWriteFailure();
        return;
      }
      remoteBytes = Math.max(remoteBytes, position + length);
      callback(null);
      if (position === 2 * 32 * 1024) {
        thirdWriteCompleted = true;
        scheduleSecondWriteFailure();
      }
    },
    close(_handle, callback) {
      callback(null);
    },
    end() {},
  });
  const client = {
    sftp: createFastSftp({
      open(_remotePath, _flags, callback) {
        callback(new Error("shared open also fails"));
      },
      createWriteStream() {
        createWriteStreamCalls += 1;
        throw new Error("serial WriteStream must not run");
      },
    }),
    stat() {
      return Promise.resolve({ size: remoteBytes });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-sparse-fail-closed",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.match(result.error || "", /pipelined upload failed|second range failed/i);
  assert.equal(createWriteStreamCalls, 0);
});

test("resumable fast uploads reject a source that grows during transfer", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-source-growth-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(32 * 1024, 59);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  let sourceGrew = false;
  let promoted = false;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      if (!sourceGrew) {
        sourceGrew = true;
        fs.appendFileSync(localPath, Buffer.from([1]));
      }
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    rename() {
      promoted = true;
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-source-growth",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.match(result.error || "", /source size changed/);
  assert.equal(promoted, false);
});

test("resumable uploads reject changed ranges before writing them", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-upload-metadata-change-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const changedChunkIndex = UPLOAD_TRANSFER_CONCURRENCY;
  const payload = Buffer.alloc(
    (UPLOAD_TRANSFER_CONCURRENCY + 8) * TRANSFER_CHUNK_SIZE,
    73,
  );
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);
  const frozenStat = await fs.promises.stat(localPath);
  let changeStarted = false;
  let changed = false;
  let uploadedChangedChunk = null;
  let promoted = false;
  let stagedDeleted = false;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      if (position === changedChunkIndex * TRANSFER_CHUNK_SIZE) {
        uploadedChangedChunk = Buffer.from(buffer.subarray(offset, offset + length));
      }
      if (changeStarted) {
        callback(null);
        return;
      }
      changeStarted = true;
      // Rewrite a later range after the digest baseline has been created. That
      // changed range must be rejected before its remote WRITE.
      const fd = fs.openSync(localPath, "r+");
      try {
        fs.writeSync(
          fd,
          Buffer.alloc(TRANSFER_CHUNK_SIZE, 74),
          0,
          TRANSFER_CHUNK_SIZE,
          changedChunkIndex * TRANSFER_CHUNK_SIZE,
        );
      } finally {
        fs.closeSync(fd);
      }
      fs.utimesSync(localPath, frozenStat.atime, frozenStat.mtime);
      changed = true;
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    rename() {
      promoted = true;
      return Promise.resolve();
    },
    delete() {
      stagedDeleted = true;
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-metadata-change",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(changed, true);
  assert.match(result.error || "", /source.*changed/i);
  assert.equal(promoted, false);
  assert.equal(stagedDeleted, true);
  assert.equal(uploadedChangedChunk, null);
  const digestId = crypto.createHash("sha256")
    .update("upload-metadata-change")
    .digest("hex")
    .slice(0, 16);
  const digestPath = tempDirBridge.getTransferTempFilePath(
    `upload-digest-${digestId}`,
    "ranges.sha256",
  );
  assert.equal(fs.existsSync(digestPath), false);
});

test("resumable fast downloads clear staged data after a same-second source change", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-download-source-change-"));
  const transferId = "download-source-change";
  const targetPath = path.join(tempDir, "download.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
    await fs.promises.rm(stagedPath, { force: true });
  });
  await fs.promises.writeFile(targetPath, "original");

  const payload = Buffer.alloc(32 * 1024, 61);
  const latestPayload = Buffer.alloc(payload.length, 62);
  let reads = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      const currentPayload = reads++ === 0 ? payload : latestPayload;
      currentPayload.copy(buffer, offset, position, position + length);
      callback(null, length, buffer, position);
    },
    close(_handle, callback) {
      callback(new Error("remote close failed"));
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: payload.length, mtime: 1 });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.match(result.error || "", /source.*changed/);
  assert.equal(await fs.promises.readFile(targetPath, "utf8"), "original");
  await assert.rejects(fs.promises.stat(stagedPath), { code: "ENOENT" });
});

test("resumable SFTP downloads succeed when the remote source only grows (live logs)", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-download-growth-"));
  const transferId = "download-source-growth";
  const targetPath = path.join(tempDir, "download.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
    await fs.promises.rm(stagedPath, { force: true }).catch(() => {});
  });

  // Snapshot at transfer start; remote appends more bytes while ranges run.
  const snapshot = Buffer.alloc(64 * 1024, 71);
  const grownTail = Buffer.alloc(8 * 1024, 72);
  let remoteSize = snapshot.length;
  let mtimeMs = 1_000;
  const remotePayload = () => Buffer.concat([snapshot, Buffer.alloc(Math.max(0, remoteSize - snapshot.length), 72)]);
  const serveReadStream = (_remotePath, options = {}) => {
    const current = remotePayload();
    const start = Number.isFinite(options.start) ? options.start : 0;
    const end = Number.isFinite(options.end) ? options.end : current.length - 1;
    return Readable.from([Buffer.from(current.subarray(start, end + 1))]);
  };
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      // First range completion simulates an append-only log writer.
      if (remoteSize === snapshot.length) {
        remoteSize = snapshot.length + grownTail.length;
        mtimeMs += 1;
      }
      const current = remotePayload();
      current.copy(buffer, offset, position, position + length);
      callback(null, length, buffer, position);
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream: serveReadStream,
  });
  const client = {
    // Shared session is used for post-transfer prefix verification on growth.
    sftp: createFastSftp({ createReadStream: serveReadStream }),
    stat() {
      return Promise.resolve({
        size: remoteSize,
        mtimeMs,
        ctimeMs: mtimeMs,
        mtime: mtimeMs / 1000,
        ctime: mtimeMs / 1000,
      });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/var/log/app.log",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: snapshot.length,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined, result.error);
  assert.equal(remoteSize, snapshot.length + grownTail.length);
  const downloaded = await fs.promises.readFile(targetPath);
  assert.equal(downloaded.length, snapshot.length);
  assert.deepEqual(downloaded, snapshot);
  await assert.rejects(fs.promises.stat(stagedPath), { code: "ENOENT" });
});

test("resumable SFTP downloads reject growth when the planned prefix was rewritten", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-download-rewrite-growth-"));
  const transferId = "download-source-rewrite-growth";
  const targetPath = path.join(tempDir, "download.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
    await fs.promises.rm(stagedPath, { force: true }).catch(() => {});
  });

  // Mid-transfer rotate: prefix bytes change and the file also grows.
  const snapshot = Buffer.alloc(64 * 1024, 71);
  const rewritten = Buffer.alloc(64 * 1024, 81);
  const grownTail = Buffer.alloc(8 * 1024, 72);
  let useRewritten = false;
  let remoteSize = snapshot.length;
  let mtimeMs = 1_000;
  const remotePayload = () => {
    const head = useRewritten ? rewritten : snapshot;
    if (remoteSize <= head.length) return Buffer.from(head.subarray(0, remoteSize));
    return Buffer.concat([head, Buffer.alloc(remoteSize - head.length, 72)]);
  };
  const serveReadStream = (_remotePath, options = {}) => {
    const current = remotePayload();
    const start = Number.isFinite(options.start) ? options.start : 0;
    const end = Number.isFinite(options.end) ? options.end : current.length - 1;
    return Readable.from([Buffer.from(current.subarray(start, end + 1))]);
  };
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      // After half the snapshot is staged from the original prefix, rotate the
      // remote file in place and grow it so finish-path growth acceptance is
      // the only remaining safety net if samples miss the hole.
      if (!useRewritten && position >= snapshot.length / 2) {
        useRewritten = true;
        remoteSize = snapshot.length + grownTail.length;
        mtimeMs += 1;
      }
      const current = remotePayload();
      current.copy(buffer, offset, position, position + length);
      callback(null, length, buffer, position);
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream: serveReadStream,
  });
  const client = {
    sftp: createFastSftp({ createReadStream: serveReadStream }),
    stat() {
      return Promise.resolve({
        size: remoteSize,
        mtimeMs,
        ctimeMs: mtimeMs,
        mtime: mtimeMs / 1000,
        ctime: mtimeMs / 1000,
      });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/var/log/app.log",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: snapshot.length,
      resumable: true,
    },
  );

  // Sample verify or full planned-prefix proof must reject the mixed file.
  assert.match(result.error || "", /source|content|changed/i);
  await assert.rejects(fs.promises.stat(stagedPath), { code: "ENOENT" });
  if (fs.existsSync(targetPath)) {
    assert.notEqual((await fs.promises.stat(targetPath)).size, snapshot.length);
  }
});

test("stream fallback resume skips source open when checkpoint already covers the snapshot", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-download-complete-checkpoint-"));
  const transferId = "download-complete-checkpoint-growth";
  const targetPath = path.join(tempDir, "download.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
    await fs.promises.rm(stagedPath, { force: true }).catch(() => {});
  });

  // Staged file already holds the full planned snapshot; remote has grown since.
  const snapshot = Buffer.alloc(16 * 1024, 91);
  const grownRemote = Buffer.concat([snapshot, Buffer.alloc(4 * 1024, 92)]);
  await fs.promises.mkdir(path.dirname(stagedPath), { recursive: true });
  await fs.promises.writeFile(stagedPath, snapshot);

  let bodyStreamAtOldEof = false;
  const serveReadStream = (_remotePath, options = {}) => {
    const start = Number.isFinite(options.start) ? options.start : 0;
    const end = Number.isFinite(options.end) ? options.end : grownRemote.length - 1;
    // A buggy resume would open the transfer body at the planned EOF and pull
    // the append tail. Prefix verification only reads [0, snapshot).
    if (start >= snapshot.length) {
      bodyStreamAtOldEof = true;
    }
    return Readable.from([Buffer.from(grownRemote.subarray(start, end + 1))]);
  };
  const sharedSftp = createFastSftp({
    createReadStream: serveReadStream,
  });
  const client = {
    // Force sequential fallback (no isolated fast channel).
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: grownRemote.length,
        mtimeMs: 2_000,
        ctimeMs: 2_000,
        mtime: 2,
        ctime: 2,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/var/log/app.log",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: snapshot.length,
      resumable: true,
      checkpointBytes: snapshot.length,
    },
  );

  assert.equal(result.error, undefined, result.error);
  assert.equal(bodyStreamAtOldEof, false, "must not open transfer body stream at the old EOF");
  const downloaded = await fs.promises.readFile(targetPath);
  assert.deepEqual(downloaded, snapshot);
});

test("stream fallback treats a zero-byte snapshot as complete when remote has grown", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-download-empty-growth-"));
  const transferId = "download-empty-snapshot-growth";
  const targetPath = path.join(tempDir, "empty.log");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
    await fs.promises.rm(stagedPath, { force: true }).catch(() => {});
  });

  // Preflight sees an empty file; later stats report append growth. The body
  // stream must not open (unbounded read would pull the tail and fail).
  const grownRemote = Buffer.from("appended-after-empty-snapshot");
  let remoteSize = 0;
  let bodyStreamOpened = false;
  const sharedSftp = createFastSftp({
    createReadStream() {
      bodyStreamOpened = true;
      remoteSize = grownRemote.length;
      return Readable.from([grownRemote]);
    },
  });
  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: remoteSize,
        mtimeMs: remoteSize > 0 ? 3_000 : 1_000,
        ctimeMs: remoteSize > 0 ? 3_000 : 1_000,
        mtime: remoteSize > 0 ? 3 : 1,
        ctime: remoteSize > 0 ? 3 : 1,
      });
    },
  };
  // Grow after the planned empty snapshot is fixed (async gap before body I/O).
  const growTimer = setTimeout(() => { remoteSize = grownRemote.length; }, 0);
  t.after(() => clearTimeout(growTimer));
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/var/log/empty-then-grown.log",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: 0,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined, result.error);
  assert.equal(bodyStreamOpened, false, "zero-byte planned snapshot must not open a source stream");
  const downloaded = await fs.promises.readFile(targetPath);
  assert.equal(downloaded.length, 0);
});

test("SFTP uploads fail when remote size does not match local size", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-size-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "archive.zip");
  await fs.promises.writeFile(localPath, Buffer.alloc(1024 * 1024));

  let deletedRemotePath = null;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      // Simulate a truncated remote file after a "successful" fastPut.
      return Promise.resolve({ size: 512 * 1024 });
    },
    delete(remotePath) {
      deletedRemotePath = remotePath;
      return Promise.resolve();
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const result = await transferBridge.startTransfer(
    { sender },
    {
      transferId: "upload-truncated",
      sourcePath: localPath,
      targetPath: "/tmp/archive.zip",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
    },
  );

  assert.match(result.error || "", /Upload size mismatch/);
  assert.match(String(deletedRemotePath), /\.netcatty-upload-.*archive\.zip\.part$/);
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"));
});

test("uploads prefer concurrent shared channel over serial WriteStream", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-shared-concurrent-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(UPLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE, 91);
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, payload);

  let maxInFlight = 0;
  let activeWrites = 0;
  let createWriteStreamCalls = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "w");
      callback(null, Buffer.from("shared-handle"));
    },
    write(_handle, _buffer, _offset, length, _position, callback) {
      activeWrites += 1;
      maxInFlight = Math.max(maxInFlight, activeWrites);
      setImmediate(() => {
        activeWrites -= 1;
        callback(null);
      });
    },
    close(_handle, callback) {
      callback(null);
    },
    createWriteStream() {
      createWriteStreamCalls += 1;
      throw new Error("serial WriteStream must not be used when concurrent works");
    },
  });

  const client = {
    // Force the isolated-channel path to be unavailable (sudo / no secondary channel).
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-shared-concurrent",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined);
  assert.equal(createWriteStreamCalls, 0);
  // Pipelined fanout must be multi-WRITE (not serial 1-in-flight).
  assert.ok(
    maxInFlight >= 2,
    `expected pipelined concurrency >= 2, got ${maxInFlight}`,
  );
  assert.ok(
    maxInFlight <= UPLOAD_TRANSFER_CONCURRENCY,
    `expected concurrency <= ${UPLOAD_TRANSFER_CONCURRENCY}, got ${maxInFlight}`,
  );
});

test("cancel during stalled shared upload OPEN drains before remote cleanup", async (t) => {
  // Codex P2 on a7f96c94: cancel rejected shared write OPEN immediately, so
  // runRemoteUploadTransaction cleaned up the stage before a late "w" OPEN
  // recreated it — leaving an orphan .part (or truncating an in-place target).
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-shared-upload-open-drain-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(TRANSFER_CHUNK_SIZE, 41));

  const remoteFiles = new Map();
  const eventLog = [];
  let releaseOpen = null;
  let endCalls = 0;
  const sharedSftp = createFastSftp({
    open(remotePath, flags, callback) {
      assert.equal(flags, "w");
      const key = String(remotePath);
      releaseOpen = () => {
        remoteFiles.set(key, Buffer.alloc(0));
        eventLog.push(`open-created:${key}`);
        callback(null, Buffer.from(`handle:${key}`));
      };
    },
    write() {
      throw new Error("WRITE must not run after cancel during OPEN");
    },
    close(_handle, callback) {
      eventLog.push("close");
      callback(null);
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat(remotePath) {
      const key = String(remotePath);
      if (!remoteFiles.has(key)) {
        const error = new Error("ENOENT");
        error.code = 2;
        return Promise.reject(error);
      }
      return Promise.resolve({ size: remoteFiles.get(key).length });
    },
    rename() {
      return Promise.resolve();
    },
    async delete(remotePath) {
      const key = String(remotePath);
      eventLog.push(`delete:${key}`);
      remoteFiles.delete(key);
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const transferId = "upload-shared-open-drain-cancel";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: TRANSFER_CHUNK_SIZE,
      resumable: true,
      skipAdmission: true,
    },
  );

  const ready = await waitUntil(() => typeof releaseOpen === "function", 2000);
  assert.ok(ready, "expected shared write OPEN to stall");

  await transferBridge.cancelTransfer(null, { transferId });

  // Must still be waiting for OPEN drain — immediate settle would be the bug.
  let settledEarly = false;
  await Promise.race([
    running.then(() => { settledEarly = true; }),
    new Promise((resolve) => setTimeout(resolve, 100)),
  ]);
  assert.equal(settledEarly, false, "shared write OPEN cancel must drain before settle");

  releaseOpen?.();
  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error("transfer did not settle after drained shared write OPEN")), 3000);
    }),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"));

  const closeIdx = eventLog.indexOf("close");
  const deleteIdx = eventLog.findIndex((entry) => entry.startsWith("delete:"));
  assert.ok(closeIdx >= 0, `expected CLOSE before cleanup, log=${eventLog.join(",")}`);
  assert.ok(deleteIdx >= 0, `expected stage delete after CLOSE, log=${eventLog.join(",")}`);
  assert.ok(closeIdx < deleteIdx, `CLOSE must precede cleanup delete, log=${eventLog.join(",")}`);
  assert.equal(
    [...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")),
    false,
    `expected no orphan staged upload, remaining=${[...remoteFiles.keys()].join(",")}`,
  );
});

test("shared upload OPEN drain survives cancel settle timeout", async (t) => {
  // Codex P2 on af9cef2e: 2s settle timeout unblocked cancel, but cleanup could
  // still race a later truncating OPEN. sharedWriteOpenDrain must outlive the
  // settle timeout so finally awaits close before stage delete.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-shared-upload-open-drain-timeout-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(TRANSFER_CHUNK_SIZE, 43));

  const remoteFiles = new Map();
  const eventLog = [];
  let releaseOpen = null;
  let endCalls = 0;
  const sharedSftp = createFastSftp({
    open(remotePath, flags, callback) {
      assert.equal(flags, "w");
      const key = String(remotePath);
      releaseOpen = () => {
        remoteFiles.set(key, Buffer.alloc(0));
        eventLog.push(`open-created:${key}`);
        callback(null, Buffer.from(`handle:${key}`));
      };
    },
    write() {
      throw new Error("WRITE must not run after cancel during OPEN");
    },
    close(_handle, callback) {
      eventLog.push("close");
      callback(null);
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat(remotePath) {
      const key = String(remotePath);
      if (!remoteFiles.has(key)) {
        const error = new Error("ENOENT");
        error.code = 2;
        return Promise.reject(error);
      }
      return Promise.resolve({ size: remoteFiles.get(key).length });
    },
    rename() {
      return Promise.resolve();
    },
    async delete(remotePath) {
      const key = String(remotePath);
      eventLog.push(`delete:${key}`);
      remoteFiles.delete(key);
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const transferId = "upload-shared-open-drain-timeout";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload-timeout.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: TRANSFER_CHUNK_SIZE,
      resumable: true,
      skipAdmission: true,
    },
  );

  const ready = await waitUntil(() => typeof releaseOpen === "function", 2000);
  assert.ok(ready, "expected shared write OPEN to stall");

  await transferBridge.cancelTransfer(null, { transferId });

  // Let the 2s OPEN cancel settle timeout fire while OPEN is still pending.
  await new Promise((resolve) => setTimeout(resolve, 2100));
  assert.ok(typeof releaseOpen === "function", "OPEN must still be pending after settle timeout");
  assert.equal(eventLog.some((entry) => entry.startsWith("delete:")), false,
    "cleanup must not run before late OPEN drain");

  // Late OPEN after settle timeout — must close then delete, no orphan.
  releaseOpen?.();
  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error("transfer did not settle after late shared write OPEN")), 5000);
    }),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  const closeIdx = eventLog.indexOf("close");
  const deleteIdx = eventLog.findIndex((entry) => entry.startsWith("delete:"));
  assert.ok(closeIdx >= 0, `expected CLOSE after late OPEN, log=${eventLog.join(",")}`);
  assert.ok(deleteIdx >= 0, `expected stage delete after drain, log=${eventLog.join(",")}`);
  assert.ok(closeIdx < deleteIdx, `CLOSE must precede cleanup delete, log=${eventLog.join(",")}`);
  assert.equal(
    [...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")),
    false,
    `expected no orphan staged upload, remaining=${[...remoteFiles.keys()].join(",")}`,
  );
});

test("shared upload OPEN drain survives channel error before OPEN callback", async (t) => {
  // Codex P2 on 532cf6cc: channel error completed sharedWriteOpenDrain before the
  // OPEN callback, so cleanup could race a late truncating OPEN.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-shared-upload-open-channel-error-drain-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(TRANSFER_CHUNK_SIZE, 47));

  const remoteFiles = new Map();
  const eventLog = [];
  let releaseOpen = null;
  let endCalls = 0;
  const sharedSftp = createFastSftp({
    open(remotePath, flags, callback) {
      assert.equal(flags, "w");
      const key = String(remotePath);
      releaseOpen = () => {
        remoteFiles.set(key, Buffer.alloc(0));
        eventLog.push(`open-created:${key}`);
        callback(null, Buffer.from(`handle:${key}`));
      };
    },
    write() {
      throw new Error("WRITE must not run after channel error during OPEN");
    },
    close(_handle, callback) {
      eventLog.push("close");
      callback(null);
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat(remotePath) {
      const key = String(remotePath);
      if (!remoteFiles.has(key)) {
        const error = new Error("ENOENT");
        error.code = 2;
        return Promise.reject(error);
      }
      return Promise.resolve({ size: remoteFiles.get(key).length });
    },
    rename() {
      return Promise.resolve();
    },
    async delete(remotePath) {
      const key = String(remotePath);
      eventLog.push(`delete:${key}`);
      remoteFiles.delete(key);
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const transferId = "upload-shared-open-channel-error-drain";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload-channel-error.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: TRANSFER_CHUNK_SIZE,
      resumable: true,
      skipAdmission: true,
    },
  );

  const ready = await waitUntil(() => typeof releaseOpen === "function", 2000);
  assert.ok(ready, "expected shared write OPEN to stall");

  sharedSftp.emit("error", new Error("shared SFTP channel died during upload OPEN"));

  // Channel error settles the OPEN wait, but cleanup must wait for drain.
  await new Promise((resolve) => setTimeout(resolve, 100));
  assert.equal(eventLog.some((entry) => entry.startsWith("delete:")), false,
    "cleanup must not run before late OPEN drain after channel error");

  releaseOpen?.();
  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error("transfer did not settle after channel error + late OPEN")), 5000);
    }),
  ]);

  assert.ok(result.error, "expected transfer to fail after channel error");
  assert.match(result.error, /shared SFTP channel died during upload OPEN/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  assert.ok(eventLog.includes("close"), `expected CLOSE after late OPEN, log=${eventLog.join(",")}`);
  // Resumable uploads preserve the stage on error (not cancel), so delete may
  // not run; the invariant is drain completed (CLOSE) before the failure path
  // returned, with no racing truncating OPEN after settle.
  assert.ok(
    eventLog.indexOf("close") >= 0
      && eventLog.findIndex((entry) => entry.startsWith("open-created:")) >= 0
      && eventLog.indexOf("close")
        > eventLog.findIndex((entry) => entry.startsWith("open-created:")),
    `late OPEN must be closed before failure returns, log=${eventLog.join(",")}`,
  );
});

test("shared upload OPEN drain force-completes when channel error has no OPEN callback", async (t) => {
  // Codex P2 on 90494c0c: channel error rejected OPEN but left sharedWriteOpenDrain
  // pending forever when the OPEN callback never arrived, hanging the transfer
  // and SFTP lease on a dead shared channel.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-shared-upload-open-channel-error-no-callback-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(TRANSFER_CHUNK_SIZE, 49));

  let endCalls = 0;
  let openCalls = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, _callback) {
      assert.equal(flags, "w");
      openCalls += 1;
      // Never invoke the OPEN callback (dead channel after error).
    },
    write() {
      throw new Error("WRITE must not run after channel error during OPEN");
    },
    close() {
      throw new Error("CLOSE must not run without an OPEN handle");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      const error = new Error("ENOENT");
      error.code = 2;
      return Promise.reject(error);
    },
    rename() {
      return Promise.resolve();
    },
    async delete() {},
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const transferId = "upload-shared-open-channel-error-no-callback";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: localPath,
      targetPath: "/tmp/upload-channel-error-hang.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: TRANSFER_CHUNK_SIZE,
      resumable: true,
      skipAdmission: true,
    },
  );

  const ready = await waitUntil(() => openCalls >= 1, 2000);
  assert.ok(ready, "expected shared write OPEN to stall");

  sharedSftp.emit("error", new Error("shared SFTP channel died with no OPEN callback"));

  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(
        () => reject(new Error("transfer hung awaiting shared write OPEN drain after channel error")),
        5000,
      );
    }),
  ]);

  assert.ok(result.error, "expected transfer to fail after channel error");
  assert.match(result.error, /shared SFTP channel died with no OPEN callback/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("sudo SFTP downloads prefer concurrent shared READ over serial createReadStream", async (t) => {
  // Sudo cannot open an isolated channel, so downloads used to fall straight to
  // createReadStream (1-in-flight READs → hundreds of KB/s). Uploads already
  // keep pipelined WRITE fanout on the shared browse channel; downloads must
  // mirror that for the elevated SFTP path (#2719).
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-download-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(DOWNLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE, 47);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 251;
  const targetPath = path.join(tempDir, "download.bin");

  let maxInFlight = 0;
  let activeReads = 0;
  let createReadStreamCalls = 0;
  let endCalls = 0;
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from("shared-read-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      activeReads += 1;
      maxInFlight = Math.max(maxInFlight, activeReads);
      const slice = payload.subarray(position, position + length);
      slice.copy(buffer, offset);
      setImmediate(() => {
        activeReads -= 1;
        callback(null, slice.length);
      });
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream() {
      createReadStreamCalls += 1;
      throw new Error("serial createReadStream must not be used when concurrent READ works");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-sudo-shared-concurrent",
      sourcePath: "/root/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined, result.error);
  assert.equal(createReadStreamCalls, 0);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  assert.ok(
    maxInFlight >= 2,
    `expected pipelined READ concurrency >= 2, got ${maxInFlight}`,
  );
  assert.ok(
    maxInFlight <= DOWNLOAD_TRANSFER_CONCURRENCY,
    `expected concurrency <= ${DOWNLOAD_TRANSFER_CONCURRENCY}, got ${maxInFlight}`,
  );
  const downloaded = await fs.promises.readFile(targetPath);
  assert.deepEqual(downloaded, payload);
});

test("non-sudo downloads keep serial stream when isolated channel is unavailable", async (t) => {
  // P1 regression lock: without sudo, isolated-channel miss (pool full / open
  // unavailable) must stay on createReadStream. concurrent-shared READ fanout
  // on the browse channel is sudo-only (#2719); otherwise #1507's one-fast-path
  // per session is bypassed.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-nonsudo-stream-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(4 * TRANSFER_CHUNK_SIZE, 19);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 197;
  const targetPath = path.join(tempDir, "download.bin");

  let openCalls = 0;
  let createReadStreamCalls = 0;
  let endCalls = 0;
  const sharedSftp = createFastSftp({
    open() {
      openCalls += 1;
      throw new Error("non-sudo must not use concurrent shared OPEN after isolated miss");
    },
    read() {
      throw new Error("non-sudo must not use concurrent shared READ after isolated miss");
    },
    createReadStream(_remotePath, options = {}) {
      createReadStreamCalls += 1;
      const start = Number(options.start) || 0;
      const end = options.end == null ? payload.length - 1 : Number(options.end);
      return Readable.from([payload.subarray(start, end + 1)]);
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    // No client.client → openIsolatedSftpChannel returns null (isolated miss).
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-nonsudo-isolated-miss-stream",
      sourcePath: "/home/user/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined, result.error);
  assert.equal(openCalls, 0, "non-sudo isolated miss must not open shared concurrent handle");
  assert.ok(createReadStreamCalls >= 1, "expected serial createReadStream fallback");
  assert.equal(endCalls, 0, "browse channel must not be ended");
  const downloaded = await fs.promises.readFile(targetPath);
  assert.deepEqual(downloaded, payload);
});

test("second concurrent sudo download uses stream while shared fast slot is busy", async (t) => {
  // P2: sudo shared fanout must honor FAST_DOWNLOAD_CHANNELS_PER_SESSION (#1507).
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-slot-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payloadA = Buffer.alloc(4 * TRANSFER_CHUNK_SIZE, 11);
  const payloadB = Buffer.alloc(2 * TRANSFER_CHUNK_SIZE, 22);
  const targetA = path.join(tempDir, "a.bin");
  const targetB = path.join(tempDir, "b.bin");

  let endCalls = 0;
  let createReadStreamCalls = 0;
  const pendingReadCallbacks = [];
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from(`handle:${_remotePath}`));
    },
    read(_handle, buffer, offset, length, position, callback) {
      // Stall forever so the first transfer keeps the shared fast slot.
      pendingReadCallbacks.push(() => {
        buffer.fill(11, offset, offset + length);
        callback(null, length);
      });
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream(_remotePath, options = {}) {
      createReadStreamCalls += 1;
      const start = Number(options.start) || 0;
      const end = options.end == null ? payloadB.length - 1 : Number(options.end);
      return Readable.from([payloadB.subarray(start, end + 1)]);
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat(remotePath) {
      const size = String(remotePath).includes("b.bin") ? payloadB.length : payloadA.length;
      return Promise.resolve({
        size,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const first = transferBridge.startTransfer(
    { sender },
    {
      transferId: "download-sudo-slot-first",
      sourcePath: "/root/a.bin",
      targetPath: targetA,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payloadA.length,
      resumable: true,
    },
  );

  const firstReady = await waitUntil(() => pendingReadCallbacks.length >= 1, 2000);
  assert.ok(firstReady, "expected first sudo download to hold shared READ slot");

  const second = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-sudo-slot-second",
      sourcePath: "/root/b.bin",
      targetPath: targetB,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payloadB.length,
      resumable: true,
    },
  );

  assert.equal(second.error, undefined, second.error);
  assert.ok(createReadStreamCalls >= 1, "second sudo download must use stream while fast slot busy");
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  const downloadedB = await fs.promises.readFile(targetB);
  assert.deepEqual(downloadedB, payloadB);

  await transferBridge.cancelTransfer(null, { transferId: "download-sudo-slot-first" });
  const firstResult = await Promise.race([
    first,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error("first transfer did not settle after cancel")), 5000);
    }),
  ]);
  assert.match(firstResult.error || "", /cancel/i);
  for (const release of pendingReadCallbacks.splice(0)) {
    try { release(); } catch { /* ignore */ }
  }
});

test("isolated download CLOSE timeout disposes channel instead of returning it to the pool", async (t) => {
  // Codex P2 on a3b11137: success-path CLOSE timeout with disposeChannel:true
  // only logged and left remoteCloseError unset, so downloadFile released the
  // isolated channel back to the pool without dispose.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-isolated-close-timeout-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(2 * TRANSFER_CHUNK_SIZE, 33);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 173;
  const targetPath = path.join(tempDir, "download.bin");

  let openedChannels = 0;
  let endCalls = 0;
  let closeCalls = 0;
  const makeIsolated = () => createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from(`isolated-${openedChannels}`));
    },
    read(_handle, buffer, offset, length, position, callback) {
      const slice = payload.subarray(position, position + length);
      slice.copy(buffer, offset);
      setImmediate(() => callback(null, slice.length));
    },
    close(_handle, _callback) {
      closeCalls += 1;
      // Never invoke callback — simulates a hung CLOSE on an isolated channel.
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    sftp: createFastSftp({
      createReadStream() {
        throw new Error("serial stream must not run when isolated concurrent READ succeeds");
      },
    }),
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
    client: {
      sftp(callback) {
        openedChannels += 1;
        callback(null, makeIsolated());
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const first = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-isolated-close-timeout",
      sourcePath: "/tmp/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(first.error, undefined, first.error);
  assert.ok(closeCalls >= 1, "expected isolated CLOSE to be attempted");
  assert.ok(endCalls >= 1, "CLOSE timeout must dispose the isolated channel");
  assert.deepEqual(await fs.promises.readFile(targetPath), payload);

  // A follow-up download must open a fresh isolated channel, not reuse the timed-out one.
  const targetPath2 = path.join(tempDir, "download-2.bin");
  const second = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-isolated-close-timeout-2",
      sourcePath: "/tmp/download.bin",
      targetPath: targetPath2,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );
  assert.equal(second.error, undefined, second.error);
  assert.ok(openedChannels >= 2, `expected a new isolated channel after dispose, got ${openedChannels}`);
  assert.deepEqual(await fs.promises.readFile(targetPath2), payload);
});

test("cancel during stalled shared sudo download OPEN settles without sftp.end()", async (t) => {
  // Codex P2 on 2c369403: disposeChannel:false made pre-OPEN abort a no-op while
  // openSftpHandle had no cancel reject, so cancel hung forever and held the lease.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-open-stall-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const targetPath = path.join(tempDir, "download.bin");
  const fileSize = DOWNLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE;
  let endCalls = 0;
  let createReadStreamCalls = 0;
  let closeCalls = 0;
  let releaseOpen = null;
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      releaseOpen = () => callback(null, Buffer.from("late-shared-handle"));
    },
    read() {
      throw new Error("READ must not run after cancel during OPEN");
    },
    close(_handle, callback) {
      closeCalls += 1;
      callback(null);
    },
    createReadStream() {
      createReadStreamCalls += 1;
      throw new Error("serial createReadStream must not run after cancel");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: fileSize,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const transferId = "download-sudo-open-stall-cancel";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: "/root/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: fileSize,
      resumable: true,
    },
  );

  const ready = await waitUntil(() => typeof releaseOpen === "function", 2000);
  assert.ok(ready, "expected shared OPEN to stall");

  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error("transfer did not settle after cancel during shared OPEN")), 3000);
    }),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended on cancel");
  assert.equal(createReadStreamCalls, 0, "cancel must not fall through to serial stream");
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"));
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"), false);

  // Late OPEN success after cancel should close the handle, not leave it open.
  releaseOpen?.();
  await new Promise((resolve) => setImmediate(resolve));
  assert.ok(closeCalls >= 1, `expected late shared handle close, got ${closeCalls}`);
});

test("shared sudo download OPEN rejects on channel error without hanging", async (t) => {
  // Codex P2 on 50ef8fac: outer onChannelError only recorded the error for a
  // post-await check. If the shared channel emitted error while sftp.open was
  // pending and never invoked the OPEN callback, the transfer hung forever and
  // held the SFTP lease. openSftpHandleForTransfer must reject on channel error.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-open-channel-error-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const targetPath = path.join(tempDir, "download.bin");
  const fileSize = DOWNLOAD_TRANSFER_CONCURRENCY * TRANSFER_CHUNK_SIZE;
  let endCalls = 0;
  let createReadStreamCalls = 0;
  let closeCalls = 0;
  let releaseOpen = null;
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      releaseOpen = () => callback(null, Buffer.from("late-shared-handle-after-error"));
    },
    read() {
      throw new Error("READ must not run after channel error during OPEN");
    },
    close(_handle, callback) {
      closeCalls += 1;
      callback(null);
    },
    createReadStream() {
      createReadStreamCalls += 1;
      // Concurrent shared OPEN failure falls through to stream fallback on the
      // same (already dead) channel — fail fast so the transfer can settle.
      const stream = new Readable({ read() {} });
      setImmediate(() => stream.destroy(new Error("shared channel already dead")));
      return stream;
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: fileSize,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const transferId = "download-sudo-open-channel-error";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: "/root/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: fileSize,
      resumable: true,
    },
  );

  const ready = await waitUntil(() => typeof releaseOpen === "function", 2000);
  assert.ok(ready, "expected shared OPEN to stall");

  sharedSftp.emit("error", new Error("shared SFTP channel died during OPEN"));

  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(
        () => reject(new Error("transfer did not settle after shared channel error during OPEN")),
        3000,
      );
    }),
  ]);

  assert.ok(result.error, "expected transfer to fail after channel error");
  assert.match(
    result.error,
    /shared SFTP channel died during OPEN|shared channel already dead/i,
  );
  assert.equal(endCalls, 0, "shared sudo channel must not be ended on channel error");
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), false);
  assert.ok(
    createReadStreamCalls >= 1,
    "concurrent shared OPEN failure should fall through to stream fallback",
  );

  // Late OPEN success after channel-error reject should close the handle.
  releaseOpen?.();
  await new Promise((resolve) => setImmediate(resolve));
  assert.ok(closeCalls >= 1, `expected late shared handle close, got ${closeCalls}`);
});

test("cancel during stalled shared sudo READ settles without sftp.end()", async (t) => {
  // Codex P2 on d855e69c: disposeChannel:false passed forceSettleOnError:false, so
  // cancel while an sftp.read callback never arrives hung forever (abortChannel is
  // a no-op on shared channels). Downloads have no remote WRITEs to drain, so they
  // must force-settle on cancel without ending the sudo session.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-read-stall-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const targetPath = path.join(tempDir, "download.bin");
  const fileSize = 4 * TRANSFER_CHUNK_SIZE;
  let endCalls = 0;
  let createReadStreamCalls = 0;
  let activeReads = 0;
  let maxInFlight = 0;
  const pendingReadCallbacks = [];
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from("shared-read-handle"));
    },
    read(_handle, _buffer, _offset, _length, _position, callback) {
      activeReads += 1;
      maxInFlight = Math.max(maxInFlight, activeReads);
      // Stall forever — never invoke callback (simulates dead/hung READ).
      pendingReadCallbacks.push(() => {
        activeReads -= 1;
        callback(new Error("late READ after cancel"));
      });
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream() {
      createReadStreamCalls += 1;
      throw new Error("serial createReadStream must not run after cancel");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: fileSize,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const transferId = "download-sudo-read-stall-cancel";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: "/root/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: fileSize,
      resumable: true,
    },
  );

  const ready = await waitUntil(() => pendingReadCallbacks.length >= 1, 2000);
  assert.ok(ready, "expected at least one shared READ to stall");
  assert.ok(maxInFlight >= 1, `expected in-flight READ, got ${maxInFlight}`);

  await transferBridge.cancelTransfer(null, { transferId });
  // forceSettleOnError grace is 2s; shared cancel/fail CLOSE is fire-and-forget.
  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(
        () => reject(new Error("transfer did not settle after cancel during stalled shared READ")),
        5000,
      );
    }),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended on cancel during READ");
  assert.equal(createReadStreamCalls, 0, "cancel must not fall through to serial stream");
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"));
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"), false);

  // Late READ callbacks after settle must not crash the process.
  for (const release of pendingReadCallbacks.splice(0)) {
    try { release(); } catch { /* ignore */ }
  }
  await new Promise((resolve) => setImmediate(resolve));
});

test("cancel during stalled shared verification READ settles without sftp.end()", async (t) => {
  // Codex P2 on cba714f3: after ranged READs finish, verifyFastDownloadSamples still
  // awaits bare sftp.read outside forceSettleOnError. Shared cancel cannot
  // sftp.end(), so a hung verification READ would hold the transfer and lease.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-verify-stall-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(3 * TRANSFER_CHUNK_SIZE, 61);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 251;
  const targetPath = path.join(tempDir, "download.bin");
  let endCalls = 0;
  let createReadStreamCalls = 0;
  let transferredBytes = 0;
  const pendingVerifyCallbacks = [];
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from("shared-verify-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      // Complete the concurrent download first; stall only verification samples.
      if (transferredBytes < payload.length) {
        const slice = payload.subarray(position, position + length);
        slice.copy(buffer, offset);
        transferredBytes += slice.length;
        setImmediate(() => callback(null, slice.length));
        return;
      }
      pendingVerifyCallbacks.push(() => {
        callback(new Error("late verification READ after cancel"));
      });
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream() {
      createReadStreamCalls += 1;
      throw new Error("serial createReadStream must not run after cancel");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const transferId = "download-sudo-verify-stall-cancel";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: "/root/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const ready = await waitUntil(() => pendingVerifyCallbacks.length >= 1, 3000);
  assert.ok(ready, "expected verification READ to stall after ranged download");

  await transferBridge.cancelTransfer(null, { transferId });
  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(
        () => reject(new Error("transfer did not settle after cancel during stalled verification READ")),
        5000,
      );
    }),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended during verification cancel");
  assert.equal(createReadStreamCalls, 0, "cancel must not fall through to serial stream");
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"));
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"), false);

  for (const release of pendingVerifyCallbacks.splice(0)) {
    try { release(); } catch { /* ignore */ }
  }
  await new Promise((resolve) => setImmediate(resolve));
});

test("cancel during responsive last verification sample reports cancelled", async (t) => {
  // Codex P2 on 74ae5e0a: cancel during the last sample can let the READ win the
  // race before the 2s force-settle fires. Without a post-race cancelled check,
  // verification returns successfully and sendComplete() reports done.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-verify-race-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(3 * TRANSFER_CHUNK_SIZE, 67);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 251;
  const targetPath = path.join(tempDir, "download.bin");
  let endCalls = 0;
  let transferredBytes = 0;
  let verifyReadCount = 0;
  let releaseLastVerifyRead = null;
  const lastVerifyReadHeld = new Promise((resolve) => {
    releaseLastVerifyRead = resolve;
  });
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from("shared-verify-race-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      if (transferredBytes < payload.length) {
        const slice = payload.subarray(position, position + length);
        slice.copy(buffer, offset);
        transferredBytes += slice.length;
        setImmediate(() => callback(null, slice.length));
        return;
      }
      verifyReadCount += 1;
      const slice = payload.subarray(position, position + length);
      slice.copy(buffer, offset);
      // Hold only the last sample briefly so cancel can land mid-READ, then
      // complete successfully (responsive server — force-settle never fires).
      if (verifyReadCount >= 3) {
        lastVerifyReadHeld.then(() => {
          setImmediate(() => callback(null, slice.length));
        });
        return;
      }
      setImmediate(() => callback(null, slice.length));
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream() {
      throw new Error("serial createReadStream must not run");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const transferId = "download-sudo-verify-race-cancel";
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId,
      sourcePath: "/root/download.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const ready = await waitUntil(() => verifyReadCount >= 3, 3000);
  assert.ok(ready, "expected last verification sample to be held");

  await transferBridge.cancelTransfer(null, { transferId });
  releaseLastVerifyRead();

  const result = await Promise.race([
    running,
    new Promise((_, reject) => {
      setTimeout(
        () => reject(new Error("transfer did not settle after cancel during last verification sample")),
        3000,
      );
    }),
  ]);

  assert.match(result.error || "", /cancel/i);
  assert.equal(endCalls, 0, "shared sudo channel must not be ended");
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"));
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"), false);
});

test("shared verification channel error between samples fails without hanging", async (t) => {
  // Codex P2 on 14783168: channel error between sample races (after one sample
  // resolves, before the next rejectPending is installed) was dropped; the next
  // readSftpRange could hang forever on a dead shared channel.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-verify-gap-error-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(3 * TRANSFER_CHUNK_SIZE, 71);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 251;
  const targetPath = path.join(tempDir, "download.bin");
  let endCalls = 0;
  let transferredBytes = 0;
  let verifyReadCount = 0;
  let sharedSftp = null;
  const pendingSecondVerify = [];
  sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from("shared-verify-gap-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      if (transferredBytes < payload.length) {
        const slice = payload.subarray(position, position + length);
        slice.copy(buffer, offset);
        transferredBytes += slice.length;
        setImmediate(() => callback(null, slice.length));
        return;
      }
      verifyReadCount += 1;
      if (verifyReadCount === 1) {
        const slice = payload.subarray(position, position + length);
        slice.copy(buffer, offset);
        setImmediate(() => {
          callback(null, slice.length);
          // Emit after the first sample resolves and before the next race installs
          // rejectPending — the gap Codex identified.
          setImmediate(() => {
            sharedSftp.emit("error", new Error("shared channel died between samples"));
          });
        });
        return;
      }
      // Subsequent verification READs never callback (dead channel).
      pendingSecondVerify.push(callback);
    },
    close(_handle, callback) {
      callback(null);
    },
    createReadStream() {
      throw new Error("serial createReadStream must not run after channel error");
    },
    end() {
      endCalls += 1;
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: 1_000,
        ctimeMs: 1_000,
        mtime: 1,
        ctime: 1,
      });
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const result = await Promise.race([
    transferBridge.startTransfer(
      { sender },
      {
        transferId: "download-sudo-verify-gap-error",
        sourcePath: "/root/download.bin",
        targetPath,
        sourceType: "sftp",
        targetType: "local",
        sourceSftpId: "source",
        totalBytes: payload.length,
        resumable: true,
      },
    ),
    new Promise((_, reject) => {
      setTimeout(
        () => reject(new Error("transfer hung after shared verification channel error between samples")),
        4000,
      );
    }),
  ]);

  // Concurrent path already staged the full file, so fallback may complete via
  // the checkpoint>=fileSize short-circuit — either outcome is fine as long as
  // we settle instead of hanging on the dead-channel READ.
  assert.ok(result, "expected transfer to settle after inter-sample channel error");
  assert.equal(endCalls, 0, "shared sudo channel must not be ended on verification error");
  assert.ok(
    sender.sent.some((entry) => (
      entry.channel === "netcatty:transfer:complete"
      || entry.channel === "netcatty:transfer:error"
      || entry.channel === "netcatty:transfer:cancelled"
    )),
    "expected a terminal transfer event after channel error between samples",
  );
});

test("shared upload errors wait for all in-flight WRITEs before returning", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-shared-error-drain-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const fileSize = 4 * TRANSFER_CHUNK_SIZE;
  const localPath = path.join(tempDir, "upload.bin");
  await fs.promises.writeFile(localPath, Buffer.alloc(fileSize, 13));

  const pendingWriteCallbacks = [];
  let triggerFirstError;
  const firstErrorTriggered = new Promise((resolve) => {
    triggerFirstError = resolve;
  });
  let firstErrorScheduled = false;
  const sharedSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "w");
      callback(null, Buffer.from("shared-handle"));
    },
    write(_handle, _buffer, _offset, _length, _position, callback) {
      pendingWriteCallbacks.push(callback);
      if (!firstErrorScheduled && pendingWriteCallbacks.length === 4) {
        firstErrorScheduled = true;
        setImmediate(() => {
          const fail = pendingWriteCallbacks.shift();
          fail(new Error("shared WRITE failed"));
          triggerFirstError();
        });
      }
    },
    close(_handle, callback) {
      callback(null);
    },
  });

  const client = {
    __netcattySudoMode: true,
    sftp: sharedSftp,
    stat() {
      return Promise.resolve({ size: 0 });
    },
    delete() {
      return Promise.resolve();
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  let transferSettled = false;
  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "upload-shared-error-drain",
      sourcePath: localPath,
      targetPath: "/tmp/upload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: fileSize,
      resumable: true,
    },
  ).finally(() => {
    transferSettled = true;
  });

  await firstErrorTriggered;
  await new Promise((resolve) => setTimeout(resolve, 2100));
  const settledBeforeRemainingWrites = transferSettled;
  for (const callback of pendingWriteCallbacks.splice(0)) callback(null);

  const result = await running;
  assert.equal(settledBeforeRemainingWrites, false);
  assert.match(result.error || "", /shared WRITE failed|pipelined upload failed/i);
});

test("sudo SFTP sessions without open/write fail closed (no serial WriteStream)", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sudo-fail-closed-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const localPath = path.join(tempDir, "payload.bin");
  const payload = Buffer.alloc(64 * 1024, 7);
  await fs.promises.writeFile(localPath, payload);

  let createWriteStreamCalls = 0;
  const streamOnlySftp = createFastSftp({
    createWriteStream() {
      createWriteStreamCalls += 1;
      throw new Error("serial WriteStream must not run");
    },
  });

  const client = {
    // Isolated channel skipped; shared sftp has only createWriteStream.
    __netcattySudoMode: true,
    sftp: streamOnlySftp,
    stat() {
      return Promise.resolve({ size: 0 });
    },
    rename() {
      return Promise.resolve();
    },
    delete() {
      return Promise.resolve();
    },
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const sender = createSender();
  const result = await transferBridge.startTransfer(
    { sender },
    {
      transferId: "upload-sudo-fail-closed",
      sourcePath: localPath,
      targetPath: "/tmp/payload.bin",
      sourceType: "local",
      targetType: "sftp",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.match(result.error || "", /pipelined upload failed|open\/write missing/i);
  assert.equal(createWriteStreamCalls, 0);
  assert.ok(sender.sent.some((entry) => entry.channel === "netcatty:transfer:error"));
});

test("resumable SFTP downloads preserve a 2MB request window on high-latency paths", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(4 * 1024 * 1024, 7);
  let activeReads = 0;
  let observedConcurrency = 0;
  let observedChunkSize = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, flags, callback) {
      assert.equal(flags, "r");
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      activeReads += 1;
      observedConcurrency = Math.max(observedConcurrency, activeReads);
      observedChunkSize = Math.max(observedChunkSize, length);
      payload.copy(buffer, offset, position, position + length);
      setImmediate(() => {
        activeReads -= 1;
        callback(null, length, buffer, position);
      });
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        return Readable.from(payload);
      },
    }),
    stat(_path) {
      return Promise.resolve({ size: payload.length });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const result = await transferBridge.startTransfer(
    { sender },
    {
      transferId: "download-large",
      sourcePath: "/tmp/large.bin",
      targetPath: path.join(tempDir, "large.bin"),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      resumable: true,
    },
  );

  assert.equal(result.error, undefined);
  assert.equal(observedChunkSize, 32 * 1024);
  assert.equal(observedConcurrency * observedChunkSize, 2 * 1024 * 1024);
  assert.deepEqual(await fs.promises.readFile(path.join(tempDir, "large.bin")), payload);
});

test("fast resumable downloads pause only at a complete checkpoint", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-fast-pause-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(4 * 1024 * 1024, 11);
  const pendingReads = [];
  let holdReads = true;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      const complete = () => {
        payload.copy(buffer, offset, position, position + length);
        callback(null, length, buffer, position);
      };
      if (holdReads) pendingReads.push(complete);
      else setImmediate(complete);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        return Readable.from(payload);
      },
    }),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const targetPath = path.join(tempDir, "large.bin");
  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-fast-paused",
      sourcePath: "/tmp/large.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (pendingReads.length < 64 && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(pendingReads.length, 64);

  const originalStat = fs.promises.stat;
  fs.promises.stat = async (filePath) => {
    if (String(filePath).includes("download-fast-paused")) {
      throw new Error("fast range checkpoints must not be inferred from file size");
    }
    return originalStat(filePath);
  };
  const pausing = transferBridge.pauseTransfer(null, { transferId: "download-fast-paused" });
  holdReads = false;
  for (const complete of pendingReads.splice(0)) complete();
  let paused;
  try {
    paused = await pausing;
  } finally {
    fs.promises.stat = originalStat;
  }
  assert.equal(paused.success, true);
  assert.equal(paused.checkpointBytes, 2 * 1024 * 1024);

  {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "download-fast-paused" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  assert.equal((await running).error, undefined);
  assert.deepEqual(await fs.promises.readFile(targetPath), payload);
});

test("fast resumable downloads fall back from the highest contiguous checkpoint", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-contiguous-fallback-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(3 * 32 * 1024, 17);
  let fallbackStart = null;
  let secondReadCallback = null;
  let targetPath = null;
  let stagedPath = null;
  const progress = [];
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      if (position === 32 * 1024) {
        secondReadCallback = callback;
        return;
      }
      payload.copy(buffer, offset, position, position + length);
      callback(null, length, buffer, position);
      if (position === 2 * 32 * 1024) {
        queueMicrotask(() => secondReadCallback(new Error("second range failed")));
      }
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream(_remotePath, options) {
        fallbackStart = options.start;
        assert.equal(fs.statSync(stagedPath).size, options.start);
        return Readable.from(payload.subarray(options.start));
      },
    }),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  targetPath = path.join(tempDir, "fallback.bin");
  stagedPath = tempDirBridge.getTransferTempFilePath(
    "download-contiguous-fallback",
    path.basename(targetPath),
  );
  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-contiguous-fallback",
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
    (transferred) => progress.push(transferred),
  );

  assert.equal(result.error, undefined);
  assert.equal(fallbackStart, 32 * 1024);
  const firstPastCheckpoint = progress.findIndex((transferred) => transferred > fallbackStart);
  assert.ok(firstPastCheckpoint >= 0);
  assert.ok(progress.slice(firstPastCheckpoint + 1).includes(fallbackStart));
  assert.deepEqual(await fs.promises.readFile(targetPath), payload);
});

test("resumable download fallback rejects a remote source changed during streaming", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-download-fallback-change-"));
  const transferId = "download-fallback-change";
  const targetPath = path.join(tempDir, "download.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
    await fs.promises.rm(stagedPath, { force: true });
  });
  await fs.promises.writeFile(targetPath, "original");

  const payload = Buffer.alloc(32 * 1024, 51);
  let sourceChanged = false;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, _buffer, _offset, _length, _position, callback) {
      callback(new Error("range download unavailable"));
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        sourceChanged = true;
        return Readable.from(payload);
      },
    }),
    stat() {
      return Promise.resolve({
        size: payload.length,
        mtimeMs: sourceChanged ? 2 : 1,
        ctimeMs: sourceChanged ? 2 : 1,
      });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(sourceChanged, true);
  assert.match(result.error || "", /source.*changed/i);
  assert.equal(await fs.promises.readFile(targetPath, "utf8"), "original");
  assert.equal(fs.existsSync(stagedPath), false);
});

test("range-failure fallback truncates sparse local tail before streaming", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-sparse-tail-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  // Two chunks: finish the second first, then fail the first so contiguous stays 0
  // while the local file already has a sparse tail past the durable checkpoint.
  const chunk = 32 * 1024;
  const payload = Buffer.alloc(2 * chunk, 17);
  let firstReadCallback = null;
  let fallbackStart = null;
  let sizeAtFallback = null;
  const transferId = "download-sparse-tail";
  const targetPath = path.join(tempDir, "sparse.bin");
  // Resumable downloads stage under the transfer temp path, not the final target.
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath));
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      if (position === 0) {
        firstReadCallback = () => callback(new Error("first range failed"));
        // Complete the later range first, then fail the first.
        queueMicrotask(() => {
          // second range is already in flight separately
        });
        return;
      }
      payload.copy(buffer, offset, position, position + length);
      callback(null, length, buffer, position);
      queueMicrotask(() => firstReadCallback?.());
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream(_remotePath, options) {
        fallbackStart = options.start;
        // Capture *staged* size when stream fallback opens (post-truncate).
        try {
          sizeAtFallback = fs.statSync(stagedPath).size;
        } catch {
          sizeAtFallback = -1;
        }
        return Readable.from(payload.subarray(options.start || 0));
      },
    }),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(result.error, undefined, result.error);
  assert.equal(fallbackStart, 0);
  // Contiguous checkpoint never advanced past 0; sparse tail must be truncated
  // on the staged .part (final target is only written at promote time).
  assert.equal(sizeAtFallback, 0);
  assert.deepEqual(await fs.promises.readFile(targetPath), payload);
});

test("S2S upload-phase concurrent failure does not truncate the complete local temp source", async (t) => {
  const transferId = `s2s-no-truncate-${crypto.randomUUID()}`;
  const payload = Buffer.alloc(64 * 1024, 23);
  const localStage = tempDirBridge.getTransferTempFilePath(transferId, "payload.bin");
  await fs.promises.writeFile(localStage, payload);
  t.after(async () => {
    await fs.promises.unlink(localStage).catch(() => {});
  });

  let sizeAfterFail = null;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, _length, position, callback) {
      if (position === 0) {
        callback(new Error("first upload range failed"));
        return;
      }
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
    end() {},
  });
  const sourceClient = {
    sftp: createFastSftp({}),
    stat: async () => ({ size: payload.length }),
  };
  const targetClient = {
    sftp: createFastSftp({
      open(_remotePath, _flags, callback) {
        callback(new Error("shared open also fails"));
      },
      createWriteStream() {
        throw new Error("serial WriteStream must not run");
      },
    }),
    stat: async () => ({ size: 0 }),
    rename: async () => {},
    delete: async () => {},
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({
    sftpClients: new Map([
      ["source", sourceClient],
      ["target", targetClient],
    ]),
  });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/source/payload.bin",
      targetPath: "/target/payload.bin",
      sourceType: "sftp",
      targetType: "sftp",
      sourceSftpId: "source",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
      resumeStage: "upload",
      downloadCheckpointBytes: payload.length,
      uploadCheckpointBytes: 0,
      checkpointBytes: 0,
    },
  );

  try {
    sizeAfterFail = fs.statSync(localStage).size;
  } catch {
    sizeAfterFail = -1;
  }
  // Fail closed — local S2S temp is the fully-downloaded source, never truncated.
  assert.match(result.error || "", /pipelined upload failed|first upload range failed/i);
  assert.equal(sizeAfterFail, payload.length);
});

test("cancelled fast resumable downloads release their isolated channel", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-cancel-release-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(TRANSFER_CHUNK_SIZE * 2, 19);
  let openedChannels = 0;
  let fallbackReads = 0;
  const pendingReads = [];
  let lateReadScheduled = false;
  const firstChannel = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("first-handle"));
    },
    read(_handle, _buffer, _offset, _length, _position, callback) {
      pendingReads.push(callback);
    },
    end() {
      if (lateReadScheduled) return;
      lateReadScheduled = true;
      setTimeout(() => {
        pendingReads.shift()?.(new Error("late channel cancellation"));
      }, 1500);
    },
  });
  const secondChannel = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("second-handle"));
    },
    read(_handle, buffer, offset, length, position, callback) {
      payload.copy(buffer, offset, position, position + length);
      callback(null, length, buffer, position);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream(_remotePath, options) {
        if (options?.start !== undefined) fallbackReads += 1;
        return Readable.from(payload);
      },
    }),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    client: {
      sftp(callback) {
        openedChannels += 1;
        callback(null, openedChannels === 1 ? firstChannel : secondChannel);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const first = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-cancel-release-first",
      sourcePath: "/tmp/source.bin",
      targetPath: path.join(tempDir, "first.bin"),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );
  const readyDeadline = Date.now() + 1000;
  while (pendingReads.length < 2 && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(pendingReads.length, 2);
  const cancelledAt = Date.now();
  await transferBridge.cancelTransfer(null, { transferId: "download-cancel-release-first" });
  assert.equal((await first).error, "Transfer cancelled");
  const cancelDuration = Date.now() - cancelledAt;
  assert.ok(cancelDuration >= 1900);
  assert.ok(cancelDuration < 3000);

  const second = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-cancel-release-second",
      sourcePath: "/tmp/source.bin",
      targetPath: path.join(tempDir, "second.bin"),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  assert.equal(second.error, undefined);
  assert.equal(openedChannels, 2);
  assert.equal(fallbackReads, 0);
});

test("SFTP downloads fall back to a compatible stream after fastGet fails", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-fallback-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const expected = Buffer.from("complete fallback download");
  let fastGetAttempts = 0;
  const fastSftp = createFastSftp({
    fastGet(_remotePath, localPath, _options, done) {
      fastGetAttempts += 1;
      fs.promises.writeFile(localPath, "partial").then(
        () => done(new Error("server rejected concurrent reads")),
        done,
      );
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        const { Readable } = require("node:stream");
        return Readable.from(expected);
      },
    }),
    stat() {
      return Promise.resolve({ size: expected.length });
    },
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const targetPath = path.join(tempDir, "fallback.bin");
  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: "download-fallback",
      sourcePath: "/tmp/fallback.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: expected.length,
    },
  );

  assert.equal(result.error, undefined);
  assert.equal(fastGetAttempts, 1);
  assert.deepEqual(await fs.promises.readFile(targetPath), expected);
});

test("SFTP downloads keep concurrent files moving within the fast-channel budget", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-budget-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const completions = [];
  let activeFastGets = 0;
  let maxActiveFastGets = 0;
  let openedChannels = 0;
  let fallbackReads = 0;
  const fastSftp = createFastSftp({
    fastGet(_remotePath, localPath, _options, done) {
      activeFastGets += 1;
      maxActiveFastGets = Math.max(maxActiveFastGets, activeFastGets);
      completions.push(async () => {
        await fs.promises.writeFile(localPath, "downloaded");
        activeFastGets -= 1;
        done();
      });
    },
  });
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        fallbackReads += 1;
        const { Readable } = require("node:stream");
        return Readable.from("downloaded");
      },
    }),
    stat() {
      return Promise.resolve({ size: 10 });
    },
    client: {
      sftp(callback) {
        openedChannels += 1;
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const start = (id) => transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: id,
      sourcePath: `/tmp/${id}.bin`,
      targetPath: path.join(tempDir, `${id}.bin`),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: 10,
      // Isolate the fast-channel budget test from global file admission.
      skipAdmission: true,
    },
  );

  const first = start("download-one");
  const firstDeadline = Date.now() + 1000;
  while (completions.length < 1 && Date.now() < firstDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(completions.length, 1);
  assert.equal(openedChannels, 1);
  const second = start("download-two");
  const secondResult = await second;
  assert.equal(secondResult.error, undefined);
  assert.equal(fallbackReads, 1);

  await completions[0]();
  assert.equal((await first).error, undefined);
  assert.equal(maxActiveFastGets, 1);
  assert.equal(openedChannels, 1);
});

test("idle fast-download channels are discarded when a delayed error arrives", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-idle-error-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.from("downloaded");
  const channels = [];
  let endedChannels = 0;
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: payload.length });
    },
    client: {
      sftp(callback) {
        const channel = createFastSftp({
          fastGet(_remotePath, localPath, _options, done) {
            fs.promises.writeFile(localPath, payload).then(() => done(), done);
          },
          end() {
            endedChannels += 1;
          },
        });
        channels.push(channel);
        callback(null, channel);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const start = (id) => transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: id,
      sourcePath: `/tmp/${id}.bin`,
      targetPath: path.join(tempDir, `${id}.bin`),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: payload.length,
    },
  );

  assert.equal((await start("idle-error-first")).error, undefined);
  assert.equal(channels.length, 1);
  assert.ok(channels[0].listenerCount("error") > 0);
  channels[0].emit("error", new Error("delayed idle failure"));
  assert.equal(endedChannels, 1);

  assert.equal((await start("idle-error-second")).error, undefined);
  assert.equal(channels.length, 2);
  channels[1].emit("error", new Error("test cleanup"));
});

test("SFTP downloads cancelled while opening do not block the session", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-open-cancel-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  let delayedOpen = null;
  let abandonedChannelClosed = false;
  let openCalls = 0;
  const abandonedSftp = createFastSftp({
    end() {
      abandonedChannelClosed = true;
    },
  });
  const workingSftp = createFastSftp({
    fastGet(_remotePath, localPath, _options, done) {
      fs.promises.writeFile(localPath, "downloaded").then(
        () => done(),
        done,
      );
    },
  });
  const client = {
    sftp: createFastSftp({}),
    stat() {
      return Promise.resolve({ size: 10 });
    },
    client: {
      sftp(callback) {
        openCalls += 1;
        if (openCalls === 1) {
          delayedOpen = callback;
        } else {
          callback(null, workingSftp);
        }
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const start = (id) => transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId: id,
      sourcePath: `/tmp/${id}.bin`,
      targetPath: path.join(tempDir, `${id}.bin`),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: 10,
    },
  );

  const cancelledPromise = start("download-opening");
  const deadline = Date.now() + 1000;
  while (!delayedOpen && Date.now() < deadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(typeof delayedOpen, "function");
  await transferBridge.cancelTransfer(null, { transferId: "download-opening" });
  delayedOpen(null, abandonedSftp);

  const cancelled = await cancelledPromise;
  assert.equal(cancelled.error, "Transfer cancelled");
  assert.equal(abandonedChannelClosed, true);

  const next = await Promise.race([
    start("download-after-cancel"),
    new Promise((_, reject) => setTimeout(() => reject(new Error("next download remained blocked")), 1000)),
  ]);
  assert.equal(next.error, undefined);
  assert.equal(openCalls, 2);
});

test("resumable stream transfers pause without losing their checkpoint and continue", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-pause-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const source = new PassThrough();
  const sink = new Writable({ write(_chunk, _encoding, callback) { callback(); } });
  let readStreamCalls = 0;
  const sftp = createFastSftp({
    createReadStream() {
      readStreamCalls += 1;
      return readStreamCalls === 1 ? source : Readable.from(Buffer.from("abcdef"));
    },
    createWriteStream() { return sink; },
  });
  const client = {
    sftp,
    stat() { return Promise.resolve({ size: 6 }); },
    client: { sftp(callback) { callback(new Error("isolated channel unavailable")); } },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const lifecycleEvents = [];
  const originalLoad = Module._load;
  // broadcastGlobalTransferEvent only loads electron when process.versions.electron
  // is set (avoids install.js downloads in bare Node unit tests).
  const previousElectronVersion = process.versions.electron;
  Object.defineProperty(process.versions, "electron", {
    configurable: true,
    enumerable: true,
    value: previousElectronVersion || "test",
  });
  Module._load = function load(request, parent, isMain) {
    if (request === "electron") {
      return {
        BrowserWindow: {
          getAllWindows: () => [{
            isDestroyed: () => false,
            webContents: {
              isDestroyed: () => false,
              send(channel, payload) {
                if (channel === "netcatty:sftp:global-transfer") lifecycleEvents.push(payload);
              },
            },
          }],
        },
      };
    }
    return originalLoad(request, parent, isMain);
  };
  t.after(() => {
    Module._load = originalLoad;
    if (previousElectronVersion === undefined) {
      delete process.versions.electron;
    } else {
      Object.defineProperty(process.versions, "electron", {
        configurable: true,
        enumerable: true,
        value: previousElectronVersion,
      });
    }
  });

  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "download-paused",
      sourcePath: "/tmp/source.bin",
      targetPath: path.join(tempDir, "target.bin"),
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: 6,
      resumable: true,
    },
  );

  const readyDeadline = Date.now() + 1000;
  while (source.listenerCount("data") === 0 && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(source.listenerCount("data") > 0);
  source.write(Buffer.from("abc"));
  await new Promise((resolve) => setImmediate(resolve));
  const paused = await transferBridge.pauseTransfer(null, { transferId: "download-paused" });
  // Full-file source fingerprint is no longer on the pause critical path.
  assert.equal(paused.success, true);
  assert.equal(paused.checkpointBytes, 3);
  assert.equal(paused.resumeStage, "direct");
  assert.equal(paused.downloadCheckpointBytes, 0);
  assert.equal(paused.uploadCheckpointBytes, 0);
  const pausedEvent = lifecycleEvents.findLast((event) => event.type === "paused");
  assert.equal(pausedEvent?.transferId, "download-paused");
  assert.equal(pausedEvent?.checkpointBytes, 3);
  assert.equal(pausedEvent?.resumeStage, "direct");
  assert.equal(pausedEvent?.downloadCheckpointBytes, 0);
  assert.equal(pausedEvent?.uploadCheckpointBytes, 0);
  assert.equal(pausedEvent?.lifecycleEpoch, 1);

  source.write(Buffer.from("def"));
  await new Promise((resolve) => setImmediate(resolve));
  const pausedAgain = await transferBridge.pauseTransfer(null, { transferId: "download-paused" });
  assert.equal(pausedAgain.success, true);
  assert.equal(pausedAgain.checkpointBytes, 3);
  assert.equal(pausedAgain.resumeStage, "direct");

  const resumed = await transferBridge.resumeTransfer(null, { transferId: "download-paused" });
  assert.equal(resumed.success, true);
  assert.ok(Number.isFinite(resumed.lifecycleEpoch), "soft-resume must return bridge lifecycleEpoch");
  assert.deepEqual(
    lifecycleEvents.findLast((event) => event.type === "resumed"),
    { type: "resumed", transferId: "download-paused", lifecycleEpoch: 2 },
  );
  source.end();
  assert.equal((await running).error, undefined);
});

test("stream local-copy pause survives write-stream drain without auto-resuming the pipe", async (t) => {
  // Regression for transfer-list pause (#2458): Node's .pipe() resumes the source
  // on destination 'drain'. SFTP upload no longer uses serial WriteStream; cover
  // unpipe on local→local copies (same pauseTransfer path). Mock write stream
  // still persists bytes so pause checkpoint stat succeeds.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-pipe-pause-local-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(256 * 1024, 91);
  const sourcePath = path.join(tempDir, "source.bin");
  const targetPath = path.join(tempDir, "target.bin");
  await fs.promises.writeFile(sourcePath, payload);

  let durableBytes = 0;
  const pendingWriteCallbacks = [];
  let holdWrites = true;
  let activeWriteStream = null;
  const originalCreateWriteStream = fs.createWriteStream;
  t.after(() => {
    fs.createWriteStream = originalCreateWriteStream;
  });
  fs.createWriteStream = (filePath, options) => {
    const real = originalCreateWriteStream(filePath, options);
    activeWriteStream = new Writable({
      highWaterMark: 16,
      write(chunk, encoding, callback) {
        real.write(chunk, encoding, (err) => {
          if (err) return callback(err);
          durableBytes += chunk.length;
          activeWriteStream.bytesWritten = durableBytes;
          if (holdWrites) {
            pendingWriteCallbacks.push(callback);
            return;
          }
          callback();
        });
      },
      final(callback) {
        real.end(callback);
      },
      destroy(err, callback) {
        real.destroy(err);
        callback(err);
      },
    });
    activeWriteStream.bytesWritten = 0;
    return activeWriteStream;
  };

  transferBridge.init({ sftpClients: new Map() });
  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "local-pipe-pause",
      sourcePath,
      targetPath,
      sourceType: "local",
      targetType: "local",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const backpressureDeadline = Date.now() + 2000;
  while (pendingWriteCallbacks.length === 0 && Date.now() < backpressureDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(pendingWriteCallbacks.length > 0, "write stream should be backpressured");
  const bytesWhenPauseRequested = durableBytes;

  const pausing = transferBridge.pauseTransfer(null, { transferId: "local-pipe-pause" });
  holdWrites = false;
  for (const callback of pendingWriteCallbacks.splice(0)) callback();
  const paused = await pausing;
  assert.equal(paused.success, true, paused.reason);
  const checkpoint = paused.checkpointBytes;
  assert.ok(checkpoint >= bytesWhenPauseRequested);
  assert.ok(
    checkpoint < payload.length,
    "pause must stop before the copy finishes under backpressure",
  );

  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(
    durableBytes,
    checkpoint,
    "paused stream copy must not keep writing after drain",
  );

  {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "local-pipe-pause" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  assert.equal((await running).error, undefined);
  assert.equal(durableBytes, payload.length);
});

test("repeated resume does not double-pipe the same stream pair", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-double-resume-local-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.alloc(256 * 1024, 77);
  const sourcePath = path.join(tempDir, "source.bin");
  const targetPath = path.join(tempDir, "target.bin");
  await fs.promises.writeFile(sourcePath, payload);

  let durableBytes = 0;
  const pendingWriteCallbacks = [];
  let holdWrites = true;
  let pipeCount = 0;
  let activeWriteStream = null;
  const originalCreateWriteStream = fs.createWriteStream;
  t.after(() => {
    fs.createWriteStream = originalCreateWriteStream;
  });
  fs.createWriteStream = (filePath, options) => {
    const real = originalCreateWriteStream(filePath, options);
    activeWriteStream = new Writable({
      highWaterMark: 16,
      write(chunk, encoding, callback) {
        real.write(chunk, encoding, (err) => {
          if (err) return callback(err);
          durableBytes += chunk.length;
          activeWriteStream.bytesWritten = durableBytes;
          if (holdWrites) {
            pendingWriteCallbacks.push(callback);
            return;
          }
          callback();
        });
      },
      final(callback) {
        real.end(callback);
      },
      destroy(err, callback) {
        real.destroy(err);
        callback(err);
      },
    });
    activeWriteStream.bytesWritten = 0;
    return activeWriteStream;
  };
  const originalReadablePipe = Readable.prototype.pipe;
  t.after(() => {
    Readable.prototype.pipe = originalReadablePipe;
  });
  Readable.prototype.pipe = function patchedPipe(dest, ...args) {
    if (dest === activeWriteStream) pipeCount += 1;
    return originalReadablePipe.apply(this, [dest, ...args]);
  };

  transferBridge.init({ sftpClients: new Map() });
  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "local-double-resume",
      sourcePath,
      targetPath,
      sourceType: "local",
      targetType: "local",
      totalBytes: payload.length,
      resumable: true,
    },
  );

  const backpressureDeadline = Date.now() + 2000;
  while (pendingWriteCallbacks.length === 0 && Date.now() < backpressureDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(pendingWriteCallbacks.length > 0, "write stream should be backpressured");

  const pausing = transferBridge.pauseTransfer(null, { transferId: "local-double-resume" });
  holdWrites = false;
  for (const callback of pendingWriteCallbacks.splice(0)) callback();
  const paused = await pausing;
  assert.equal(paused.success, true, paused.reason);
  const pipesAfterPause = pipeCount;

  {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "local-double-resume" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  {
    const resumed = await transferBridge.resumeTransfer(null, { transferId: "local-double-resume" });
    assert.equal(resumed.success, true);
    assert.ok(Number.isFinite(resumed.lifecycleEpoch));
  }
  assert.equal(
    pipeCount,
    pipesAfterPause + 1,
    "second resume must not call pipe() again on the same pair",
  );

  assert.equal((await running).error, undefined);
  assert.equal(durableBytes, payload.length);
});

test("resumable downloads never promote a partial staged file", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-partial-test-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const targetPath = path.join(tempDir, "target.bin");
  await fs.promises.writeFile(targetPath, Buffer.from("original"));
  const source = new PassThrough();
  const sftp = createFastSftp({ createReadStream() { return source; } });
  const client = {
    sftp,
    stat() { return Promise.resolve({ size: 6 }); },
    client: { sftp(callback) { callback(new Error("isolated channel unavailable")); } },
  };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const sender = createSender();
  const running = transferBridge.startTransfer(
    { sender },
    {
      transferId: "download-partial",
      sourcePath: "/tmp/source.bin",
      targetPath,
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: "source",
      totalBytes: 6,
      resumable: true,
    },
  );
  const readyDeadline = Date.now() + 1000;
  while (source.listenerCount("data") === 0 && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  source.end(Buffer.from("abc"));

  const result = await running;
  assert.match(result.error || "", /full source|size mismatch/i);
  assert.equal(await fs.promises.readFile(targetPath, "utf8"), "original");
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), false);
});

test("old-style transfers explicitly reject pause", async () => {
  transferBridge.init({ sftpClients: new Map() });
  assert.deepEqual(
    await transferBridge.pauseTransfer(null, { transferId: "missing" }),
    { success: false, reason: "Transfer is no longer active" },
  );
});

test("server-to-server upload resume uses its own checkpoint instead of overall progress", async (t) => {
  const transferId = `server-copy-${crypto.randomUUID()}`;
  const sourcePath = "/source/payload.bin";
  const targetPath = "/target/payload.bin";
  const payload = Buffer.from("abcdef");
  const localStage = tempDirBridge.getTransferTempFilePath(transferId, "payload.bin");
  await fs.promises.writeFile(localStage, payload);
  t.after(async () => { await fs.promises.unlink(localStage).catch(() => {}); });

  let remote = Buffer.alloc(0);
  let promoted = false;
  const targetSftp = createFastSftp({
    open(_path, flags, callback) {
      callback(null, Buffer.from("target-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      const chunk = buffer.subarray(offset, offset + length);
      if (remote.length < position) {
        remote = Buffer.concat([remote, Buffer.alloc(position - remote.length)]);
      }
      remote = Buffer.concat([
        remote.subarray(0, position),
        chunk,
        remote.subarray(position + chunk.length),
      ]);
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
  });
  const sourceClient = { sftp: createFastSftp({}), stat: async () => ({ size: payload.length }) };
  const targetClient = {
    __netcattySudoMode: true,
    sftp: targetSftp,
    stat: async () => ({ size: remote.length }),
    rename: async () => { promoted = true; },
    delete: async () => {},
  };
  transferBridge.init({ sftpClients: new Map([["source", sourceClient], ["target", targetClient]]) });

  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "sftp",
    targetType: "sftp",
    sourceSftpId: "source",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: true,
    resumeStage: "upload",
    checkpointBytes: payload.length / 2,
    downloadCheckpointBytes: payload.length,
    uploadCheckpointBytes: 0,
  });

  assert.equal(result.error, undefined, result.error);
  assert.deepEqual(remote, payload);
  assert.equal(promoted, true);
});

test("server-to-server staged upload does not promote after AbortSignal-only cancellation", async (t) => {
  const transferId = `server-copy-signal-cancel-${crypto.randomUUID()}`;
  const sourcePath = "/source/payload.bin";
  const targetPath = "/target/payload.bin";
  const payload = Buffer.from("abcdef");
  const localStage = tempDirBridge.getTransferTempFilePath(transferId, "payload.bin");
  await fs.promises.writeFile(localStage, payload);
  t.after(async () => { await fs.promises.unlink(localStage).catch(() => {}); });

  const controller = new AbortController();
  let remote = Buffer.alloc(0);
  let promoted = false;
  const removed = [];
  const targetSftp = createFastSftp({
    open(_path, _flags, callback) {
      callback(null, Buffer.from("target-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      const chunk = buffer.subarray(offset, offset + length);
      remote = Buffer.concat([
        remote.subarray(0, position),
        chunk,
        remote.subarray(position + chunk.length),
      ]);
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
      controller.abort();
    },
  });
  const sourceClient = { sftp: createFastSftp({}), stat: async () => ({ size: payload.length }) };
  const targetClient = {
    __netcattySudoMode: true,
    sftp: targetSftp,
    stat: async () => ({ size: remote.length }),
    rename: async () => { promoted = true; },
    delete: async (remotePath) => { removed.push(String(remotePath)); },
  };
  transferBridge.init({ sftpClients: new Map([["source", sourceClient], ["target", targetClient]]) });

  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "sftp",
    targetType: "sftp",
    sourceSftpId: "source",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: true,
    resumeStage: "upload",
    downloadCheckpointBytes: payload.length,
    uploadCheckpointBytes: 0,
    abortSignal: controller.signal,
  });

  assert.match(result.error || "", /cancel/i);
  assert.equal(promoted, false);
  assert.ok(removed.some((remotePath) => remotePath.includes(".netcatty-")));
});

test("server-to-server SCP fallback uses the followed symlink content size", async (t) => {
  const transferId = `scp-symlink-s2s-${crypto.randomUUID()}`;
  const sourcePath = "/source/link";
  const targetPath = "/target/copied.bin";
  const payload = Buffer.from("followed-symlink-target-content");
  const localStage = tempDirBridge.getTransferTempFilePath(transferId, path.basename(sourcePath));
  t.after(async () => { await fs.promises.unlink(localStage).catch(() => {}); });

  const sourceBackend = {
    async stat() {
      return { type: "symlink", isSymbolicLink: true, size: 4 };
    },
    async downloadFile(_remotePath, localPath, options = {}) {
      await fs.promises.writeFile(localPath, payload);
      const halfway = Math.floor(payload.length / 2);
      options.onProgress?.(halfway, payload.length);
      options.onProgress?.(payload.length, payload.length);
      return { fileSize: payload.length, transferred: payload.length };
    },
  };
  const remoteFiles = new Map([[targetPath, Buffer.from("old target")]]);
  let releaseFinalRename;
  let markFinalRenameStarted;
  const finalRenameStarted = new Promise((resolve) => { markFinalRenameStarted = resolve; });
  const missing = () => {
    const error = new Error("No such file");
    error.code = "ENOENT";
    return error;
  };
  const targetBackend = {
    async stat(remotePath) {
      if (!remoteFiles.has(remotePath)) throw missing();
      return { type: "file", isDirectory: false, size: remoteFiles.get(remotePath).length };
    },
    async mkdir() {},
    async uploadFile(localPath, remotePath) {
      remoteFiles.set(remotePath, await fs.promises.readFile(localPath));
      return true;
    },
    async rename(from, to) {
      if (!remoteFiles.has(from)) throw missing();
      if (from.includes(".netcatty-upload-") && to === targetPath) {
        markFinalRenameStarted();
        await new Promise((resolve) => { releaseFinalRename = resolve; });
      }
      remoteFiles.set(to, remoteFiles.get(from));
      remoteFiles.delete(from);
    },
    async remove(remotePath) {
      remoteFiles.delete(remotePath);
    },
    async chmod() {},
  };
  const sourceClient = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: sourceBackend,
  };
  const targetClient = {
    __netcattyFileProtocol: "scp",
    __netcattyScpBackend: targetBackend,
  };
  transferBridge.init({
    sftpClients: new Map([["source-scp", sourceClient], ["target-scp", targetClient]]),
  });

  const sender = createSender();
  const running = transferBridge.startTransfer({ sender }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "sftp",
    targetType: "sftp",
    sourceSftpId: "source-scp",
    targetSftpId: "target-scp",
    resumable: false,
    sameHost: false,
  });
  await finalRenameStarted;
  await transferBridge.cancelTransfer(null, { transferId });
  releaseFinalRename();
  const result = await running;

  assert.equal(result.error, undefined, result.error);
  assert.deepEqual(remoteFiles.get(targetPath), payload);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-upload-")), false);
  assert.equal([...remoteFiles.keys()].some((key) => key.includes(".netcatty-backup-")), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:cancelled"), false);
  assert.equal(sender.sent.some((entry) => entry.channel === "netcatty:transfer:complete"), true);
  const progress = sender.sent
    .filter((entry) => entry.channel === "netcatty:transfer:progress")
    .map((entry) => entry.payload);
  for (let index = 1; index < progress.length; index += 1) {
    assert.ok(
      progress[index].transferred >= progress[index - 1].transferred,
      `progress moved backwards: ${JSON.stringify(progress)}`,
    );
  }
  for (const entry of progress.slice(0, -1)) {
    assert.ok(
      entry.totalBytes === 0 || entry.transferred < entry.totalBytes,
      `progress completed before the transfer: ${JSON.stringify(progress)}`,
    );
  }
  assert.ok(
    progress.some((entry) => (
      entry.totalBytes === payload.length
      && entry.transferred === Math.floor(payload.length / 2)
    )),
    `missing completed-download midpoint: ${JSON.stringify(progress)}`,
  );
});

test("server-to-server concurrent failure does not complete via serial stream", async (t) => {
  const transferId = `server-copy-fail-closed-${crypto.randomUUID()}`;
  const sourcePath = "/source/payload.bin";
  const targetPath = "/target/payload.bin";
  const payload = Buffer.alloc(3 * 32 * 1024, 53);
  const localStage = tempDirBridge.getTransferTempFilePath(transferId, path.basename(sourcePath));
  await fs.promises.writeFile(localStage, payload);
  t.after(async () => { await fs.promises.unlink(localStage).catch(() => {}); });

  let secondWriteCallback = null;
  let thirdWriteCompleted = false;
  let secondWriteFailureScheduled = false;
  const scheduleSecondWriteFailure = () => {
    if (secondWriteFailureScheduled || !thirdWriteCompleted || !secondWriteCallback) return;
    secondWriteFailureScheduled = true;
    const callback = secondWriteCallback;
    secondWriteCallback = null;
    queueMicrotask(() => callback(new Error("second range failed")));
  };
  let remoteBytes = 0;
  let createWriteStreamCalls = 0;
  const fastSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(null, Buffer.from("remote-handle"));
    },
    write(_handle, _buffer, _offset, length, position, callback) {
      if (position === 32 * 1024) {
        secondWriteCallback = callback;
        scheduleSecondWriteFailure();
        return;
      }
      remoteBytes = Math.max(remoteBytes, position + length);
      callback(null);
      if (position === 2 * 32 * 1024) {
        thirdWriteCompleted = true;
        scheduleSecondWriteFailure();
      }
    },
    close(_handle, callback) {
      callback(null);
    },
    end() {},
  });
  const targetSftp = createFastSftp({
    open(_remotePath, _flags, callback) {
      callback(new Error("shared open also fails"));
    },
    createWriteStream() {
      createWriteStreamCalls += 1;
      throw new Error("serial WriteStream must not run");
    },
  });
  const sourceClient = {
    sftp: createFastSftp({}),
    stat: async () => ({ size: payload.length }),
  };
  const targetClient = {
    sftp: targetSftp,
    stat: async () => ({ size: remoteBytes }),
    rename: async () => {},
    delete: async () => {},
    client: {
      sftp(callback) {
        callback(null, fastSftp);
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([["source", sourceClient], ["target", targetClient]]) });

  const result = await transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath,
      targetPath,
      sourceType: "sftp",
      targetType: "sftp",
      sourceSftpId: "source",
      targetSftpId: "target",
      totalBytes: payload.length,
      resumable: true,
      resumeStage: "upload",
      downloadCheckpointBytes: payload.length,
      uploadCheckpointBytes: 0,
    },
  );

  assert.match(result.error || "", /pipelined upload failed|second range failed/i);
  assert.equal(createWriteStreamCalls, 0);
  // Local S2S temp (full download) must survive failed upload attempts.
  assert.equal(fs.statSync(localStage).size, payload.length);
});

test("upload recovery without a full source identity restarts from zero", async (t) => {
  // Simulates force-quit mid-upload: UI progress saved a high checkpoint, but
  // only a shorter prefix made it into the remote staged .part file.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-crash-resume-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const transferId = `crash-resume-${crypto.randomUUID()}`;
  const sourcePath = path.join(tempDir, "source.bin");
  const payload = Buffer.alloc(TRANSFER_CHUNK_SIZE * 2 + 19);
  for (let index = 0; index < payload.length; index += 1) payload[index] = index % 251;
  await fs.promises.writeFile(sourcePath, payload);

  // The durable prefix is deliberately not chunk-aligned. The first resumed
  // request must stop at the digest boundary, and the final bytes must match.
  let remote = Buffer.from(payload.subarray(0, 4));
  let promoted = false;
  let minWritePosition = Infinity;
  const concurrentSftp = createFastSftp({
    open(_path, flags, callback) {
      callback(null, Buffer.from("resume-handle"));
    },
    write(_handle, buffer, offset, length, position, callback) {
      minWritePosition = Math.min(minWritePosition, position);
      const chunk = buffer.subarray(offset, offset + length);
      if (remote.length < position) {
        remote = Buffer.concat([remote, Buffer.alloc(position - remote.length)]);
      }
      remote = Buffer.concat([
        remote.subarray(0, position),
        chunk,
        remote.subarray(position + chunk.length),
      ]);
      callback(null);
    },
    close(_handle, callback) {
      callback(null);
    },
    // Resume safety samples the durable remote prefix via createReadStream.
    createReadStream(_path, options = {}) {
      const start = options.start || 0;
      const end = options.end;
      const slice = end === undefined ? remote.subarray(start) : remote.subarray(start, end + 1);
      return Readable.from([slice]);
    },
    createWriteStream() {
      throw new Error("serial WriteStream must not run");
    },
  });
  const client = {
    __netcattySudoMode: true, // shared concurrent path only
    sftp: concurrentSftp,
    // First stats clamp checkpoint from claimed 8 down to durable remote size 4.
    stat: async () => ({ size: remote.length }),
    rename: async () => { promoted = true; },
    delete: async () => {},
  };
  transferBridge.init({ sftpClients: new Map([["target", client]]) });

  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath,
    targetPath: "/root/source.bin",
    sourceType: "local",
    targetType: "sftp",
    targetSftpId: "target",
    totalBytes: payload.length,
    resumable: true,
    checkpointBytes: 8, // ahead of durable remote size 4
  });

  assert.equal(result.error, undefined, result.error);
  assert.deepEqual(remote, payload);
  assert.equal(promoted, true);
  // A checkpoint without a full source digest cannot prove the source suffix
  // stayed unchanged across the crash, so the safe recovery is a full restart.
  assert.equal(minWritePosition, 0);
});

test("local resume after hard quit clamps checkpoint to durable staged file size", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-local-crash-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const transferId = `local-crash-${crypto.randomUUID()}`;
  const sourcePath = path.join(tempDir, "source.bin");
  const targetPath = path.join(tempDir, "target.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, "target.bin");
  await fs.promises.writeFile(sourcePath, Buffer.from("abcdef"));
  // Durable staged file is shorter than claimed checkpoint (crash / unflushed write).
  await fs.promises.writeFile(stagedPath, Buffer.from("ab"));
  t.after(async () => { await fs.promises.unlink(stagedPath).catch(() => {}); });

  transferBridge.init({ sftpClients: new Map() });
  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "local",
    targetType: "local",
    totalBytes: 6,
    resumable: true,
    checkpointBytes: 5,
  });

  assert.equal(result.error, undefined, result.error);
  assert.equal(await fs.promises.readFile(targetPath, "utf8"), "abcdef");
});

test("resume rejects a same-size temporary prefix that does not match the source", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-prefix-test-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const transferId = `prefix-${crypto.randomUUID()}`;
  const sourcePath = path.join(tempDir, "source.bin");
  const targetPath = path.join(tempDir, "target.bin");
  const stagedPath = tempDirBridge.getTransferTempFilePath(transferId, "target.bin");
  await fs.promises.writeFile(sourcePath, Buffer.from("abcdef"));
  await fs.promises.writeFile(stagedPath, Buffer.from("xyz"));
  t.after(async () => { await fs.promises.unlink(stagedPath).catch(() => {}); });

  transferBridge.init({ sftpClients: new Map() });
  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId,
    sourcePath,
    targetPath,
    sourceType: "local",
    targetType: "local",
    totalBytes: 6,
    resumable: true,
    checkpointBytes: 3,
    sourceFingerprint: `sha256:${crypto.createHash("sha256").update("abcdef").digest("hex")}`,
  });

  assert.match(result.error || "", /saved content does not match/i);
  assert.equal(await fs.promises.readFile(sourcePath, "utf8"), "abcdef");
});

test("bridge admission applies one global concurrency limit across callers", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-admission-test-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const firstSource = new PassThrough();
  const secondSource = new PassThrough();
  const sftp = createFastSftp({
    createReadStream(remotePath) {
      return remotePath === "/first" ? firstSource : secondSource;
    },
  });
  const client = { sftp, stat: async () => ({ size: 1 }) };
  transferBridge.init({ sftpClients: new Map([["source", client]]) });

  const start = (id, remotePath) => transferBridge.startTransfer({ sender: createSender() }, {
    transferId: id,
    sourcePath: remotePath,
    targetPath: path.join(tempDir, `${id}.bin`),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes: 1,
    resumable: true,
    globalConcurrency: 1,
  });
  const first = start("admission-first", "/first");
  assert.equal(
    await waitUntil(() => firstSource.listenerCount("data") > 0),
    true,
    "first admitted transfer did not start",
  );
  const second = start("admission-second", "/second");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(secondSource.listenerCount("data"), 0);
  firstSource.end(Buffer.from("a"));
  assert.equal((await first).error, undefined);
  assert.equal(
    await waitUntil(() => secondSource.listenerCount("data") > 0),
    true,
    "second admitted transfer did not start after the first completed",
  );
  secondSource.end(Buffer.from("b"));
  assert.equal((await second).error, undefined);
});

test("bridge admission gives different remote sessions independent concurrency", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-per-session-test-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const sourceA = new PassThrough();
  const sourceB = new PassThrough();
  const makeClient = (source) => ({
    sftp: createFastSftp({ createReadStream() { return source; } }),
    stat: async () => ({ size: 1 }),
  });
  transferBridge.init({ sftpClients: new Map([
    ["source-a", makeClient(sourceA)],
    ["source-b", makeClient(sourceB)],
  ]) });

  const start = (id, sourceSftpId, source) => transferBridge.startTransfer({ sender: createSender() }, {
    transferId: id,
    sourcePath: `/${id}`,
    targetPath: path.join(tempDir, `${id}.bin`),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId,
    totalBytes: 1,
    resumable: true,
    globalConcurrency: 1,
  }).finally(() => source.destroy());
  const first = start("per-session-a", "source-a", sourceA);
  const second = start("per-session-b", "source-b", sourceB);
  const deadline = Date.now() + 500;
  while ((sourceA.listenerCount("data") === 0 || sourceB.listenerCount("data") === 0) && Date.now() < deadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  const bothStarted = sourceA.listenerCount("data") > 0 && sourceB.listenerCount("data") > 0;
  sourceA.end(Buffer.from("a"));
  sourceB.end(Buffer.from("b"));
  await Promise.all([first, second]);
  assert.equal(bothStarted, true);
});

test("clearPendingCancel allows intentional same-id start after a pre-start cancel", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-clear-pending-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const source = new PassThrough();
  const sftp = createFastSftp({
    createReadStream() {
      return source;
    },
  });
  transferBridge.init({ sftpClients: new Map([["source", { sftp, stat: async () => ({ size: 1 }) }]]) });

  await transferBridge.cancelTransfer(null, { transferId: "retry-same-id" });
  transferBridge.clearPendingCancel("retry-same-id");
  const resultPromise = transferBridge.startTransfer({ sender: createSender() }, {
    transferId: "retry-same-id",
    sourcePath: "/remote",
    targetPath: path.join(tempDir, "out.bin"),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes: 1,
    skipAdmission: true,
  });
  while (source.listenerCount("data") === 0) await new Promise((resolve) => setImmediate(resolve));
  source.end(Buffer.from("a"));
  const result = await resultPromise;
  assert.equal(result.cancelled, undefined);
  assert.equal(result.error, undefined);
});

test("cancel before skipAdmission start rejects the transfer without writing", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-pending-cancel-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const source = new PassThrough();
  const sftp = createFastSftp({
    createReadStream() {
      return source;
    },
  });
  transferBridge.init({ sftpClients: new Map([["source", { sftp, stat: async () => ({ size: 1 }) }]]) });

  await transferBridge.cancelTransfer(null, { transferId: "pending-cancel-1" });
  const result = await transferBridge.startTransfer({ sender: createSender() }, {
    transferId: "pending-cancel-1",
    sourcePath: "/remote",
    targetPath: path.join(tempDir, "out.bin"),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes: 1,
    skipAdmission: true,
  });
  assert.equal(result.cancelled, true);
  assert.equal(source.listenerCount("data"), 0);
});

test("pre-start cancel latches stay hard bounded when cancelled work never starts", async (t) => {
  t.after(() => {
    for (let index = 0; index < 5_000; index += 1) {
      transferBridge.clearPendingCancel(`orphan-cancel-${index}`);
    }
  });
  for (let index = 0; index < 5_000; index += 1) {
    await transferBridge.cancelTransfer(null, { transferId: `orphan-cancel-${index}` });
  }
  assert.ok(
    transferBridge._getPendingCancelCountForTests() <= 4_096,
    `pending cancel latches grew to ${transferBridge._getPendingCancelCountForTests()}`,
  );
});

test("pausing a queued admission job preserves the payload checkpoint", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-queued-checkpoint-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const firstSource = new PassThrough();
  const secondSource = new PassThrough();
  const sftp = createFastSftp({
    createReadStream(remotePath) {
      return remotePath === "/first" ? firstSource : secondSource;
    },
  });
  transferBridge.init({ sftpClients: new Map([["source", { sftp, stat: async () => ({ size: 1 }) }]]) });
  const start = (id, remotePath, checkpointBytes) => transferBridge.startTransfer({ sender: createSender() }, {
    transferId: id,
    sourcePath: remotePath,
    targetPath: path.join(tempDir, `${id}.bin`),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes: 1,
    resumable: true,
    checkpointBytes,
    globalConcurrency: 1,
  });

  const first = start("queued-ckpt-first", "/first", 0);
  while (firstSource.listenerCount("data") === 0) await new Promise((resolve) => setImmediate(resolve));
  const second = start("queued-ckpt-second", "/second", 42);
  const paused = await transferBridge.pauseTransfer(null, { transferId: "queued-ckpt-second" });
  assert.equal(paused.success, true);
  assert.equal(paused.checkpointBytes, 42);
  assert.equal((await transferBridge.cancelTransfer(null, { transferId: "queued-ckpt-second" })).success, true);
  assert.equal((await second).cancelled, true);
  firstSource.end(Buffer.from("a"));
  assert.equal((await first).error, undefined);
});

test("queued admission jobs can be paused, resumed, prioritized, and cancelled before opening a stream", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-queued-controls-"));
  t.after(async () => { await fs.promises.rm(tempDir, { recursive: true, force: true }); });
  const firstSource = new PassThrough();
  const secondSource = new PassThrough();
  const sftp = createFastSftp({
    createReadStream(remotePath) {
      return remotePath === "/first" ? firstSource : secondSource;
    },
  });
  transferBridge.init({ sftpClients: new Map([["source", { sftp, stat: async () => ({ size: 1 }) }]]) });
  const start = (id, remotePath) => transferBridge.startTransfer({ sender: createSender() }, {
    transferId: id,
    sourcePath: remotePath,
    targetPath: path.join(tempDir, `${id}.bin`),
    sourceType: "sftp",
    targetType: "local",
    sourceSftpId: "source",
    totalBytes: 1,
    resumable: true,
    globalConcurrency: 1,
  });

  const first = start("queued-control-first", "/first");
  while (firstSource.listenerCount("data") === 0) await new Promise((resolve) => setImmediate(resolve));
  const second = start("queued-control-second", "/second");
  assert.equal((await transferBridge.pauseTransfer(null, { transferId: "queued-control-second" })).success, true);
  assert.equal(secondSource.listenerCount("data"), 0);
  assert.equal((await transferBridge.resumeTransfer(null, { transferId: "queued-control-second" })).success, true);
  assert.equal((await transferBridge.prioritizeTransfer(null, { transferId: "queued-control-second" })).success, true);
  assert.equal((await transferBridge.cancelTransfer(null, { transferId: "queued-control-second" })).success, true);
  assert.equal((await second).cancelled, true);
  firstSource.end(Buffer.from("a"));
  assert.equal((await first).error, undefined);
  assert.equal(secondSource.listenerCount("data"), 0);
});

test("transfer session leases hold SFTP ids across soft-close until release", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  let hardCloseCalls = 0;
  const sftpBridge = require("./sftpBridge.cjs");
  const originalClose = sftpBridge.closeSftp;
  sftpBridge.closeSftp = async (_event, payload) => {
    if (payload?.force) {
      hardCloseCalls += 1;
      sftpTransferSessionLeaseStore.clear(payload.sftpId);
      return { success: true, deferred: false };
    }
    if (sftpTransferSessionLeaseStore.markSoftClosed(payload.sftpId)) {
      return {
        success: true,
        deferred: true,
        leaseCount: sftpTransferSessionLeaseStore.getLeaseCount(payload.sftpId),
      };
    }
    return { success: true, deferred: false };
  };
  t.after(() => {
    sftpBridge.closeSftp = originalClose;
  });

  assert.deepEqual(
    transferBridge.listTransferSftpIds({ sourceSftpId: "s1", targetSftpId: "s2", sourceHostId: "h" }),
    ["s1", "s2"],
  );

  // Hold two transfers on s1 before soft-close (re-acquire after soft-close
  // would clear the deferred flag by design).
  transferBridge.acquireTransferSessionLeases("xfer-1", {
    sourceSftpId: "s1",
    targetSftpId: "s2",
  });
  transferBridge.acquireTransferSessionLeases("xfer-2", { sourceSftpId: "s1" });
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount("s1"), 2);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount("s2"), 1);

  const soft = await sftpBridge.closeSftp(null, { sftpId: "s1" });
  assert.equal(soft.deferred, true);
  assert.equal(sftpTransferSessionLeaseStore.isSoftClosed("s1"), true);
  assert.equal(hardCloseCalls, 0);

  transferBridge.releaseTransferSessionLeases("xfer-1", ["s1", "s2"]);
  assert.equal(hardCloseCalls, 0);
  assert.equal(sftpTransferSessionLeaseStore.isHeld("s1"), true);
  assert.equal(sftpTransferSessionLeaseStore.isHeld("s2"), false);

  // Last release on soft-closed session triggers hard close.
  transferBridge.releaseTransferSessionLeases("xfer-2", ["s1"]);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(hardCloseCalls, 1);
  assert.equal(sftpTransferSessionLeaseStore.isHeld("s1"), false);
});

test("a directory pool lease survives terminal close between child files", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  let hardCloseCalls = 0;
  const sftpBridge = require("./sftpBridge.cjs");
  const originalClose = sftpBridge.closeSftp;
  sftpBridge.closeSftp = async (_event, payload) => {
    if (payload?.force) {
      hardCloseCalls += 1;
      sftpTransferSessionLeaseStore.clear(payload.sftpId);
      return { success: true, deferred: false };
    }
    if (sftpTransferSessionLeaseStore.markSoftClosed(payload.sftpId)) {
      return {
        success: true,
        deferred: true,
        leaseCount: sftpTransferSessionLeaseStore.getLeaseCount(payload.sftpId),
      };
    }
    return { success: true, deferred: false };
  };
  t.after(() => {
    sftpBridge.closeSftp = originalClose;
  });

  transferBridge.init({ sftpClients: new Map([["folder-sftp", {}]]) });
  assert.deepEqual(
    transferBridge.retainSftpTransferSession(null, {
      sftpId: "folder-sftp",
      leaseId: "pool:folder-sftp",
    }),
    { success: true },
  );
  transferBridge.acquireTransferSessionLeases("child-1", { targetSftpId: "folder-sftp" });

  const soft = await sftpBridge.closeSftp(null, { sftpId: "folder-sftp" });
  assert.equal(soft.deferred, true);
  transferBridge.releaseTransferSessionLeases("child-1", ["folder-sftp"]);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(hardCloseCalls, 0, "the directory pool must keep the session alive between files");

  assert.doesNotThrow(() => {
    transferBridge.acquireTransferSessionLeases("child-2", { targetSftpId: "folder-sftp" });
  });
  transferBridge.releaseTransferSessionLeases("child-2", ["folder-sftp"]);
  assert.deepEqual(
    transferBridge.releaseSftpTransferSession(null, {
      sftpId: "folder-sftp",
      leaseId: "pool:folder-sftp",
    }),
    { success: true },
  );
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(hardCloseCalls, 1, "the deferred close should run only after the pool releases");
});

test("directory child lifecycle events preserve their parent id", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-child-event-"));
  t.after(async () => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const sourcePath = path.join(tempDir, "child.txt");
  const targetPath = path.join(tempDir, "copied.txt");
  await fs.promises.writeFile(sourcePath, "child");
  transferBridge.init({ sftpClients: new Map() });
  const sender = createSender();

  const result = await transferBridge.startTransfer({ sender }, {
    transferId: "folder-child-event",
    parentTaskId: "folder-root-event",
    sourcePath,
    targetPath,
    sourceType: "local",
    targetType: "local",
    totalBytes: 5,
    resumable: false,
    skipAdmission: true,
  });

  assert.equal(result.error, undefined);
  const started = sender.sent.find((entry) => entry.channel === "netcatty:transfer:started");
  assert.equal(started?.payload?.parentTaskId, "folder-root-event");
});

test("a committed soft-close never lends the SFTP client again while client.end is pending", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  const sftpBridge = require("./sftpBridge.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  let releaseEnd;
  const endGate = new Promise((resolve) => { releaseEnd = resolve; });
  let markEndStarted;
  const endStarted = new Promise((resolve) => { markEndStarted = resolve; });
  let endCalls = 0;
  const client = {
    async end() {
      endCalls += 1;
      markEndStarted();
      await endGate;
    },
  };
  const sftpClients = new Map([["sftp-soft-close-race", client]]);
  transferBridge.init({ sftpClients });
  sftpBridge.init({ sftpClients, sessions: new Map(), electronModule: {} });
  t.after(() => {
    releaseEnd();
    sftpTransferSessionLeaseStore.resetForTests();
    sftpClients.clear();
  });

  transferBridge.acquireTransferSessionLeases("directory-child-1", {
    sourceSftpId: "sftp-soft-close-race",
  });
  const softClose = await sftpBridge.closeSftp(null, { sftpId: "sftp-soft-close-race" });
  assert.equal(softClose.deferred, true);

  transferBridge.releaseTransferSessionLeases("directory-child-1", ["sftp-soft-close-race"]);
  await endStarted;

  assert.throws(
    () => transferBridge.acquireTransferSessionLeases("directory-child-2", {
      sourceSftpId: "sftp-soft-close-race",
    }),
    /closing|closed/i,
  );
  assert.equal(sftpTransferSessionLeaseStore.isHeld("sftp-soft-close-race"), false);

  releaseEnd();
  const deadline = Date.now() + 1_000;
  while (sftpClients.has("sftp-soft-close-race")) {
    if (Date.now() >= deadline) throw new Error("Timed out waiting for committed SFTP close");
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(endCalls, 1);
});

test("a direct panel close never lends the SFTP client while client.end is pending", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  const sftpBridge = require("./sftpBridge.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  let releaseEnd;
  const endGate = new Promise((resolve) => { releaseEnd = resolve; });
  let markEndStarted;
  const endStarted = new Promise((resolve) => { markEndStarted = resolve; });
  const client = {
    async end() {
      markEndStarted();
      await endGate;
    },
  };
  const sftpClients = new Map([["sftp-direct-close-race", client]]);
  transferBridge.init({ sftpClients });
  sftpBridge.init({ sftpClients, sessions: new Map(), electronModule: {} });
  t.after(() => {
    releaseEnd();
    sftpTransferSessionLeaseStore.resetForTests();
    sftpClients.clear();
  });

  const closePromise = sftpBridge.closeSftp(null, { sftpId: "sftp-direct-close-race" });
  await endStarted;

  assert.throws(
    () => transferBridge.acquireTransferSessionLeases("new-transfer", {
      sourceSftpId: "sftp-direct-close-race",
    }),
    /closing|closed/i,
  );

  releaseEnd();
  const closeResult = await closePromise;
  assert.equal(closeResult.deferred, false);
  assert.equal(sftpClients.has("sftp-direct-close-race"), false);
  assert.equal(sftpTransferSessionLeaseStore.isHeld("sftp-direct-close-race"), false);
});

test("a transfer rejected by a committed SFTP close leaves no active transfer entry", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  const sftpId = "sftp-rejected-before-lease";
  const closeToken = sftpTransferSessionLeaseStore.beginHardClose(sftpId);
  assert.equal(sftpTransferSessionLeaseStore.commitHardClose(sftpId, closeToken), true);
  transferBridge.init({ sftpClients: new Map([[sftpId, {}]]) });
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  await assert.rejects(
    transferBridge.startTransfer({ sender: createSender() }, {
      transferId: "rejected-before-lease",
      sourcePath: "/remote/file.bin",
      targetPath: "/tmp/rejected-before-lease.bin",
      sourceType: "sftp",
      targetType: "local",
      sourceSftpId: sftpId,
      totalBytes: 1,
      skipAdmission: true,
    }),
    /closing|closed/i,
  );
  assert.equal(transferBridge._getActiveTransferCountForTests(), 0);
});

test("same-host directory copy rejected by a committed close leaves no lease or active transfer", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  const sftpId = "same-host-directory-closing";
  sftpTransferSessionLeaseStore.resetForTests();
  const closeToken = sftpTransferSessionLeaseStore.beginHardClose(sftpId);
  assert.equal(sftpTransferSessionLeaseStore.commitHardClose(sftpId, closeToken), true);
  transferBridge.init({ sftpClients: new Map([[sftpId, {}]]) });
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  await assert.rejects(
    transferBridge.sameHostCopyDirectory(
      { sender: createSender() },
      {
        transferId: "same-host-directory-rejected",
        sftpId,
        sourcePath: "/source",
        targetPath: "/target",
        encoding: "utf-8",
      },
    ),
    /closing|closed/i,
  );

  assert.equal(transferBridge._getActiveTransferCountForTests(), 0);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount(sftpId), 0);
});

test("same-host directory copy cancellation aborts cp and releases its lease", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  const sftpId = "same-host-directory-cancel";
  let markExecOpened;
  const execOpened = new Promise((resolve) => { markExecOpened = resolve; });
  const stream = new EventEmitter();
  stream.stderr = new EventEmitter();
  stream.close = () => {};
  stream.end = () => {};
  stream.destroy = () => {};
  const client = {
    client: {
      exec(_command, callback) {
        callback(null, stream);
        markExecOpened();
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([[sftpId, client]]) });

  const running = transferBridge.sameHostCopyDirectory(
    { sender: createSender() },
    {
      transferId: "same-host-directory-cancelled",
      sftpId,
      sourcePath: "/source",
      targetPath: "/target",
      encoding: "utf-8",
    },
  );
  await execOpened;
  assert.equal(transferBridge._getActiveTransferCountForTests(), 1);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount(sftpId), 1);

  await transferBridge.cancelTransfer(null, { transferId: "same-host-directory-cancelled" });
  await assert.rejects(running, /cancel/i);

  assert.equal(transferBridge._getActiveTransferCountForTests(), 0);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount(sftpId), 0);
});

test("a pre-start cancellation prevents same-host directory registration and lease acquisition", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  const sftpId = "same-host-directory-pre-cancel";
  let execCalls = 0;
  transferBridge.init({
    sftpClients: new Map([[sftpId, {
      client: {
        exec() { execCalls += 1; },
      },
    }]]),
  });
  const transferId = "same-host-directory-pre-cancelled";
  await transferBridge.cancelTransfer(null, { transferId });

  const result = await transferBridge.sameHostCopyDirectory(
    { sender: createSender() },
    {
      transferId,
      sftpId,
      sourcePath: "/source",
      targetPath: "/target",
      encoding: "utf-8",
    },
  );

  assert.equal(result.cancelled, true);
  assert.equal(execCalls, 0);
  assert.equal(transferBridge._getActiveTransferCountForTests(), 0);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount(sftpId), 0);
  assert.equal(transferBridge._getPendingCancelCountForTests(), 0);
});

test("same-host file cp cancellation releases the shared session lease", async (t) => {
  const {
    sftpTransferSessionLeaseStore,
  } = require("./sftpTransferSessionLease.cjs");
  sftpTransferSessionLeaseStore.resetForTests();
  t.after(() => sftpTransferSessionLeaseStore.resetForTests());

  const sftpId = "same-host-file-cancel";
  let markExecOpened;
  const execOpened = new Promise((resolve) => { markExecOpened = resolve; });
  const stream = new EventEmitter();
  stream.stderr = new EventEmitter();
  stream.close = () => {};
  stream.end = () => {};
  stream.destroy = () => {};
  const client = {
    client: {
      exec(_command, callback) {
        callback(null, stream);
        markExecOpened();
      },
    },
  };
  transferBridge.init({ sftpClients: new Map([[sftpId, client]]) });
  const transferId = "same-host-file-cancelled";
  const running = transferBridge.startTransfer(
    { sender: createSender() },
    {
      transferId,
      sourcePath: "/source/file.bin",
      targetPath: "/target/file.bin",
      sourceType: "sftp",
      targetType: "sftp",
      sourceSftpId: sftpId,
      targetSftpId: sftpId,
      totalBytes: 1,
      sameHost: true,
      skipAdmission: true,
    },
  );
  await execOpened;
  assert.equal(transferBridge._getActiveTransferCountForTests(), 1);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount(sftpId), 1);

  await transferBridge.cancelTransfer(null, { transferId });
  const result = await running;
  assert.equal(result.error, "Transfer cancelled");
  assert.equal(transferBridge._getActiveTransferCountForTests(), 0);
  assert.equal(sftpTransferSessionLeaseStore.getLeaseCount(sftpId), 0);
});

test("local promotion restores concurrent replacement moved into the backup", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-local-promo-backup-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const stagedPath = path.join(tempDir, "download.staged");
  const targetPath = path.join(tempDir, "target.bin");
  const original = Buffer.from("original-target-content");
  const replacement = Buffer.from("concurrent-replacement");
  const downloaded = Buffer.from("downloaded-replacement");
  await fs.promises.writeFile(targetPath, original);
  await fs.promises.writeFile(stagedPath, downloaded);

  const originalStat = await fs.promises.lstat(targetPath);
  const expectedStable = transferBridge._stableLocalFileIdentityForTests(originalStat);

  // validateTarget claims the original identity, but a concurrent process has
  // already replaced the live path. rename then moves the wrong file to backup.
  await assert.rejects(
    () => transferBridge._promoteLocalTransferForTests(stagedPath, targetPath, {
      async validateTarget() {
        await fs.promises.unlink(targetPath);
        await fs.promises.writeFile(targetPath, replacement);
        return {
          // Keep existingMode null so promotion does not re-validate after chmod.
          existingMode: null,
          stableIdentity: expectedStable,
          targetIdentity: [
            originalStat.dev,
            originalStat.ino,
            originalStat.size,
            originalStat.mtimeMs,
            originalStat.ctimeMs,
          ].join(":"),
        };
      },
    }),
    /changed during replacement/i,
  );

  // Replacement must still be at the target (restored from backup), download not published.
  assert.deepEqual(await fs.promises.readFile(targetPath), replacement);
  assert.equal(
    (await fs.promises.readdir(tempDir)).some((name) => name.includes(".backup")),
    false,
  );
});

test("local promotion does not restore mismatched backup over a recreated target", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-local-promo-mismatch-recreate-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const stagedPath = path.join(tempDir, "download.staged");
  const targetPath = path.join(tempDir, "target.bin");
  const original = Buffer.from("original-target-content");
  const concurrent = Buffer.from("recreated-during-mismatch");
  const downloaded = Buffer.from("downloaded-replacement");
  await fs.promises.writeFile(targetPath, original);
  await fs.promises.writeFile(stagedPath, downloaded);
  // Claim a stable identity that will not match whatever lands in backup.
  const fakeStable = "999:999:999";

  const originalRename = fs.promises.rename.bind(fs.promises);
  let injected = false;
  fs.promises.rename = async (from, to) => {
    const result = await originalRename(from, to);
    if (!injected && path.resolve(from) === path.resolve(targetPath) && String(to).includes(".backup")) {
      injected = true;
      // Recreate target after backup move; mismatch branch must not restore over it.
      await fs.promises.writeFile(targetPath, concurrent);
    }
    return result;
  };
  t.after(() => {
    fs.promises.rename = originalRename;
  });

  await assert.rejects(
    () => transferBridge._promoteLocalTransferForTests(stagedPath, targetPath, {
      async validateTarget() {
        return {
          existingMode: null,
          stableIdentity: fakeStable,
          targetIdentity: `${fakeStable}:0:0`,
        };
      },
    }),
    /changed during replacement/i,
  );

  assert.deepEqual(await fs.promises.readFile(targetPath), concurrent);
  const backupName = (await fs.promises.readdir(tempDir)).find((name) => name.includes(".backup"));
  assert.ok(backupName);
  assert.deepEqual(await fs.promises.readFile(path.join(tempDir, backupName)), original);
});

test("local promotion does not clobber a target recreated after backup", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-local-promo-recreate-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const stagedPath = path.join(tempDir, "download.staged");
  const targetPath = path.join(tempDir, "target.bin");
  const original = Buffer.from("original-target-content");
  const concurrent = Buffer.from("recreated-after-backup");
  const downloaded = Buffer.from("downloaded-replacement");
  await fs.promises.writeFile(targetPath, original);
  await fs.promises.writeFile(stagedPath, downloaded);
  const originalStat = await fs.promises.lstat(targetPath);

  const originalRename = fs.promises.rename.bind(fs.promises);
  let injected = false;
  fs.promises.rename = async (from, to) => {
    const result = await originalRename(from, to);
    if (!injected && path.resolve(from) === path.resolve(targetPath) && String(to).includes(".backup")) {
      injected = true;
      await fs.promises.writeFile(targetPath, concurrent);
    }
    return result;
  };
  t.after(() => {
    fs.promises.rename = originalRename;
  });

  await assert.rejects(
    () => transferBridge._promoteLocalTransferForTests(stagedPath, targetPath, {
      async validateTarget() {
        return {
          existingMode: null,
          stableIdentity: transferBridge._stableLocalFileIdentityForTests(originalStat),
          targetIdentity: [
            originalStat.dev,
            originalStat.ino,
            originalStat.size,
            originalStat.mtimeMs,
            originalStat.ctimeMs,
          ].join(":"),
        };
      },
    }),
    /changed during replacement/i,
  );

  assert.deepEqual(await fs.promises.readFile(targetPath), concurrent);
  // Original preserved in a backup artifact for recovery.
  const backupName = (await fs.promises.readdir(tempDir)).find((name) => name.includes(".backup"));
  assert.ok(backupName);
  assert.deepEqual(await fs.promises.readFile(path.join(tempDir, backupName)), original);
});

test("local promotion rollback does not clobber a concurrent post-publish target", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-local-promo-postpub-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const stagedPath = path.join(tempDir, "download.staged");
  const targetPath = path.join(tempDir, "target.bin");
  const original = Buffer.from("original-target-content");
  const concurrent = Buffer.from("post-publish-concurrent");
  const downloaded = Buffer.from("downloaded-replacement");
  await fs.promises.writeFile(targetPath, original);
  await fs.promises.writeFile(stagedPath, downloaded);
  const originalStat = await fs.promises.lstat(targetPath);

  await assert.rejects(
    () => transferBridge._promoteLocalTransferForTests(stagedPath, targetPath, {
      async validateTarget() {
        return {
          existingMode: null,
          stableIdentity: transferBridge._stableLocalFileIdentityForTests(originalStat),
          targetIdentity: [
            originalStat.dev,
            originalStat.ino,
            originalStat.size,
            originalStat.mtimeMs,
            originalStat.ctimeMs,
          ].join(":"),
        };
      },
      assertNotCancelled() {
        // After ready is published onto targetPath, simulate concurrent replace + cancel.
        try {
          if (fs.readFileSync(targetPath).equals(downloaded)) {
            fs.writeFileSync(targetPath, concurrent);
            throw new Error("Transfer cancelled");
          }
        } catch (err) {
          if (String(err.message || err).includes("cancelled")) throw err;
        }
      },
    }),
    /cancelled/i,
  );

  assert.deepEqual(await fs.promises.readFile(targetPath), concurrent);
  const backupName = (await fs.promises.readdir(tempDir)).find((name) => name.includes(".backup"));
  assert.ok(backupName, "original should remain in backup");
  assert.deepEqual(await fs.promises.readFile(path.join(tempDir, backupName)), original);
});

test("local promotion succeeds when backup still matches validated identity", async (t) => {
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-local-promo-ok-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const stagedPath = path.join(tempDir, "download.staged");
  const targetPath = path.join(tempDir, "target.bin");
  await fs.promises.writeFile(targetPath, Buffer.from("old"));
  await fs.promises.writeFile(stagedPath, Buffer.from("new"));
  const originalStat = await fs.promises.lstat(targetPath);

  await transferBridge._promoteLocalTransferForTests(stagedPath, targetPath, {
    async validateTarget() {
      const latest = await fs.promises.lstat(targetPath);
      return {
        existingMode: latest.mode & 0o7777,
        stableIdentity: transferBridge._stableLocalFileIdentityForTests(latest),
        targetIdentity: [
          latest.dev,
          latest.ino,
          latest.size,
          latest.mtimeMs,
          latest.ctimeMs,
        ].join(":"),
      };
    },
  });

  assert.deepEqual(await fs.promises.readFile(targetPath), Buffer.from("new"));
  assert.notEqual(
    transferBridge._stableLocalFileIdentityForTests(await fs.promises.lstat(targetPath)),
    transferBridge._stableLocalFileIdentityForTests(originalStat),
  );
});
