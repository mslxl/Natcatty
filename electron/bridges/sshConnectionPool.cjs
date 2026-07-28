"use strict";

/**
 * Shared SSH transport registry (borrow / return + idle park).
 *
 * Background (issue #1204): "Copy Tab" on an MFA-protected host used to open a
 * brand-new SSH connection, forcing the user through a second MFA prompt. Like
 * Tabby's session-multiplexing, we open additional channels on an already-
 * authenticated connection. The SSH protocol natively supports many session
 * channels over one transport, so no re-authentication is needed.
 *
 * Lifecycle model (OpenSSH ControlPersist-style):
 * - Consumers **borrow** a lease (shell / sftp / transfer / forward).
 * - **return** drops the lease. When no leases remain, the transport enters
 *   idle park for a configurable TTL instead of ending immediately.
 * - A later borrow against the same endpoint can wake an idle transport.
 * - When the idle TTL fires (or TTL is 0 on last return), the underlying
 *   ssh2 Client and jump-host chain are torn down.
 *
 * Compatibility: createConnectionRef / acquireConnectionRef /
 * releaseConnectionRef / findReusableSession keep working. `connRef` is the
 * transport object; `count` mirrors active lease size for existing callers.
 *
 * The same `sessions` Map is shared by sshBridge and terminalBridge (see
 * registerBridges.cjs). SFTP session-backed clients and (later) port-forward
 * tunnels borrow the same transports via this registry.
 */

const { randomUUID } = require("node:crypto");

/**
 * Default idle park after last lease returns (5 minutes).
 * 0 = park until app quit / discard (ControlPersist-style, never auto-reclaim).
 * Positive = park that many ms then end.
 */
const DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS = 5 * 60_000;

/** Storage key mirrored in infrastructure/config/storageKeys.ts (main + renderer). */
const STORAGE_KEY_SSH_TRANSPORT_IDLE_TTL_MS = "netcatty_ssh_transport_idle_ttl_ms_v1";

const LEASE_KINDS = Object.freeze({
  shell: "shell",
  sftp: "sftp",
  transfer: "transfer",
  forward: "forward",
});

/** @type {Map<string, object>} transportId -> transport */
const transportsById = new Map();
/** @type {Map<string, Set<string>>} endpointKey -> transport ids */
const transportIdsByEndpoint = new Map();
/** @type {Map<string, { transport: object, holder: object|null }>} leaseId -> entry */
const leasesById = new Map();

let defaultIdleTtlMs = resolveEnvIdleTtlMs(DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS);
let timerApi = {
  setTimeout: (...args) => setTimeout(...args),
  clearTimeout: (...args) => clearTimeout(...args),
};
let nowFn = () => Date.now();
let nextLeaseSeq = 0;

function resolveEnvIdleTtlMs(fallback) {
  const raw = process.env.NETCATTY_SSH_TRANSPORT_IDLE_TTL_MS;
  if (raw === undefined || raw === "") return fallback;
  const n = Number(raw);
  if (!Number.isFinite(n) || n < 0) return fallback;
  return n;
}

function fingerprintProxy(proxy) {
  if (!proxy || typeof proxy !== "object") return "-";
  const type = proxy.type || proxy.proxyType || proxy.mode || "";
  const host = proxy.host || proxy.hostname || proxy.server || "";
  const port = proxy.port || "";
  const user = proxy.username || proxy.user || "";
  if (!type && !host && !port) return "-";
  return `${type}:${host}:${port}:${user}`;
}

function fingerprintJumpHosts(jumpHosts) {
  if (!Array.isArray(jumpHosts) || jumpHosts.length === 0) return "-";
  return jumpHosts.map((h) => {
    if (typeof h === "string") return h;
    // Prefer stable vault ids when present so profile edits change the key.
    const id = h?.hostId || h?.id || "";
    const host = h?.hostname || h?.host || "";
    const port = h?.port || 22;
    const user = h?.username || "root";
    return id ? `id:${id}` : `${host}:${port}:${user}`;
  }).join(">");
}

