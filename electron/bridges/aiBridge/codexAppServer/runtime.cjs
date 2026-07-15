"use strict";

const {
  CodexAppServerConnection,
  buildCodexAppServerKey,
} = require("./connection.cjs");
const {
  parseCodexModelSelection,
  toCodexMcpConfig,
} = require("../sdk/codexDriver.cjs");

const INTERACTION_TIMEOUT_MS = 5 * 60 * 1000;

function resolveCodexPermissionConfig(permissionMode) {
  if (permissionMode === "observer") {
    return {
      approvalPolicy: "never",
      approvalsReviewer: "user",
      sandbox: "read-only",
      sandboxPolicy: { type: "readOnly", networkAccess: false },
    };
  }
  if (permissionMode === "auto") {
    return {
      approvalPolicy: "never",
      approvalsReviewer: "user",
      sandbox: "danger-full-access",
      sandboxPolicy: { type: "dangerFullAccess" },
    };
  }
  return {
    approvalPolicy: "on-request",
    approvalsReviewer: "user",
    sandbox: "read-only",
    sandboxPolicy: { type: "readOnly", networkAccess: false },
  };
}

function buildThreadConfig(injectedMcpServers) {
  return {
    mcp_servers: toCodexMcpConfig(injectedMcpServers),
    model_reasoning_summary: "concise",
  };
}

function normalizeFileChanges(changes) {
  if (!Array.isArray(changes)) return [];
  return changes
    .filter((change) => change && typeof change.path === "string")
    .map((change) => ({
      path: change.path,
      kind: change.kind?.type === "add"
        ? "add"
        : change.kind?.type === "delete"
          ? "delete"
          : "update",
    }));
}

function normalizeGrantedPermissions(requested) {
  const granted = {};
  if (requested?.network != null) granted.network = requested.network;
  if (requested?.fileSystem != null) granted.fileSystem = requested.fileSystem;
  return granted;
}

function stringifyMcpContent(result) {
  if (!result) return "";
  const content = Array.isArray(result.content) ? result.content : [];
  const text = content.map((item) => {
    if (item && typeof item === "object" && typeof item.text === "string") return item.text;
    return typeof item === "string" ? item : JSON.stringify(item);
  }).join("");
  if (text) return text;
  return result.structuredContent == null ? "" : JSON.stringify(result.structuredContent);
}

function buildTurnInput(prompt, attachments) {
  const input = [{ type: "text", text: String(prompt || ""), text_elements: [] }];
  for (const attachment of attachments || []) {
    if (!attachment?.filePath) continue;
    if (!String(attachment.mediaType || "").toLowerCase().startsWith("image/")) continue;
    input.push({ type: "localImage", path: attachment.filePath });
  }
  return input;
}

function mapAppServerModels(rawModels) {
  return (Array.isArray(rawModels) ? rawModels : [])
    .filter((model) => model && model.id && !model.hidden)
    .map((model) => ({
      id: model.id,
      name: model.displayName || model.id,
      description: model.description || undefined,
      thinkingLevels: Array.isArray(model.supportedReasoningEfforts)
        ? model.supportedReasoningEfforts
          .map((option) => option?.reasoningEffort)
          .filter(Boolean)
        : [],
      defaultThinkingLevel: model.defaultReasoningEffort || undefined,
      isDefault: model.isDefault === true,
    }));
}

function resolveAppServerModelSelection(model) {
  if (!model) return null;
  const defaultThinkingLevel = model.defaultThinkingLevel;
  if (
    defaultThinkingLevel
    && Array.isArray(model.thinkingLevels)
    && model.thinkingLevels.includes(defaultThinkingLevel)
  ) {
    return `${model.id}/${defaultThinkingLevel}`;
  }
  return model.id;
}

class CodexAppServerRuntime {
  constructor({
    appVersion = "0.0.0",
    connectionFactory,
    sendInteractionRequest,
    sendInteractionCleared,
  } = {}) {
    this.appVersion = appVersion;
    this.connectionFactory = connectionFactory;
    this.sendInteractionRequest = sendInteractionRequest;
    this.sendInteractionCleared = sendInteractionCleared;
    this.connections = new Map();
    this.activeByRequest = new Map();
    this.activeByThread = new Map();
    this.activeByTurn = new Map();
    this.pendingInteractions = new Map();
    this.interactionCounter = 0;
    this.eventCounter = 0;
  }

