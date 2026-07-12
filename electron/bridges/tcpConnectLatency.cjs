"use strict";

const dns = require("node:dns");

const DEFAULT_TIMEOUT_MS = 3000;
const DEFAULT_CACHE_TTL_MS = 30000;
const DEFAULT_FAILURE_CACHE_TTL_MS = 5000;
const MAX_CACHE_ENTRIES = 256;

function createTcpConnectLatencyProbe({
  net,
  lookup = dns.lookup,
  now = () => performance.now(),
  cacheNow = () => Date.now(),
  cacheTtlMs = DEFAULT_CACHE_TTL_MS,
  failureCacheTtlMs = DEFAULT_FAILURE_CACHE_TTL_MS,
}) {
  const cache = new Map();

  function runProbe({ hostname, port, timeoutMs }) {
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
  }

  return function measureTcpConnectLatency({ hostname, port, timeoutMs = DEFAULT_TIMEOUT_MS }) {
    if (!hostname || !Number.isInteger(port) || port < 1 || port > 65535) {
      return Promise.resolve(null);
    }

    const key = `${String(hostname).toLowerCase()}\u0000${port}`;
    const cached = cache.get(key);
    const checkedAt = cacheNow();
    if (cached?.promise || (cached && cached.expiresAt > checkedAt)) {
      return cached.promise || Promise.resolve(cached.value);
    }

    if (cache.size >= MAX_CACHE_ENTRIES) {
      for (const [cachedKey, entry] of cache) {
        if (!entry.promise && entry.expiresAt <= checkedAt) cache.delete(cachedKey);
      }
      while (cache.size >= MAX_CACHE_ENTRIES) {
        cache.delete(cache.keys().next().value);
      }
    }

    const promise = runProbe({ hostname, port, timeoutMs }).then((value) => {
      cache.set(key, {
        value,
        expiresAt: cacheNow() + (value === null ? failureCacheTtlMs : cacheTtlMs),
      });
      return value;
    });
    cache.set(key, { promise, expiresAt: Number.POSITIVE_INFINITY });
    return promise;
  };
}

module.exports = {
  createTcpConnectLatencyProbe,
  DEFAULT_TIMEOUT_MS,
  DEFAULT_CACHE_TTL_MS,
};
