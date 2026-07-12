"use strict";

const dns = require("node:dns");

const DEFAULT_TIMEOUT_MS = 3000;

function createTcpConnectLatencyProbe({
  net,
  lookup = dns.lookup,
  now = () => performance.now(),
}) {
  return function measureTcpConnectLatency({ hostname, port, timeoutMs = DEFAULT_TIMEOUT_MS }) {
    if (!hostname || !Number.isInteger(port) || port < 1 || port > 65535) {
      return Promise.resolve(null);
    }

    return new Promise((resolve) => {
      let startedAt = net.isIP?.(hostname) ? now() : null;
      let settled = false;
      let socket = null;

      const finish = (latencyMs) => {
        if (settled) return;
        settled = true;
        try { socket?.destroy(); } catch { /* best-effort cleanup */ }
        resolve(latencyMs);
      };

      try {
        const connectOptions = { host: hostname, port };
        if (startedAt === null) {
          connectOptions.lookup = (host, options, callback) => {
            lookup(host, options, (err, address, family) => {
              startedAt = now();
              callback(err, address, family);
            });
          };
        }
        socket = net.createConnection(connectOptions, () => {
          const connectedAt = now();
          finish(Math.max(0, Math.round(connectedAt - (startedAt ?? connectedAt))));
        });
        socket.once("error", () => finish(null));
        socket.setTimeout(timeoutMs, () => finish(null));
      } catch {
        finish(null);
      }
    });
  };
}

module.exports = { createTcpConnectLatencyProbe, DEFAULT_TIMEOUT_MS };
