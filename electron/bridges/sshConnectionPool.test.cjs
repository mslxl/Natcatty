const test = require("node:test");
const assert = require("node:assert/strict");

const {
  createConnectionRef,
  acquireConnectionRef,
  releaseConnectionRef,
  transferConnectionRef,
  findReusableSession,
  createTransport,
  borrowTransport,
  returnTransport,
  discardTransport,
  discardAllTransports,
  findTransportByEndpoint,
  resolveTransportForReuse,
  getTransportStats,
  setDefaultTransportIdleTtlMs,
  getDefaultTransportIdleTtlMs,
  buildEndpointKey,
  endpointAllowsReuse,
  fingerprintAuth,
  resetSshTransportRegistryForTests,
  DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS,
  LEASE_KINDS,
} = require("./sshConnectionPool.cjs");

function makeConn() {
  return {
    ended: 0,
    _sock: { destroyed: false },
    end() { this.ended += 1; },
  };
}

function makeChainConn() {
  return {
    ended: 0,
    end() { this.ended += 1; },
  };
}

/** Product default: TTL 0 parks until quit (never auto-reclaim). */
function useParkForever() {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 0 });
}

/**
 * Short positive TTL with fake timers so tests can fire idle reclaim.
 * Returns the timer list.
 */
function useShortTtlTimers(ttlMs = 1) {
  const timers = [];
  resetSshTransportRegistryForTests({
    defaultIdleTtlMs: ttlMs,
    setTimeout: (fn, ms) => {
      const handle = { fn, ms, cleared: false };
      timers.push(handle);
      return handle;
    },
    clearTimeout: (handle) => {
      if (handle) handle.cleared = true;
    },
  });
  return timers;
}

function fireIdleTimers(timers) {
  for (const t of [...timers]) {
    if (!t.cleared && typeof t.fn === "function") t.fn();
  }
}

test.beforeEach(() => {
  useParkForever();
});

test.afterEach(() => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 0 });
});

test("releaseConnectionRef parks on last channel when TTL is 0 (until quit)", () => {
  const conn = makeConn();
  const chain = [makeChainConn(), makeChainConn()];
  const owner = {};
  const reused = {};

  const connRef = createConnectionRef(owner, conn, chain);
  assert.equal(connRef.count, 1);

  acquireConnectionRef(reused, connRef);
  assert.equal(connRef.count, 2);
  assert.equal(reused.connRef, connRef);

  let ended = releaseConnectionRef(reused);
  assert.equal(ended, false);
  assert.equal(conn.ended, 0);
  assert.equal(reused.connRef, null);

  // Last lease parks (TTL 0 = never auto-end).
  ended = releaseConnectionRef(owner);
  assert.equal(ended, false);
  assert.equal(conn.ended, 0);
  assert.equal(connRef.state, "idle");
  assert.equal(owner.connRef, null);

  assert.equal(discardTransport(connRef), true);
  assert.equal(conn.ended, 1);
  assert.equal(chain[0].ended, 1);
  assert.equal(chain[1].ended, 1);
});

test("releaseConnectionRef keeps siblings alive when the owner closes first", () => {
  const timers = useShortTtlTimers(1);
  const conn = makeConn();
  const owner = {};
  const reused = {};
  const connRef = createConnectionRef(owner, conn, []);
  acquireConnectionRef(reused, connRef);

  assert.equal(releaseConnectionRef(owner), false);
  assert.equal(conn.ended, 0, "connection must survive for the remaining copy");

  // Last holder parks; fire TTL to end.
  assert.equal(releaseConnectionRef(reused), false);
  assert.equal(connRef.state, "idle");
  fireIdleTimers(timers);
  assert.equal(conn.ended, 1);
});

test("releaseConnectionRef is idempotent per session", () => {
  const conn = makeConn();
  const owner = {};
  const connRef = createConnectionRef(owner, conn, []);
  acquireConnectionRef({}, connRef); // bump count to 2 so a double release can't reach 0 by itself

  assert.equal(releaseConnectionRef(owner), false);
  assert.equal(releaseConnectionRef(owner), false);
  assert.equal(connRef.count, 1);
  assert.equal(conn.ended, 0);
});

test("releaseConnectionRef on a session without a descriptor is a safe no-op", () => {
  assert.equal(releaseConnectionRef({}), false);
  assert.equal(releaseConnectionRef(null), false);
  assert.equal(releaseConnectionRef(undefined), false);
});

