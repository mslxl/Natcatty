/**
 * MCP Server Bridge — TCP host in Electron main process
 *
 * Starts a local TCP server that the netcatty-mcp-server.cjs child process
 * connects to. Handles JSON-RPC calls by dispatching to real SSH sessions
 * and SFTP clients.
 */
"use strict";

const net = require("node:net");
const path = require("node:path");
const { existsSync } = require("node:fs");

let sessions = null;   // Map<sessionId, { sshClient, stream, pty, conn, ... }>
let sftpClients = null; // Map<sftpId, SFTPWrapper>
let tcpServer = null;
let tcpPort = null;

// Session metadata registered by renderer (sessionId → { hostname, label, os, username })
const sessionMetadata = new Map();

// Command safety checking (reuse from aiBridge)
let commandBlocklist = [];

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

/**
 * Register metadata for terminal sessions (called from renderer via IPC).
 * @param {Array<{sessionId, hostname, label, os, username, connected}>} sessionList
 */
function updateSessionMetadata(sessionList) {
  sessionMetadata.clear();
  for (const s of sessionList) {
    sessionMetadata.set(s.sessionId, {
      hostname: s.hostname || "",
      label: s.label || "",
      os: s.os || "",
      username: s.username || "",
      connected: s.connected !== false,
    });
  }
}