function normalizeEndpoint(endpoint) {
  if (!endpoint || typeof endpoint !== "object") return null;
  const hostname = String(endpoint.hostname || "").trim();
  if (!hostname) return null;
  // hostId scopes reuse to a vault profile so two saved hosts that share
  // hostname:port:user but differ in credentials/proxy/host-key policy never
  // silently share an authenticated transport.
  const hostId = endpoint.hostId != null && String(endpoint.hostId).trim()
    ? String(endpoint.hostId).trim()
    : "";
  return {
    hostId,
    hostname,
    port: endpoint.port || 22,
    username: endpoint.username || "root",
    protocol: endpoint.protocol || "ssh",
    sftpSudo: Boolean(endpoint.sftpSudo),
    jumpFingerprint: endpoint.jumpFingerprint
      ? String(endpoint.jumpFingerprint)
      : fingerprintJumpHosts(endpoint.jumpHosts),
    proxyFingerprint: endpoint.proxyFingerprint
      ? String(endpoint.proxyFingerprint)
      : fingerprintProxy(endpoint.proxy),
  };
}

function buildEndpointKey(endpoint) {
  const ep = normalizeEndpoint(endpoint);
  if (!ep) return null;
  const sudo = ep.sftpSudo ? "sudo" : "nosudo";
  const jump = ep.jumpFingerprint || "-";
  const proxy = ep.proxyFingerprint || "-";
  const profile = ep.hostId || "-";
  return [
    profile,
    ep.hostname,
    ep.port,
    ep.username,
    ep.protocol,
    sudo,
    jump,
    proxy,
  ].join("|");
}

function sameEndpoint(a, b) {
  const left = normalizeEndpoint(a);
  const right = normalizeEndpoint(b);
  if (!left || !right) return false;
  // When both sides carry a vault hostId they must match so different profiles
  // never cross-reuse. If either omits hostId (legacy / explicit session-id
  // reuse), fall back to hostname:port:user:jump:proxy comparison only.
  if (left.hostId && right.hostId && left.hostId !== right.hostId) return false;
  return left.hostname === right.hostname
    && left.port === right.port
    && left.username === right.username
    && left.protocol === right.protocol
    && left.sftpSudo === right.sftpSudo
    && left.jumpFingerprint === right.jumpFingerprint
    && left.proxyFingerprint === right.proxyFingerprint;
}

function isTransportSocketHealthy(transport) {
  if (!transport || !transport.conn) return false;
  if (transport.state === "dead" || transport.state === "closing") return false;
  const sock = transport.conn._sock;
  if (sock && sock.destroyed) return false;
  return true;
}

function clearIdleTimer(transport) {
  if (!transport?.idleTimer) return;
  try {
    timerApi.clearTimeout(transport.idleTimer);
  } catch {
    /* ignore */
  }
  transport.idleTimer = null;
  transport.idleDeadlineAt = null;
}

function unregisterTransport(transport) {
  if (!transport) return;
  transportsById.delete(transport.id);
  if (transport.endpointKey) {
    const set = transportIdsByEndpoint.get(transport.endpointKey);
    if (set) {
      set.delete(transport.id);
      if (set.size === 0) transportIdsByEndpoint.delete(transport.endpointKey);
    }
  }
}

function endTransport(transport, reason = "end") {
  if (!transport) return false;
  if (transport.state === "dead" || transport.state === "closing") return false;

  transport.state = "closing";
  clearIdleTimer(transport);

  for (const leaseId of [...transport.leases.keys()]) {
    const entry = leasesById.get(leaseId);
    leasesById.delete(leaseId);
    transport.leases.delete(leaseId);
    if (entry?.holder && entry.holder.connRef === transport) {
      entry.holder.connRef = null;
      entry.holder._sshTransportLeaseId = null;
    }
  }
  transport.count = 0;

  try {
    transport.conn?.end();
  } catch {
    /* connection may already be gone */
  }
  const chain = Array.isArray(transport.chainConnections) ? transport.chainConnections : [];
  for (const c of chain) {
    try {
      c?.end();
    } catch {
      /* ignore */
    }
  }
  transport.chainConnections = [];
  transport.conn = null;
  transport.state = "dead";
  transport.endedReason = reason;
  unregisterTransport(transport);
  return true;
}