test("single-channel connection parks on release when TTL is 0", () => {
  const conn = makeConn();
  const chain = [makeChainConn()];
  const owner = {};
  const transport = createConnectionRef(owner, conn, chain);

  assert.equal(releaseConnectionRef(owner), false);
  assert.equal(transport.state, "idle");
  assert.equal(conn.ended, 0);
  assert.equal(discardTransport(transport), true);
  assert.equal(conn.ended, 1);
  assert.equal(chain[0].ended, 1);
});

test("findReusableSession returns a live interactive SSH shell session", () => {
  const sessions = new Map();
  const source = {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1, state: "live" },
  };
  sessions.set("src", source);

  assert.equal(findReusableSession(sessions, "src"), source);
});

test("findReusableSession rejects sessions missing a usable connection", () => {
  const sessions = new Map();

  sessions.set("no-stream", { conn: {}, connRef: { count: 1, state: "live" } });
  assert.equal(findReusableSession(sessions, "no-stream"), null);

  sessions.set("no-ref", { conn: {}, stream: {} });
  assert.equal(findReusableSession(sessions, "no-ref"), null);

  sessions.set("no-conn", { stream: {}, connRef: { count: 1, state: "live" } });
  assert.equal(findReusableSession(sessions, "no-conn"), null);

  sessions.set("dead", {
    conn: { _sock: { destroyed: true } },
    stream: {},
    connRef: { count: 1, state: "live" },
  });
  assert.equal(findReusableSession(sessions, "dead"), null);
});

test("findReusableSession handles missing inputs gracefully", () => {
  assert.equal(findReusableSession(null, "x"), null);
  assert.equal(findReusableSession(new Map(), ""), null);
  assert.equal(findReusableSession(new Map(), "absent"), null);
});

test("findReusableSession enforces an exact target endpoint match", () => {
  const sessions = new Map();
  const source = {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1, state: "live" },
    _reuseEndpoint: { hostname: "10.0.0.1", port: 22, username: "alice" },
  };
  sessions.set("src", source);

  assert.equal(
    findReusableSession(sessions, "src", { hostname: "10.0.0.1", port: 22, username: "alice" }),
    source,
  );
  assert.equal(
    findReusableSession(sessions, "src", { hostname: "10.0.0.1", username: "alice" }),
    source,
  );
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.2", port: 22, username: "alice" }), null);
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.1", port: 2222, username: "alice" }), null);
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.1", port: 22, username: "bob" }), null);

  sessions.set("root-src", {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1, state: "live" },
    _reuseEndpoint: { hostname: "10.0.0.9", port: 22, username: "root" },
  });
  assert.ok(findReusableSession(sessions, "root-src", { hostname: "10.0.0.9" }));
});

test("findReusableSession refuses reuse when the source has no recorded endpoint", () => {
  const sessions = new Map();
  sessions.set("src", {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1, state: "live" },
  });
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.1" }), null);
  assert.ok(findReusableSession(sessions, "src"));
});

// ---------------------------------------------------------------------------
// Transport registry + idle park
// ---------------------------------------------------------------------------

test("default idle TTL constant is 5 minutes", () => {
  assert.equal(DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS, 5 * 60_000);
});

test("buildEndpointKey normalizes port and username defaults", () => {
  assert.equal(
    buildEndpointKey({ hostname: "a.example", port: 22, username: "root" }),
    buildEndpointKey({ hostname: "a.example" }),
  );
  assert.notEqual(
    buildEndpointKey({ hostname: "a.example", username: "alice" }),
    buildEndpointKey({ hostname: "a.example", username: "bob" }),
  );
  assert.notEqual(
    buildEndpointKey({ hostname: "a.example", jumpFingerprint: "bastion" }),
    buildEndpointKey({ hostname: "a.example", jumpFingerprint: "other" }),
  );
});

test("buildEndpointKey distinguishes jump host chains", () => {
  assert.notEqual(
    buildEndpointKey({
      hostname: "target",
      jumpHosts: [{ hostname: "bastion-a", port: 22, username: "j" }],
    }),
    buildEndpointKey({
      hostname: "target",
      jumpHosts: [{ hostname: "bastion-b", port: 22, username: "j" }],
    }),
  );
});