function checkCommandSafety(command) {
  for (const pattern of commandBlocklist) {
    try {
      if (new RegExp(pattern).test(command)) {
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

async function dispatch(method, params) {
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

  const scopedIds = params?.scopedSessionIds
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
      hostname: meta.hostname || "",
      label: meta.label || "",
      os: meta.os || "",
      username: meta.username || "",
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

// ── ANSI escape code stripping ──

function stripAnsi(str) {
  // eslint-disable-next-line no-control-regex
  return str.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, "")
    .replace(/\x1b\][^\x07]*\x07/g, "")   // OSC sequences
    .replace(/\r/g, "");
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
    return execViaChannel(sshClient, command);
  }

  // Execute via PTY stream so user sees the command in the terminal
  return execViaPty(ptyStream, command);
}

/**
 * Execute command through the terminal PTY stream.
 * The user sees the command typed and output in their terminal.
 * Uses a unique marker to detect when the command finishes and capture the exit code.
 */
function execViaPty(ptyStream, command) {
  const marker = `__NCMCP_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 6)}__`;

  return new Promise((resolve) => {
    let output = "";
    let foundStart = false;
    let timeoutId = null;

    const MAX_TIMEOUT = 300000; // 5 min max (docker pull, pip install, etc.)

    const onData = (data) => {
      const text = data.toString();

      if (!foundStart) {
        const startIdx = text.indexOf(marker + "_S");
        if (startIdx !== -1) {
          foundStart = true;
          // Capture everything after the start marker line
          const afterMarker = text.slice(startIdx);
          const nlIdx = afterMarker.indexOf("\n");
          if (nlIdx !== -1) {
            output += afterMarker.slice(nlIdx + 1);
          }
        }
        // Check if end marker is already in this chunk
        if (foundStart) {
          checkEnd();
        }
        return;
      }

      output += text;
      checkEnd();
    };

    function checkEnd() {
      const endPattern = marker + "_E:";
      const endIdx = output.indexOf(endPattern);
      if (endIdx === -1) return;

      // Extract exit code
      const afterEnd = output.slice(endIdx + endPattern.length);
      const codeMatch = afterEnd.match(/^(\d+)/);
      const exitCode = codeMatch ? parseInt(codeMatch[1], 10) : null;

      // Output is everything before the end marker
      const stdout = output.slice(0, endIdx);
      finish(stdout, exitCode);
    }

    function finish(stdout, exitCode) {
      clearTimeout(timeoutId);
      ptyStream.removeListener("data", onData);
      activePtyExecs.delete(marker);

      const cleaned = stripAnsi(stdout || "").trim();
      resolve({
        ok: exitCode === 0 || exitCode === null,
        stdout: cleaned,
        stderr: "",
        exitCode: exitCode ?? 0,
      });
    }

    timeoutId = setTimeout(() => {
      ptyStream.removeListener("data", onData);
      activePtyExecs.delete(marker);
      // Send Ctrl+C to kill the timed-out command
      if (typeof ptyStream.write === "function") ptyStream.write("\x03");
      const cleaned = stripAnsi(output).trim();
      resolve({ ok: false, stdout: cleaned, stderr: "", exitCode: -1, error: "Command timed out (5min)" });
    }, MAX_TIMEOUT);

    ptyStream.on("data", onData);

    // Register for cancellation
    activePtyExecs.set(marker, {
      ptyStream,
      cleanup: () => { clearTimeout(timeoutId); ptyStream.removeListener("data", onData); },
    });

    // Markers are filtered from terminal display by preload.cjs (MCP_MARKER_RE).
    // Start marker + command are on the SAME line to avoid an extra shell prompt.
    const noPager = "PAGER=cat SYSTEMD_PAGER= GIT_PAGER=cat LESS= ";
    ptyStream.write(
      `printf '${marker}_S\\n';${noPager}${command}\n` +
      `__nc=$?;printf '${marker}_E:'$__nc'\\n';(exit $__nc)\n`
    );
  });
}

/**
 * Fallback: execute via a separate SSH exec channel (invisible to terminal).
 */
function execViaChannel(sshClient, command) {
  return new Promise((resolve) => {
    sshClient.exec(command, (err, execStream) => {
      if (err) {
        resolve({ ok: false, error: err.message });
        return;
      }
      let stdout = "";
      let stderr = "";
      execStream.on("data", (data) => { stdout += data.toString(); });
      execStream.stderr.on("data", (data) => { stderr += data.toString(); });
      execStream.on("close", (code) => {
        resolve({ ok: code === 0, stdout, stderr, exitCode: code });
      });
    });
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
  const result = await handleExec({ sessionId, command: `ls -la ${dirPath}` });
  if (!result.ok) return { error: result.error };
  return { output: result.stdout || "(empty directory)" };
}

// ── Handler: sftpRead ──

async function handleSftpRead(params) {
  const { sessionId, path: filePath, maxBytes = 10000 } = params;
  if (!sessionId || !filePath) throw new Error("sessionId and path are required");

  // Fallback to SSH exec (more reliable across SFTP client states)
  const result = await handleExec({ sessionId, command: `head -c ${maxBytes} ${JSON.stringify(filePath)}` });
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

  const escaped = content.replace(/'/g, "'\\''");
  const result = await handleExec({ sessionId, command: `cat > ${JSON.stringify(filePath)} << 'NETCATTY_EOF'\n${escaped}\nNETCATTY_EOF` });
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

  const result = await handleExec({ sessionId, command: `mkdir -p ${JSON.stringify(dirPath)}` });
  if (!result.ok) return { error: result.error };
  return { created: dirPath };
}

// ── Handler: sftpRemove ──

async function handleSftpRemove(params) {
  const { sessionId, path: targetPath } = params;
  if (!sessionId || !targetPath) throw new Error("sessionId and path are required");

  // Use SSH exec with rm -rf for reliability (handles both files and dirs)
  const result = await handleExec({ sessionId, command: `rm -rf ${JSON.stringify(targetPath)}` });
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

  const result = await handleExec({ sessionId, command: `mv ${JSON.stringify(oldPath)} ${JSON.stringify(newPath)}` });
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
  const result = await handleExec({ sessionId, command: `stat -c '{"size":%s,"mode":"%a","mtime":%Y,"type":"%F"}' ${JSON.stringify(targetPath)}` });
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

function toUnpackedAsarPath(filePath) {
  const unpackedPath = filePath.replace(/app\.asar([\\/])/, "app.asar.unpacked$1");
  if (unpackedPath !== filePath && existsSync(unpackedPath)) {
    return unpackedPath;
  }
  return filePath;
}

function buildMcpServerConfig(port, scopedSessionIds) {
  const runtimePath = toUnpackedAsarPath(
    path.join(__dirname, "..", "mcp", "netcatty-mcp-server.cjs"),
  );

  const env = [
    { name: "NETCATTY_MCP_PORT", value: String(port) },
  ];

  if (scopedSessionIds && scopedSessionIds.length > 0) {
    env.push({ name: "NETCATTY_MCP_SESSION_IDS", value: scopedSessionIds.join(",") });
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

function cleanup() {
  if (tcpServer) {
    tcpServer.close();
    tcpServer = null;
    tcpPort = null;
    console.log("[MCP Bridge] TCP server closed");
  }
}

module.exports = {
  init,
  setCommandBlocklist,
  updateSessionMetadata,
  getOrCreateHost,
  buildMcpServerConfig,
  cancelAllPtyExecs,
  cleanup,
};