  #scopedKey(connectionKey, id) {
    return `${connectionKey}\u0000${String(id || "")}`;
  }

  #getConnection(binPath, env) {
    const connectionKey = buildCodexAppServerKey(binPath, env);
    const existing = this.connections.get(connectionKey);
    if (existing) return { connection: existing, connectionKey };
    for (const [otherKey, otherConnection] of this.connections) {
      const inUse = Array.from(this.activeByRequest.values())
        .some((context) => context.connectionKey === otherKey);
      if (!inUse) {
        otherConnection.close();
        this.connections.delete(otherKey);
      }
    }
    const factory = this.connectionFactory || ((options) => new CodexAppServerConnection(options));
    const connection = factory({
      binPath,
      env,
      appVersion: this.appVersion,
      onNotification: (message) => this.#handleNotification(connectionKey, message),
      onServerRequest: (message, source) => this.#handleServerRequest(connectionKey, source, message),
      onFatal: (error) => this.#handleConnectionFatal(connectionKey, error),
    });
    this.connections.set(connectionKey, connection);
    return { connection, connectionKey };
  }

  async runTurn({
    requestId,
    chatSessionId,
    prompt,
    attachments,
    cwd,
    model,
    permissionMode,
    env,
    binPath,
    injectedMcpServers,
    resumeThreadId,
    emitter,
    signal,
    sender,
  }) {
    const throwIfAborted = () => {
      if (!signal?.aborted) return;
      const error = new Error("Codex App Server turn was interrupted before it started");
      error.name = "AbortError";
      throw error;
    };
    throwIfAborted();
    const { connection, connectionKey } = this.#getConnection(binPath, env);
    await connection.start();
    throwIfAborted();
    const permission = resolveCodexPermissionConfig(permissionMode);
    const selection = parseCodexModelSelection(model);
    const threadParams = {
      model: selection.model || null,
      cwd: cwd || process.cwd(),
      approvalPolicy: permission.approvalPolicy,
      approvalsReviewer: permission.approvalsReviewer,
      sandbox: permission.sandbox,
      config: buildThreadConfig(injectedMcpServers),
    };

    const threadResult = resumeThreadId
      ? await connection.request("thread/resume", { threadId: resumeThreadId, ...threadParams })
      : await connection.request("thread/start", threadParams);
    throwIfAborted();
    const threadId = threadResult?.thread?.id || resumeThreadId;
    if (!threadId) throw new Error("Codex App Server did not return a thread id");
    emitter.sessionId(threadId);

    const context = {
      requestId,
      chatSessionId,
      connection,
      connectionKey,
      threadId,
      turnId: null,
      emitter,
      signal,
      sender,
      lastError: null,
      settled: false,
      cancelRequested: false,
      interruptPromise: null,
      reasoningOpen: false,
      streamedTextByItem: new Map(),
      streamedReasoningByItem: new Map(),
      commandOutputByItem: new Map(),
      emittedToolCalls: new Set(),
      emittedToolResults: new Set(),
    };
    this.activeByRequest.set(requestId, context);
    this.activeByThread.set(this.#scopedKey(connectionKey, threadId), context);

    const completion = new Promise((resolve, reject) => {
      context.resolve = resolve;
      context.reject = reject;
    });

    try {
      const turnResult = await connection.request("turn/start", {
        threadId,
        input: buildTurnInput(prompt, attachments),
        cwd: cwd || process.cwd(),
        approvalPolicy: permission.approvalPolicy,
        approvalsReviewer: permission.approvalsReviewer,
        sandboxPolicy: permission.sandboxPolicy,
        model: selection.model || null,
        effort: selection.effort || null,
        summary: "concise",
      });
      const turnId = turnResult?.turn?.id;
      if (turnId) this.#assignTurnId(context, turnId);
      await completion;
      return { threadId, turnId: context.turnId };
    } finally {
      this.#removeContext(context);
    }
  }

  async listModels({ binPath, env }) {
    const { connection } = this.#getConnection(binPath, env);
    await connection.start();
    const all = [];
    let cursor = null;
    do {
      const response = await connection.request("model/list", {
        cursor,
        limit: 100,
      }, 10_000);
      all.push(...(response?.data || []));
      cursor = response?.nextCursor || null;
    } while (cursor);
    const models = mapAppServerModels(all);
    const defaultModel = models.find((model) => model.isDefault);
    return {
      currentModelId: resolveAppServerModelSelection(defaultModel),
      models,
    };
  }

  #assignTurnId(context, turnId) {
    if (!turnId || context.turnId === turnId) return;
    if (context.turnId) {
      this.activeByTurn.delete(this.#scopedKey(context.connectionKey, context.turnId));
    }
    context.turnId = turnId;
    this.activeByTurn.set(this.#scopedKey(context.connectionKey, turnId), context);
    if (context.cancelRequested) void this.#interruptContext(context);
  }

  #interruptContext(context) {
    if (!context.turnId) return Promise.resolve(false);
    if (context.interruptPromise) return context.interruptPromise;
    context.interruptPromise = context.connection.request("turn/interrupt", {
      threadId: context.threadId,
      turnId: context.turnId,
    }, 5_000).then(() => true).catch(() => false);
    return context.interruptPromise;
  }

  #findContext(connectionKey, params) {
    if (params?.turnId) {
      const byTurn = this.activeByTurn.get(this.#scopedKey(connectionKey, params.turnId));
      if (byTurn) return byTurn;
    }
    if (params?.threadId) {
      return this.activeByThread.get(this.#scopedKey(connectionKey, params.threadId)) || null;
    }
    return null;
  }

  #handleNotification(connectionKey, message) {
    const params = message.params || {};
    const context = this.#findContext(connectionKey, params);
    if (!context) {
      if (message.method === "warning") {
        const contexts = Array.from(this.activeByRequest.values())
          .filter((candidate) => candidate.connectionKey === connectionKey);
        for (const candidate of contexts) {
          candidate.emitter.warning(
            `codex-warning:connection:${++this.eventCounter}`,
            params.message || "Codex warning",
          );
        }
      }
      return;
    }
    const emitter = context.emitter;

    switch (message.method) {
      case "turn/started":
        this.#assignTurnId(context, params.turn?.id);
        return;
      case "item/agentMessage/delta": {
        const previous = context.streamedTextByItem.get(params.itemId) || "";
        context.streamedTextByItem.set(params.itemId, previous + String(params.delta || ""));
        emitter.text(params.delta || "");
        return;
      }
      case "item/reasoning/summaryTextDelta": {
        const previous = context.streamedReasoningByItem.get(params.itemId) || "";
        context.streamedReasoningByItem.set(params.itemId, previous + String(params.delta || ""));
        emitter.reasoning(params.delta || "");
        context.reasoningOpen = true;
        return;
      }
      case "item/commandExecution/outputDelta": {
        const previous = context.commandOutputByItem.get(params.itemId) || "";
        context.commandOutputByItem.set(params.itemId, previous + String(params.delta || ""));
        return;
      }
      case "item/started":
        this.#handleItem(context, params.item, false);
        return;
      case "item/completed":
        this.#handleItem(context, params.item, true);
        return;
      case "turn/plan/updated":
        emitter.planUpdate(
          `codex-plan:${params.turnId}`,
          (params.plan || []).map((item) => ({
            text: item.step || "",
            completed: item.status === "completed",
          })),
          (params.plan || []).every((item) => item.status === "completed") ? "completed" : "running",
        );
        return;
      case "thread/tokenUsage/updated": {
        const usage = params.tokenUsage?.last;
        if (usage) {
          emitter.usage({
            inputTokens: Number(usage.inputTokens) || 0,
            cachedInputTokens: Number(usage.cachedInputTokens) || 0,
            outputTokens: Number(usage.outputTokens) || 0,
            reasoningTokens: Number(usage.reasoningOutputTokens) || 0,
            totalTokens: Number(usage.totalTokens) || 0,
          });
        }
        return;
      }
      case "warning":
        emitter.warning(
          `codex-warning:${params.turnId || context.turnId}:${++this.eventCounter}`,
          params.message || "Codex warning",
        );
        return;
      case "error":
        context.lastError = params.error?.message || "Codex App Server error";
        emitter.warning(
          `codex-error:${params.turnId || context.turnId}:${++this.eventCounter}`,
          params.willRetry ? `${context.lastError} (retrying)` : context.lastError,
        );
        return;
      case "turn/completed":
        this.#completeTurn(context, params.turn);
        return;
      default:
        return;
    }
  }

  #closeReasoning(context) {
    if (!context.reasoningOpen) return;
    context.emitter.reasoningEnd();
    context.reasoningOpen = false;
  }

  #emitToolCallOnce(context, item, name, args) {
    if (!item?.id || context.emittedToolCalls.has(item.id)) return;
    context.emittedToolCalls.add(item.id);
    this.#closeReasoning(context);
    context.emitter.toolCall(name, args || {}, item.id);
  }

  #emitToolResultOnce(context, item, output, name) {
    if (!item?.id || context.emittedToolResults.has(item.id)) return;
    context.emittedToolResults.add(item.id);
    context.emitter.toolResult(item.id, output || "", name);
  }

  #handleItem(context, item, completed) {
    if (!item || typeof item !== "object") return;
    const emitter = context.emitter;
    switch (item.type) {
      case "agentMessage": {
        if (!completed) return;
        this.#closeReasoning(context);
        const streamed = context.streamedTextByItem.get(item.id) || "";
        if (item.text && item.text.startsWith(streamed)) emitter.text(item.text.slice(streamed.length));
        else if (item.text && !streamed) emitter.text(item.text);
        return;
      }
      case "reasoning": {
        if (!completed) return;
        const finalText = Array.isArray(item.summary) ? item.summary.join("\n") : "";
        const streamed = context.streamedReasoningByItem.get(item.id) || "";
        if (finalText && finalText.startsWith(streamed)) emitter.reasoning(finalText.slice(streamed.length));
        else if (finalText && !streamed) emitter.reasoning(finalText);
        context.reasoningOpen = true;
        this.#closeReasoning(context);
        return;
      }
      case "commandExecution": {
        const toolName = "codex.command";
        this.#emitToolCallOnce(context, item, toolName, { command: item.command, cwd: item.cwd });
        if (completed) {
          const output = item.aggregatedOutput ?? context.commandOutputByItem.get(item.id) ?? "";
          const suffix = item.exitCode == null ? "" : `\n[exit code: ${item.exitCode}]`;
          this.#emitToolResultOnce(context, item, `${output}${suffix}`, toolName);
        }
        return;
      }
      case "mcpToolCall": {
        const toolName = `${item.server || "mcp"}.${item.tool || "tool"}`;
        this.#emitToolCallOnce(context, item, toolName, item.arguments || {});
        if (completed) {
          const output = item.error?.message || stringifyMcpContent(item.result);
          this.#emitToolResultOnce(context, item, output, toolName);
        }
        return;
      }
      case "fileChange":
        if (completed) {
          emitter.fileChange(
            item.id,
            normalizeFileChanges(item.changes),
            item.status === "completed" ? "completed" : "failed",
          );
        }
        return;
      case "webSearch":
        emitter.webSearch(item.id, item.query || "", completed ? "completed" : "running");
        return;
      default:
        return;
    }
  }

  #completeTurn(context, turn) {
    if (context.settled) return;
    context.settled = true;
    this.#closeReasoning(context);
    this.#clearInteractionsForContext(context, "cancel");
    if (turn?.status === "failed") {
      context.reject(new Error(turn.error?.message || context.lastError || "Codex turn failed"));
      return;
    }
    context.emitter.emitDone();
    context.resolve();
  }

  async #handleServerRequest(connectionKey, connection, message) {
    const params = message.params || {};
    const context = this.#findContext(connectionKey, params);
    const supported = new Map([
      ["item/commandExecution/requestApproval", "command"],
      ["item/fileChange/requestApproval", "file-change"],
      ["item/permissions/requestApproval", "permissions"],
      ["item/tool/requestUserInput", "user-input"],
    ]);
    const kind = supported.get(message.method);
    if (!kind) {
      connection.respondError(message.id, -32601, `Unsupported Codex App Server request: ${message.method}`);
      context?.emitter.warning(
        `codex-unsupported-request:${++this.eventCounter}`,
        `Unsupported Codex request: ${message.method}`,
      );
      return;
    }
    if (!context) {
      connection.respond(message.id, this.#safeInteractionResponse(kind, params, "reject"));
      return;
    }

    const interactionId = `codex_interaction_${++this.interactionCounter}_${Date.now()}`;
    const timeoutMs = kind === "user-input" && Number(params.autoResolutionMs) > 0
      ? Number(params.autoResolutionMs)
      : INTERACTION_TIMEOUT_MS;
    const timer = setTimeout(() => {
      this.#resolveInteraction(interactionId, kind === "user-input" ? { answers: {} } : { decision: "reject" });
    }, timeoutMs);
    this.pendingInteractions.set(interactionId, {
      interactionId,
      connection,
      rpcId: message.id,
      kind,
      params,
      context,
      timer,
    });

    const payload = {
      interactionId,
      source: "codex-app-server",
      kind,
      requestId: context.requestId,
      chatSessionId: context.chatSessionId,
      itemId: params.itemId,
      toolName: kind === "command"
        ? "codex.command"
        : kind === "file-change"
          ? "codex.file_change"
          : kind === "permissions"
            ? "codex.permissions"
            : undefined,
      args: kind === "command"
        ? {
            command: params.command,
            cwd: params.cwd,
            reason: params.reason,
            commandActions: params.commandActions,
          }
        : kind === "file-change"
          ? { reason: params.reason, grantRoot: params.grantRoot, itemId: params.itemId }
          : kind === "permissions"
            ? { cwd: params.cwd, reason: params.reason, permissions: params.permissions }
            : undefined,
      availableDecisions: kind === "command" && Array.isArray(params.availableDecisions)
        ? params.availableDecisions
        : undefined,
      questions: kind === "user-input" ? params.questions || [] : undefined,
      autoResolutionMs: kind === "user-input" ? params.autoResolutionMs : undefined,
    };
    let delivered = false;
    try {
      delivered = typeof this.sendInteractionRequest === "function"
        && this.sendInteractionRequest(payload, context) !== false;
    } catch {
      delivered = false;
    }
    if (!delivered) {
      this.#resolveInteraction(interactionId, kind === "user-input" ? { answers: {} } : { decision: "reject" });
    }
  }

  #safeInteractionResponse(kind, params, decision) {
    if (kind === "user-input") return { answers: {} };
    if (kind === "permissions") {
      const granted = decision === "once" || decision === "session"
        ? normalizeGrantedPermissions(params.permissions)
        : {};
      return { permissions: granted, scope: decision === "session" ? "session" : "turn" };
    }
    const mapped = decision === "once"
      ? "accept"
      : decision === "session"
        ? "acceptForSession"
        : decision === "cancel"
          ? "cancel"
          : "decline";
    return { decision: mapped };
  }

  #resolveInteraction(interactionId, response) {
    const pending = this.pendingInteractions.get(interactionId);
    if (!pending) return false;
    this.pendingInteractions.delete(interactionId);
    clearTimeout(pending.timer);
    try {
      const result = pending.kind === "user-input"
        ? { answers: response?.answers || {} }
        : this.#safeInteractionResponse(pending.kind, pending.params, response?.decision || "reject");
      try { pending.connection.respond(pending.rpcId, result); } catch {}
    } finally {
      this.sendInteractionCleared?.({
        interactionIds: [interactionId],
        chatSessionId: pending.context.chatSessionId,
      }, pending.context);
    }
    return true;
  }

  respondInteraction(interactionId, response, sender) {
    const pending = this.pendingInteractions.get(interactionId);
    if (sender && pending?.context?.sender && pending.context.sender !== sender) return false;
    return this.#resolveInteraction(interactionId, response);
  }

  #clearInteractionsForContext(context, decision) {
    for (const [interactionId, pending] of Array.from(this.pendingInteractions)) {
      if (pending.context === context) {
        this.#resolveInteraction(
          interactionId,
          pending.kind === "user-input" ? { answers: {} } : { decision },
        );
      }
    }
  }

  async cancelTurn(requestId) {
    const context = this.activeByRequest.get(requestId);
    if (!context) return false;
    context.cancelRequested = true;
    this.#clearInteractionsForContext(context, "cancel");
    if (!context.turnId) return true;
    await this.#interruptContext(context);
    return true;
  }

  async cleanupChatSession(chatSessionId) {
    const contexts = Array.from(this.activeByRequest.values())
      .filter((context) => context.chatSessionId === chatSessionId);
    await Promise.all(contexts.map((context) => this.cancelTurn(context.requestId)));
  }

  #handleConnectionFatal(connectionKey, error) {
    const connection = this.connections.get(connectionKey);
    if (connection) this.connections.delete(connectionKey);
    const contexts = Array.from(this.activeByRequest.values())
      .filter((context) => context.connectionKey === connectionKey);
    for (const context of contexts) {
      if (context.settled) continue;
      context.settled = true;
      this.#clearInteractionsForContext(context, "cancel");
      context.reject(error);
    }
  }

  #removeContext(context) {
    this.activeByRequest.delete(context.requestId);
    this.activeByThread.delete(this.#scopedKey(context.connectionKey, context.threadId));
    if (context.turnId) this.activeByTurn.delete(this.#scopedKey(context.connectionKey, context.turnId));
  }

  close() {
    for (const interactionId of Array.from(this.pendingInteractions.keys())) {
      this.#resolveInteraction(interactionId, { decision: "cancel", answers: {} });
    }
    for (const [, connection] of this.connections) connection.close();
    this.connections.clear();
    for (const context of this.activeByRequest.values()) {
      if (!context.settled) {
        context.settled = true;
        context.reject(new Error("Codex App Server shut down"));
      }
    }
    this.activeByRequest.clear();
    this.activeByThread.clear();
    this.activeByTurn.clear();
  }
}

module.exports = {
  CodexAppServerRuntime,
  INTERACTION_TIMEOUT_MS,
  buildThreadConfig,
  buildTurnInput,
  mapAppServerModels,
  normalizeFileChanges,
  normalizeGrantedPermissions,
  resolveAppServerModelSelection,
  resolveCodexPermissionConfig,
  stringifyMcpContent,
};
