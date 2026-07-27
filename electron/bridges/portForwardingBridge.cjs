/**
 * Port Forwarding Bridge - Handles SSH port forwarding tunnels
 * Extracted from main.cjs for single responsibility
 */

const net = require("node:net");
require("./boringSslDhCompat.cjs").installBoringSslDhCompat();
const { Client: SSHClient } = require("ssh2");
const { NetcattyAgent } = require("./netcattyAgent.cjs");
const keyboardInteractiveHandler = require("./keyboardInteractiveHandler.cjs");
const { connectThroughChain, buildAlgorithms } = require("./sshBridge.cjs");
const { resolveSshConnectionTimeouts } = require("./sshBridge/startSession.cjs");
const hostKeyVerifier = require("./hostKeyVerifier.cjs");
const { createProxySocket, runWhenProxyConnectionReady } = require("./proxyUtils.cjs");
const { 
  buildAuthHandler, 
  createKeyboardInteractiveHandler, 
  applyAuthToConnOpts,
  shouldSkipKiPasswordAutoFill,
  findAllDefaultPrivateKeys: findAllDefaultPrivateKeysFromHelper,
  preparePrivateKeyForAuth,
  loadFirstIdentityFileForAuth,
  getAvailableAgentSocket,
  prepareSystemSshAgentForAuth,
  isPassphraseCancelledError,
} = require("./sshAuthHelper.cjs");
const {
  createTransport,
  borrowTransport,
  returnTransport,
  findTransportByEndpoint,
  LEASE_KINDS,
} = require("./sshConnectionPool.cjs");

// Active port forwarding tunnels
const portForwardingTunnels = new Map();

function buildPortForwardEndpoint({ hostname, port, username, jumpHosts }) {
  return {
    hostname,
    port: port || 22,
    username: username || "root",
    jumpHosts: Array.isArray(jumpHosts) ? jumpHosts : [],
  };
}

/**
 * Release SSH for a tunnel: if the tunnel holds a transport lease, return it
 * (may idle-park). Otherwise end the dedicated Client as before.
 */
function releaseTunnelSsh(tunnel) {
  if (!tunnel) return;
  if (tunnel.sshTransportManaged) {
    try {
      returnTransport(tunnel);
    } catch {
      /* ignore */
    }
    tunnel.conn = null;
    tunnel.chainConnections = [];
    tunnel.sshTransportManaged = false;
    return;
  }
  if (Array.isArray(tunnel.chainConnections)) {
    cleanupChainConnections(tunnel.chainConnections);
    tunnel.chainConnections = [];
  }
  if (tunnel.conn) {
    try { tunnel.conn.end(); } catch { /* ignore */ }
    tunnel.conn = null;
  }
}

/**
 * Register an authenticated SSH connection as a shared transport + forward lease.
 * Lets terminal/SFTP later borrow the same conn; stop returns the lease only.
 */
function attachForwardTransportLease(tunnel, conn, chainConnections, endpoint) {
  if (!tunnel || !conn) return;
  const transport = createTransport({
    conn,
    chainConnections: Array.isArray(chainConnections) ? chainConnections : [],
    endpoint,
  });
  borrowTransport(transport, {
    kind: LEASE_KINDS.forward,
    holder: tunnel,
    leaseId: `forward:${tunnel.tunnelId || tunnel.id || "unknown"}`,
    meta: { source: "port-forward" },
  });
  tunnel.sshTransportManaged = true;
  tunnel.conn = conn;
  // Chain is owned by the transport registry now.
  tunnel.chainConnections = [];
}

/**
 * Bind local/remote/dynamic forwarding onto an already-authenticated conn.
 * Used for both shared-transport and post-dial paths.
 */
