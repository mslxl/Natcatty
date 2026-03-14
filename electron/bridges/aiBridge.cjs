/**
 * AI Bridge - Handles AI provider API calls and agent tool execution
 *
 * Proxies LLM API calls through the main process (avoiding CORS),
 * and provides tool execution capabilities for the Catty Agent.
 */

const https = require("node:https");
const http = require("node:http");
const { URL } = require("node:url");
const { spawn } = require("node:child_process");
const path = require("node:path");

let sessions = null;
let sftpClients = null;
let electronModule = null;

// Active streaming requests (for cancellation)
const activeStreams = new Map();

// External agent processes
const agentProcesses = new Map();

// ACP providers (module-level so cleanup() can access them)
const acpProviders = new Map();
const acpActiveStreams = new Map();

function init(deps) {
  sessions = deps.sessions;
  sftpClients = deps.sftpClients;
  electronModule = deps.electronModule;
}

/**
 * Make a streaming HTTP request and forward SSE events back to renderer
 */
function streamRequest(url, options, event, requestId) {
  return new Promise((resolve, reject) => {
    const parsedUrl = new URL(url);
    const isHttps = parsedUrl.protocol === "https:";
    const lib = isHttps ? https : http;

    const req = lib.request(
      parsedUrl,
      {
        method: options.method || "POST",
        headers: options.headers || {},
        timeout: 120000, // 2 min connection timeout
      },
      (res) => {
        if (res.statusCode && (res.statusCode < 200 || res.statusCode >= 300)) {
          let errorBody = "";
          res.on("data", (chunk) => { errorBody += chunk.toString(); });
          res.on("end", () => {
            event.sender.send("netcatty:ai:stream:error", {
              requestId,
              error: `HTTP ${res.statusCode}: ${errorBody}`,
            });
            resolve();
          });
          return;
        }

        let buffer = "";

        res.on("data", (chunk) => {
          buffer += chunk.toString();
          const lines = buffer.split("\n");
          buffer = lines.pop() || "";

          for (const line of lines) {
            const trimmed = line.trim();
            if (!trimmed) continue;

            // Forward raw SSE data line to renderer
            if (trimmed.startsWith("data: ")) {
              event.sender.send("netcatty:ai:stream:data", {
                requestId,
                data: trimmed.slice(6),
              });
            }
          }
        });

        res.on("end", () => {
          // Flush any remaining buffer
          if (buffer.trim().startsWith("data: ")) {
            event.sender.send("netcatty:ai:stream:data", {
              requestId,
              data: buffer.trim().slice(6),
            });
          }
          event.sender.send("netcatty:ai:stream:end", { requestId });
          activeStreams.delete(requestId);
          resolve();
        });

        res.on("error", (err) => {
          event.sender.send("netcatty:ai:stream:error", {
            requestId,
            error: err.message,
          });
          activeStreams.delete(requestId);
          resolve();
        });
      }
    );

    req.on("error", (err) => {
      event.sender.send("netcatty:ai:stream:error", {
        requestId,
        error: err.message,
      });
      activeStreams.delete(requestId);
      reject(err);
    });

    req.on("timeout", () => {
      req.destroy();
      event.sender.send("netcatty:ai:stream:error", {
        requestId,
        error: "Request timeout",
      });
      activeStreams.delete(requestId);
    });

    // Store ref for cancellation
    activeStreams.set(requestId, req);

    if (options.body) {
      req.write(options.body);
    }
    req.end();
  });
}

