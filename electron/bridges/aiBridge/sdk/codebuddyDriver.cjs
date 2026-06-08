"use strict";

/**
 * CodeBuddy backend driver — wraps @tencent-ai/agent-sdk query().
 *
 * - Spawns the user's system `codebuddy` binary (auto-discovered or via
 *   CODEBUDDY_CODE_PATH env var).
 * - Bypasses the SDK's built-in permission system and routes all side effects
 *   through the injected netcatty MCP server (approval/scope/blocklist enforced
 *   there).
 * - Translates SDK messages into the canonical renderer event protocol.
 *
 * CodeBuddy SDK is in Preview — interfaces may change between releases.
 */
const { mcpEnvPairsToObject } = require("./injectMcp.cjs");

/** Convert neutral injectMcp configs into the SDK's keyed mcpServers map. */
function toSdkMcpServers(injectedMcpServers) {
  const map = {};
  for (const cfg of injectedMcpServers || []) {
    if (!cfg || !cfg.name) continue;
    map[cfg.name] = {
      type: "stdio",
      command: cfg.command,
      args: cfg.args || [],
      env: mcpEnvPairsToObject(cfg.env),
    };
  }
  return map;
}

function buildCodebuddyQueryOptions({
  cwd, model, env, injectedMcpServers, abortController,
}) {
  const options = {
    cwd,
    permissionMode: "bypassPermissions",
    mcpServers: toSdkMcpServers(injectedMcpServers),
    env,
  };
  if (model) options.model = model;
  if (abortController) options.abortController = abortController;
  return options;
}

/**
 * Translate one CodeBuddy SDK message into emitter calls.
 * CodeBuddy SDK (Preview) does NOT support includePartialMessages, so text
 * arrives as complete TextBlock within assistant messages rather than via
 * streaming deltas. Tool calls and results are also content blocks.
 */
function translateCodebuddyMessage(message, emitter) {
  if (!message || typeof message !== "object") return;
  const type = message.type;

  if (type === "system" && message.session_id) {
    emitter.sessionId(message.session_id);
    return;
  }

  if (type === "assistant" && message.message && Array.isArray(message.message.content)) {
    for (const block of message.message.content) {
      if (!block || typeof block !== "object") continue;
      if (block.type === "text" && block.text) {
        emitter.text(block.text);
      } else if (block.type === "tool_use") {
        emitter.toolCall(block.name, block.input || {}, block.id);
      } else if (block.type === "tool_result") {
        const output = typeof block.content === "string"
          ? block.content
          : JSON.stringify(block.content);
        emitter.toolResult(block.tool_use_id || "", output, undefined);
      }
    }
    return;
  }
  // 'result' carries final duration/cost — handled by the run loop.
}

/** Classify a spawn failure for user-friendly error messages. */
function classifyCodebuddySpawnError(error) {
  const code = error && error.code;
  const msg = String((error && error.message) || error || "");
  const isSpawnEnoent =
    code === "ENOENT" ||
    /ENOENT/i.test(msg) ||
    /not found/i.test(msg);
  return { isSpawnEnoent, message: msg };
}

/**
 * Run a CodeBuddy turn. Streams events via `emitter`, resolves with { sessionId }.
 * @param {object} args
 * @param {string} args.prompt
 * @param {Array<object>} [args.attachments]
 * @param {object} args.options  result of buildCodebuddyQueryOptions
 * @param {object} args.emitter  createStreamEmitter(...)
 * @param {Function} [args.queryFn] inject @tencent-ai/agent-sdk query (for tests)
 */
async function runCodebuddyTurn({ prompt, attachments, options, emitter, queryFn }) {
  let query = queryFn;
  if (!query) {
    let sdk;
    try { sdk = await import("@tencent-ai/agent-sdk"); } catch { emitter.emitError("CodeBuddy Agent SDK not installed. Run: npm install @tencent-ai/agent-sdk"); return { sessionId: null }; }
    query = sdk.query;
  }

  let sessionId = null;
  let hasContent = false;
  let queryRef = null;
  try {
    queryRef = query({ prompt: String(prompt || ""), options });
    for await (const message of queryRef) {
      if (options.abortController?.signal?.aborted) {
        // CodeBuddy SDK uses interrupt() rather than AbortController; call it
        // if available to stop the running query cleanly.
        if (typeof queryRef.interrupt === "function") {
          try { await queryRef.interrupt(); } catch { /* best effort */ }
        }
        break;
      }
      if (message?.session_id && message.session_id !== sessionId) {
        sessionId = message.session_id;
      }
      if (message?.type === "assistant" && Array.isArray(message?.message?.content) && message.message.content.length > 0) {
        hasContent = true;
      }
      translateCodebuddyMessage(message, emitter);
    }
    if (!hasContent && !options.abortController?.signal?.aborted) {
      emitter.emitError(
        "CodeBuddy returned an empty response. Run `codebuddy` in a terminal to log in, " +
        "or set CODEBUDDY_API_KEY / CODEBUDDY_AUTH_TOKEN.",
      );
      return { sessionId };
    }
    emitter.emitDone();
    return { sessionId };
  } catch (error) {
    const classified = classifyCodebuddySpawnError(error);
    if (classified.isSpawnEnoent) {
      emitter.emitError(
        "CodeBuddy CLI not found or not runnable. " +
        "Install codebuddy and ensure it's on PATH, or set CODEBUDDY_CODE_PATH.",
      );
    } else {
      emitter.emitError(classified.message || "CodeBuddy turn failed");
    }
    return { sessionId };
  }
}

// CodeBuddy SDK has no listModels API; the UI falls back to curated presets.
async function listCodebuddyModels() {
  return [];
}

module.exports = {
  buildCodebuddyQueryOptions,
  translateCodebuddyMessage,
  classifyCodebuddySpawnError,
  toSdkMcpServers,
  runCodebuddyTurn,
  listCodebuddyModels,
};