function bindPortForwardChannels({
  type,
  conn,
  tunnelId,
  tunnelState,
  sender,
  bindAddress,
  localPort,
  remoteHost,
  remotePort,
  chainConnections,
  sendStatus,
  releaseOnError = false,
  endpoint = null,
  registerTransport = false,
}) {
  return new Promise((resolve, reject) => {
    const fail = (err) => {
      sendStatus?.("error", err?.message || String(err));
      if (releaseOnError) {
        releaseTunnelSsh(tunnelState);
      } else if (!tunnelState.sshTransportManaged) {
        try { conn.end(); } catch { /* ignore */ }
        cleanupChainConnections(chainConnections);
      }
      reject(err);
    };

    if (type === "local") {
      const server = net.createServer((socket) => {
        conn.forwardOut(
          bindAddress,
          localPort,
          remoteHost,
          remotePort,
          (err, stream) => {
            if (err) {
              console.error(`[PortForward] Forward error:`, err.message);
              socket.end();
              return;
            }
            socket.pipe(stream).pipe(socket);
            socket.on("error", (e) => console.warn("[PortForward] Socket error:", e.message));
            stream.on("error", (e) => console.warn("[PortForward] Stream error:", e.message));
          },
        );
      });

      server.on("error", (err) => {
        console.error(`[PortForward] Server error:`, err.message);
        fail(err);
      });

      server.listen(localPort, bindAddress, () => {
        console.log(`[PortForward] Local forwarding active: ${bindAddress}:${localPort} -> ${remoteHost}:${remotePort}`);
        tunnelState.type = "local";
        tunnelState.conn = conn;
        tunnelState.server = server;
        tunnelState.chainConnections = chainConnections;
        tunnelState.status = "active";
        tunnelState.webContentsId = sender.id;
        tunnelState.pendingConn = null;
        if (registerTransport && endpoint && !tunnelState.sshTransportManaged) {
          attachForwardTransportLease(tunnelState, conn, chainConnections, endpoint);
        }
        portForwardingTunnels.set(tunnelId, tunnelState);
        sendStatus?.("active");
        resolve({ tunnelId, success: true });
      });
      return;
    }

    if (type === "remote") {
      conn.forwardIn(bindAddress, localPort, (err) => {
        if (err) {
          console.error(`[PortForward] Remote forward error:`, err.message);
          fail(err);
          return;
        }

        console.log(`[PortForward] Remote forwarding active: remote ${bindAddress}:${localPort} -> local ${remoteHost}:${remotePort}`);
        tunnelState.type = "remote";
        tunnelState.conn = conn;
        tunnelState.server = null;
        tunnelState.bindAddress = bindAddress;
        tunnelState.localPort = localPort;
        tunnelState.chainConnections = chainConnections;
        tunnelState.status = "active";
        tunnelState.webContentsId = sender.id;
        tunnelState.pendingConn = null;
        if (registerTransport && endpoint && !tunnelState.sshTransportManaged) {
          attachForwardTransportLease(tunnelState, conn, chainConnections, endpoint);
        }
        portForwardingTunnels.set(tunnelId, tunnelState);
        sendStatus?.("active");
        resolve({ tunnelId, success: true });
      });

      conn.on("tcp connection", (info, accept) => {
        const stream = accept();
        const socket = net.connect(remotePort, remoteHost || "127.0.0.1", () => {
          stream.pipe(socket).pipe(stream);
        });
        socket.on("error", (e) => {
          console.warn("[PortForward] Local socket error:", e.message);
          stream.end();
        });
        stream.on("error", (e) => {
          console.warn("[PortForward] Remote stream error:", e.message);
          socket.end();
        });
      });
      return;
    }

    if (type === "dynamic") {
      const server = net.createServer((socket) => {
        socket.once("data", (data) => {
          if (data[0] !== 0x05) {
            socket.end();
            return;
          }
          socket.write(Buffer.from([0x05, 0x00]));
          socket.once("data", (request) => {
            if (request[0] !== 0x05 || request[1] !== 0x01) {
              socket.write(Buffer.from([0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0]));
              socket.end();
              return;
            }

            let targetHost;
            let targetPort;
            const addressType = request[3];

            if (addressType === 0x01) {
              targetHost = `${request[4]}.${request[5]}.${request[6]}.${request[7]}`;
              targetPort = request.readUInt16BE(8);
            } else if (addressType === 0x03) {
              const domainLength = request[4];
              targetHost = request.slice(5, 5 + domainLength).toString();
              targetPort = request.readUInt16BE(5 + domainLength);
            } else if (addressType === 0x04) {
              socket.write(Buffer.from([0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0]));
              socket.end();
              return;
            } else {
              socket.write(Buffer.from([0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0]));
              socket.end();
              return;
            }

            conn.forwardOut(
              bindAddress,
              0,
              targetHost,
              targetPort,
              (err, stream) => {
                if (err) {
                  socket.write(Buffer.from([0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0]));
                  socket.end();
                  return;
                }
                const reply = Buffer.alloc(10);
                reply[0] = 0x05;
                reply[1] = 0x00;
                reply[2] = 0x00;
                reply[3] = 0x01;
                reply.writeUInt16BE(0, 8);
                socket.write(reply);
                socket.pipe(stream).pipe(socket);
                socket.on("error", () => stream.end());
                stream.on("error", () => socket.end());
              },
            );
          });
        });
      });

      server.on("error", (err) => {
        console.error(`[PortForward] SOCKS server error:`, err.message);
        fail(err);
      });

      server.listen(localPort, bindAddress, () => {
        console.log(`[PortForward] Dynamic SOCKS5 proxy active on ${bindAddress}:${localPort}`);
        tunnelState.type = "dynamic";
        tunnelState.conn = conn;
        tunnelState.server = server;
        tunnelState.chainConnections = chainConnections;
        tunnelState.status = "active";
        tunnelState.webContentsId = sender.id;
        tunnelState.pendingConn = null;
        if (registerTransport && endpoint && !tunnelState.sshTransportManaged) {
          attachForwardTransportLease(tunnelState, conn, chainConnections, endpoint);
        }
        portForwardingTunnels.set(tunnelId, tunnelState);
        sendStatus?.("active");
        resolve({ tunnelId, success: true });
      });
      return;
    }

    reject(new Error(`Unknown forwarding type: ${type}`));
  });
}

