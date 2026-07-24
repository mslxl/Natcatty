/**
 * Drive the shipped downloadSftpToLocal / uploadLocalToSftp SCP branches with
 * AbortSignal — the AI/MCP transfer path must cancel mid-flight, not only
 * throwIfAborted before/after.
 */
"use strict";

const { describe, it, beforeEach, afterEach } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { EventEmitter } = require("node:events");

const sftpBridge = require("../sftpBridge.cjs");
const { createScpBackend } = require("./scpBackend.cjs");

function createMockStream() {
  const ee = new EventEmitter();
  ee.writable = true;
  ee.readable = true;
  ee.stderr = new EventEmitter();
  ee.write = (buf, cb) => {
    if (typeof cb === "function") cb();
    return true;
  };
  ee.end = (cb) => { if (typeof cb === "function") cb(); };
  ee.close = () => ee.emit("close");
  ee.destroy = () => ee.emit("close");
  return ee;
}

describe("AI/MCP SCP transfer abort on shipped download/upload entry points", () => {
  let tmpDir;
  let sftpClients;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-scp-ai-abort-"));
    sftpClients = new Map();
    sftpBridge.init({
      electronModule: {},
      sessions: new Map(),
      sftpClients,
    });
  });

  afterEach(() => {
    try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch { /* ignore */ }
  });

  function registerScpClient(id, { hangOnStream = true } = {}) {
    const backend = createScpBackend({
      exec: async () => ({ stdout: "", stderr: "", code: 0 }),
      execStream: async () => {
        const stream = createMockStream();
        if (!hangOnStream) {
          // ready ACK immediately for success paths (not used in abort tests)
          setImmediate(() => stream.emit("data", Buffer.from([0])));
        }
        // hang: never ACK so waitForAck blocks until cancel
        return stream;
      },
    });
    backend.stat = async () => ({ type: "file", isDirectory: false, size: 256 });
    const client = {
      client: { exec: () => {} },
      sftp: null,
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
      async end() {},
    };
    sftpClients.set(id, client);
    return client;
  }

  it("downloadSftpToLocal rejects when AbortSignal fires mid-SCP download", async () => {
    registerScpClient("scp-dl");
    const controller = new AbortController();
    const localPath = path.join(tmpDir, "out.bin");
    const original = Buffer.from("existing-local-content");
    fs.writeFileSync(localPath, original);
    const promise = sftpBridge.downloadSftpToLocal(null, {
      sftpId: "scp-dl",
      remotePath: "/remote/file.bin",
      localPath,
      abortSignal: controller.signal,
    });
    await new Promise((r) => setTimeout(r, 40));
    controller.abort();
    await assert.rejects(() => promise, /cancel|abort/i);
    assert.deepEqual(fs.readFileSync(localPath), original);
  });

  it("downloadSftpToLocal preserves the destination when cancellation arrives after SCP download", async () => {
    const controller = new AbortController();
    const downloaded = Buffer.from("new-downloaded-content");
    const backend = {
      async stat() {
        return { type: "file", isDirectory: false, size: downloaded.length };
      },
      async downloadFile(_remotePath, localPath) {
        await fs.promises.writeFile(localPath, downloaded);
        controller.abort();
      },
    };
    sftpClients.set("scp-late-abort", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "late-abort.bin");
    const original = Buffer.from("existing-local-content");
    fs.writeFileSync(localPath, original);

    await assert.rejects(
      () => sftpBridge.downloadSftpToLocal(null, {
        sftpId: "scp-late-abort",
        remotePath: "/remote/file.bin",
        localPath,
        abortSignal: controller.signal,
      }),
      /cancel|abort/i,
    );
    assert.deepEqual(fs.readFileSync(localPath), original);
  });

  it("downloadSftpToLocal uses the SCP header size when downloading through a symlink", async () => {
    const downloaded = Buffer.from("target-content-is-longer-than-link");
    const backend = {
      async stat() {
        return { type: "symlink", isSymbolicLink: true, size: 4 };
      },
      async downloadFile(_remotePath, localPath) {
        await fs.promises.writeFile(localPath, downloaded);
        return { fileSize: downloaded.length, transferred: downloaded.length };
      },
    };
    sftpClients.set("scp-symlink-download", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "symlink-download.bin");

    const result = await sftpBridge.downloadSftpToLocal(null, {
      sftpId: "scp-symlink-download",
      remotePath: "/remote/link",
      localPath,
    });

    assert.equal(result.success, true);
    assert.deepEqual(fs.readFileSync(localPath), downloaded);
  });

  it("downloadSftpToLocal restores the destination when cancellation arrives during promotion", async (t) => {
    const controller = new AbortController();
    const downloaded = Buffer.from("new-downloaded-content");
    const backend = {
      async stat() {
        return { type: "file", isDirectory: false, size: downloaded.length };
      },
      async downloadFile(_remotePath, localPath) {
        await fs.promises.writeFile(localPath, downloaded);
      },
    };
    sftpClients.set("scp-promotion-abort", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "promotion-abort.bin");
    const original = Buffer.from("existing-local-content");
    fs.writeFileSync(localPath, original);

    const originalRename = fs.promises.rename;
    let renameCalls = 0;
    fs.promises.rename = async (...args) => {
      await originalRename(...args);
      renameCalls += 1;
      if (renameCalls === 2) controller.abort();
    };
    t.after(() => { fs.promises.rename = originalRename; });

    await assert.rejects(
      () => sftpBridge.downloadSftpToLocal(null, {
        sftpId: "scp-promotion-abort",
        remotePath: "/remote/file.bin",
        localPath,
        abortSignal: controller.signal,
      }),
      /cancel|abort/i,
    );
    assert.equal(renameCalls >= 3, true, "the backup should be restored after cancellation");
    assert.deepEqual(fs.readFileSync(localPath), original);
  });

  it("downloadSftpToLocal rolls back a published file when cancellation wins the final rename", async (t) => {
    const controller = new AbortController();
    const downloaded = Buffer.from("new-downloaded-content");
    const backend = {
      async stat() {
        return { type: "file", isDirectory: false, size: downloaded.length };
      },
      async downloadFile(_remotePath, localPath) {
        await fs.promises.writeFile(localPath, downloaded);
      },
    };
    sftpClients.set("scp-final-rename-abort", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "final-rename-abort.bin");
    const original = Buffer.from("existing-local-content");
    fs.writeFileSync(localPath, original);

    const originalRename = fs.promises.rename;
    let renameCalls = 0;
    fs.promises.rename = async (...args) => {
      await originalRename(...args);
      renameCalls += 1;
      if (renameCalls === 3) controller.abort();
    };
    t.after(() => { fs.promises.rename = originalRename; });

    await assert.rejects(
      () => sftpBridge.downloadSftpToLocal(null, {
        sftpId: "scp-final-rename-abort",
        remotePath: "/remote/file.bin",
        localPath,
        abortSignal: controller.signal,
      }),
      /cancel|abort/i,
    );
    assert.equal(renameCalls >= 4, true, "the published file should be rolled back to the backup");
    assert.deepEqual(fs.readFileSync(localPath), original);
  });

  it("downloadSftpToLocal reports and preserves the backup when cancellation rollback fails", async (t) => {
    const controller = new AbortController();
    const downloaded = Buffer.from("new-downloaded-content");
    const backend = {
      async stat() {
        return { type: "file", isDirectory: false, size: downloaded.length };
      },
      async downloadFile(_remotePath, localPath) {
        await fs.promises.writeFile(localPath, downloaded);
      },
    };
    sftpClients.set("scp-rollback-failure", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "rollback-failure.bin");
    const original = Buffer.from("existing-local-content");
    fs.writeFileSync(localPath, original);

    const originalRename = fs.promises.rename;
    let renameCalls = 0;
    let backupPath = null;
    fs.promises.rename = async (...args) => {
      renameCalls += 1;
      if (renameCalls === 4) {
        const error = new Error("injected backup restore failure");
        error.code = "EIO";
        throw error;
      }
      await originalRename(...args);
      if (renameCalls === 2) backupPath = args[1];
      if (renameCalls === 3) controller.abort();
    };
    t.after(() => { fs.promises.rename = originalRename; });

    await assert.rejects(
      () => sftpBridge.downloadSftpToLocal(null, {
        sftpId: "scp-rollback-failure",
        remotePath: "/remote/file.bin",
        localPath,
        abortSignal: controller.signal,
      }),
      (error) => {
        assert.match(error.message, /Could not restore the original file/);
        assert.match(error.message, /Backup:/);
        assert.doesNotMatch(error.message, /^Transfer cancelled$/);
        return true;
      },
    );
    assert.ok(backupPath);
    assert.deepEqual(fs.readFileSync(backupPath), original);
  });

  it("downloadSftpToLocal reports both recovery files when pre-publish restoration fails", async (t) => {
    const controller = new AbortController();
    const downloaded = Buffer.from("new-downloaded-content");
    const backend = {
      async stat() {
        return { type: "file", isDirectory: false, size: downloaded.length };
      },
      async downloadFile(_remotePath, localPath) {
        await fs.promises.writeFile(localPath, downloaded);
      },
    };
    sftpClients.set("scp-pre-publish-rollback-failure", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "pre-publish-rollback-failure.bin");
    fs.writeFileSync(localPath, Buffer.from("existing-local-content"));

    const originalRename = fs.promises.rename;
    let renameCalls = 0;
    let readyPath = null;
    let backupPath = null;
    fs.promises.rename = async (...args) => {
      renameCalls += 1;
      if (renameCalls === 3) {
        const error = new Error("injected backup restore failure");
        error.code = "EIO";
        throw error;
      }
      await originalRename(...args);
      if (renameCalls === 1) readyPath = args[1];
      if (renameCalls === 2) {
        backupPath = args[1];
        controller.abort();
      }
    };
    t.after(() => { fs.promises.rename = originalRename; });

    await assert.rejects(
      () => sftpBridge.downloadSftpToLocal(null, {
        sftpId: "scp-pre-publish-rollback-failure",
        remotePath: "/remote/file.bin",
        localPath,
        abortSignal: controller.signal,
      }),
      (error) => {
        assert.match(error.message, /Could not restore the original file/);
        assert.match(error.message, new RegExp(readyPath.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
        assert.match(error.message, new RegExp(backupPath.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
        return true;
      },
    );
    assert.equal(fs.existsSync(readyPath), true);
    assert.equal(fs.existsSync(backupPath), true);
  });

  it("uploadLocalToSftp rejects when AbortSignal fires mid-SCP upload", async () => {
    registerScpClient("scp-up");
    const localPath = path.join(tmpDir, "in.bin");
    fs.writeFileSync(localPath, Buffer.alloc(256, 9));
    const controller = new AbortController();
    const promise = sftpBridge.uploadLocalToSftp(null, {
      sftpId: "scp-up",
      localPath,
      remotePath: "/remote/in.bin",
      abortSignal: controller.signal,
    });
    await new Promise((r) => setTimeout(r, 40));
    controller.abort();
    await assert.rejects(() => promise, /cancel|abort/i);
  });

  it("uploadLocalToSftp aborts while SCP target inspection is still pending", async () => {
    let uploadCalls = 0;
    const backend = {
      async stat(_remotePath, options = {}) {
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
      async uploadFile() {
        uploadCalls += 1;
      },
    };
    sftpClients.set("scp-setup-abort", {
      __netcattyFileProtocol: "scp",
      __netcattyScpBackend: backend,
    });
    const localPath = path.join(tmpDir, "setup.bin");
    fs.writeFileSync(localPath, Buffer.alloc(16, 1));
    const controller = new AbortController();
    const startedAt = Date.now();
    const promise = sftpBridge.uploadLocalToSftp(null, {
      sftpId: "scp-setup-abort",
      localPath,
      remotePath: "/remote/setup.bin",
      abortSignal: controller.signal,
    });
    await new Promise((resolve) => setImmediate(resolve));
    controller.abort();
    await assert.rejects(() => promise, /cancel|abort/i);
    assert.equal(uploadCalls, 0);
    assert.ok(Date.now() - startedAt < 1000);
  });

  it("legacy entry points delegate cancellation to the unified transfer engine", () => {
    const src = fs.readFileSync(path.join(__dirname, "../sftpBridge.cjs"), "utf8");
    assert.doesNotMatch(src, /cancelledFlag/);
    assert.match(
      src,
      /async function downloadSftpToLocal\(_event, payload\) \{\s*return runUnifiedSftpTransfer\(payload, "download"\);\s*\}/,
    );
    assert.match(
      src,
      /async function uploadLocalToSftp\(_event, payload\) \{\s*return runUnifiedSftpTransfer\(payload, "upload"\);\s*\}/,
    );
    const unifiedIdx = src.indexOf("async function runUnifiedSftpTransfer");
    const unifiedBlock = src.slice(unifiedIdx, src.indexOf("async function downloadSftpToLocal", unifiedIdx));
    assert.match(unifiedBlock, /transferBridge\.startTransfer/);
    assert.match(unifiedBlock, /transferBridge\.cancelTransfer/);
  });
});