test("buildEndpointKey scopes vault hostId so different profiles never share", () => {
  assert.notEqual(
    buildEndpointKey({ hostId: "host-a", hostname: "same.example", username: "root" }),
    buildEndpointKey({ hostId: "host-b", hostname: "same.example", username: "root" }),
  );
  // Missing hostId still indexes under a distinct profile slot ("-").
  assert.notEqual(
    buildEndpointKey({ hostname: "same.example", username: "root" }),
    buildEndpointKey({ hostId: "host-a", hostname: "same.example", username: "root" }),
  );
});

test("fingerprintAuth changes when credential material rotates under same keyId", () => {
  const base = {
    hostname: "h.example",
    username: "alice",
    authType: "key",
    keyId: "key-1",
  };
  assert.notEqual(
    fingerprintAuth({ ...base, privateKey: "-----BEGIN OLD-----" }),
    fingerprintAuth({ ...base, privateKey: "-----BEGIN NEW-----" }),
  );
  assert.notEqual(
    fingerprintAuth({ ...base, password: "secret-a" }),
    fingerprintAuth({ ...base, password: "secret-b" }),
  );
  assert.notEqual(
    fingerprintAuth({ ...base, certificate: "old-cert" }),
    fingerprintAuth({ ...base, certificate: "new-cert" }),
  );
  // Same material is stable.
  assert.equal(
    fingerprintAuth({ ...base, privateKey: "same" }),
    fingerprintAuth({ ...base, privateKey: "same" }),
  );
});

test("endpointAllowsReuse: shell exact vs channel asymmetric for agentForwarding", () => {
  const base = { hostname: "a.example", username: "root", port: 22 };
  // Channel (default): request needs ForwardAgent, existing never enabled it -> reject.
  assert.equal(
    endpointAllowsReuse({ ...base, agentForwarding: true }, { ...base, agentForwarding: false }),
    false,
  );
  // Channel: request does not need ForwardAgent; existing with it is fine (SFTP/PF).
  assert.equal(
    endpointAllowsReuse({ ...base, agentForwarding: false }, { ...base, agentForwarding: true }),
    true,
  );
  // Shell: disabling ForwardAgent must not reattach to a warm agent-forward conn.
  assert.equal(
    endpointAllowsReuse(
      { ...base, agentForwarding: false },
      { ...base, agentForwarding: true },
      "shell",
    ),
    false,
  );
  assert.equal(
    endpointAllowsReuse(
      { ...base, agentForwarding: true },
      { ...base, agentForwarding: true },
      "shell",
    ),
    true,
  );
  // agentForwarding does not change the shared endpoint key (park index stays stable).
  assert.equal(
    buildEndpointKey({ ...base, agentForwarding: true }),
    buildEndpointKey({ ...base, agentForwarding: false }),
  );
});

test("findTransportByEndpoint shell kind refuses mismatched agentForwarding", () => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 0 });
  const holder = { id: "s1" };
  const withFwd = createTransport({
    conn: makeConn(),
    endpoint: { hostname: "fwd.example", username: "root", agentForwarding: true },
  });
  borrowTransport(withFwd, { kind: "shell", holder, leaseId: "shell:s1" });
  returnTransport(holder); // park idle forever

  // Shell open after user disables ForwardAgent must not reuse.
  assert.equal(
    findTransportByEndpoint(
      { hostname: "fwd.example", username: "root", agentForwarding: false },
      { kind: "shell" },
    ),
    null,
  );
  // SFTP/PF channel reuse may still borrow the ForwardAgent transport.
  assert.ok(
    findTransportByEndpoint({ hostname: "fwd.example", username: "root", agentForwarding: false }),
  );
  assert.ok(
    findTransportByEndpoint(
      { hostname: "fwd.example", username: "root", agentForwarding: true },
      { kind: "shell" },
    ),
  );
  discardTransport(withFwd);
});

test("last return parks with positive TTL then ends when timer fires", () => {
  const timers = useShortTtlTimers(60_000);

  const conn = makeConn();
  const holder = { id: "shell-1" };
  const transport = createTransport({
    conn,
    endpoint: { hostname: "10.0.0.1", username: "alice" },
  });
  borrowTransport(transport, { kind: LEASE_KINDS.shell, holder });

  const result = returnTransport(holder);
  assert.equal(result.released, true);
  assert.equal(result.ended, false);
  assert.equal(result.idle, true);
  assert.equal(conn.ended, 0);
  assert.equal(transport.state, "idle");
  assert.equal(timers.length, 1);
  assert.equal(timers[0].ms, 60_000);

  timers[0].fn();
  assert.equal(conn.ended, 1);
  assert.equal(transport.state, "dead");
});