function cleanupChainConnections(connections) {
  if (!Array.isArray(connections)) return;
  for (const chainConn of connections) {
    try { chainConn.end(); } catch { /* ignore */ }
  }
}

function isTunnelCancelled(tunnelState) {
  return Boolean(tunnelState?.cancelled);
}

function isReusableTunnelStatus(status) {
  return status === 'active' || status === 'connecting';
}

function publishTunnelStatus(tunnelId, tunnel, status, error = null) {
  if (!tunnel) return;
  tunnel.status = status;
  tunnel.error = error || undefined;
  const subscribers = tunnel.subscribers instanceof Map
    ? Array.from(tunnel.subscribers.entries())
    : [];
  for (const [subscriberId, subscriber] of subscribers) {
    if (subscriber?.isDestroyed?.()) {
      tunnel.subscribers.delete(subscriberId);
      continue;
    }
    safeSend(subscriber, "netcatty:portforward:status", { tunnelId, status, error });
  }
}

function shouldFinalizeTunnelClose(tunnel) {
  return !tunnel?.cleanupFailed && !tunnel?.cleanupInProgress;
}

function cancelTunnel(tunnelId, tunnel, sendStatus, { deleteEntry = false } = {}) {
  if (!tunnel) return;
  const errors = [];
  const cleanup = (label, action) => {
    try {
      action();
      return true;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      errors.push(`${label}: ${message}`);
      return false;
    }
  };
  tunnel.cancelled = true;
  tunnel.cleanupInProgress = true;
  keyboardInteractiveHandler.cancelRequestsForSession(tunnelId, "tunnel-stopped");
  if (tunnel.server) {
    if (cleanup('server', () => tunnel.server.close())) tunnel.server = null;
  }
  if (tunnel.passphraseAbortController && !tunnel.passphraseAbortController.signal.aborted) {
    cleanup('passphrase prompt', () => tunnel.passphraseAbortController.abort());
  }
  if (tunnel.pendingConn) {
    if (cleanup('pending SSH connection', () => tunnel.pendingConn.end())) tunnel.pendingConn = null;
  }
  // Remote forwards leave a server-side listen until unforwardIn; do this
  // before returning a shared-transport lease so the listen does not outlive
  // the stopped tunnel.
  if (
    tunnel.type === "remote"
    && tunnel.conn
    && typeof tunnel.conn.unforwardIn === "function"
    && Number.isFinite(tunnel.localPort)
  ) {
    const bind = tunnel.bindAddress || "127.0.0.1";
    const port = tunnel.localPort;
    cleanup("remote forward listen", () => {
      try {
        tunnel.conn.unforwardIn(bind, port, () => {});
      } catch {
        /* ignore */
      }
    });
  }
  if (tunnel.sshTransportManaged) {
    if (cleanup('SSH transport lease', () => releaseTunnelSsh(tunnel))) {
      /* lease returned */
    }
  } else {
    if (Array.isArray(tunnel.chainConnections)) {
      tunnel.chainConnections = tunnel.chainConnections.filter((chainConn, index) => (
        !cleanup(`jump connection ${index + 1}`, () => chainConn.end())
      ));
    }
    if (tunnel.conn) {
      if (cleanup('SSH connection', () => tunnel.conn.end())) tunnel.conn = null;
    }
  }
  if (errors.length > 0) {
    const error = errors.join('; ');
    tunnel.status = 'error';
    tunnel.error = error;
    tunnel.cleanupFailed = true;
    tunnel.cleanupInProgress = false;
    sendStatus?.('error', error);
    throw new Error(error);
  }
  tunnel.status = 'inactive';
  tunnel.cleanupFailed = false;
  tunnel.cleanupInProgress = false;
  sendStatus?.('inactive');
  if (deleteEntry) {
    portForwardingTunnels.delete(tunnelId);
  }
}

const { safeSend } = require("./ipcUtils.cjs");

