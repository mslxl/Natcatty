"use strict";

const { invalidateSshTransport } = require("./sshTransportInvalidation.cjs");

const DEFAULT_SSH_CHANNEL_OPEN_TIMEOUT_MS = 30_000;

function closeLateChannel(channel) {
  if (!channel || typeof channel === "number") return;
  try { channel.once?.("error", () => {}); } catch { /* ignore */ }
  try { channel.close?.(); } catch { /* ignore */ }
  try { channel.end?.(); } catch { /* ignore */ }
  try { channel.destroy?.(); } catch { /* ignore */ }
}

function channelAbortError(signal, label) {
  const reason = signal?.reason;
  const error = reason instanceof Error ? reason : new Error(`${label} was cancelled`);
  if (!error.code) error.code = "ABORT_ERR";
  return error;
}

function openBoundedSshChannel(sshClient, invoke, options = {}) {
  const label = String(options.label || "SSH channel open");
  const timeoutMs = Math.max(
    1,
    Number(options.timeoutMs) || DEFAULT_SSH_CHANNEL_OPEN_TIMEOUT_MS,
  );
  const signal = options.signal || null;
  const closeLateResult = options.closeLateResult || closeLateChannel;
  const timeoutCode = options.timeoutCode || "SSH_CHANNEL_OPEN_TIMEOUT";
  const setTimeoutFn = options.setTimeoutFn || setTimeout;
  const clearTimeoutFn = options.clearTimeoutFn || clearTimeout;

  return new Promise((resolve, reject) => {
    let settled = false;
    let timer = null;
    const cleanup = () => {
      if (timer) clearTimeoutFn(timer);
      timer = null;
      signal?.removeEventListener?.("abort", onAbort);
    };
    const finish = (error, result, { invalidate = false } = {}) => {
      if (settled) {
        if (result) closeLateResult(result);
        return false;
      }
      settled = true;
      cleanup();
      if (error && result) closeLateResult(result);
      if (invalidate) invalidateSshTransport(sshClient);
      if (error) reject(error);
      else resolve(result);
      return true;
    };
    const onAbort = () => finish(channelAbortError(signal, label), null, { invalidate: true });

    if (signal?.aborted) {
      finish(channelAbortError(signal, label));
      return;
    }
    signal?.addEventListener?.("abort", onAbort, { once: true });
    // This is a correctness deadline, not background housekeeping. It must
    // remain referenced until the channel open settles; cleanup clears it on
    // every success, error, abort, and synchronous-throw path.
    timer = setTimeoutFn(() => {
      const error = new Error(`${label} timed out after ${timeoutMs} ms`);
      error.code = timeoutCode;
      finish(error, null, { invalidate: true });
    }, timeoutMs);

    try {
      invoke((error, result) => finish(error, result));
    } catch (error) {
      finish(error);
    }
  });
}

function openBoundedSshShell(sshClient, windowOptions, shellOptions, options = {}) {
  return openBoundedSshChannel(
    sshClient,
    (callback) => sshClient.shell(windowOptions, shellOptions, callback),
    {
      ...options,
      label: options.label || "SSH shell channel open",
      timeoutCode: "SSH_SHELL_OPEN_TIMEOUT",
    },
  );
}

function openBoundedForwardOut(
  sshClient,
  sourceAddress,
  sourcePort,
  targetAddress,
  targetPort,
  options = {},
) {
  return openBoundedSshChannel(
    sshClient,
    (callback) => sshClient.forwardOut(
      sourceAddress,
      sourcePort,
      targetAddress,
      targetPort,
      callback,
    ),
    {
      ...options,
      label: options.label || "SSH forwardOut channel open",
      timeoutCode: "SSH_FORWARD_OUT_TIMEOUT",
    },
  );
}

function openBoundedForwardIn(sshClient, bindAddress, bindPort, options = {}) {
  return openBoundedSshChannel(
    sshClient,
    (callback) => sshClient.forwardIn(bindAddress, bindPort, callback),
    {
      ...options,
      label: options.label || "SSH forwardIn request",
      timeoutCode: "SSH_FORWARD_IN_TIMEOUT",
      closeLateResult: () => {},
    },
  );
}

function deliverChannelOpenCallback(promise, callback) {
  void promise.then(
    (result) => callback(null, result),
    (error) => callback(error),
  );
}

function openBoundedSshShellCallback(
  sshClient,
  windowOptions,
  shellOptions,
  callback,
  options = {},
) {
  deliverChannelOpenCallback(
    openBoundedSshShell(sshClient, windowOptions, shellOptions, options),
    callback,
  );
}

function openBoundedForwardOutCallback(
  sshClient,
  sourceAddress,
  sourcePort,
  targetAddress,
  targetPort,
  callback,
  options = {},
) {
  deliverChannelOpenCallback(
    openBoundedForwardOut(
      sshClient,
      sourceAddress,
      sourcePort,
      targetAddress,
      targetPort,
      options,
    ),
    callback,
  );
}

function openBoundedForwardInCallback(
  sshClient,
  bindAddress,
  bindPort,
  callback,
  options = {},
) {
  deliverChannelOpenCallback(
    openBoundedForwardIn(sshClient, bindAddress, bindPort, options),
    callback,
  );
}

module.exports = {
  DEFAULT_SSH_CHANNEL_OPEN_TIMEOUT_MS,
  closeLateChannel,
  openBoundedSshChannel,
  openBoundedSshShell,
  openBoundedSshShellCallback,
  openBoundedForwardOut,
  openBoundedForwardOutCallback,
  openBoundedForwardIn,
  openBoundedForwardInCallback,
};
