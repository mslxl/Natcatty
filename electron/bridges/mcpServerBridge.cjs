/**
 * MCP Server Bridge — TCP host in Electron main process
 *
 * Starts a local TCP server that the netcatty-mcp-server.cjs child process
 * connects to. Handles JSON-RPC calls by dispatching to real SSH sessions
 * and SFTP clients.
 */
"use strict";

const net = require("node:net");
const crypto = require("node:crypto");
const path = require("node:path");
const { existsSync } = require("node:fs");

const { toUnpackedAsarPath } = require("./ai/shellUtils.cjs");
const { execViaPty, execViaChannel } = require("./ai/ptyExec.cjs");

let sessions = null;   // Map<sessionId, { sshClient, stream, pty, conn, ... }>
let sftpClients = null; // Map<sftpId, SFTPWrapper>
let tcpServer = null;
let tcpPort = null;
let authToken = null;  // Random token generated when TCP server starts

// Track which sockets have completed authentication
const authenticatedSockets = new WeakSet();

/**
 * Safely quote a string for use in a POSIX shell command.
 * Wraps the value in single quotes and escapes any embedded single quotes.
 */
function shellQuote(s) {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

// Session metadata registered by renderer (sessionId → { hostname, label, os, username })
// Global metadata (union of all scopes) for fallback
const sessionMetadata = new Map();

// Per-scope metadata: chatSessionId → { sessionIds: string[], metadata: Map }
const scopedMetadata = new Map();

// Track which session IDs are in the current scope (set by updateSessionMetadata)
let currentScopedSessionIds = [];

// Command safety checking (reuse from aiBridge)
let commandBlocklist = [];

// Command timeout in milliseconds (default 60s, synced from user settings)
let commandTimeoutMs = 60000;

// Max iterations for AI agent loops (default 20, synced from user settings)
let maxIterations = 20;

// Permission mode: 'observer' | 'confirm' | 'autonomous' (synced from user settings)
let permissionMode = "confirm";

// Track active PTY executions for cancellation
const activePtyExecs = new Map(); // marker → { ptyStream, cleanup }

function cancelAllPtyExecs() {
  for (const [marker, entry] of activePtyExecs) {
    try {
      entry.cleanup();
      // Send Ctrl+C to kill the running command
      if (entry.ptyStream && typeof entry.ptyStream.write === "function") {
        entry.ptyStream.write("\x03");
      }
    } catch { /* ignore */ }
  }
  activePtyExecs.clear();
}

function init(deps) {
  sessions = deps.sessions;
  sftpClients = deps.sftpClients;
  if (deps.commandBlocklist) {
    commandBlocklist = deps.commandBlocklist;
  }
}

function setCommandBlocklist(list) {
  commandBlocklist = list || [];
}

function setCommandTimeout(seconds) {
  commandTimeoutMs = Math.max(1, Math.min(3600, seconds || 60)) * 1000;
}

function getCommandTimeoutMs() {
  return commandTimeoutMs;
}

function setMaxIterations(value) {
  maxIterations = Math.max(1, Math.min(100, value || 20));
}

function getMaxIterations() {
  return maxIterations;
}

function setPermissionMode(mode) {
  if (mode === "observer" || mode === "confirm" || mode === "autonomous") {
    permissionMode = mode;
  }
}

function getPermissionMode() {
  return permissionMode;
}

/**
 * Register metadata for terminal sessions (called from renderer via IPC).
 * @param {Array<{sessionId, hostname, label, os, username, connected}>} sessionList
 * @param {string} [chatSessionId] - AI chat session ID for per-scope isolation
 */
function updateSessionMetadata(sessionList, chatSessionId) {
  // Update global metadata (additive — do NOT clear, multiple scopes coexist)
  for (const s of sessionList) {
    sessionMetadata.set(s.sessionId, {
      hostname: s.hostname || "",
      label: s.label || "",
      os: s.os || "",
      username: s.username || "",
      connected: s.connected !== false,
    });
  }
  // Track scoped session IDs for use by buildMcpServerConfig
  currentScopedSessionIds = sessionList.map(s => s.sessionId);

  // Store per-scope metadata if chatSessionId provided
  if (chatSessionId) {
    scopedMetadata.set(chatSessionId, {
      sessionIds: currentScopedSessionIds.slice(),
    });
  }

}

function getCurrentScopedSessionIds() {
  return currentScopedSessionIds;
}

function checkCommandSafety(command) {
  for (const pattern of commandBlocklist) {
    try {
      if (new RegExp(pattern, "i").test(command)) {
        return { blocked: true, matchedPattern: pattern };
      }
    } catch {
      // ignore invalid patterns
    }
  }
  return { blocked: false };
}

// ── TCP Server ──

function getOrCreateHost() {
  if (tcpServer && tcpPort) return Promise.resolve(tcpPort);

  // Generate a random auth token for this server instance
  authToken = crypto.randomBytes(32).toString("hex");

  return new Promise((resolve, reject) => {
    const server = net.createServer((socket) => {
      handleConnection(socket);
    });

    server.listen(0, "127.0.0.1", () => {
      tcpPort = server.address().port;
      tcpServer = server;
      console.log(`[MCP Bridge] TCP server listening on 127.0.0.1:${tcpPort}`);
      resolve(tcpPort);
    });

    server.on("error", (err) => {
      console.error("[MCP Bridge] TCP server error:", err.message);
      reject(err);
    });
  });
}

function handleConnection(socket) {
  let buffer = "";
  socket.setEncoding("utf-8");

  socket.on("data", (chunk) => {
    buffer += chunk;
    let newlineIdx;
    while ((newlineIdx = buffer.indexOf("\n")) !== -1) {
      const line = buffer.slice(0, newlineIdx);
      buffer = buffer.slice(newlineIdx + 1);
      if (!line.trim()) continue;
      handleMessage(socket, line);
    }
  });

  socket.on("error", () => {
    // Client disconnected — nothing to do
  });
}

async function handleMessage(socket, line) {
  let msg;
  try {
    msg = JSON.parse(line);
  } catch {
    return;
  }

  const { id, method, params } = msg;
  if (id == null || !method) return;

  // ── Authentication gate ──
  // The first message from any connection MUST be auth/verify with the correct token.
  // All other methods are rejected until the socket is authenticated.
  if (!authenticatedSockets.has(socket)) {
    if (method === "auth/verify" && params?.token === authToken) {
      authenticatedSockets.add(socket);
      const response = JSON.stringify({ jsonrpc: "2.0", id, result: { ok: true } }) + "\n";
      if (!socket.destroyed) socket.write(response);
      return;
    }
    // Wrong token or wrong method — reject and close
    const response = JSON.stringify({
      jsonrpc: "2.0",
      id,
      error: { code: -32001, message: "Authentication required. Send auth/verify with valid token first." },
    }) + "\n";
    if (!socket.destroyed) {
      socket.write(response);
      socket.destroy();
    }
    return;
  }

  try {
    const result = await dispatch(method, params || {});
    const response = JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n";
    if (!socket.destroyed) socket.write(response);
  } catch (err) {
    const response = JSON.stringify({
      jsonrpc: "2.0",
      id,
      error: { code: -32000, message: err?.message || String(err) },
    }) + "\n";
    if (!socket.destroyed) socket.write(response);
  }
}

// ── RPC Dispatch ──

// Methods that modify remote state — blocked in observer mode
const WRITE_METHODS = new Set([
  "netcatty/exec",
  "netcatty/terminalWrite",
  "netcatty/sftpWrite",
  "netcatty/sftpMkdir",
  "netcatty/sftpRemove",
  "netcatty/sftpRename",
  "netcatty/multiExec",
]);

async function dispatch(method, params) {
  // Observer mode: block all write operations
  if (permissionMode === "observer" && WRITE_METHODS.has(method)) {
    return { ok: false, error: `Operation denied: permission mode is "observer" (read-only). Change to "confirm" or "autonomous" in Settings → AI → Safety to allow this action.` };
  }

  switch (method) {
    case "netcatty/getContext":
      return handleGetContext(params);
    case "netcatty/exec":
      return handleExec(params);
    case "netcatty/terminalWrite":
      return handleTerminalWrite(params);
    case "netcatty/sftpList":
      return handleSftpList(params);
    case "netcatty/sftpRead":
      return handleSftpRead(params);
    case "netcatty/sftpWrite":
      return handleSftpWrite(params);
    case "netcatty/sftpMkdir":
      return handleSftpMkdir(params);
    case "netcatty/sftpRemove":
      return handleSftpRemove(params);
    case "netcatty/sftpRename":
      return handleSftpRename(params);
    case "netcatty/sftpStat":
      return handleSftpStat(params);
    case "netcatty/multiExec":
      return handleMultiExec(params);
    default:
      throw new Error(`Unknown method: ${method}`);
  }
}

// ── Handler: getContext ──

function handleGetContext(params) {
  if (!sessions) return { hosts: [], instructions: "No sessions available." };

  // Scope resolution: use explicit scopedSessionIds from MCP server env var (per-process, set at spawn)
  const scopedIds = (params?.scopedSessionIds && params.scopedSessionIds.length > 0)
    ? new Set(params.scopedSessionIds)
    : null;

  const hosts = [];
  for (const [sessionId, session] of sessions.entries()) {
    if (scopedIds && !scopedIds.has(sessionId)) continue;
    // Only include SSH sessions (skip local terminal sessions)
    const sshClient = session.conn || session.sshClient;
    if (!sshClient || typeof sshClient.exec !== "function") continue;

    const meta = sessionMetadata.get(sessionId) || {};
    hosts.push({
      sessionId,
      hostname: meta.hostname || session.hostname || "",
      label: meta.label || session.label || "",
      os: meta.os || "",
      username: meta.username || session.username || "",
      connected: meta.connected !== undefined ? meta.connected : !!(session.sshClient || session.conn),
    });
  }

  return {
    environment: "netcatty-terminal",
    description: "You are operating inside Netcatty, a multi-host SSH terminal manager. " +
      "The user is managing remote servers. Use the provided tools to execute commands, " +
      "read/write files, and manage hosts on the remote machines. " +
      "Always prefer these tools over suggesting the user to do things manually.",
    hosts,
    hostCount: hosts.length,
  };
}

// ── Handler: exec ──

function handleExec(params) {
  const { sessionId, command } = params;
  if (!sessionId || !command) throw new Error("sessionId and command are required");

  const safety = checkCommandSafety(command);
  if (safety.blocked) {
    return { ok: false, error: `Command blocked by safety policy. Pattern: ${safety.matchedPattern}` };
  }

  const session = sessions?.get(sessionId);
  if (!session) return { ok: false, error: "Session not found" };

  const sshClient = session.conn || session.sshClient;
  if (!sshClient || typeof sshClient.exec !== "function") {
    return { ok: false, error: "Not an SSH session" };
  }

  const ptyStream = session.stream;

  // If no PTY stream, fall back to exec channel (invisible to terminal)
  if (!ptyStream || typeof ptyStream.write !== "function") {
    return execViaChannel(sshClient, command, { timeoutMs: commandTimeoutMs });
  }

  // Execute via PTY stream so user sees the command in the terminal
  return execViaPty(ptyStream, command, {
    trackForCancellation: activePtyExecs,
    timeoutMs: commandTimeoutMs,
  });
}

// ── Handler: terminalWrite ──

function handleTerminalWrite(params) {
  const { sessionId, input } = params;
  if (!sessionId || input == null) throw new Error("sessionId and input are required");

  const session = sessions?.get(sessionId);
  if (!session) return { ok: false, error: "Session not found" };

  if (session.stream) {
    session.stream.write(input);
    return { ok: true };
  }
  if (session.pty) {
    session.pty.write(input);
    return { ok: true };
  }
  return { ok: false, error: "No writable stream" };
}

// ── SFTP Helpers ──

function findSftpForSession(sessionId) {
  // Try to find an SFTP client keyed by the same sessionId
  if (sftpClients?.has(sessionId)) {
    return sftpClients.get(sessionId);
  }
  // Look through all SFTP clients for one sharing the same SSH connection
  const session = sessions?.get(sessionId);
  if (!session?.sshClient) return null;

  for (const [, client] of sftpClients || []) {
    if (client.client === session.sshClient || client._sshClient === session.sshClient) {
      return client;
    }
  }
  return null;
}

// ── Handler: sftpList ──

async function handleSftpList(params) {
  const { sessionId, path: dirPath } = params;
  if (!sessionId || !dirPath) throw new Error("sessionId and path are required");

  const sftpClient = findSftpForSession(sessionId);
  if (sftpClient) {
    try {
      const list = await sftpClient.list(dirPath);
      return {
        files: list.map(f => ({
          name: f.name,
          type: f.type === "d" ? "directory" : f.type === "l" ? "symlink" : "file",
          size: f.size,
          lastModified: f.modifyTime,
          permissions: f.rights ? `${f.rights.user}${f.rights.group}${f.rights.other}` : undefined,
        })),
      };
    } catch (err) {
      return { error: err.message };
    }
  }

  // Fallback: use SSH exec
  const result = await handleExec({ sessionId, command: `ls -la ${shellQuote(dirPath)}` });
  if (!result.ok) return { error: result.error };
  return { output: result.stdout || "(empty directory)" };
}

// ── Handler: sftpRead ──

async function handleSftpRead(params) {
  const { sessionId, path: filePath, maxBytes = 10000 } = params;
  if (!sessionId || !filePath) throw new Error("sessionId and path are required");

  // Fallback to SSH exec (more reliable across SFTP client states)
  const result = await handleExec({ sessionId, command: `head -c ${maxBytes} ${shellQuote(filePath)}` });
  if (!result.ok) return { error: result.error };
  return { content: result.stdout || "(empty file)" };
}

// ── Handler: sftpWrite ──

async function handleSftpWrite(params) {
  const { sessionId, path: filePath, content } = params;
  if (!sessionId || !filePath || content == null) throw new Error("sessionId, path and content are required");

  const sftpClient = findSftpForSession(sessionId);
  if (sftpClient) {
    try {
      await sftpClient.put(Buffer.from(content, "utf-8"), filePath);
      return { written: filePath };
    } catch {
      // Fallback to SSH
    }
  }

  const result = await handleExec({ sessionId, command: `cat > ${shellQuote(filePath)} << 'NETCATTY_EOF'\n${content.replace(/^NETCATTY_EOF$/gm, 'NETCATTY_EO\\F')}\nNETCATTY_EOF` });
  if (!result.ok) return { error: result.error };
  return { written: filePath };
}

// ── Handler: sftpMkdir ──

async function handleSftpMkdir(params) {
  const { sessionId, path: dirPath } = params;
  if (!sessionId || !dirPath) throw new Error("sessionId and path are required");

  const sftpClient = findSftpForSession(sessionId);
  if (sftpClient) {
    try {
      await sftpClient.mkdir(dirPath, true); // recursive
      return { created: dirPath };
    } catch {
      // Fallback
    }
  }

  const result = await handleExec({ sessionId, command: `mkdir -p ${shellQuote(dirPath)}` });
  if (!result.ok) return { error: result.error };
  return { created: dirPath };
}

// ── Handler: sftpRemove ──

async function handleSftpRemove(params) {
  const { sessionId, path: targetPath } = params;
  if (!sessionId || !targetPath) throw new Error("sessionId and path are required");

  // Use SSH exec with rm -rf for reliability (handles both files and dirs)
  const result = await handleExec({ sessionId, command: `rm -rf ${shellQuote(targetPath)}` });
  if (!result.ok) return { error: result.error };
  return { removed: targetPath };
}

// ── Handler: sftpRename ──

async function handleSftpRename(params) {
  const { sessionId, oldPath, newPath } = params;
  if (!sessionId || !oldPath || !newPath) throw new Error("sessionId, oldPath and newPath are required");

  const sftpClient = findSftpForSession(sessionId);
  if (sftpClient) {
    try {
      await sftpClient.rename(oldPath, newPath);
      return { renamed: `${oldPath} → ${newPath}` };
    } catch {
      // Fallback
    }
  }

  const result = await handleExec({ sessionId, command: `mv ${shellQuote(oldPath)} ${shellQuote(newPath)}` });
  if (!result.ok) return { error: result.error };
  return { renamed: `${oldPath} → ${newPath}` };
}

// ── Handler: sftpStat ──

async function handleSftpStat(params) {
  const { sessionId, path: targetPath } = params;
  if (!sessionId || !targetPath) throw new Error("sessionId and path are required");

  const sftpClient = findSftpForSession(sessionId);
  if (sftpClient) {
    try {
      const stat = await sftpClient.stat(targetPath);
      return {
        name: path.basename(targetPath),
        type: stat.isDirectory ? "directory" : stat.isSymbolicLink ? "symlink" : "file",
        size: stat.size,
        lastModified: stat.modifyTime,
        permissions: stat.mode ? (stat.mode & 0o777).toString(8) : undefined,
      };
    } catch {
      // Fallback
    }
  }

  // Fallback: use stat command
  const result = await handleExec({ sessionId, command: `stat -c '{"size":%s,"mode":"%a","mtime":%Y,"type":"%F"}' ${shellQuote(targetPath)}` });
  if (!result.ok) return { error: result.error };
  try {
    const parsed = JSON.parse(result.stdout.trim());
    return {
      name: path.basename(targetPath),
      type: parsed.type?.includes("directory") ? "directory" : "file",
      size: parsed.size,
      lastModified: parsed.mtime * 1000,
      permissions: parsed.mode,
    };
  } catch {
    return { error: "Failed to parse stat output" };
  }
}

// ── Handler: multiExec ──

async function handleMultiExec(params) {
  const { sessionIds, command, mode = "parallel", stopOnError = false } = params;
  if (!Array.isArray(sessionIds) || !command) throw new Error("sessionIds and command are required");

  const safety = checkCommandSafety(command);
  if (safety.blocked) {
    return { error: `Command blocked by safety policy. Pattern: ${safety.matchedPattern}` };
  }

  const results = {};

  if (mode === "sequential") {
    for (const sid of sessionIds) {
      const result = await handleExec({ sessionId: sid, command });
      results[sid] = {
        ok: result.ok,
        output: result.ok ? (result.stdout || "(no output)") : `Error: ${result.error || result.stderr || "Failed"}`,
      };
      if (!result.ok && stopOnError) break;
    }
  } else {
    const promises = sessionIds.map(async (sid) => {
      const result = await handleExec({ sessionId: sid, command });
      return {
        sid,
        ok: result.ok,
        output: result.ok ? (result.stdout || "(no output)") : `Error: ${result.error || result.stderr || "Failed"}`,
      };
    });
    for (const r of await Promise.all(promises)) {
      results[r.sid] = { ok: r.ok, output: r.output };
    }
  }

  return { results };
}

// ── MCP Server Config Builder ──

function buildMcpServerConfig(port, scopedSessionIds) {
  // Use provided scoped IDs, or fall back to the current scope from updateSessionMetadata
  const effectiveIds = (scopedSessionIds && scopedSessionIds.length > 0)
    ? scopedSessionIds
    : currentScopedSessionIds;

  const runtimePath = toUnpackedAsarPath(
    path.join(__dirname, "..", "mcp", "netcatty-mcp-server.cjs"),
  );

  const env = [
    { name: "NETCATTY_MCP_PORT", value: String(port) },
  ];

  if (authToken) {
    env.push({ name: "NETCATTY_MCP_TOKEN", value: authToken });
  }

  if (effectiveIds && effectiveIds.length > 0) {
    env.push({ name: "NETCATTY_MCP_SESSION_IDS", value: effectiveIds.join(",") });
  }

  return {
    name: "netcatty-remote-hosts",
    type: "stdio",
    command: "node",
    args: [runtimePath],
    env,
  };
}

// ── Cleanup ──

function cleanupScopedMetadata(chatSessionId) {
  if (chatSessionId) {
    scopedMetadata.delete(chatSessionId);
  }
}

function cleanup() {
  if (tcpServer) {
    tcpServer.close();
    tcpServer = null;
    tcpPort = null;
    console.log("[MCP Bridge] TCP server closed");
  }
  scopedMetadata.clear();
}

module.exports = {
  init,
  setCommandBlocklist,
  setCommandTimeout,
  getCommandTimeoutMs,
  setMaxIterations,
  getMaxIterations,
  setPermissionMode,
  getPermissionMode,
  updateSessionMetadata,
  getCurrentScopedSessionIds,
  getOrCreateHost,
  buildMcpServerConfig,
  cancelAllPtyExecs,
  cleanupScopedMetadata,
  cleanup,
};
