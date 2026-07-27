"use strict";

/**
 * CodeBuddy V2 Session Manager — @experimental
 *
 * Manages persistent multi-turn sessions using the SDK's unstable_v2 Session
 * API (createSession / resumeSession). Falls back to the legacy query() path
 * when the V2 API is unavailable.
 *
 * Benefits over query()-per-turn:
 * - CLI process stays warm across turns (faster subsequent responses)
 * - True multi-turn context without replaying history
 * - Supports steer (mid-turn追加消息) via session.send()
 */

const {
  buildCodebuddyQueryOptions,
  buildCodebuddyPromptInput,
  buildCodebuddyHooks,
  buildCodebuddyElicitation,
  translateCodebuddyMessage,
  classifyCodebuddySpawnError,
} = require("./codebuddyDriver.cjs");

/**
 * Compute a stable fingerprint from option-affecting fields so we can detect
 * when the user changes model, env, permission mode, tools, etc. between turns.
 * Only JSON-serializable fields are included; function-valued fields (hooks,
 * canUseTool, elicitation) are excluded since they are rebuilt every turn.
 */
function computeOptionsFingerprint(sessionOptions) {
  const relevant = {
    cwd: sessionOptions.cwd,
    model: sessionOptions.model,
    env: sessionOptions.env,
    pathToCodebuddyCode: sessionOptions.pathToCodebuddyCode,
    mcpServers: sessionOptions.mcpServers,
    permissionMode: sessionOptions.permissionMode,
    systemPrompt: sessionOptions.systemPrompt,
    tools: sessionOptions.tools,
    disallowedTools: sessionOptions.disallowedTools,
    maxTurns: sessionOptions.maxTurns,
  };
  try {
    return JSON.stringify(relevant);
  } catch {
    return null;
  }
}

class CodebuddySessionManager {
  constructor() {
    /** @type {Map<string, { session: object, fingerprint: string|null }>} */
    this.sessions = new Map();
    /** @type {Map<string, { resolve: Function, reject: Function }>} */
    this.elicitationPending = new Map();
  }

  /**
   * Get an existing session or create/resume one.
   * If the session exists but its option-affecting fields have changed,
   * the stale session is closed and a fresh one is created.
   * @param {object} args
   * @param {string} args.sessionKey  unique key (chatSessionId + backend + binPath)
   * @param {object} args.sessionOptions  SDK SessionOptions
   * @param {string} [args.resumeSessionId]  resume an existing session by ID
   * @returns {Promise<object|null>} session instance or null if V2 unavailable
   */
  async getOrCreateSession({ sessionKey, sessionOptions, resumeSessionId }) {
    const fingerprint = computeOptionsFingerprint(sessionOptions);
    const existing = this.sessions.get(sessionKey);
    if (existing) {
      // Reuse only when options still match.
      if (existing.fingerprint === fingerprint) return existing.session;
      // Options changed — close the stale session and create a fresh one.
      try { existing.session.close(); } catch { /* best effort */ }
      this.sessions.delete(sessionKey);
    }

    let sdk;
    try {
      sdk = await import("@tencent-ai/agent-sdk");
    } catch {
      return null;
    }

    const createSession = sdk.unstable_v2_createSession;
    const resumeSession = sdk.unstable_v2_resumeSession;
    if (!createSession || !resumeSession) return null;

    let session;
    try {
      if (resumeSessionId) {
        session = resumeSession(resumeSessionId, sessionOptions);
      } else {
        session = createSession(sessionOptions);
      }
      await session.connect();
      this.sessions.set(sessionKey, { session, fingerprint });
      return session;
    } catch {
      // V2 session creation failed — caller should fall back to query().
      return null;
    }
  }

