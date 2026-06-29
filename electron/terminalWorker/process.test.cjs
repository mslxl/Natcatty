const assert = require("node:assert/strict");
const test = require("node:test");

const {
  createZmodemUploadFileSelector,
  normalizeParentPortMessage,
} = require("./process.cjs");

function createParentPort() {
  const messages = [];
  const listeners = new Map();
  return {
    messages,
    on(channel, callback) {
      listeners.set(channel, callback);
    },
    postMessage(message) {
      messages.push(message);
    },
    emitMessage(message) {
      listeners.get("message")?.(message);
    },
  };
}

test("normalizeParentPortMessage unwraps Electron utility process MessageEvent data", () => {
  assert.deepEqual(
    normalizeParentPortMessage({ data: { kind: "zmodem-upload-dialog-result" } }),
    { kind: "zmodem-upload-dialog-result" },
  );
  assert.deepEqual(
    normalizeParentPortMessage({ kind: "request" }),
    { kind: "request" },
  );
});

test("ZMODEM upload selector resolves dialog results delivered as MessageEvent data", async () => {
  const parentPort = createParentPort();
  const selectUploadFiles = createZmodemUploadFileSelector(parentPort, {
    randomUUID: () => "dialog-1",
  });

  const promise = selectUploadFiles(7);
  assert.deepEqual(parentPort.messages, [{
    kind: "zmodem-upload-dialog",
    requestId: "dialog-1",
    webContentsId: 7,
  }]);

  parentPort.emitMessage({
    data: {
      kind: "zmodem-upload-dialog-result",
      requestId: "dialog-1",
      result: { canceled: false, filePaths: ["/tmp/upload.txt"] },
    },
  });

  assert.deepEqual(await promise, {
    canceled: false,
    filePaths: ["/tmp/upload.txt"],
  });
});
