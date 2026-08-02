"use strict";

/**
 * Grok Build ACP turn runner — `grok agent … stdio` JSON-RPC client.
 *
 * Lifecycle (Agent Client Protocol):
 *   initialize → session/new (cwd + mcpServers) → session/prompt
 *   session/update notifications → canonical Netcatty emitter events
 *
 * Prefer session-level mcpServers over project `.grok/config.toml` merge.
 * Keep the headless streaming-json driver as an explicit fallback runtime.
 */
const { spawn } = require("node:child_process");
const { StringDecoder } = require("node:string_decoder");
const { mcpEnvPairsToObject } = require("./injectMcp.cjs");
const {
  GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS,
  createLineBuffer,
  formatGrokErrorForUser,
} = require("./grokDriver.cjs");

const GROK_ACP_ABORT_GRACE_MS = 1_500;
const MAX_GROK_ACP_LINE_BYTES = 10 * 1024 * 1024;
const MAX_GROK_ACP_STDERR_CHARS = 64 * 1024;
const ACP_PROTOCOL_VERSION = 1;

function signalProcessTree(child, signal, forceKillImpl) {
  if (!child) return;
  if (typeof forceKillImpl === "function") {
    try { forceKillImpl(child, signal); } catch { /* ignore */ }
    return;
  }
  if (process.platform === "win32" && signal === "SIGKILL" && child.pid) {
    try {
      const killer = spawn("taskkill", ["/pid", String(child.pid), "/T", "/F"], {
        stdio: "ignore",
        windowsHide: true,
      });
      killer.on("error", () => {});
      killer.unref?.();
      return;
    } catch {
      // fall through
    }
  }
  if (process.platform !== "win32" && child.pid) {
    try {
      process.kill(-child.pid, signal);
      return;
    } catch {
      // fall through
    }
  }
  try { child.kill(signal); } catch { /* ignore */ }
}

/**
 * Build argv for `grok agent [flags] stdio`.
 */
function buildGrokAcpSpawnArgs({
  model,
  permissionMode,
  toolIntegrationMode,
} = {}) {
  const args = ["agent"];
  const mode = String(permissionMode || "confirm").toLowerCase();
  // Non-interactive Netcatty turns cannot answer ACP permission prompts.
  if (mode !== "observer") {
    args.push("--always-approve");
  }
  const modelId = String(model || "").trim();
  if (modelId) {
    args.push("-m", modelId);
  }
  args.push("stdio");
  return args;
}

/**
 * Convert Netcatty injectMcp configs into ACP session/new mcpServers entries.
 */
function toAcpMcpServers(injectedMcpServers) {
  const out = [];
  for (const cfg of injectedMcpServers || []) {
    if (!cfg || !cfg.name || !cfg.command) continue;
    const entry = {
      name: String(cfg.name),
      command: String(cfg.command),
      args: Array.isArray(cfg.args) ? cfg.args.map(String) : [],
      env: mcpEnvPairsToObject(cfg.env),
    };
    out.push(entry);
  }
  return out;
}

/**
 * Build session/new params including MCP servers and permission meta.
 */
function buildGrokAcpSessionNewParams({
  cwd,
  injectedMcpServers,
  permissionMode,
  toolIntegrationMode,
  systemContext,
} = {}) {
  const mode = String(permissionMode || "confirm").toLowerCase();
  const toolMode = String(toolIntegrationMode || "mcp").toLowerCase();
  const params = {
    cwd: String(cwd || process.cwd() || "."),
    mcpServers: toAcpMcpServers(injectedMcpServers),
    _meta: {},
  };
  if (mode !== "observer") {
    params._meta.yoloMode = true;
  } else {
    // Soft read-oriented path when no interactive approval UI is available.
    params._meta.autoMode = true;
  }
  if (toolMode !== "skills") {
    params._meta.rules = [
      "Netcatty MCP mode is active. Do not use local shell, search_replace, or write tools for side effects.",
      "Operate on remote terminal sessions only through the injected netcatty-remote-hosts MCP server.",
      `Disallowed local built-ins (policy): ${GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS.join(", ")}.`,
    ].join(" ");
  }
  if (systemContext && String(systemContext).trim()) {
    // Prefer additive rules so Grok keeps its agent profile; overflow goes to rules.
    const existing = params._meta.rules ? `${params._meta.rules} ` : "";
    params._meta.rules = `${existing}${String(systemContext).trim()}`.slice(0, 16_000);
  }
  return params;
}

function buildGrokAcpInitializeParams() {
  return {
    protocolVersion: ACP_PROTOCOL_VERSION,
    clientCapabilities: {
      fs: { readTextFile: false, writeTextFile: false },
      terminal: false,
    },
    clientInfo: {
      name: "netcatty",
      version: "0.0.0",
    },
  };
}

