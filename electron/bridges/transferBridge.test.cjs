const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");
const { EventEmitter } = require("node:events");
const { PassThrough, Readable, Writable } = require("node:stream");

const transferBridge = require("./transferBridge.cjs");
const sftpBridge = require("./sftpBridge.cjs");
const tempDirBridge = require("./tempDirBridge.cjs");
const {
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
    lstat(_remotePath, callback) {
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
    async stat() {
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
    lstat(_remotePath, callback) {
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
    stat: async () => ({ size: remote.length }),
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

test("server-to-server resume cancellation settles while source prefix verification is stalled", async (t) => {
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

  assert.deepEqual(
    await transferBridge.resumeTransfer(null, { transferId: "upload-fast-paused" }),
    { success: true },
  );
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
  // Soft drain is ~300ms; allow headroom without waiting multi-second full drain.
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
  assert.deepEqual(
    await resuming,
    { success: true },
  );
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
  assert.deepEqual(
    await transferBridge.resumeTransfer(null, { transferId: "pause-resume-race" }),
    { success: true },
  );
  assert.deepEqual(await pausing, {
    success: false,
    reason: "Pause was superseded by resume",
  });

  finishWrite();
  assert.equal((await running).error, undefined);
});

test("pause stores a lightweight source identity without full-file hashing", async (t) => {
  // Pause must not await full-file SHA-256 (or start a post-pause background
  // full read). A size+mtime meta fingerprint is durable for resume and cheap.
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

  const originalCreateReadStream = fs.createReadStream;
  let fullHashStreamOpened = false;
  fs.createReadStream = (filePath, options) => {
    // Full-file fingerprint uses createReadStream(path) with no options.
    if (filePath === localPath && !options) {
      fullHashStreamOpened = true;
      return new Readable({ read() {} });
    }
    return originalCreateReadStream(filePath, options);
  };

  let running;
  try {
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
    assert.equal(fullHashStreamOpened, false, "pause must not open a full-file hash stream");
    assert.match(paused.sourceFingerprint || "", /^meta:\d+:\d+$/);
    assert.ok(
      sender.sent.some((entry) => (
        entry.channel === "netcatty:transfer:progress"
        && entry.payload.sourceFingerprint === paused.sourceFingerprint
      )),
      "pause identity must be published for transfer-center persistence",
    );

    assert.deepEqual(
      await transferBridge.resumeTransfer(null, { transferId: "late-pause-race" }),
      { success: true },
    );
  } finally {
    fs.createReadStream = originalCreateReadStream;
  }

  assert.equal((await running).error, undefined);
});

test("remote pause identity reads modifyTime from session-backed stat", async (t) => {
  // Session-backed client.stat() returns modifyTime (ms), not mtime/mtimeMs.
  // Ignoring it collapses every remote identity to meta:<size>:0 and lets a
  // same-size rewrite with an unchanged 256 KiB prefix resume unsafely.
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-transfer-modifytime-id-"));
  t.after(async () => {
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  });

  const payload = Buffer.from("abcdef");
  const modifyTimeMs = 1_700_000_000_000;
  let currentModifyTime = modifyTimeMs;
  const source = new PassThrough();
  let readStreamCalls = 0;
  const client = {
    sftp: createFastSftp({
      createReadStream() {
        readStreamCalls += 1;
        return readStreamCalls === 1 ? source : Readable.from(payload);
      },
    }),
    stat() {
      return Promise.resolve({ size: payload.length, modifyTime: currentModifyTime });
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

  const readyDeadline = Date.now() + 1000;
  while (source.listenerCount("data") === 0 && Date.now() < readyDeadline) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.ok(source.listenerCount("data") > 0);
  source.write(payload.subarray(0, 3));
  await new Promise((resolve) => setImmediate(resolve));

  const paused = await transferBridge.pauseTransfer(null, { transferId: "download-modifytime-id" });
  assert.equal(paused.success, true);
  assert.equal(paused.sourceFingerprint, `meta:${payload.length}:${modifyTimeMs}`);

  await transferBridge.cancelTransfer(null, { transferId: "download-modifytime-id" });
  assert.match((await running).error || "", /cancel/i);

  currentModifyTime = modifyTimeMs + 1000;
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
      sourceFingerprint: paused.sourceFingerprint,
    },
  );
  assert.match(restarted.error || "", /source file has changed/i);
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

  assert.deepEqual(
    await transferBridge.resumeTransfer(null, { transferId: "download-fast-paused" }),
    { success: true },
  );
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

  source.write(Buffer.from("def"));
  await new Promise((resolve) => setImmediate(resolve));
  const pausedAgain = await transferBridge.pauseTransfer(null, { transferId: "download-paused" });
  assert.equal(pausedAgain.success, true);
  assert.equal(pausedAgain.checkpointBytes, 3);
  assert.equal(pausedAgain.resumeStage, "direct");

  assert.deepEqual(await transferBridge.resumeTransfer(null, { transferId: "download-paused" }), { success: true });
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

  assert.deepEqual(
    await transferBridge.resumeTransfer(null, { transferId: "local-pipe-pause" }),
    { success: true },
  );
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

  assert.deepEqual(
    await transferBridge.resumeTransfer(null, { transferId: "local-double-resume" }),
    { success: true },
  );
  assert.deepEqual(
    await transferBridge.resumeTransfer(null, { transferId: "local-double-resume" }),
    { success: true },
  );
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

test("upload resume after hard quit clamps checkpoint to durable remote .part size", async (t) => {
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
  // Resume must not rewrite the durable prefix from offset 0.
  assert.ok(minWritePosition >= 4, `expected resume from >=4, got ${minWritePosition}`);
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
  while (firstSource.listenerCount("data") === 0) await new Promise((resolve) => setImmediate(resolve));
  const second = start("admission-second", "/second");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(secondSource.listenerCount("data"), 0);
  firstSource.end(Buffer.from("a"));
  assert.equal((await first).error, undefined);
  while (secondSource.listenerCount("data") === 0) await new Promise((resolve) => setImmediate(resolve));
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
