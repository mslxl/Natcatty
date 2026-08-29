const assert = require("node:assert/strict");
const test = require("node:test");

const { installTerminalWorkerErrorGuards } = require("./workerProcessGuards.cjs");

function createFakeProcess() {
  const listeners = new Map();
  return {
    on(name, callback) {
      listeners.set(name, callback);
    },
    removeListener(name, callback) {
      if (listeners.get(name) === callback) listeners.delete(name);
    },
    emit(name, err) {
      const callback = listeners.get(name);
      if (!callback) throw new Error(`no listener for ${name}`);
      callback(err);
    },
  };
}

test("worker guards suppress uncaught exceptions and report them", () => {
  const fakeProcess = createFakeProcess();
  const reports = [];
  const logs = [];
  installTerminalWorkerErrorGuards({
    processObject: fakeProcess,
    report: (origin, err, decision) => reports.push({ origin, err, reason: decision.reason }),
    logError: (...args) => logs.push(args),
  });

  const err = Object.assign(new Error("read ECONNRESET"), { code: "ECONNRESET" });
  fakeProcess.emit("uncaughtException", err);

  assert.equal(reports.length, 1);
  assert.equal(reports[0].origin, "uncaughtException");
  assert.equal(reports[0].err, err);
  assert.equal(reports[0].reason, "non-fatal network error");
  assert.equal(logs.length, 1);
});

test("worker guards suppress unhandled rejections with non-Error reasons", () => {
  const fakeProcess = createFakeProcess();
  const reports = [];
  installTerminalWorkerErrorGuards({
    processObject: fakeProcess,
    report: (origin, err) => reports.push({ origin, err }),
    logError: () => {},
  });

  assert.doesNotThrow(() => fakeProcess.emit("unhandledRejection", "plain string reason"));
  assert.equal(reports.length, 1);
  assert.equal(reports[0].origin, "unhandledRejection");
  assert.equal(reports[0].err, "plain string reason");
});

test("worker guards absorb report failures instead of rethrowing", () => {
  const fakeProcess = createFakeProcess();
  installTerminalWorkerErrorGuards({
    processObject: fakeProcess,
    report: () => {
      throw new Error("report route is dead");
    },
    logError: () => {},
  });

  assert.doesNotThrow(() => fakeProcess.emit("uncaughtException", new Error("boom")));
});

test("uninstall removes the installed handlers", () => {
  const fakeProcess = createFakeProcess();
  const uninstall = installTerminalWorkerErrorGuards({
    processObject: fakeProcess,
    logError: () => {},
  });
  assert.doesNotThrow(() => fakeProcess.emit("uncaughtException", new Error("before")));
  uninstall();
  assert.throws(
    () => fakeProcess.emit("uncaughtException", new Error("after")),
    /no listener for uncaughtException/u,
  );
});

test("guards require a process-like EventEmitter", () => {
  assert.throws(
    () => installTerminalWorkerErrorGuards({ processObject: {} }),
    /process-like EventEmitter/u,
  );
});