function buildGrokAcpPromptParams(sessionId, prompt) {
  return {
    sessionId: String(sessionId || ""),
    prompt: [{ type: "text", text: String(prompt || "") }],
  };
}

function resultToText(result) {
  if (result == null) return "";
  if (typeof result === "string") return result;
  if (typeof result === "number" || typeof result === "boolean") return String(result);
  if (typeof result === "object") {
    if (typeof result.content === "string") return result.content;
    if (typeof result.text === "string") return result.text;
    try { return JSON.stringify(result); } catch { return String(result); }
  }
  return String(result);
}

function closeReasoning(state, emitter) {
  if (state?.reasoningOpen) {
    emitter.reasoningEnd();
    state.reasoningOpen = false;
  }
}

/**
 * Map one ACP session/update payload (or x.ai notification) to emitter calls.
 * @returns {boolean} true when the stream should stop on error
 */
function translateGrokAcpUpdate(update, emitter, state = {}) {
  if (!update || typeof update !== "object") return false;
  const kind = String(
    update.sessionUpdate
    || update.type
    || update.kind
    || "",
  );

  switch (kind) {
    case "agent_message_chunk":
    case "message_chunk":
    case "text": {
      closeReasoning(state, emitter);
      const text = update.content?.text
        ?? update.text
        ?? update.data
        ?? (typeof update.content === "string" ? update.content : "");
      if (text) {
        emitter.text(String(text));
        state.streamedAssistantText = true;
      }
      return false;
    }

    case "agent_thought_chunk":
    case "thought_chunk":
    case "thought": {
      const text = update.content?.text
        ?? update.text
        ?? update.data
        ?? "";
      if (text) {
        emitter.reasoning(String(text));
        state.reasoningOpen = true;
      }
      return false;
    }

    case "tool_call": {
      closeReasoning(state, emitter);
      const id = update.toolCallId || update.tool_call_id || update.id;
      if (!id) return false;
      if (!state.emittedToolCalls) state.emittedToolCalls = new Set();
      if (!state.toolNames) state.toolNames = new Map();
      const name = String(
        update.toolName
        || update.tool_name
        || update.title
        || update.kind
        || "tool",
      );
      const args = update.rawInput && typeof update.rawInput === "object"
        ? update.rawInput
        : (update.input && typeof update.input === "object" ? update.input : {});
      state.toolNames.set(id, name);
      if (!state.emittedToolCalls.has(id)) {
        state.emittedToolCalls.add(id);
        emitter.toolCall(name, args, id);
      }
      return false;
    }

    case "tool_call_update": {
      closeReasoning(state, emitter);
      const id = update.toolCallId || update.tool_call_id || update.id;
      if (!id) return false;
      if (!state.emittedToolResults) state.emittedToolResults = new Set();
      if (!state.emittedToolCalls) state.emittedToolCalls = new Set();
      if (!state.toolNames) state.toolNames = new Map();
      const status = String(update.status || "").toLowerCase();
      const name = state.toolNames.get(id)
        || String(update.toolName || update.tool_name || update.title || "tool");
      if (!state.emittedToolCalls.has(id)) {
        state.emittedToolCalls.add(id);
        state.toolNames.set(id, name);
        emitter.toolCall(name, {}, id);
      }
      if (
        status === "completed"
        || status === "failed"
        || status === "error"
        || status === "cancelled"
        || update.rawOutput != null
      ) {
        if (!state.emittedToolResults.has(id)) {
          state.emittedToolResults.add(id);
          emitter.toolResult(
            id,
            resultToText(update.rawOutput ?? update.content ?? update.error ?? ""),
            name,
          );
        }
      }
      return false;
    }

    case "plan": {
      const entries = Array.isArray(update.entries) ? update.entries : [];
      if (entries.length && typeof emitter.planUpdate === "function") {
        const items = entries.map((entry, index) => {
          if (typeof entry === "string") {
            return { id: `plan-${index}`, content: entry, status: "pending" };
          }
          if (entry && typeof entry === "object") {
            return {
              id: String(entry.id || `plan-${index}`),
              content: String(entry.content || entry.text || entry.title || ""),
              status: String(entry.status || "pending"),
            };
          }
          return { id: `plan-${index}`, content: String(entry ?? ""), status: "pending" };
        }).filter((item) => item.content);
        if (items.length) emitter.planUpdate("grok-plan", items, "updated");
      }
      return false;
    }

    case "error": {
      closeReasoning(state, emitter);
      state.failed = true;
      emitter.emitError(formatGrokErrorForUser(update.message || update.error || "Grok ACP turn failed"));
      return true;
    }

    default:
      return false;
  }
}

