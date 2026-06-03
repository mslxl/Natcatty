"use strict";

/**
 * Shared SSH connection pool helpers.
 *
 * Background (issue #1204): "Copy Tab" on an MFA-protected host used to open a
 * brand-new SSH connection, forcing the user through a second MFA prompt. Like
 * Tabby's session-multiplexing, we instead open an additional shell *channel*
 * on the already-authenticated connection. The SSH protocol natively supports
 * many session channels over one transport, so no re-authentication is needed.
 *
 * Multiplexing means several terminal sessions can share one ssh2 `Client`
 * (`conn`) and one underlying jump-host chain. The transport must only be torn
 * down once the *last* of those sessions goes away — closing a single channel
 * (or even the channel of the session that originally opened the connection)
 * must not kill the siblings. We model that with a small reference-counted
 * descriptor shared by every session on the same connection, mirroring Tabby's
 * ref()/unref()/destroy() lifecycle.
 *
 * The same `sessions` Map is shared by sshBridge and terminalBridge (see
 * registerBridges.cjs), so the session objects — and the `connRef` descriptor
 * attached here — are visible to both. That lets terminalBridge's closeSession
 * and sshBridge's own connection event handlers funnel teardown through the
 * same release path.
 */

/**
 * Attach a fresh reference-counted connection descriptor to the session that
 * established the connection. Called once, for the "owner" session, right after
 * its shell channel opens.
 *
 * @param {object} session - the owner session object stored in the sessions Map
 * @param {object} conn - the ssh2 Client for the established connection
 * @param {Array} chainConnections - jump-host connections that must be ended
 *   together with the transport (owned by the connection, not any one channel)
 * @returns {{ count: number, conn: object, chainConnections: Array }} descriptor
 */
function createConnectionRef(session, conn, chainConnections) {
  const connRef = {
    count: 1,
    conn,
    chainConnections: Array.isArray(chainConnections) ? chainConnections : [],
  };
  session.connRef = connRef;
  return connRef;
}

/**
 * Register an additional session (a reused channel) against an existing
 * connection descriptor, incrementing its reference count.
 *
 * @param {object} session - the new session sharing the connection
 * @param {{ count: number }} connRef - descriptor from createConnectionRef
 */
function acquireConnectionRef(session, connRef) {
  if (!connRef) return;
  connRef.count += 1;
  session.connRef = connRef;
}

/**
 * Release this session's hold on its shared connection.
 *
 * Decrements the reference count. When it reaches zero (the last channel is
 * gone) the underlying transport and any jump-host chain connections are torn
 * down. The caller remains responsible for closing this session's own shell
 * stream/channel; this only governs the *shared* transport.
 *
 * Safe to call multiple times for the same session — the descriptor is detached
 * after the first release so a later duplicate call is a no-op (important
 * because both a stream "close" event and an explicit closeSession can fire).
 *
 * @param {object} session - the session being torn down
 * @returns {boolean} true if the shared transport was ended by this call
 */
function releaseConnectionRef(session) {
  const connRef = session && session.connRef;
  if (!connRef) return false;
  // Detach immediately so re-entrant / duplicate releases for the same session
  // cannot double-decrement the shared counter.
  session.connRef = null;

  connRef.count -= 1;
  if (connRef.count > 0) {
    return false;
  }

  try {
    connRef.conn?.end();
  } catch {
    /* connection may already be gone */
  }
  for (const c of connRef.chainConnections) {
    try {
      c?.end();
    } catch {
      /* ignore */
    }
  }
  // Drop references so the descriptor doesn't pin connections after teardown.
  connRef.chainConnections = [];
  connRef.conn = null;
  return true;
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
 * The target check matters because a saved host can be edited after the source
 * tab connected; the duplicate would then carry the new hostname/port/username
 * while the source connection still points at the old machine. Reusing it would
 * silently run commands on the wrong host, so we require an exact endpoint match.
 *
 * @param {Map} sessions - the shared sessions Map
 * @param {string} sourceSessionId - id of the session to reuse
 * @param {{ hostname: string, port?: number, username?: string }} [requestedTarget]
 *   the endpoint the duplicate wants to connect to; when provided, the source's
 *   recorded endpoint must match it
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
  // ssh2 Client exposes no public "is connected" flag; rely on the descriptor
  // still being attached (it is nulled out on teardown) plus a non-destroyed
  // underlying socket when ssh2 exposes one.
  const sock = source.conn._sock;
  if (sock && sock.destroyed) return null;

  if (requestedTarget) {
    const ep = source._reuseEndpoint;
    // No recorded endpoint -> can't prove it's the same target, so don't reuse.
    if (!ep) return null;
    const sameHost = ep.hostname === (requestedTarget.hostname || '');
    const samePort = (ep.port || 22) === (requestedTarget.port || 22);
    const sameUser = (ep.username || 'root') === (requestedTarget.username || 'root');
    if (!sameHost || !samePort || !sameUser) return null;
  }

  return source;
}

module.exports = {
  createConnectionRef,
  acquireConnectionRef,
  releaseConnectionRef,
  findReusableSession,
};