/**
 * Start a port forwarding tunnel
 */
async function startPortForward(event, payload) {
  const {
    ruleId,
    tunnelId,
    type, // 'local' | 'remote' | 'dynamic'
    localPort,
    bindAddress = '127.0.0.1',
    remoteHost,
    remotePort,
    hostname,
    hostId,
    port = 22,
    username,
    authMethod,
    requiresMfa,
    password,
    privateKey,
    certificate,
    keyId,
    passphrase,
    knownHosts,
    verifyHostKeys,
    proxy,
    jumpHosts = [],
    identityFilePaths,
    useSshAgent,
    agentPublicKeys,
    identityAgent,
    identitiesOnly,
    addKeysToAgent,
    useKeychain,
    legacyAlgorithms,
    skipEcdsaHostKey,
    algorithmOverrides,
    keepaliveInterval: resolvedKeepaliveInterval,
    keepaliveCountMax: resolvedKeepaliveCountMax,
    sshTcpConnectTimeoutMs,
    sshAuthReadyTimeoutMs,
  } = payload;

  // The rule is the durable identity; tunnelId is only one renderer's
  // attempt. Reuse an in-flight/live tunnel so two windows cannot create
  // duplicate listeners for the same saved rule.
  if (ruleId) {
    for (const [existingTunnelId, existingTunnel] of portForwardingTunnels) {
      if (existingTunnel.ruleId !== ruleId) continue;
      if (existingTunnel.cancelled) {
        if (existingTunnel.cleanupFailed) {
          return {
            tunnelId: existingTunnelId,
            success: false,
            blockedByCleanup: true,
            error: 'The existing tunnel could not be cleaned up. Stop it successfully before restarting.',
          };
        }
        continue;
      }
      if (!isReusableTunnelStatus(existingTunnel.status)) {
        return {
          tunnelId: existingTunnelId,
          success: false,
          error: existingTunnel.error || 'The existing tunnel is no longer reusable.',
        };
      }
      if (!(existingTunnel.subscribers instanceof Map)) {
        existingTunnel.subscribers = new Map();
      }
      existingTunnel.subscribers.set(event.sender.id, event.sender);
      return {
        tunnelId: existingTunnelId,
        success: true,
        reused: true,
        status: existingTunnel.status || 'active',
      };
    }
  }

  const connectionTimeouts = resolveSshConnectionTimeouts({
    sshTcpConnectTimeoutMs,
    sshAuthReadyTimeoutMs,
  });

  const sender = event.sender;
  const hasJumpHosts = jumpHosts.length > 0;
  const hasProxy = !!proxy;
  let chainConnections = [];
  let connectionSocket = null;
  const passphraseAbortController = new AbortController();
  const reuseEndpoint = buildPortForwardEndpoint({ hostname, port, username, jumpHosts });

  // Prefer an already-authenticated transport (live terminal or idle park).
  const existingTransport = findTransportByEndpoint(reuseEndpoint);
  if (existingTransport?.conn) {
    const tunnelState = {
      type,
      tunnelId,
      conn: existingTransport.conn,
      pendingConn: null,
      server: null,
      chainConnections: [],
      passphraseAbortController,
      ruleId,
      status: 'connecting',
      webContentsId: sender.id,
      subscribers: new Map([[sender.id, sender]]),
      cancelled: false,
      sshTransportManaged: false,
    };
    const sendStatus = (status, error = null) => {
      publishTunnelStatus(tunnelId, tunnelState, status, error);
    };
    portForwardingTunnels.set(tunnelId, tunnelState);
    sendStatus("connecting");
    try {
      borrowTransport(existingTransport, {
        kind: LEASE_KINDS.forward,
        holder: tunnelState,
        leaseId: `forward:${tunnelId}`,
        meta: { source: "port-forward-shared" },
      });
      tunnelState.sshTransportManaged = true;
      console.log(`[PortForward] Reusing shared SSH transport for tunnel ${tunnelId}`);
      const sharedResult = await bindPortForwardChannels({
        type,
        conn: existingTransport.conn,
        tunnelId,
        tunnelState,
        sender,
        bindAddress,
        localPort,
        remoteHost,
        remotePort,
        chainConnections: [],
        sendStatus,
        releaseOnError: true,
      });
      return sharedResult;
    } catch (shareErr) {
      console.warn(
        `[PortForward] Shared transport bind failed for ${hostname}; dialing dedicated:`,
        shareErr?.message || shareErr,
      );
      try { returnTransport(tunnelState); } catch { /* ignore */ }
      portForwardingTunnels.delete(tunnelId);
      // Fall through to a dedicated dial.
    }
  }

  const conn = new SSHClient();
  const tunnelState = {
    type,
    tunnelId,
    conn,
    pendingConn: null,
    server: null,
    chainConnections,
    passphraseAbortController,
    ruleId,
    status: 'connecting',
    webContentsId: sender.id,
    subscribers: new Map([[sender.id, sender]]),
    cancelled: false,
    sshTransportManaged: false,
  };

  const sendStatus = (status, error = null) => {
    publishTunnelStatus(tunnelId, tunnelState, status, error);
  };

  // Keepalive policy:
  //   - positive value: honor it
  //   - explicit 0: truly disabled (host opted out via per-host override —
  //     a router/switch that doesn't reply to keepalive@openssh.com would
  //     otherwise be killed by ssh2 after countMax unanswered probes)
  //   - undefined: legacy caller path, fall back to 10s/3 so an idle
  //     forwarded TCP tunnel doesn't get dropped by NAT state tables.
  const tunnelKeepaliveMs = resolvedKeepaliveInterval == null
    ? 10000
    : (resolvedKeepaliveInterval > 0 ? resolvedKeepaliveInterval * 1000 : 0);
  const tunnelKeepaliveCountMax = resolvedKeepaliveInterval == null
    ? 3
    : (resolvedKeepaliveInterval > 0 ? (resolvedKeepaliveCountMax ?? 3) : 0);
  const connectOpts = {
    host: hostname,
    port: port,
    username: username || 'root',
    timeout: connectionTimeouts.tcpConnectTimeoutMs,
    readyTimeout: 0,
    keepaliveInterval: tunnelKeepaliveMs,
    keepaliveCountMax: tunnelKeepaliveCountMax,
    // Enable keyboard-interactive authentication (required for 2FA/MFA)
    tryKeyboard: true,
    algorithms: buildAlgorithms(legacyAlgorithms, { skipEcdsaHostKey, algorithmOverrides }),
  };
  connectOpts.hostVerifier = hostKeyVerifier.createHostVerifier({
    sender,
    sessionId: tunnelId,
    hostId,
    hostname,
    port,
    knownHosts,
    verifyHostKeys,
  });

  const hasCertificate = typeof certificate === "string" && certificate.trim().length > 0;
  sendStatus('connecting');
  portForwardingTunnels.set(tunnelId, tunnelState);

  let defaultKeys = [];
  let portForwardAuthPhase = { hadPartialSuccess: false, passwordAlreadySucceeded: false };
  let authBanner = "";
  try {
    const fallbackAgentSocket = useSshAgent === false
      ? null
      : useSshAgent === true
        ? undefined
        : await getAvailableAgentSocket(identityAgent, { hostname, port, username });
    const systemAuthAgent = hasCertificate ? null : await prepareSystemSshAgentForAuth({
      useSshAgent,
      agentPublicKeys,
      identityAgent,
      identityFilePaths,
      identitiesOnly,
      addKeysToAgent,
      useKeychain,
      hostname,
      port,
      username,
    }, "[PortForward]");
    const identityFile = !privateKey && !systemAuthAgent
      ? await loadFirstIdentityFileForAuth({
        sender,
        identityFilePaths,
        hostname,
        initialPassphrase: passphrase,
        passphraseSignal: passphraseAbortController.signal,
        logPrefix: "[PortForward]",
        onError: (err, keyPath) => {
          console.warn(`[PortForward] Failed to read identity file ${keyPath}:`, err.message);
        },
      })
      : null;
    const inlineKey = privateKey && !systemAuthAgent
      ? await preparePrivateKeyForAuth({
        sender,
        privateKey,
        keyId,
        keyName: keyId || username,
        hostname,
        initialPassphrase: passphrase,
        passphraseSignal: passphraseAbortController.signal,
        logPrefix: "[PortForward]",
      })
      : null;
    const effectivePrivateKey = inlineKey?.privateKey || identityFile?.privateKey;
    const effectivePassphrase = inlineKey?.passphrase || identityFile?.passphrase;

    if (isTunnelCancelled(tunnelState)) {
      portForwardingTunnels.delete(tunnelId);
      return { tunnelId, success: false, cancelled: true };
    }

    if (systemAuthAgent) {
      connectOpts.agent = systemAuthAgent;
    }
    if (hasCertificate) {
      connectOpts.agent = new NetcattyAgent({
        mode: "certificate",
        webContents: sender,
        meta: {
          label: keyId || username || "",
          certificate,
          privateKey: effectivePrivateKey,
          passphrase: effectivePassphrase,
        },
      });
    } else if (effectivePrivateKey) {
      connectOpts.privateKey = effectivePrivateKey;
      if (effectivePassphrase) {
        connectOpts.passphrase = effectivePassphrase;
      }
    }
    if (password) {
      connectOpts.password = password;
    }

    // Keep the discovered keys available to unrelated jump hosts even when
    // strict agent selection disables them for the final target.
    const discoveredDefaultKeys = await findAllDefaultPrivateKeysFromHelper();
    defaultKeys = systemAuthAgent && identitiesOnly
      ? []
      : discoveredDefaultKeys;
    if (isTunnelCancelled(tunnelState)) {
      portForwardingTunnels.delete(tunnelId);
      return { tunnelId, success: false, cancelled: true };
    }

    // Build auth handler using shared helper
    const authConfig = buildAuthHandler({
      authMethod,
      requiresMfa: !!requiresMfa,
      privateKey: connectOpts.privateKey,
      password,
      passphrase: connectOpts.passphrase,
      agent: connectOpts.agent,
      username: connectOpts.username,
      logPrefix: "[PortForward]",
      defaultKeys,
      sshAgentSocketOverride: fallbackAgentSocket,
      allowAgentFallback: useSshAgent !== false,
    });
    applyAuthToConnOpts(connectOpts, authConfig);
    portForwardAuthPhase = authConfig.authPhase || portForwardAuthPhase;
    if (isTunnelCancelled(tunnelState)) {
      portForwardingTunnels.delete(tunnelId);
      return { tunnelId, success: false, cancelled: true };
    }

    if (hasJumpHosts) {
      const chainResult = await connectThroughChain(
        event,
        {
          hostname,
          port,
          username,
          authMethod,
          password,
          privateKey,
          passphrase,
          useSshAgent,
          identityAgent,
          identityFilePaths,
          identitiesOnly,
          addKeysToAgent,
          useKeychain,
          proxy,
          knownHosts,
          verifyHostKeys,
          jumpHosts,
          legacyAlgorithms,
          skipEcdsaHostKey,
          algorithmOverrides,
          sshTcpConnectTimeoutMs: connectionTimeouts.tcpConnectTimeoutMs,
          sshAuthReadyTimeoutMs: connectionTimeouts.authReadyTimeoutMs,
          _defaultKeys: discoveredDefaultKeys,
          _connectionsRef: chainConnections,
          _tunnelRef: tunnelState,
          _passphraseSignal: passphraseAbortController.signal,
          _keyboardInteractiveScope: "external",
        },
        jumpHosts,
        hostname,
        port,
        tunnelId,
      );
      connectionSocket = chainResult.socket;
      chainConnections = chainResult.connections;
      tunnelState.chainConnections = chainConnections;
      if (isTunnelCancelled(tunnelState)) {
        cleanupChainConnections(chainConnections);
        if (!tunnelState.cleanupFailed) {
          portForwardingTunnels.delete(tunnelId);
        }
        return { tunnelId, success: false, cancelled: true };
      }
      connectOpts.sock = connectionSocket;
      delete connectOpts.host;
      delete connectOpts.port;
    } else if (hasProxy) {
      connectionSocket = await createProxySocket(proxy, hostname, port, {
        timeoutMs: connectionTimeouts.tcpConnectTimeoutMs,
        onSocket: (socket) => {
          tunnelState.pendingConn = socket;
        },
      });
      if (isTunnelCancelled(tunnelState)) {
        try { connectionSocket?.end?.(); } catch { /* ignore */ }
        try { connectionSocket?.destroy?.(); } catch { /* ignore */ }
        if (!tunnelState.cleanupFailed) {
          portForwardingTunnels.delete(tunnelId);
        }
        return { tunnelId, success: false, cancelled: true };
      }
      tunnelState.pendingConn = null;
      connectOpts.sock = connectionSocket;
      delete connectOpts.host;
      delete connectOpts.port;
    }
  } catch (err) {
    if (isTunnelCancelled(tunnelState)) {
      if (!tunnelState.cleanupFailed) {
        portForwardingTunnels.delete(tunnelId);
      }
      return { tunnelId, success: false, cancelled: true };
    }
    if (isPassphraseCancelledError(err)) {
      cancelTunnel(tunnelId, tunnelState, sendStatus, { deleteEntry: true });
      return { tunnelId, success: false, cancelled: true };
    }
    tunnelState.cancelled = true;
    if (tunnelState.pendingConn) {
      try { tunnelState.pendingConn.end(); } catch { /* ignore */ }
    }
    cleanupChainConnections(tunnelState.chainConnections);
    if (connectionSocket) {
      try { connectionSocket.end?.(); } catch { /* ignore */ }
      try { connectionSocket.destroy?.(); } catch { /* ignore */ }
    }
    keyboardInteractiveHandler.cancelRequestsForSession(tunnelId, "connection-ended");
    portForwardingTunnels.delete(tunnelId);
    sendStatus('error', err?.message || String(err));
    throw err;
  }

  // Handle keyboard-interactive authentication (2FA/MFA)
  conn.on("banner", (message) => {
    authBanner = String(message || "").trim();
  });
  conn.on("keyboard-interactive", createKeyboardInteractiveHandler({
    sender,
    sessionId: tunnelId,
    hostId,
    hostname,
    password,
    logPrefix: "[PortForward]",
    scope: "external",
    getAuthBanner: () => authBanner,
    shouldSkipAutoFill: () => shouldSkipKiPasswordAutoFill(portForwardAuthPhase),
  }));

  return new Promise((resolve, reject) => {
    // Track whether the Promise has been settled so conn.on('close')
    // can reject if the tunnel was killed during SSH handshake.
    let settled = false;
    let authReadyTimer = null;
    const clearAuthReadyTimer = () => {
      if (!authReadyTimer) return;
      clearTimeout(authReadyTimer);
      authReadyTimer = null;
    };

    conn.once('connect', () => {
      runWhenProxyConnectionReady(conn._sock, () => {
        try { conn._sock?.setTimeout?.(0); } catch { /* ignore */ }
        clearAuthReadyTimer();
        authReadyTimer = setTimeout(
          () => conn.emit('timeout'),
          connectionTimeouts.authReadyTimeoutMs,
        );
        authReadyTimer.unref?.();
      });
    });

    conn.once('ready', () => {
      clearAuthReadyTimer();
      console.log(`[PortForward] SSH connection ready for tunnel ${tunnelId}`);

      bindPortForwardChannels({
        type,
        conn,
        tunnelId,
        tunnelState,
        sender,
        bindAddress,
        localPort,
        remoteHost,
        remotePort,
        chainConnections,
        sendStatus,
        releaseOnError: false,
        endpoint: reuseEndpoint,
        registerTransport: true,
      }).then((result) => {
        settled = true;
        resolve(result);
      }).catch((err) => {
        settled = true;
        reject(err);
      });
    });

    conn.on('error', (err) => {
      clearAuthReadyTimer();
      console.error(`[PortForward] SSH error:`, err.message);
      if (settled) return;
      sendStatus('error', err.message);
      cleanupChainConnections(chainConnections);
      settled = true;
      reject(err);
    });

    conn.once('close', () => {
      clearAuthReadyTimer();
      keyboardInteractiveHandler.cancelRequestsForSession(tunnelId, "connection-ended");
      console.log(`[PortForward] SSH connection closed for tunnel ${tunnelId}`);
      const tunnel = portForwardingTunnels.get(tunnelId) || tunnelState;
      // Capture the cancelled flag BEFORE cleanup deletes the entry.
      const wasCancelled = !!tunnel?.cancelled;
      if (tunnel) {
        if (tunnel.server) {
          try { tunnel.server.close(); } catch { }
        }
        if (tunnel.pendingConn) {
          try { tunnel.pendingConn.end(); } catch { /* ignore */ }
        }
        if (tunnel.sshTransportManaged) {
          // Socket died under us; drop the lease without trying to end again.
          try { returnTransport(tunnel); } catch { /* ignore */ }
          tunnel.sshTransportManaged = false;
          tunnel.conn = null;
        } else if (Array.isArray(tunnel.chainConnections)) {
          cleanupChainConnections(tunnel.chainConnections);
        }
        if (shouldFinalizeTunnelClose(tunnel)) {
          sendStatus('inactive');
          portForwardingTunnels.delete(tunnelId);
        }
      }
      // If the Promise was never settled (tunnel killed during
      // handshake by stopPortForwardByRuleId), settle it.
      if (!settled) {
        settled = true;
        if (wasCancelled) {
          resolve({ tunnelId, success: false, cancelled: true });
        } else {
          reject(new Error(`Tunnel ${tunnelId} closed before connection established`));
        }
      }
    });

    conn.once('timeout', () => {
      clearAuthReadyTimer();
      if (settled) return;
      const err = new Error(`Connection timeout to ${hostname}`);
      sendStatus('error', err.message);
      cleanupChainConnections(chainConnections);
      settled = true;
      reject(err);
      conn.end();
    });

    conn.connect(connectOpts);
  });
}

