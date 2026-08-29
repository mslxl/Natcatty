"use strict";

const {
  classifyProcessError,
} = require("../bridges/processErrorGuards.cjs");

/**
 * Terminal worker process error guards.
 *
 * Every terminal, SSH, SFTP, and port-forwarding session shares this
 * utilityProcess, so a single stray async error must never let Node's default
 * `uncaughtException` behavior exit the worker with code 1 — that would
 * disconnect every session at once. This mirrors the main-process guards
 * (`bridges/processErrorGuards.cjs`), but worker policy is stricter: once the
 * worker is running, every process-level error is suppressed. The error is
 * still reported (see `report`) so the main process can record it in the
 * crash log for later diagnosis.
 */
function installTerminalWorkerErrorGuards(options = {}) {
  const processObject = options.processObject || process;
  if (!processObject?.on || !processObject?.removeListener) {
    throw new Error("A process-like EventEmitter is required");
  }
  const report = typeof options.report === "function" ? options.report : () => {};
  const logError = typeof options.logError === "function"
    ? options.logError
    : (...args) => console.error(...args);

  const labelFor = (origin) => (
    origin === "unhandledRejection" ? "unhandled rejection" : "uncaught exception"
  );

  const makeHandler = (origin) => (err) => {
    const decision = classifyProcessError(err, {
      runtimeStarted: true,
      origin,
    });
    logError(
      `Suppressed terminal worker ${labelFor(origin)} (${decision.reason}):`,
      err,
    );
    try {
      report(origin, err, decision);
    } catch {
      // Error reporting must never be able to escalate into a worker crash.
    }
  };

  const handleUncaughtException = makeHandler("uncaughtException");
  const handleUnhandledRejection = makeHandler("unhandledRejection");

  processObject.on("uncaughtException", handleUncaughtException);
  processObject.on("unhandledRejection", handleUnhandledRejection);

  return () => {
    processObject.removeListener("uncaughtException", handleUncaughtException);
    processObject.removeListener("unhandledRejection", handleUnhandledRejection);
  };
}

module.exports = {
  installTerminalWorkerErrorGuards,
};
