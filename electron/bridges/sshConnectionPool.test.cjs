const test = require("node:test");
const assert = require("node:assert/strict");

const {
  createConnectionRef,
  acquireConnectionRef,
  releaseConnectionRef,
  findReusableSession,
} = require("./sshConnectionPool.cjs");

function makeConn() {
  return {
    ended: 0,
    end() { this.ended += 1; },
  };
}

function makeChainConn() {
  return {
    ended: 0,
    end() { this.ended += 1; },
  };
}

test("releaseConnectionRef ends transport only when the last channel closes", () => {
  const conn = makeConn();
  const chain = [makeChainConn(), makeChainConn()];
  const owner = {};
  const reused = {};

  const connRef = createConnectionRef(owner, conn, chain);
  assert.equal(connRef.count, 1);

  acquireConnectionRef(reused, connRef);
  assert.equal(connRef.count, 2);
  assert.equal(reused.connRef, connRef);

  // Closing the reused channel must not end the shared transport.
  let ended = releaseConnectionRef(reused);
  assert.equal(ended, false);
  assert.equal(conn.ended, 0);
  assert.equal(reused.connRef, null);

  // Closing the last (owner) channel ends the transport + chain.
  ended = releaseConnectionRef(owner);
  assert.equal(ended, true);
  assert.equal(conn.ended, 1);
  assert.equal(chain[0].ended, 1);
  assert.equal(chain[1].ended, 1);
  assert.equal(owner.connRef, null);
});

test("releaseConnectionRef keeps siblings alive when the owner closes first", () => {
  const conn = makeConn();
  const owner = {};
  const reused = {};
  const connRef = createConnectionRef(owner, conn, []);
  acquireConnectionRef(reused, connRef);

  // Owner (the session that opened the connection) closes while a copy is live.
  assert.equal(releaseConnectionRef(owner), false);
  assert.equal(conn.ended, 0, "connection must survive for the remaining copy");

  // The remaining copy is the last holder and ends the transport.
  assert.equal(releaseConnectionRef(reused), true);
  assert.equal(conn.ended, 1);
});

test("releaseConnectionRef is idempotent per session", () => {
  const conn = makeConn();
  const owner = {};
  const connRef = createConnectionRef(owner, conn, []);
  acquireConnectionRef({}, connRef); // bump count to 2 so a double release can't reach 0 by itself

  assert.equal(releaseConnectionRef(owner), false);
  // A second release for the same session must be a no-op (no double decrement).
  assert.equal(releaseConnectionRef(owner), false);
  assert.equal(connRef.count, 1);
  assert.equal(conn.ended, 0);
});

test("releaseConnectionRef on a session without a descriptor is a safe no-op", () => {
  assert.equal(releaseConnectionRef({}), false);
  assert.equal(releaseConnectionRef(null), false);
  assert.equal(releaseConnectionRef(undefined), false);
});

test("single-channel connection ends immediately on release", () => {
  const conn = makeConn();
  const chain = [makeChainConn()];
  const owner = {};
  createConnectionRef(owner, conn, chain);

  assert.equal(releaseConnectionRef(owner), true);
  assert.equal(conn.ended, 1);
  assert.equal(chain[0].ended, 1);
});

test("findReusableSession returns a live interactive SSH shell session", () => {
  const sessions = new Map();
  const source = {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1 },
  };
  sessions.set("src", source);

  assert.equal(findReusableSession(sessions, "src"), source);
});

test("findReusableSession rejects sessions missing a usable connection", () => {
  const sessions = new Map();

  // Missing stream (e.g. SFTP-only session)
  sessions.set("no-stream", { conn: {}, connRef: { count: 1 } });
  assert.equal(findReusableSession(sessions, "no-stream"), null);

  // Missing connRef (not started through the shell path / already torn down)
  sessions.set("no-ref", { conn: {}, stream: {} });
  assert.equal(findReusableSession(sessions, "no-ref"), null);

  // Missing conn (local/telnet/serial session)
  sessions.set("no-conn", { stream: {}, connRef: { count: 1 } });
  assert.equal(findReusableSession(sessions, "no-conn"), null);

  // Destroyed underlying socket
  sessions.set("dead", {
    conn: { _sock: { destroyed: true } },
    stream: {},
    connRef: { count: 1 },
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
    connRef: { count: 1 },
    _reuseEndpoint: { hostname: "10.0.0.1", port: 22, username: "alice" },
  };
  sessions.set("src", source);

  // Exact match -> reusable.
  assert.equal(
    findReusableSession(sessions, "src", { hostname: "10.0.0.1", port: 22, username: "alice" }),
    source,
  );
  // Omitted port defaults to 22 and still matches.
  assert.equal(
    findReusableSession(sessions, "src", { hostname: "10.0.0.1", username: "alice" }),
    source,
  );
  // Different host / port / user -> not reusable.
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.2", port: 22, username: "alice" }), null);
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.1", port: 2222, username: "alice" }), null);
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.1", port: 22, username: "bob" }), null);

  // A root source matches a request that omits the username (defaults to root).
  sessions.set("root-src", {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1 },
    _reuseEndpoint: { hostname: "10.0.0.9", port: 22, username: "root" },
  });
  assert.ok(findReusableSession(sessions, "root-src", { hostname: "10.0.0.9" }));
});

test("findReusableSession refuses reuse when the source has no recorded endpoint", () => {
  const sessions = new Map();
  sessions.set("src", {
    conn: { _sock: { destroyed: false } },
    stream: {},
    connRef: { count: 1 },
    // no _reuseEndpoint
  });
  // With a requested target we can't prove same-host, so refuse.
  assert.equal(findReusableSession(sessions, "src", { hostname: "10.0.0.1" }), null);
  // Without a requested target (legacy callers), endpoint check is skipped.
  assert.ok(findReusableSession(sessions, "src"));
});