/**
 * Stop a port forwarding tunnel
 */
async function stopPortForward(event, payload) {
  const { tunnelId } = payload;
  const tunnel = portForwardingTunnels.get(tunnelId);

  if (!tunnel) {
    return { tunnelId, success: false, error: 'Tunnel not found' };
  }

  try {
    cancelTunnel(
      tunnelId,
      tunnel,
      (status, error) => publishTunnelStatus(tunnelId, tunnel, status, error),
      { deleteEntry: true },
    );
    return { tunnelId, success: true };
  } catch (err) {
    return { tunnelId, success: false, error: err.message };
  }
}

/**
 * Get status of a tunnel
 */
async function getPortForwardStatus(event, payload) {
  const { tunnelId } = payload;
  const tunnel = portForwardingTunnels.get(tunnelId);

  if (!tunnel) {
    return { tunnelId, status: 'inactive' };
  }

  return {
    tunnelId,
    status: tunnel.status || 'active',
    type: tunnel.type,
    ...(tunnel.error ? { error: tunnel.error } : {}),
  };
}

/**
 * Register the calling renderer for status events from an existing tunnel and
 * return the status from the same main-process turn.
 */
async function subscribePortForward(event, payload) {
  const { tunnelId } = payload;
  const tunnel = portForwardingTunnels.get(tunnelId);

  if (!tunnel) {
    return { tunnelId, status: 'inactive' };
  }

  if (!(tunnel.subscribers instanceof Map)) {
    tunnel.subscribers = new Map();
  }
  tunnel.subscribers.set(event.sender.id, event.sender);
  return {
    tunnelId,
    status: tunnel.status || 'active',
    type: tunnel.type,
    ...(tunnel.error ? { error: tunnel.error } : {}),
  };
}