/**
 * Handle a parsed JSON-RPC message from grok agent stdio.
 */
function handleGrokAcpMessage(message, { emitter, state, pending, onPromptComplete }) {
  if (!message || typeof message !== "object") return;

  // Response to a request
  if (Object.prototype.hasOwnProperty.call(message, "id") && message.id != null && !message.method) {
    const waiter = pending.get(message.id);
    if (waiter) {
      pending.delete(message.id);
      if (message.error) {
        waiter.reject(new Error(
          message.error.message || message.error.data || JSON.stringify(message.error),
        ));
      } else {
        waiter.resolve(message.result);
      }
    }
    // session/prompt resolves when the turn completes
    if (state.promptRequestId != null && message.id === state.promptRequestId) {
      onPromptComplete?.(message);
    }
    return;
  }

  const method = String(message.method || "");
  const params = message.params && typeof message.params === "object" ? message.params : {};

  if (method === "session/update" || method === "x.ai/session/update") {
    const update = params.update || params.sessionUpdate || params;
    // Nested: params.update.sessionUpdate
    const payload = update?.sessionUpdate || update?.type
      ? update
      : (params.sessionUpdate ? { sessionUpdate: params.sessionUpdate, ...params } : update);
    if (payload) translateGrokAcpUpdate(payload, emitter, state);
    if (params.sessionId && !state.sessionId) {
      state.sessionId = params.sessionId;
      emitter.sessionId?.(params.sessionId);
    }
    return;
  }

  if (method === "session/request_permission" || method === "request_permission") {
    // Non-interactive: auto-allow when yolo was requested; otherwise deny.
    const id = message.id;
    if (id == null) return;
    const allow = state.autoAllowPermissions !== false;
    const result = allow
      ? { outcome: { outcome: "selected", optionId: "allow-once" } }
      : { outcome: { outcome: "cancelled" } };
    // Caller writes responses via pending write hook
    if (typeof state.writeResponse === "function") {
      state.writeResponse(id, result);
    }
    return;
  }
}

function createJsonRpcClient({ write, onMessage }) {
  let nextId = 1;
  const pending = new Map();

  function request(method, params, { timeoutMs = 120_000 } = {}) {
    const id = nextId++;
    const payload = { jsonrpc: "2.0", id, method, params };
    write(`${JSON.stringify(payload)}\n`);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error(`Grok ACP request timed out: ${method}`));
      }, timeoutMs);
      timer.unref?.();
      pending.set(id, {
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (err) => {
          clearTimeout(timer);
          reject(err);
        },
      });
    });
  }

  function notify(method, params) {
    write(`${JSON.stringify({ jsonrpc: "2.0", method, params })}\n`);
  }

  function handleLine(line) {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      return;
    }
    onMessage(message, pending);
  }

  function rejectAll(err) {
    for (const [, waiter] of pending) {
      try { waiter.reject(err); } catch { /* ignore */ }
    }
    pending.clear();
  }

  return { request, notify, handleLine, pending, rejectAll, getNextId: () => nextId };
}