test("TTL 0 parks forever without scheduling a timer", () => {
  const timers = [];
  resetSshTransportRegistryForTests({
    defaultIdleTtlMs: 0,
    setTimeout: (fn, ms) => {
      const handle = { fn, ms, cleared: false };
      timers.push(handle);
      return handle;
    },
    clearTimeout: (handle) => {
      if (handle) handle.cleared = true;
    },
  });

  const conn = makeConn();
  const transport = createTransport({ conn, endpoint: { hostname: "forever.example" } });
  const holder = {};
  borrowTransport(transport, { holder });
  const result = returnTransport(holder);
  assert.equal(result.idle, true);
  assert.equal(result.ended, false);
  assert.equal(timers.length, 0, "never-reclaim must not schedule idle end");
  assert.equal(conn.ended, 0);
  assert.equal(transport.state, "idle");
  assert.ok(findTransportByEndpoint({ hostname: "forever.example" }));
});

test("borrow while idle cancels park and reuses the same conn", () => {
  const timers = useShortTtlTimers(60_000);

  const conn = makeConn();
  const endpoint = { hostname: "10.0.0.2", port: 22, username: "root" };
  const transport = createTransport({ conn, endpoint });
  const first = {};
  borrowTransport(transport, { kind: LEASE_KINDS.shell, holder: first });
  returnTransport(first);
  assert.equal(transport.state, "idle");
  assert.equal(timers[0].cleared, false);

  const found = findTransportByEndpoint(endpoint);
  assert.equal(found, transport);

  const second = {};
  borrowTransport(found, { kind: LEASE_KINDS.sftp, holder: second, leaseId: "sftp:panel-1" });
  assert.equal(transport.state, "live");
  assert.equal(timers[0].cleared, true);
  assert.equal(conn.ended, 0);
  assert.equal(transport.count, 1);
  assert.equal(second.connRef, transport);
});

test("findTransportByEndpoint prefers live transport over idle", () => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 60_000 });
  const endpoint = { hostname: "shared.example", username: "u" };

  const idleConn = makeConn();
  const idleTransport = createTransport({ conn: idleConn, endpoint });
  const idleHolder = {};
  borrowTransport(idleTransport, { holder: idleHolder });
  returnTransport(idleHolder);
  assert.equal(idleTransport.state, "idle");

  const liveConn = makeConn();
  const liveTransport = createTransport({ conn: liveConn, endpoint });
  borrowTransport(liveTransport, { holder: {} });

  assert.equal(findTransportByEndpoint(endpoint), liveTransport);
});

test("sftp and shell leases share one transport until both return", () => {
  const timers = useShortTtlTimers(1);
  const conn = makeConn();
  const session = { id: "term-1", _reuseEndpoint: { hostname: "h", port: 22, username: "root" } };
  const transport = createConnectionRef(session, conn, []);
  const sftpHolder = { id: "sftp-1", __sshLeaseKind: LEASE_KINDS.sftp };
  acquireConnectionRef(sftpHolder, transport);
  assert.equal(transport.count, 2);

  assert.equal(releaseConnectionRef(session), false);
  assert.equal(conn.ended, 0);
  assert.equal(releaseConnectionRef(sftpHolder), false);
  assert.equal(transport.state, "idle");
  fireIdleTimers(timers);
  assert.equal(conn.ended, 1);
});

test("resolveTransportForReuse finds idle transport by endpoint without a session", () => {
  const timers = useShortTtlTimers(30_000);

  const conn = makeConn();
  const endpoint = { hostname: "parked.example", username: "ops" };
  const transport = createTransport({ conn, endpoint });
  const holder = {};
  borrowTransport(transport, { holder });
  returnTransport(holder);

  const resolved = resolveTransportForReuse({ endpoint });
  assert.equal(resolved, transport);
  assert.equal(resolved.state, "idle");
  assert.ok(timers.length >= 1);
});

test("findTransportByEndpoint ends transports whose socket is destroyed", () => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 0 });
  const endpoint = { hostId: "h1", hostname: "dead.example", username: "root" };
  const conn = makeConn();
  const transport = createTransport({ conn, endpoint });
  const holder = {};
  borrowTransport(transport, { holder });
  returnTransport(holder);
  assert.equal(transport.state, "idle");
  conn._sock.destroyed = true;
  assert.equal(findTransportByEndpoint(endpoint), null);
  assert.equal(conn.ended, 1);
  assert.equal(getTransportStats().transports, 0);
});

