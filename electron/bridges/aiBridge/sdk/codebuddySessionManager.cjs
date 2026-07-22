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

class CodebuddySessionManager {
  constructor() {
    /** @type {Map<string, import('@tencent-ai/agent-sdk').Session>} */
    this.sessions = new Map();
    /** @type {Map<string, { resolve: Function, reject: Function }>} */
    this.elicitationPending = new Map();
  }

  /**
   * Get an existing session or create/resume one.
   * @param {object} args
   * @param {string} args.sessionKey  unique key (chatSessionId + backend + binPath)
   * @param {object} args.sessionOptions  SDK SessionOptions
   * @param {string} [args.resumeSessionId]  resume an existing session by ID
   * @returns {Promise<object|null>} session instance or null if V2 unavailable
   */
  async getOrCreateSession({ sessionKey, sessionOptions, resumeSessionId }) {
    const existing = this.sessions.get(sessionKey);
    if (existing) return existing;

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
      this.sessions.set(sessionKey, session);
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
    }
  }

  /**
   * Steer (mid-turn追加消息) using the V2 Session.
   * Returns { status: 'accepted' } on success, or { status: 'unsupported' }.
   */
  async steer({ sessionKey, prompt, attachments, emitter }) {
    const session = this.sessions.get(sessionKey);
    if (!session) return { status: "unsupported" };

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
    const session = this.sessions.get(sessionKey);
    if (!session) return false;
    try {
      await session.setModel(model);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Close a specific session.
   */
  closeSession(sessionKey) {
    const session = this.sessions.get(sessionKey);
    if (session) {
      try { session.close(); } catch { /* best effort */ }
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

module.exports = { CodebuddySessionManager, codebuddySessionManager };