function scheduleIdleEnd(transport) {
  clearIdleTimer(transport);
  const ttl = Number.isFinite(transport.idleTtlMs) ? transport.idleTtlMs : defaultIdleTtlMs;

  transport.state = "idle";
  transport.idleSince = nowFn();

  // 0 (or negative/non-finite): park until quit/discard — matches settings
  // "Until app quit" and OpenSSH ControlPersist yes.
  if (!Number.isFinite(ttl) || ttl <= 0) {
    transport.idleDeadlineAt = null;
    return { ended: false, idle: true };
  }

  transport.idleDeadlineAt = transport.idleSince + ttl;
  transport.idleTimer = timerApi.setTimeout(() => {
    transport.idleTimer = null;
    if (transport.state !== "idle" || transport.leases.size > 0) return;
    endTransport(transport, "idle-ttl");
  }, ttl);

  return { ended: false, idle: true };
}

function wakeFromIdle(transport) {
  if (transport.state !== "idle") return;
  clearIdleTimer(transport);
  transport.state = "live";
  transport.idleSince = null;
  transport.idleDeadlineAt = null;
}

function attachEndpointIndex(transport) {
  if (!transport.endpointKey) return;
  let set = transportIdsByEndpoint.get(transport.endpointKey);
  if (!set) {
    set = new Set();
    transportIdsByEndpoint.set(transport.endpointKey, set);
  }
  set.add(transport.id);
}

function allocateLeaseId(kind) {
  nextLeaseSeq += 1;
  return `${kind || "lease"}-${nextLeaseSeq}-${randomUUID().slice(0, 8)}`;
}

/**
 * Create a transport around an authenticated ssh2 Client.
 * Does not automatically create a lease — call borrowTransport next, or use
 * createConnectionRef which creates + borrows a shell lease for a session.
 */
function createTransport({
  conn,
  chainConnections = [],
  endpoint = null,
  idleTtlMs = defaultIdleTtlMs,
  meta = null,
} = {}) {
  if (!conn) throw new Error("createTransport requires conn");

  const normalized = normalizeEndpoint(endpoint);
  const transport = {
    id: randomUUID(),
    // Compat: existing code reads connRef.count / conn / chainConnections /
    // shellOpenQueue on the shared descriptor.
    count: 0,
    conn,
    chainConnections: Array.isArray(chainConnections) ? chainConnections : [],
    shellOpenQueue: undefined,
    leases: new Map(),
    endpoint: normalized,
    endpointKey: buildEndpointKey(normalized),
    state: "live",
    idleTtlMs: Number.isFinite(idleTtlMs) && idleTtlMs >= 0 ? idleTtlMs : defaultIdleTtlMs,
    idleTimer: null,
    idleSince: null,
    idleDeadlineAt: null,
    createdAt: nowFn(),
    meta: meta || null,
    endedReason: null,
  };

  transportsById.set(transport.id, transport);
  attachEndpointIndex(transport);
  return transport;
}

/**
 * Borrow a lease on a transport. Wakes idle park if needed.
 *
 * @param {object} transport
 * @param {{ kind?: string, leaseId?: string, holder?: object|null, meta?: object }} [options]
 */
function borrowTransport(transport, options = {}) {
  if (!transport || transport.state === "dead" || transport.state === "closing") {
    throw new Error("Cannot borrow a closed SSH transport");
  }
  if (!isTransportSocketHealthy(transport)) {
    endTransport(transport, "unhealthy");
    throw new Error("SSH transport socket is not healthy");
  }

  wakeFromIdle(transport);

  const kind = options.kind && LEASE_KINDS[options.kind] ? options.kind : (options.kind || "shell");
  const leaseId = options.leaseId || allocateLeaseId(kind);
  if (transport.leases.has(leaseId) || leasesById.has(leaseId)) {
    throw new Error(`SSH transport lease already exists: ${leaseId}`);
  }

  const holder = options.holder ?? null;
  const lease = {
    id: leaseId,
    kind,
    holder,
    meta: options.meta || null,
    borrowedAt: nowFn(),
  };
  transport.leases.set(leaseId, lease);
  leasesById.set(leaseId, { transport, holder });
  transport.count = transport.leases.size;
  transport.state = "live";

  if (holder && typeof holder === "object") {
    holder.connRef = transport;
    holder._sshTransportLeaseId = leaseId;
  }

  return { transport, leaseId, lease };
}

/**
 * Move an existing lease from a temporary holder to the real session without
 * changing the lease count. Used when Copy Tab / reuse opens a shell while
 * pinned on a refHolder, then hands the pin to the live session object.
 *
 * @returns {boolean} true if the lease was rebound
 */
