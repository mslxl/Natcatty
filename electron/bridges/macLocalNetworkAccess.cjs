"use strict";

/**
 * macOS Local Network privacy gate (Apple TN3179 / issue #2663).
 *
 * Since macOS 15, outbound TCP/UDP to LAN addresses requires the user's
 * Local Network privilege. Netcatty already declares
 * NSLocalNetworkUsageDescription and rewrites the main executable LC_UUID
 * (#1040), but SSH sessions normally run inside Electron's utilityProcess
 * (terminal worker). Connections from that helper often fail with
 * EHOSTUNREACH without ever registering "Netcatty" under
 * System Settings → Privacy & Security → Local Network — so the user never
 * gets a prompt and has nothing to toggle.
 *
 * Before the worker opens a LAN socket, the main process performs a short
 * TCP connect to the first hop so TCC attributes the attempt to the app
 * bundle and can present the system alert.
 */

const DEFAULT_PROBE_TIMEOUT_MS = 60_000;
const LOCAL_NETWORK_HINT =
  "macOS may be blocking Local Network access. Open System Settings → Privacy & Security → Local Network, enable Netcatty, then reconnect.";

function stripIpBrackets(value) {
  return String(value || "").replace(/^\[|\]$/g, "").trim();
}

function isLocalNetworkHostname(hostname) {
  if (hostname == null) return false;
  const cleaned = stripIpBrackets(hostname);
  if (!cleaned) return false;

  const lower = cleaned.toLowerCase();
  if (lower.startsWith("fc") || lower.startsWith("fd")) return true; // fc00::/7 ULA
  if (
    lower.startsWith("fe8") ||
    lower.startsWith("fe9") ||
    lower.startsWith("fea") ||
    lower.startsWith("feb")
  ) {
    return true; // fe80::/10 link-local
  }
  if (lower.startsWith("::ffff:")) {
    return isLocalNetworkHostname(lower.slice(7));
  }

  const parts = cleaned.split(".");
  if (parts.length === 4 && parts.every((part) => /^\d+$/.test(part))) {
    const [a, b] = parts.map(Number);
    if (a === 10) return true;
    if (a === 172 && b >= 16 && b <= 31) return true;
    if (a === 192 && b === 168) return true;
    if (a === 169 && b === 254) return true;
    if (a === 100 && b >= 64 && b <= 127) return true; // CGNAT / Tailscale
    return false;
  }

  return false;
}

function resolveFirstTcpEndpoint(options = {}) {
  const jumpHosts = Array.isArray(options.jumpHosts) ? options.jumpHosts : [];
  if (jumpHosts.length > 0) {
    const first = jumpHosts[0] || {};
    const hostname = String(first.hostname || first.host || "").trim();
    const port = Number(first.port);
    return {
      hostname,
      port: Number.isInteger(port) && port >= 1 && port <= 65535 ? port : 22,
    };
  }

  const hostname = String(options.hostname || options.host || "").trim();
  const port = Number(options.port);
  return {
    hostname,
    port: Number.isInteger(port) && port >= 1 && port <= 65535 ? port : 22,
  };
}

function looksLikeHostUnreachableMessage(message) {
  const text = String(message || "");
  return /EHOSTUNREACH|ENETUNREACH|host is unreachable|network is unreachable/i.test(text);
}

function annotateMacLocalNetworkErrorMessage(message, options = {}) {
  const platform = options.platform || process.platform;
  const hostname = options.hostname || "";
  const text = String(message || "");
  if (platform !== "darwin") return text;
  if (!isLocalNetworkHostname(hostname)) return text;
  if (!looksLikeHostUnreachableMessage(text)) return text;
  if (text.includes("Local Network")) return text;
  return `${text}\n\n${LOCAL_NETWORK_HINT}`;
}

function createMacLocalNetworkAccessGate(options = {}) {
  const platform = options.platform || process.platform;
  const versions = options.versions || process.versions;
  const net = options.net || require("node:net");
  const probedKeys = options.probedKeys || new Set();
  const inFlight = options.inFlight || new Map();
  const probeTimeoutMs = Number.isFinite(options.probeTimeoutMs)
    ? Math.max(1_000, Math.round(options.probeTimeoutMs))
    : DEFAULT_PROBE_TIMEOUT_MS;
  const setTimer = options.setTimer || setTimeout;
  const clearTimer = options.clearTimer || clearTimeout;
  // Bare Node unit tests (and non-Electron CLIs) must never open LAN sockets.
  // Only the real Electron main process should trigger the TCC prompt.
  const electronRuntime = options.forceElectron === true
    || (options.forceElectron !== false && Boolean(versions?.electron));

  function probeKey(hostname, port) {
    return `${String(hostname).toLowerCase()}:${port}`;
  }

  function runProbe(hostname, port) {
    return new Promise((resolve) => {
      let settled = false;
      let socket = null;
      let timer = null;

      const finish = () => {
        if (settled) return;
        settled = true;
        if (timer) clearTimer(timer);
        try { socket?.destroy(); } catch { /* ignore */ }
        resolve();
      };

      try {
        socket = net.connect({ host: hostname, port }, finish);
        socket.once?.("error", finish);
        if (typeof socket.setTimeout === "function") {
          socket.setTimeout(probeTimeoutMs, finish);
        } else {
          timer = setTimer(finish, probeTimeoutMs);
        }
      } catch {
        finish();
      }
    });
  }

  async function ensureAccess(connectOptions = {}) {
    if (platform !== "darwin") return { skipped: true, reason: "platform" };
    if (!electronRuntime) return { skipped: true, reason: "not-electron" };

    const endpoint = resolveFirstTcpEndpoint(connectOptions);
    if (!endpoint.hostname || !isLocalNetworkHostname(endpoint.hostname)) {
      return { skipped: true, reason: "not-local-network" };
    }

    const key = probeKey(endpoint.hostname, endpoint.port);
    if (probedKeys.has(key)) return { skipped: true, reason: "cached" };

    const pending = inFlight.get(key);
    if (pending) {
      await pending;
      return { skipped: true, reason: "in-flight" };
    }

    const probe = runProbe(endpoint.hostname, endpoint.port).finally(() => {
      inFlight.delete(key);
      probedKeys.add(key);
    });
    inFlight.set(key, probe);
    await probe;
    return { probed: true, hostname: endpoint.hostname, port: endpoint.port };
  }

  return {
    ensureAccess,
    isLocalNetworkHostname,
    resolveFirstTcpEndpoint,
    annotateErrorMessage(message, connectOptions = {}) {
      const endpoint = resolveFirstTcpEndpoint(connectOptions);
      return annotateMacLocalNetworkErrorMessage(message, {
        platform,
        hostname: endpoint.hostname || connectOptions.hostname || connectOptions.host,
      });
    },
  };
}

const defaultGate = createMacLocalNetworkAccessGate();

module.exports = {
  LOCAL_NETWORK_HINT,
  DEFAULT_PROBE_TIMEOUT_MS,
  isLocalNetworkHostname,
  resolveFirstTcpEndpoint,
  annotateMacLocalNetworkErrorMessage,
  createMacLocalNetworkAccessGate,
  ensureMacLocalNetworkAccess: (options) => defaultGate.ensureAccess(options),
  annotateMacLocalNetworkError: (message, options) => defaultGate.annotateErrorMessage(message, options),
};