test("discardTransport force-ends and unregisters", () => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 60_000 });
  const conn = makeConn();
  const endpoint = { hostname: "x.example" };
  const transport = createTransport({ conn, endpoint });
  borrowTransport(transport, { holder: {} });

  assert.equal(discardTransport(transport), true);
  assert.equal(conn.ended, 1);
  assert.equal(findTransportByEndpoint(endpoint), null);
  assert.equal(getTransportStats().transports, 0);
});

test("discardAllTransports clears the registry", () => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 60_000 });
  const a = createTransport({ conn: makeConn(), endpoint: { hostname: "a" } });
  const b = createTransport({ conn: makeConn(), endpoint: { hostname: "b" } });
  borrowTransport(a, { holder: {} });
  borrowTransport(b, { holder: {} });
  assert.equal(discardAllTransports(), 2);
  assert.equal(getTransportStats().transports, 0);
});

test("setDefaultTransportIdleTtlMs updates default and reschedules idle transports", () => {
  const timers = [];
  resetSshTransportRegistryForTests({
    defaultIdleTtlMs: 60_000,
    setTimeout: (fn, ms) => {
      const handle = { fn, ms, cleared: false };
      timers.push(handle);
      return handle;
    },
    clearTimeout: (handle) => {
      if (handle) handle.cleared = true;
    },
  });

  const conn = makeConn();
  const transport = createTransport({ conn, endpoint: { hostname: "resched.example" } });
  const holder = {};
  borrowTransport(transport, { holder });
  returnTransport(holder);
  assert.equal(transport.state, "idle");
  assert.equal(timers[0].ms, 60_000);

  setDefaultTransportIdleTtlMs(5_000);
  assert.equal(getDefaultTransportIdleTtlMs(), 5_000);
  assert.equal(timers[0].cleared, true, "old idle timer must be cancelled");
  const last = timers[timers.length - 1];
  assert.equal(last.ms, 5_000);
  assert.equal(transport.idleTtlMs, 5_000);
});

test("createConnectionRef indexes endpoint from session._reuseEndpoint including jumpHosts and hostId", () => {
  resetSshTransportRegistryForTests({ defaultIdleTtlMs: 60_000 });
  const conn = makeConn();
  const session = {
    id: "s1",
    _reuseEndpoint: {
      hostId: "vault-host-1",
      hostname: "indexed.example",
      port: 2222,
      username: "deploy",
      jumpHosts: [{ hostname: "bastion", port: 22, username: "jump" }],
    },
  };
  createConnectionRef(session, conn, []);
  const found = findTransportByEndpoint({
    hostId: "vault-host-1",
    hostname: "indexed.example",
    port: 2222,
    username: "deploy",
    jumpHosts: [{ hostname: "bastion", port: 22, username: "jump" }],
  });
  assert.ok(found);
  assert.equal(found.conn, conn);
  assert.equal(
    findTransportByEndpoint({
      hostId: "vault-host-1",
      hostname: "indexed.example",
      port: 2222,
      username: "deploy",
    }),
    null,
    "missing jump chain must not match",
  );
  assert.equal(
    findTransportByEndpoint({
      hostId: "other-host",
      hostname: "indexed.example",
      port: 2222,
      username: "deploy",
      jumpHosts: [{ hostname: "bastion", port: 22, username: "jump" }],
    }),
    null,
    "different vault hostId must not match",
  );
});

test("transferConnectionRef rebinds a lease without changing count", () => {
  const timers = useShortTtlTimers(1);
  const conn = makeConn();
  const transport = createTransport({ conn, endpoint: { hostname: "t.example" } });
  const temp = {};
  const session = { id: "shell-copy" };
  borrowTransport(transport, { kind: LEASE_KINDS.shell, holder: temp });
  assert.equal(transport.count, 1);

  assert.equal(transferConnectionRef(temp, session), true);
  assert.equal(transport.count, 1);
  assert.equal(temp.connRef, null);
  assert.equal(session.connRef, transport);
  assert.ok(session._sshTransportLeaseId);

  assert.equal(releaseConnectionRef(session), false);
  assert.equal(transport.state, "idle");
  fireIdleTimers(timers);
  assert.equal(conn.ended, 1);
});