async function runGrokAcpTurn({
  prompt,
  systemPrompt,
  binPath,
  cwd,
  model,
  env,
  permissionMode,
  toolIntegrationMode,
  resumeSessionId,
  injectedMcpServers,
  emitter,
  signal,
  spawnImpl,
  abortGraceMs = GROK_ACP_ABORT_GRACE_MS,
  forceKillImpl,
  // inject for tests: skip real spawn
  rpcClientFactory,
}) {
  const cliPath = String(binPath || "").trim();
  if (!cliPath) {
    emitter.emitError(
      "Grok Build CLI not found. Install the Grok CLI (`grok`) and ensure it is on PATH, or set the path in Settings → AI.",
    );
    return { sessionId: resumeSessionId || null, runtime: "acp" };
  }

  const effectiveCwd = String(cwd || process.cwd() || "").trim() || process.cwd();
  const childEnv = { ...(env || process.env) };
  const spawnArgs = buildGrokAcpSpawnArgs({
    model,
    permissionMode,
    toolIntegrationMode,
  });

  const state = {
    sessionId: resumeSessionId || null,
    reasoningOpen: false,
    streamedAssistantText: false,
    failed: false,
    autoAllowPermissions: String(permissionMode || "confirm").toLowerCase() !== "observer",
    promptRequestId: null,
    writeResponse: null,
  };

  // Test inject path: pure RPC loop without process
  if (typeof rpcClientFactory === "function") {
    const client = rpcClientFactory({ state, emitter });
    try {
      await client.request("initialize", buildGrokAcpInitializeParams());
      if (resumeSessionId) {
        state.sessionId = resumeSessionId;
        emitter.sessionId?.(resumeSessionId);
      } else {
        const created = await client.request(
          "session/new",
          buildGrokAcpSessionNewParams({
            cwd: effectiveCwd,
            injectedMcpServers,
            permissionMode,
            toolIntegrationMode,
            systemContext: systemPrompt,
          }),
        );
        const sessionId = created?.sessionId || created?.session_id;
        if (sessionId) {
          state.sessionId = sessionId;
          emitter.sessionId?.(sessionId);
        }
      }
      await client.request(
        "session/prompt",
        buildGrokAcpPromptParams(state.sessionId, prompt),
      );
      closeReasoning(state, emitter);
      if (!state.failed && !signal?.aborted) emitter.emitDone();
    } catch (err) {
      if (!state.failed && !signal?.aborted) {
        state.failed = true;
        emitter.emitError(formatGrokErrorForUser(err?.message || String(err)));
      }
    }
    return { sessionId: state.sessionId, runtime: "acp" };
  }

  const spawnFn = spawnImpl || spawn;
  let child;
  try {
    child = spawnFn(cliPath, spawnArgs, {
      cwd: effectiveCwd,
      env: childEnv,
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
      detached: process.platform !== "win32",
    });
  } catch (err) {
    emitter.emitError(formatGrokErrorForUser(err?.message || String(err)));
    return { sessionId: state.sessionId, runtime: "acp" };
  }

  let stderrText = "";
  let stderrBytes = 0;
  let stderrTruncated = false;
  let stderrEnded = false;
  const stderrDecoder = new StringDecoder("utf8");
  let settled = false;
  let forceKillTimer = null;
  let abortHandler = null;

  const writeLine = (line) => {
    if (!child?.stdin || child.stdin.destroyed) return;
    try {
      child.stdin.write(line);
    } catch {
      /* ignore */
    }
  };

  state.writeResponse = (id, result) => {
    writeLine(`${JSON.stringify({ jsonrpc: "2.0", id, result })}\n`);
  };

  let promptDoneResolve;
  const promptDone = new Promise((resolve) => {
    promptDoneResolve = resolve;
  });

  const rpc = createJsonRpcClient({
    write: writeLine,
    onMessage: (message, pending) => {
      if (signal?.aborted) return;
      handleGrokAcpMessage(message, {
        emitter,
        state,
        pending,
        onPromptComplete: () => {
          promptDoneResolve?.();
        },
      });
    },
  });

  // Track prompt request id so completion is detected
  const originalRequest = rpc.request.bind(rpc);
  rpc.request = async (method, params, options) => {
    const idBefore = rpc.getNextId();
    if (method === "session/prompt") {
      state.promptRequestId = idBefore;
    }
    return originalRequest(method, params, options);
  };

  const lineBuffer = createLineBuffer((line) => {
    if (signal?.aborted) return;
    try {
      rpc.handleLine(line);
    } catch (err) {
      if (!state.failed) {
        state.failed = true;
        emitter.emitError(formatGrokErrorForUser(err?.message || String(err)));
      }
    }
  }, MAX_GROK_ACP_LINE_BYTES);

  child.stdout?.on("data", (chunk) => {
    if (signal?.aborted) return;
    try {
      lineBuffer.push(chunk);
    } catch (err) {
      if (!state.failed) {
        state.failed = true;
        emitter.emitError(formatGrokErrorForUser(err?.message || String(err)));
      }
      signalProcessTree(child, "SIGKILL", forceKillImpl);
    }
  });
  child.stderr?.on("data", (chunk) => {
    if (signal?.aborted) return;
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk));
    const remaining = Math.max(0, MAX_GROK_ACP_STDERR_CHARS - stderrBytes);
    const accepted = buffer.length <= remaining ? buffer : buffer.subarray(0, remaining);
    if (accepted.length > 0) stderrText += stderrDecoder.write(accepted);
    stderrBytes += accepted.length;
    if (accepted.length < buffer.length) stderrTruncated = true;
  });

  const closePromise = new Promise((resolve) => {
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(forceKillTimer);
      if (!signal?.aborted) {
        try { lineBuffer.flush(); } catch { /* ignore */ }
      }
      resolve();
    };
    child.on("error", (err) => {
      if (!state.failed && !signal?.aborted) {
        state.failed = true;
        emitter.emitError(formatGrokErrorForUser(err?.message || String(err)));
      }
      rpc.rejectAll(err || new Error("Grok ACP process error"));
      promptDoneResolve?.();
      finish();
    });
    child.on("close", (code) => {
      if (!stderrEnded) {
        stderrEnded = true;
        if (!stderrTruncated || stderrDecoder.lastNeed === 0) stderrText += stderrDecoder.end();
      }
      if (!state.failed && !signal?.aborted && code && code !== 0 && !state.streamedAssistantText) {
        const stderr = stderrText.trim();
        state.failed = true;
        emitter.emitError(formatGrokErrorForUser(stderr || `Grok ACP exited with code ${code}`));
      }
      rpc.rejectAll(new Error("Grok ACP process closed"));
      promptDoneResolve?.();
      finish();
    });

    let terminationStarted = false;
    abortHandler = () => {
      if (settled || terminationStarted) return;
      terminationStarted = true;
      forceKillTimer = setTimeout(() => {
        if (settled) return;
        signalProcessTree(child, "SIGKILL", forceKillImpl);
        finish();
      }, Math.max(0, abortGraceMs));
      forceKillTimer.unref?.();
      signalProcessTree(child, "SIGTERM");
      try { child.stdin?.end(); } catch { /* ignore */ }
    };
    if (signal) {
      if (signal.aborted) abortHandler();
      else signal.addEventListener("abort", abortHandler, { once: true });
    }
  });

  try {
    await rpc.request("initialize", buildGrokAcpInitializeParams(), { timeoutMs: 30_000 });

    if (resumeSessionId) {
      // Best-effort resume via session/load when supported; otherwise new session.
      try {
        const loaded = await rpc.request("session/load", {
          sessionId: resumeSessionId,
          cwd: effectiveCwd,
          mcpServers: toAcpMcpServers(injectedMcpServers),
        }, { timeoutMs: 30_000 });
        const sessionId = loaded?.sessionId || loaded?.session_id || resumeSessionId;
        state.sessionId = sessionId;
        emitter.sessionId?.(sessionId);
      } catch {
        const created = await rpc.request(
          "session/new",
          buildGrokAcpSessionNewParams({
            cwd: effectiveCwd,
            injectedMcpServers,
            permissionMode,
            toolIntegrationMode,
            systemContext: systemPrompt,
          }),
          { timeoutMs: 30_000 },
        );
        const sessionId = created?.sessionId || created?.session_id;
        if (sessionId) {
          state.sessionId = sessionId;
          emitter.sessionId?.(sessionId);
        }
      }
    } else {
      const created = await rpc.request(
        "session/new",
        buildGrokAcpSessionNewParams({
          cwd: effectiveCwd,
          injectedMcpServers,
          permissionMode,
          toolIntegrationMode,
          systemContext: systemPrompt,
        }),
        { timeoutMs: 30_000 },
      );
      const sessionId = created?.sessionId || created?.session_id;
      if (sessionId) {
        state.sessionId = sessionId;
        emitter.sessionId?.(sessionId);
      }
    }

    if (!state.sessionId) {
      throw new Error("Grok ACP session/new did not return a sessionId");
    }

    // session/prompt resolves when the turn finishes; also wait for process end.
    await Promise.race([
      rpc.request(
        "session/prompt",
        buildGrokAcpPromptParams(state.sessionId, prompt),
        { timeoutMs: 30 * 60_000 },
      ),
      promptDone,
    ]);
  } catch (err) {
    if (!state.failed && !signal?.aborted) {
      state.failed = true;
      const message = err?.message || String(err);
      const stderr = stderrText.trim();
      emitter.emitError(formatGrokErrorForUser(
        stderr && !message.includes(stderr) ? `${message} (${stderr})` : message,
      ));
    }
  } finally {
    try { child.stdin?.end(); } catch { /* ignore */ }
    // Give the process a moment to exit cleanly after prompt completes.
    if (!settled && child.exitCode == null && !child.killed) {
      signalProcessTree(child, "SIGTERM", forceKillImpl);
    }
    await closePromise;
    if (signal) signal.removeEventListener("abort", abortHandler);
  }

  closeReasoning(state, emitter);
  if (!state.failed && !signal?.aborted) {
    emitter.emitDone();
  }

  return { sessionId: state.sessionId, runtime: "acp" };
}

module.exports = {
  ACP_PROTOCOL_VERSION,
  buildGrokAcpInitializeParams,
  buildGrokAcpPromptParams,
  buildGrokAcpSessionNewParams,
  buildGrokAcpSpawnArgs,
  createJsonRpcClient,
  handleGrokAcpMessage,
  runGrokAcpTurn,
  toAcpMcpServers,
  translateGrokAcpUpdate,
};