function transferConnectionRef(fromHolder, toHolder) {
  if (!fromHolder || !toHolder || fromHolder === toHolder) return false;
  const leaseId = fromHolder._sshTransportLeaseId;
  const transport = fromHolder.connRef;
  if (!leaseId || !transport?.leases) return false;
  const lease = transport.leases.get(leaseId);
  if (!lease) return false;

  lease.holder = toHolder;
  leasesById.set(leaseId, { transport, holder: toHolder });

  fromHolder.connRef = null;
  fromHolder._sshTransportLeaseId = null;
  toHolder.connRef = transport;
  toHolder._sshTransportLeaseId = leaseId;
  return true;
}

/**
 * Return a lease by id or by holder object (compat with session/refHolder).
 * @returns {{ released: boolean, ended: boolean, idle: boolean, remaining: number }}
 */
function returnTransport(leaseIdOrHolder) {
  if (leaseIdOrHolder == null) {
    return { released: false, ended: false, idle: false, remaining: 0 };
  }

  let leaseId = null;
  let holder = null;

  if (typeof leaseIdOrHolder === "string") {
    leaseId = leaseIdOrHolder;
  } else if (typeof leaseIdOrHolder === "object") {
    holder = leaseIdOrHolder;
    leaseId = holder._sshTransportLeaseId || null;
    // Legacy: holder still points at transport but lease id was lost — try match.
    if (!leaseId && holder.connRef?.leases) {
      for (const [id, lease] of holder.connRef.leases) {
        if (lease.holder === holder) {
          leaseId = id;
          break;
        }
      }
    }
  }

  if (!leaseId) {
    return { released: false, ended: false, idle: false, remaining: 0 };
  }

  const entry = leasesById.get(leaseId);
  if (!entry) {
    if (holder) {
      holder.connRef = null;
      holder._sshTransportLeaseId = null;
    }
    return { released: false, ended: false, idle: false, remaining: 0 };
  }

  const { transport } = entry;
  leasesById.delete(leaseId);
  transport.leases.delete(leaseId);
  transport.count = transport.leases.size;

  if (entry.holder && typeof entry.holder === "object") {
    if (entry.holder.connRef === transport) entry.holder.connRef = null;
    entry.holder._sshTransportLeaseId = null;
  } else if (holder) {
    holder.connRef = null;
    holder._sshTransportLeaseId = null;
  }

  if (transport.leases.size > 0) {
    return {
      released: true,
      ended: false,
      idle: false,
      remaining: transport.leases.size,
    };
  }

  const park = scheduleIdleEnd(transport);
  return {
    released: true,
    ended: park.ended,
    idle: park.idle,
    remaining: 0,
  };
}

function discardTransport(transportOrId, reason = "discard") {
  const transport = typeof transportOrId === "string"
    ? transportsById.get(transportOrId)
    : transportOrId;
  if (!transport) return false;
  return endTransport(transport, reason);
}

function discardAllTransports(reason = "discard-all") {
  let n = 0;
  for (const transport of [...transportsById.values()]) {
    if (endTransport(transport, reason)) n += 1;
  }
  return n;
}

function findTransportById(id) {
  const transport = transportsById.get(id);
  if (!transport || !isTransportSocketHealthy(transport)) return null;
  return transport;
}

/**
 * Find a healthy live or idle transport for an endpoint.
 * Prefers live transports with fewer leases, then idle.
 */
function findTransportByEndpoint(endpoint) {
  const key = buildEndpointKey(endpoint);
  if (!key) return null;
  const ids = transportIdsByEndpoint.get(key);
  if (!ids || ids.size === 0) return null;

  /** @type {object[]} */
  const candidates = [];
  for (const id of ids) {
    const transport = transportsById.get(id);
    if (!transport) continue;
    if (!isTransportSocketHealthy(transport)) {
      // Socket died under park/live without last-lease teardown — drop it so
      // "Until app quit" cannot pin unusable jump chains forever.
      if (transport.state !== "dead") {
        endTransport(transport, "unhealthy-lookup");
      } else {
        unregisterTransport(transport);
      }
      continue;
    }
    if (transport.state === "live" || transport.state === "idle") {
      candidates.push(transport);
    }
  }
  if (candidates.length === 0) return null;

  candidates.sort((a, b) => {
    // Prefer live over idle, then fewer leases.
    if (a.state !== b.state) return a.state === "live" ? -1 : 1;
    return a.leases.size - b.leases.size;
  });
  return candidates[0];
}