/**
 * List all active port forwards
 */
async function listPortForwards() {
  const list = [];
  for (const [tunnelId, tunnel] of portForwardingTunnels) {
    list.push({
      ruleId: tunnel.ruleId,
      tunnelId,
      type: tunnel.type,
      status: tunnel.status || 'active',
      ...(tunnel.error ? { error: tunnel.error } : {}),
    });
  }
  return list;
}

/**
 * Stop all active port forwards (cleanup on app quit)
 */
function stopAllPortForwards() {
  console.log(`[PortForward] Stopping all ${portForwardingTunnels.size} active tunnels...`);
  for (const [tunnelId, tunnel] of portForwardingTunnels) {
      try {
        cancelTunnel(
          tunnelId,
          tunnel,
          (status, error) => publishTunnelStatus(tunnelId, tunnel, status, error),
          { deleteEntry: true },
        );
        console.log(`[PortForward] Stopped tunnel ${tunnelId}`);
    } catch (err) {
      console.warn(`[PortForward] Failed to stop tunnel ${tunnelId}:`, err.message);
    }
  }
  console.log('[PortForward] All tunnels stopped');
}

/**
 * Stop all active port forwards for a given rule ID.
 * This catches tunnels in ANY state (connecting, active) because it
 * operates on the main-process portForwardingTunnels map directly.
 */