  /**
   * Run a turn using the V2 Session API.
   * Returns { sessionId, usedV2: true } on success, or null to signal fallback.
   */
  async runTurn({
    sessionKey, prompt, attachments, options, emitter,
    sessionOptions, resumeSessionId,
  }) {
    const session = await this.getOrCreateSession({
      sessionKey, sessionOptions, resumeSessionId,
    });
    if (!session) return null; // signal caller to use query() fallback

    const promptInput = buildCodebuddyPromptInput(prompt, attachments);
    let sessionId = session.sessionId || null;
    let hasContent = false;
    let hasStreamedText = false;
    let removeAbortListener = null;

    try {
      // Send the prompt (string or async iterable).
      if (typeof promptInput === "string") {
        await session.send(promptInput);
      } else {
        // Async iterable of UserMessage — send first message.
        for await (const msg of promptInput) {
          await session.send(msg);
        }
      }

      // Register an abort listener that interrupts the session immediately,
      // so stop reaches the CLI even while parked inside stream().next()
      // waiting for the next message (e.g. during a long tool call).
      const signal = options.abortController?.signal;
      const interruptSession = () => {
        if (typeof session.interrupt === "function") {
          void session.interrupt().catch(() => {});
        }
      };
      if (signal) {
        if (signal.aborted) {
          interruptSession();
        } else {
          signal.addEventListener("abort", interruptSession, { once: true });
          removeAbortListener = () => signal.removeEventListener("abort", interruptSession);
        }
      }

      // Stream responses.
      for await (const message of session.stream()) {
        if (options.abortController?.signal?.aborted) {
          try { await session.interrupt(); } catch { /* best effort */ }
          break;
        }
        if (message?.session_id && message.session_id !== sessionId) {
          sessionId = message.session_id;
        }
        if (
          message?.type === "stream_event" ||
          (message?.type === "assistant" && Array.isArray(message?.message?.content) && message.message.content.length > 0)
        ) {
          hasContent = true;
        }
        translateCodebuddyMessage(message, emitter, { skipAssistantText: hasStreamedText });
        if (
          message?.type === "stream_event" &&
          message.event?.type === "content_block_delta" &&
          message.event?.delta?.type === "text_delta" &&
          message.event.delta.text
        ) {
          hasStreamedText = true;
        }
      }

      if (!hasContent && !options.abortController?.signal?.aborted) {
        emitter.emitError(
          "CodeBuddy returned an empty response. Run `codebuddy` in a terminal to log in, " +
          "or set CODEBUDDY_API_KEY / CODEBUDDY_AUTH_TOKEN.",
        );
        return { sessionId, usedV2: true };
      }
      emitter.emitDone();
      return { sessionId, usedV2: true };
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
      return { sessionId, usedV2: true };
    } finally {
      removeAbortListener?.();
    }
  }

  /**
   * Steer (mid-turn追加消息) using the V2 Session.
   * Returns { status: 'accepted' } on success, or { status: 'unsupported' }.
   */
  async steer({ sessionKey, prompt, attachments, emitter }) {
    const entry = this.sessions.get(sessionKey);
    if (!entry) return { status: "unsupported" };
    const session = entry.session;

    const promptInput = buildCodebuddyPromptInput(prompt, attachments);
    let hasStreamedText = false;

    try {
      if (typeof promptInput === "string") {
        await session.send(promptInput);
      } else {
        for await (const msg of promptInput) {
          await session.send(msg);
        }
      }

      for await (const message of session.stream()) {
        translateCodebuddyMessage(message, emitter, { skipAssistantText: hasStreamedText });
        if (
          message?.type === "stream_event" &&
          message.event?.type === "content_block_delta" &&
          message.event?.delta?.type === "text_delta" &&
          message.event.delta.text
        ) {
          hasStreamedText = true;
        }
      }
      emitter.emitDone();
      return { status: "accepted" };
    } catch (error) {
      emitter.emitError(error?.message || "CodeBuddy steer failed");
      return { status: "failed", message: error?.message || String(error) };
    }
  }

  /**
   * Set model at runtime without rebuilding the session.
   */
  async setModel(sessionKey, model) {
    const entry = this.sessions.get(sessionKey);
    if (!entry) return false;
    try {
      await entry.session.setModel(model);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Close a specific session.
   */
  closeSession(sessionKey) {
    const entry = this.sessions.get(sessionKey);
    if (entry) {
      try { entry.session.close(); } catch { /* best effort */ }
      this.sessions.delete(sessionKey);
    }
  }

  /**
   * Close all sessions for a given chat session prefix.
   */
  closeForChat(chatSessionId) {
    const prefix = `${String(chatSessionId || "")}\u0000`;
    for (const key of this.sessions.keys()) {
      if (key.startsWith(prefix)) {
        this.closeSession(key);
      }
    }
  }

  /**
   * Close all sessions (app shutdown).
   */
  closeAll() {
    for (const key of [...this.sessions.keys()]) {
      this.closeSession(key);
    }
  }

  /**
   * Resolve a pending elicitation response from the renderer.
   */
  resolveElicitation(elicitationId, response) {
    const pending = this.elicitationPending.get(elicitationId);
    if (pending) {
      this.elicitationPending.delete(elicitationId);
      pending.resolve(response);
      return true;
    }
    return false;
  }
}

// Singleton instance shared across the app lifecycle.
const codebuddySessionManager = new CodebuddySessionManager();

module.exports = { CodebuddySessionManager, codebuddySessionManager, computeOptionsFingerprint };