function getTransportStats() {
  let live = 0;
  let idle = 0;
  let leases = 0;
  for (const t of transportsById.values()) {
    if (t.state === "live") live += 1;
    else if (t.state === "idle") idle += 1;
    leases += t.leases.size;
  }
  return {
    transports: transportsById.size,
    live,
    idle,
    leases,
    defaultIdleTtlMs,
  };
}

function setDefaultTransportIdleTtlMs(ms) {
  if (!Number.isFinite(ms) || ms < 0) return defaultIdleTtlMs;
  defaultIdleTtlMs = ms;
  // Reschedule already-idle transports so a settings change takes effect without
  // waiting for a new borrow/return cycle.
  for (const transport of transportsById.values()) {
    transport.idleTtlMs = defaultIdleTtlMs;
    if (transport.state === "idle" && transport.leases.size === 0) {
      scheduleIdleEnd(transport);
    }
  }
  return defaultIdleTtlMs;
}

function getDefaultTransportIdleTtlMs() {
  return defaultIdleTtlMs;
}

/**
 * Test helpers: reset registry and optionally inject timers/clock.
 */
function resetSshTransportRegistryForTests(options = {}) {
  discardAllTransports("test-reset");
  transportsById.clear();
  transportIdsByEndpoint.clear();
  leasesById.clear();
  nextLeaseSeq = 0;
  defaultIdleTtlMs = Number.isFinite(options.defaultIdleTtlMs)
    ? options.defaultIdleTtlMs
    : resolveEnvIdleTtlMs(DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS);
  timerApi = {
    setTimeout: options.setTimeout || ((...args) => setTimeout(...args)),
    clearTimeout: options.clearTimeout || ((...args) => clearTimeout(...args)),
  };
  nowFn = options.now || (() => Date.now());
}

// ---------------------------------------------------------------------------
// Compatibility wrappers (pre-registry call sites)
// ---------------------------------------------------------------------------

/**
 * Attach a fresh reference-counted connection descriptor to the session that
 * established the connection. Called once, for the "owner" session, right after
 * its shell channel opens.
 *
 * @param {object} session - the owner session object stored in the sessions Map
 * @param {object} conn - the ssh2 Client for the established connection
 * @param {Array} chainConnections - jump-host connections that must be ended
 *   together with the transport (owned by the connection, not any one channel)
 * @returns {object} transport descriptor (still exposed as session.connRef)
 */
function createConnectionRef(session, conn, chainConnections) {
  const endpoint = session?._reuseEndpoint
    ? {
      hostId: session._reuseEndpoint.hostId,
      hostname: session._reuseEndpoint.hostname,
      port: session._reuseEndpoint.port,
      username: session._reuseEndpoint.username,
      protocol: session._reuseEndpoint.protocol,
      sftpSudo: session._reuseEndpoint.sftpSudo,
      jumpFingerprint: session._reuseEndpoint.jumpFingerprint,
      jumpHosts: session._reuseEndpoint.jumpHosts,
      proxy: session._reuseEndpoint.proxy,
      proxyFingerprint: session._reuseEndpoint.proxyFingerprint,
    }
    : null;

  const transport = createTransport({
    conn,
    chainConnections,
    endpoint,
    idleTtlMs: defaultIdleTtlMs,
  });

  borrowTransport(transport, {
    kind: LEASE_KINDS.shell,
    holder: session,
    leaseId: session?.id ? `shell:${session.id}` : undefined,
    meta: { source: "createConnectionRef" },
  });

  return transport;
}

/**
 * Register an additional session (a reused channel) against an existing
 * connection descriptor, incrementing its reference count.
 *
 * @param {object} session - the new session sharing the connection
 * @param {object} connRef - transport from createConnectionRef / createTransport
 */
function acquireConnectionRef(session, connRef) {
  if (!connRef) return;
  const kind = session?.__sshLeaseKind && LEASE_KINDS[session.__sshLeaseKind]
    ? session.__sshLeaseKind
    : LEASE_KINDS.shell;
  // Prefer stable lease ids when session/sftp ids exist.
  let leaseId;
  if (session?.id && kind === LEASE_KINDS.shell) leaseId = `shell:${session.id}`;
  else if (session?.id && kind === LEASE_KINDS.sftp) leaseId = `sftp:${session.id}`;
  else if (session?.id && kind === LEASE_KINDS.transfer) leaseId = `transfer:${session.id}`;
  else if (session?.id && kind === LEASE_KINDS.forward) leaseId = `forward:${session.id}`;

  // If this holder already has a lease on this transport, no-op (idempotent).
  if (session?._sshTransportLeaseId && connRef.leases?.has(session._sshTransportLeaseId)) {
    return;
  }

  borrowTransport(connRef, {
    kind,
    holder: session,
    leaseId,
    meta: { source: "acquireConnectionRef" },
  });
}