function stopPortForwardByRuleId(_event, { ruleId }) {
  let stopped = 0;
  let failed = 0;
  const errors = [];
  for (const [tunnelId, tunnel] of portForwardingTunnels) {
    if (tunnel.ruleId === ruleId) {
      try {
        cancelTunnel(
          tunnelId,
          tunnel,
          (status, error) => publishTunnelStatus(tunnelId, tunnel, status, error),
          { deleteEntry: true },
        );
        console.log(`[PortForward] Stopped tunnel ${tunnelId} for rule ${ruleId}`);
        stopped++;
      } catch (err) {
        console.warn(`[PortForward] Failed to stop tunnel ${tunnelId}:`, err.message);
        failed++;
        errors.push(err instanceof Error ? err.message : String(err));
      }
    }
  }
  return { stopped, failed, errors };
}

/**
 * Register IPC handlers for port forwarding operations
 */
function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:portforward:start", startPortForward);
  ipcMain.handle("netcatty:portforward:stop", stopPortForward);
  ipcMain.handle("netcatty:portforward:status", getPortForwardStatus);
  ipcMain.handle("netcatty:portforward:subscribe", subscribePortForward);
  ipcMain.handle("netcatty:portforward:list", listPortForwards);
  ipcMain.handle("netcatty:portforward:stopAll", () => stopAllPortForwards());
  ipcMain.handle("netcatty:portforward:stopByRuleId", stopPortForwardByRuleId);
}

module.exports = {
  registerHandlers,
  startPortForward,
  stopPortForward,
  getPortForwardStatus,
  subscribePortForward,
  listPortForwards,
  stopAllPortForwards,
  stopPortForwardByRuleId,
  cancelTunnel,
  publishTunnelStatus,
  shouldFinalizeTunnelClose,
  isReusableTunnelStatus,
};