function registerHandlers(ipcMain) {
  // Start a streaming chat request (proxied through main process)
  ipcMain.handle("netcatty:ai:chat:stream", async (event, { requestId, url, headers, body }) => {
    try {
      await streamRequest(url, { method: "POST", headers, body }, event, requestId);
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });

  // Cancel an active stream
  ipcMain.handle("netcatty:ai:chat:cancel", async (_event, { requestId }) => {
    const req = activeStreams.get(requestId);
    if (req) {
      req.destroy();
      activeStreams.delete(requestId);
      return true;
    }
    return false;
  });

  // Non-streaming request (for model listing, validation, etc.)
  ipcMain.handle("netcatty:ai:fetch", async (_event, { url, method, headers, body }) => {
    return new Promise((resolve) => {
      const parsedUrl = new URL(url);
      const isHttps = parsedUrl.protocol === "https:";
      const lib = isHttps ? https : http;

      const req = lib.request(
        parsedUrl,
        { method: method || "GET", headers: headers || {}, timeout: 30000 },
        (res) => {
          let data = "";
          res.on("data", (chunk) => { data += chunk.toString(); });
          res.on("end", () => {
            resolve({
              ok: res.statusCode >= 200 && res.statusCode < 300,
              status: res.statusCode,
              data,
            });
          });
        }
      );

      req.on("error", (err) => {
        resolve({ ok: false, status: 0, data: "", error: err.message });
      });
      req.on("timeout", () => {
        req.destroy();
        resolve({ ok: false, status: 0, data: "", error: "Request timeout" });
      });

      if (body) req.write(body);
      req.end();
    });
  });

  // Execute a command on a terminal session (for Catty Agent)
  ipcMain.handle("netcatty:ai:exec", async (_event, { sessionId, command }) => {
    const session = sessions?.get(sessionId);
    if (!session) {
      return { ok: false, error: "Session not found" };
    }

    try {
      // Use SSH exec for remote sessions
      if (session.sshClient) {
        return new Promise((resolve) => {
          session.sshClient.exec(command, (err, stream) => {
            if (err) {
              resolve({ ok: false, error: err.message });
              return;
            }
            let stdout = "";
            let stderr = "";
            stream.on("data", (data) => { stdout += data.toString(); });
            stream.stderr.on("data", (data) => { stderr += data.toString(); });
            stream.on("close", (code) => {
              resolve({ ok: code === 0, stdout, stderr, exitCode: code });
            });
          });
        });
      }

      // For local sessions, we can't easily exec - return info about session type
      return { ok: false, error: "Command execution only supported for SSH sessions" };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });

  // Write to terminal session (send input like a user typing)
  ipcMain.handle("netcatty:ai:terminal:write", async (_event, { sessionId, data }) => {
    const session = sessions?.get(sessionId);
    if (!session) {
      return { ok: false, error: "Session not found" };
    }
    try {
      if (session.stream) {
        session.stream.write(data);
        return { ok: true };
      }
      if (session.pty) {
        session.pty.write(data);
        return { ok: true };
      }
      return { ok: false, error: "No writable stream for session" };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });

  // Discover external agents in system PATH
  ipcMain.handle("netcatty:ai:agents:discover", async () => {
    const { execSync } = require("node:child_process");
    const agents = [];
    const knownAgents = [
      {
        command: "claude",
        name: "Claude Code",
        icon: "claude",
        description: "Anthropic's agentic coding assistant",
        acpCommand: "claude-code-acp",
        acpArgs: [],
        args: ["-p", "--output-format", "text", "{prompt}"],  // fallback
      },
      {
        command: "codex",
        name: "Codex CLI",
        icon: "openai",
        description: "OpenAI's coding agent",
        acpCommand: "codex-acp",
        acpArgs: [],
        args: ["exec", "--full-auto", "--json", "{prompt}"],  // fallback
      },
      {
        command: "gemini",
        name: "Gemini CLI",
        icon: "gemini",
        description: "Google's Gemini CLI agent",
        acpCommand: "gemini",
        acpArgs: ["--experimental-acp"],
        args: ["{prompt}"],  // fallback
      },
    ];

    // Use the real shell environment for PATH resolution
    const shellEnv = await getShellEnv();

    for (const agent of knownAgents) {
      try {
        const whichCmd = process.platform === "win32" ? "where" : "which";
        const result = execSync(`${whichCmd} ${agent.command}`, {
          encoding: "utf8",
          timeout: 5000,
          stdio: ["pipe", "pipe", "pipe"],
          env: shellEnv,
        }).trim();
        if (result) {
          // Try to get version
          let version = "";
          try {
            version = execSync(`${result.split("\n")[0].trim()} --version`, {
              encoding: "utf8",
              timeout: 5000,
              stdio: ["pipe", "pipe", "pipe"],
            }).trim().split("\n")[0];
          } catch {}

          agents.push({
            ...agent,
            path: result.split("\n")[0].trim(),
            version,
            available: true,
          });
        }
      } catch {
        // Agent not found - don't include unavailable ones
      }
    }

    return agents;
  });

  // Resolve user's real shell environment (Electron GUI apps have a minimal PATH).
  // Cache the result so we only do this once.
  let _cachedShellEnv = null;
  async function getShellEnv() {
    if (_cachedShellEnv) return _cachedShellEnv;

    const home = process.env.HOME || "";
    // Extra paths to always include
    const extraPaths = [
      `${home}/.local/bin`,
      `${home}/.npm-global/bin`,
      "/usr/local/bin",
      "/opt/homebrew/bin",
    ];

    if (process.platform === "win32") {
      _cachedShellEnv = {
        ...process.env,
        PATH: [...extraPaths, process.env.PATH || ""].join(path.delimiter),
      };
      return _cachedShellEnv;
    }

    // On macOS/Linux, spawn a login shell to capture the real environment.
    try {
      const { execSync } = require("node:child_process");
      const shell = process.env.SHELL || "/bin/zsh";
      const envOutput = execSync(`${shell} -ilc 'env'`, {
        encoding: "utf8",
        timeout: 10000,
        stdio: ["pipe", "pipe", "pipe"],
        env: { ...process.env, HOME: home },
      });
      const envMap = {};
      for (const line of envOutput.split("\n")) {
        const idx = line.indexOf("=");
        if (idx > 0) {
          envMap[line.slice(0, idx)] = line.slice(idx + 1);
        }
      }
      // Merge: login-shell env as base, then process.env overrides, then extra paths
      const shellPath = envMap.PATH || "";
      _cachedShellEnv = {
        ...envMap,
        ...process.env,
        PATH: [...extraPaths, shellPath, process.env.PATH || ""].join(path.delimiter),
      };
    } catch {
      // Fallback if login shell fails
      _cachedShellEnv = {
        ...process.env,
        PATH: [...extraPaths, process.env.PATH || ""].join(path.delimiter),
      };
    }
    return _cachedShellEnv;
  }

  // Spawn an external agent process
  ipcMain.handle("netcatty:ai:agent:spawn", async (event, { agentId, command, args, env, closeStdin }) => {
    if (agentProcesses.has(agentId)) {
      return { ok: false, error: "Agent already running" };
    }

    try {
      const shellEnv = await getShellEnv();
      const stdinMode = closeStdin ? "ignore" : "pipe";

      const proc = spawn(command, args || [], {
        stdio: [stdinMode, "pipe", "pipe"],
        env: { ...shellEnv, ...env },
      });

      proc.stdout.on("data", (data) => {
        event.sender.send("netcatty:ai:agent:stdout", {
          agentId,
          data: data.toString(),
        });
      });

      proc.stderr.on("data", (data) => {
        event.sender.send("netcatty:ai:agent:stderr", {
          agentId,
          data: data.toString(),
        });
      });

      proc.on("exit", (code) => {
        agentProcesses.delete(agentId);
        event.sender.send("netcatty:ai:agent:exit", { agentId, code });
      });

      proc.on("error", (err) => {
        agentProcesses.delete(agentId);
        event.sender.send("netcatty:ai:agent:error", {
          agentId,
          error: err.message,
        });
      });

      agentProcesses.set(agentId, proc);

      return { ok: true, pid: proc.pid };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });

  // Send data to agent's stdin
  ipcMain.handle("netcatty:ai:agent:write", async (_event, { agentId, data }) => {
    const proc = agentProcesses.get(agentId);
    if (!proc) return { ok: false, error: "Agent not found" };
    try {
      if (!proc.stdin || proc.stdin.destroyed) {
        return { ok: false, error: "stdin not available" };
      }
      proc.stdin.write(data);
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });

  // Close agent's stdin (signal EOF)
  ipcMain.handle("netcatty:ai:agent:close-stdin", async (_event, { agentId }) => {
    const proc = agentProcesses.get(agentId);
    if (!proc) return { ok: false, error: "Agent not found" };
    try {
      if (proc.stdin && !proc.stdin.destroyed) {
        proc.stdin.end();
      }
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });

  // ── ACP (Agent Client Protocol) streaming ──

  /**
   * Resolve the bundled codex-acp binary path (from @zed-industries/codex-acp).
   * Falls back to system PATH "codex-acp" if not found.
   */
  function resolveCodexAcpBinaryPath() {
    try {
      const platformPkgMap = {
        "darwin-arm64": "@zed-industries/codex-acp-darwin-arm64",
        "darwin-x64": "@zed-industries/codex-acp-darwin-x64",
        "linux-arm64": "@zed-industries/codex-acp-linux-arm64",
        "linux-x64": "@zed-industries/codex-acp-linux-x64",
        "win32-arm64": "@zed-industries/codex-acp-win32-arm64",
        "win32-x64": "@zed-industries/codex-acp-win32-x64",
      };
      const key = `${process.platform}-${process.arch}`;
      const pkgName = platformPkgMap[key];
      if (!pkgName) return "codex-acp"; // fallback

      const binaryName = process.platform === "win32" ? "codex-acp.exe" : "codex-acp";
      const pkgRoot = path.dirname(require.resolve("@zed-industries/codex-acp/package.json"));
      const resolved = require.resolve(`${pkgName}/bin/${binaryName}`, { paths: [pkgRoot] });

      // Handle Electron ASAR: the binary must be in unpacked resources
      return resolved.replace(/\.asar([\\/])/, ".asar.unpacked$1");
    } catch {
      return "codex-acp"; // fallback to PATH
    }
  }

  // acpProviders and acpActiveStreams are module-level (see top of file)

  ipcMain.handle("netcatty:ai:acp:stream", async (event, { requestId, chatSessionId, acpCommand, acpArgs, prompt, cwd, apiKey }) => {
    try {
      const { createACPProvider } = require("@mcpc-tech/acp-ai-provider");
      const { streamText } = require("ai");

      const shellEnv = await getShellEnv();

      // Reuse existing ACP provider for the same chat session
      let providerEntry = acpProviders.get(chatSessionId);
      if (!providerEntry || providerEntry.acpCommand !== acpCommand) {
        // Clean up old provider if switching agent
        if (providerEntry) {
          try { providerEntry.provider.cleanup(); } catch {}
        }

        // Auth logic:
        // - If user provided an API key, use codex-api-key method
        // - Otherwise, use "chatgpt" method to leverage stored OAuth credentials
        const agentEnv = { ...shellEnv };
        let authMethodId = "chatgpt"; // default: use stored OAuth from `codex login`

        if (apiKey) {
          agentEnv.CODEX_API_KEY = apiKey;
          authMethodId = "codex-api-key";
        }

        // Use bundled binary for known agents, fall back to the command from config
        let resolvedCommand = acpCommand;
        if (acpCommand === "codex-acp") {
          resolvedCommand = resolveCodexAcpBinaryPath();
          console.log("[ACP] Using codex-acp binary:", resolvedCommand);
        }

        const provider = createACPProvider({
          command: resolvedCommand,
          args: acpArgs || [],
          env: agentEnv,
          session: {
            cwd: cwd || process.cwd(),
            mcpServers: [],
          },
          authMethodId,
          persistSession: true,
        });
        providerEntry = { provider, acpCommand };
        acpProviders.set(chatSessionId, providerEntry);
      }

      const abortController = new AbortController();
      acpActiveStreams.set(requestId, abortController);

      const { stepCountIs } = require("ai");

      const result = streamText({
        model: providerEntry.provider.languageModel(),
        tools: providerEntry.provider.tools,
        prompt,
        stopWhen: stepCountIs(50),
        abortSignal: abortController.signal,
      });

      let hasContent = false;
      for await (const chunk of result.fullStream) {
        if (abortController.signal.aborted) break;
        try {
          const serialized = JSON.parse(JSON.stringify(chunk));
          if (serialized.type && serialized.type !== "text-delta" && serialized.type !== "reasoning-delta") {
            console.log("[ACP stream]", serialized.type, serialized.id ? `id=${serialized.id}` : "");
          }
          if (serialized.type === "text-delta" || serialized.type === "reasoning-delta" || serialized.type === "tool-call") {
            hasContent = true;
          }
          event.sender.send("netcatty:ai:acp:event", {
            requestId,
            event: serialized,
          });
        } catch (serErr) {
          console.warn("[ACP stream] Failed to serialize chunk:", chunk?.type, serErr?.message);
        }
      }

      // If stream completed with zero content, likely an auth or connection issue
      if (!hasContent && !abortController.signal.aborted) {
        event.sender.send("netcatty:ai:acp:error", {
          requestId,
          error: "Agent returned empty response. This usually means authentication failed.\n\nPlease run `codex login` in your terminal, or set an API key in Settings → AI → OpenAI provider.",
        });
      } else {
        event.sender.send("netcatty:ai:acp:done", { requestId });
      }
    } catch (err) {
      const errMsg = err?.message || String(err);
      const authHints = ["auth", "login", "token", "unauthorized", "401", "403", "invalid_grant", "invalid_token", "credentials"];
      const isAuthError = authHints.some(h => errMsg.toLowerCase().includes(h));

      if (isAuthError) {
        console.error("[ACP] Auth error — user needs to re-login:", errMsg);
        // Cleanup the provider so next attempt creates a fresh one
        const entry = acpProviders.get(chatSessionId);
        if (entry) {
          try { entry.provider.cleanup(); } catch {}
          acpProviders.delete(chatSessionId);
        }
      }

      event.sender.send("netcatty:ai:acp:error", {
        requestId,
        error: isAuthError
          ? `Authentication failed. Please run \`codex login\` in your terminal to re-authenticate, or configure an OpenAI API key in Settings → AI.\n\nDetails: ${errMsg}`
          : errMsg,
      });
    } finally {
      acpActiveStreams.delete(requestId);
    }

    return { ok: true };
  });

  ipcMain.handle("netcatty:ai:acp:cancel", async (_event, { requestId }) => {
    const controller = acpActiveStreams.get(requestId);
    if (controller) {
      controller.abort();
      acpActiveStreams.delete(requestId);
      return { ok: true };
    }
    return { ok: false, error: "Stream not found" };
  });

  // Cleanup a specific ACP session (when chat session is deleted)
  ipcMain.handle("netcatty:ai:acp:cleanup", async (_event, { chatSessionId }) => {
    const entry = acpProviders.get(chatSessionId);
    if (entry) {
      try { entry.provider.cleanup(); } catch {}
      acpProviders.delete(chatSessionId);
    }
    return { ok: true };
  });

  // Kill an agent process
  ipcMain.handle("netcatty:ai:agent:kill", async (_event, { agentId }) => {
    const proc = agentProcesses.get(agentId);
    if (!proc) return { ok: false, error: "Agent not found" };
    try {
      proc.kill("SIGTERM");
      setTimeout(() => {
        if (agentProcesses.has(agentId)) {
          try { proc.kill("SIGKILL"); } catch {}
        }
      }, 5000);
      return { ok: true };
    } catch (err) {
      return { ok: false, error: err?.message || String(err) };
    }
  });
}

// Cleanup all agent processes on shutdown
function cleanup() {
  for (const [id, proc] of agentProcesses) {
    try {
      proc.kill("SIGTERM");
    } catch {}
  }
  agentProcesses.clear();

  for (const [id, req] of activeStreams) {
    try {
      req.destroy();
    } catch {}
  }
  activeStreams.clear();

  // Abort active ACP streams
  for (const [id, controller] of acpActiveStreams) {
    try { controller.abort(); } catch {}
  }
  acpActiveStreams.clear();

  // Cleanup ACP providers (kills codex-acp child processes)
  for (const [id, entry] of acpProviders) {
    try { entry.provider.cleanup(); } catch {}
  }
  acpProviders.clear();
}

module.exports = { init, registerHandlers, cleanup };
