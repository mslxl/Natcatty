"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { stageRendererFileToTemp } = require("./stageUploadFile.cjs");

test("pathless renderer files stream into a controlled temp file without arrayBuffer", async (t) => {
  const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-upload-"));
  t.after(() => fs.promises.rm(dir, { recursive: true, force: true }));
  const localPath = path.join(dir, "upload.part");
  let arrayBufferCalls = 0;
  const chunks = [new Uint8Array([1, 2]), new Uint8Array([3, 4, 5])];
  const file = {
    arrayBuffer: async () => { arrayBufferCalls += 1; return new ArrayBuffer(0); },
    stream: () => new ReadableStream({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(chunk);
        controller.close();
      },
    }),
  };
  assert.equal(await stageRendererFileToTemp(file, localPath, fs), localPath);
  assert.deepEqual(await fs.promises.readFile(localPath), Buffer.from([1, 2, 3, 4, 5]));
  assert.equal(arrayBufferCalls, 0);
});

test("failed pathless-file staging removes its partial temp file", async (t) => {
  const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-upload-fail-"));
  t.after(() => fs.promises.rm(dir, { recursive: true, force: true }));
  const localPath = path.join(dir, "upload.part");
  let reads = 0;
  const file = {
    stream: () => ({
      getReader: () => ({
        read: async () => {
          reads += 1;
          if (reads === 1) return { done: false, value: new Uint8Array([1]) };
          throw new Error("source failed");
        },
        releaseLock: () => {},
      }),
    }),
  };
  await assert.rejects(stageRendererFileToTemp(file, localPath, fs), /source failed/);
  await assert.rejects(fs.promises.stat(localPath), { code: "ENOENT" });
});

test("a synchronous File.stream failure closes the new handle and removes the temp file", async (t) => {
  const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-upload-stream-init-"));
  t.after(() => fs.promises.rm(dir, { recursive: true, force: true }));
  const localPath = path.join(dir, "upload.part");

  await assert.rejects(stageRendererFileToTemp({
    stream() {
      throw new Error("stream init failed");
    },
  }, localPath, fs), /stream init failed/);

  await assert.rejects(fs.promises.stat(localPath), { code: "ENOENT" });
});

test("a synchronous getReader failure closes the new handle and removes the temp file", async (t) => {
  const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-upload-reader-init-"));
  t.after(() => fs.promises.rm(dir, { recursive: true, force: true }));
  const localPath = path.join(dir, "upload.part");

  await assert.rejects(stageRendererFileToTemp({
    stream: () => ({
      getReader() {
        throw new Error("reader init failed");
      },
    }),
  }, localPath, fs), /reader init failed/);

  await assert.rejects(fs.promises.stat(localPath), { code: "ENOENT" });
});

test("stream initialization failure still closes the handle when temp deletion fails", async () => {
  let closeCalls = 0;
  let unlinkCalls = 0;
  const fakeFs = {
    promises: {
      open: async () => ({
        close: async () => { closeCalls += 1; },
      }),
      unlink: async () => {
        unlinkCalls += 1;
        throw new Error("delete denied");
      },
    },
  };

  await assert.rejects(stageRendererFileToTemp({
    stream() {
      throw new Error("stream init failed");
    },
  }, "/controlled/upload.part", fakeFs), /stream init failed/);

  assert.equal(unlinkCalls, 1);
  assert.ok(closeCalls >= 1, "the file handle must close even if cleanup unlink fails");
});

test("cancelling a blocked pathless-file read closes it and removes the partial file", async (t) => {
  const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "netcatty-stage-upload-cancel-"));
  t.after(() => fs.promises.rm(dir, { recursive: true, force: true }));
  const localPath = path.join(dir, "upload.part");
  const controller = new AbortController();
  let cancelCalls = 0;
  let finishRead;
  const file = {
    stream: () => ({
      getReader: () => ({
        read: () => new Promise((resolve) => { finishRead = resolve; }),
        cancel: async () => {
          cancelCalls += 1;
          finishRead?.({ done: true });
        },
        releaseLock: () => {},
      }),
    }),
  };
  const staging = stageRendererFileToTemp(file, localPath, fs, controller.signal);
  while (!finishRead) await new Promise((resolve) => setImmediate(resolve));
  controller.abort(new Error("cancel now"));
  await assert.rejects(staging, /cancel now/);
  assert.equal(cancelCalls, 1);
  await assert.rejects(fs.promises.stat(localPath), { code: "ENOENT" });
});