/**
 * Release this session's hold on its shared connection.
 *
 * Decrements the lease count. When it reaches zero the transport enters idle
 * park (or ends immediately when idle TTL is 0). The caller remains responsible
 * for closing this session's own shell stream/channel; this only governs the
 * *shared* transport.
 *
 * Safe to call multiple times for the same session — the lease is detached
 * after the first release so a later duplicate call is a no-op.
 *
 * @param {object} session - the session being torn down
 * @returns {boolean} true if the shared transport was ended by this call
 */
function releaseConnectionRef(session) {
  const result = returnTransport(session);
  return result.ended;
}

/**
 * Find a live, fully-connected session whose authenticated SSH connection can
 * host an additional shell channel. Used to satisfy a reuse request from a
 * duplicated tab.
 *
 * Returns null when the source session is gone, has no usable connection, is
 * not an interactive SSH shell session (e.g. SFTP-only or local sessions), or
 * authenticated to a *different* target than the one now requested, so the
 * caller can safely fall back to establishing a fresh connection.
 *
 * @param {Map} sessions - the shared sessions Map
 * @param {string} sourceSessionId - id of the session to reuse
 * @param {{ hostname: string, port?: number, username?: string }} [requestedTarget]
 * @returns {object|null} the reusable source session, or null
 */
function findReusableSession(sessions, sourceSessionId, requestedTarget) {
  if (!sessions || !sourceSessionId) return null;
  const source = sessions.get(sourceSessionId);
  if (!source) return null;
  // Must be an interactive SSH shell session with a connection we own a
  // reference to. `stream` + `connRef` are only set for shell sessions started
  // through startSession.cjs; SFTP/exec-only or local/telnet/serial sessions
  // won't have both, so they're skipped.
  if (!source.conn || !source.stream || !source.connRef) return null;
  // Registry-managed transports: refuse dead/closing.
  if (source.connRef.state === "dead" || source.connRef.state === "closing") return null;
  // ssh2 Client exposes no public "is connected" flag; rely on the descriptor
  // still being attached (it is nulled out on teardown) plus a non-destroyed
  // underlying socket when ssh2 exposes one.
  const sock = source.conn._sock;
  if (sock && sock.destroyed) return null;

  if (requestedTarget) {
    const ep = source._reuseEndpoint || source.connRef.endpoint;
    // No recorded endpoint -> can't prove it's the same target, so don't reuse.
    if (!ep) return null;
    if (!sameEndpoint(ep, requestedTarget)) return null;
  }

  return source;
}

/**
 * Resolve a transport for channel reuse: prefer an explicit source session,
 * otherwise any healthy transport for the endpoint (including idle park).
 */
function resolveTransportForReuse({
  sessions,
  sourceSessionId,
  endpoint,
} = {}) {
  if (sourceSessionId && sessions) {
    const source = findReusableSession(
      sessions,
      sourceSessionId,
      endpoint || undefined,
    );
    if (source?.connRef && isTransportSocketHealthy(source.connRef)) {
      return source.connRef;
    }
  }
  if (endpoint) {
    return findTransportByEndpoint(endpoint);
  }
  return null;
}

module.exports = {
  // Constants
  DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS,
  STORAGE_KEY_SSH_TRANSPORT_IDLE_TTL_MS,
  LEASE_KINDS,
  // New registry API
  createTransport,
  borrowTransport,
  returnTransport,
  discardTransport,
  discardAllTransports,
  findTransportById,
  findTransportByEndpoint,
  resolveTransportForReuse,
  getTransportStats,
  setDefaultTransportIdleTtlMs,
  getDefaultTransportIdleTtlMs,
  buildEndpointKey,
  normalizeEndpoint,
  sameEndpoint,
  resetSshTransportRegistryForTests,
  // Compat API
  createConnectionRef,
  acquireConnectionRef,
  releaseConnectionRef,
  transferConnectionRef,
  findReusableSession,
};
