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
    const promise = sftpBridge.downloadSftpToLocal(null, {
      sftpId: "scp-dl",
      remotePath: "/remote/file.bin",
      localPath,
      abortSignal: controller.signal,
    });
    await new Promise((r) => setTimeout(r, 40));
    controller.abort();
    await assert.rejects(() => promise, /cancel|abort/i);
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
