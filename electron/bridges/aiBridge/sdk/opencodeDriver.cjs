"use strict";

const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { mcpEnvPairsToObject } = require("./injectMcp.cjs");

const OPENCODE_IMAGE_MEDIA_TYPES = new Set(["image/jpeg", "image/png", "image/gif", "image/webp"]);

function isOpenCodeImageAttachment(attachment) {
  return Boolean(
    attachment &&
    OPENCODE_IMAGE_MEDIA_TYPES.has(String(attachment.mediaType || "").toLowerCase()) &&
    attachment.filePath,
  );
}

function parseOpenCodeModel(model) {
  const raw = String(model || "").trim();
  const slash = raw.indexOf("/");
  if (slash <= 0 || slash === raw.length - 1) return undefined;
  return {
    providerID: raw.slice(0, slash),
    modelID: raw.slice(slash + 1),
  };
}

function toOpenCodeMcpConfig(injectedMcpServers) {
  const mcp = {};
  for (const cfg of injectedMcpServers || []) {
    if (!cfg || !cfg.name) continue;
    mcp[cfg.name] = {
      type: "local",
      command: [cfg.command, ...(cfg.args || [])],
      environment: mcpEnvPairsToObject(cfg.env),
      enabled: true,
    };
  }
  return mcp;
}

function buildOpenCodeConfig({ model, injectedMcpServers, toolIntegrationMode } = {}) {
  const allowBash = toolIntegrationMode === "skills";
  const config = {
    share: "disabled",
    autoupdate: false,
    permission: {
      edit: "deny",
      bash: allowBash ? "allow" : "deny",
      webfetch: "deny",
      external_directory: "deny",
    },
    mcp: toOpenCodeMcpConfig(injectedMcpServers),
  };
  if (model) config.model = model;
  return config;
}

function buildOpenCodePromptParts(prompt, attachments) {
  const parts = [{ type: "text", text: String(prompt || "") }];
  for (const attachment of Array.isArray(attachments) ? attachments : []) {
    if (!isOpenCodeImageAttachment(attachment)) continue;
    parts.push({
      type: "file",
      mime: String(attachment.mediaType).toLowerCase(),
      filename: attachment.filename,
      url: pathToFileURL(attachment.filePath).href,
    });
  }
  return parts;
}

function extractOpenCodeErrorMessage(error) {
  if (!error) return "";
  if (typeof error === "string") return error;
  return String(
    error.data?.message ||
    error.message ||
    error.name ||
    "",
  );
}

function getOpenCodeSessionIdFromEvent(event) {
  const properties = event?.payload?.properties;
  return properties?.sessionID || properties?.sessionId || properties?.info?.id || null;
}

function translateOpenCodeEvent(event, emitter, state = {}) {
  const payload = event?.payload;
  if (!payload || typeof payload !== "object") return { idle: false, error: false };

  if (payload.type === "message.part.updated") {
    const part = payload.properties?.part;
    if (!part || typeof part !== "object") return { idle: false, error: false };

    if (part.type === "text") {
      const delta = payload.properties?.delta;
      emitter.text(typeof delta === "string" ? delta : part.text);
      return { idle: false, error: false };
    }

    if (part.type === "reasoning") {
      const delta = payload.properties?.delta;
      emitter.reasoning(typeof delta === "string" ? delta : part.text);
      state.reasoningOpen = true;
      return { idle: false, error: false };
    }

    if (part.type === "tool") {
      if (state.reasoningOpen) {
        emitter.reasoningEnd?.();
        state.reasoningOpen = false;
      }
      const callId = part.callID || part.id || "";
      const toolName = part.tool || "tool";
      const input = part.state?.input || {};
      if (part.state?.status === "running" || part.state?.status === "pending") {
        state.toolCalls = state.toolCalls || new Set();
        if (!state.toolCalls.has(callId)) {
          state.toolCalls.add(callId);
          emitter.toolCall(toolName, input, callId);
        }
      } else if (part.state?.status === "completed") {
        if (state.toolCalls && !state.toolCalls.has(callId)) {
          state.toolCalls.add(callId);
          emitter.toolCall(toolName, input, callId);
        }
        state.toolResults = state.toolResults || new Set();
        if (!state.toolResults.has(callId)) {
          state.toolResults.add(callId);
          emitter.toolResult(callId, part.state.output || "", toolName);
        }
      } else if (part.state?.status === "error") {
        emitter.emitError(part.state.error || "OpenCode tool failed");
        return { idle: false, error: true };
      }
    }
    return { idle: false, error: false };
  }

  if (payload.type === "session.error") {
    emitter.emitError(extractOpenCodeErrorMessage(payload.properties?.error) || "OpenCode session failed");
    return { idle: false, error: true };
  }

  if (payload.type === "session.idle") {
    if (state.reasoningOpen) {
      emitter.reasoningEnd?.();
      state.reasoningOpen = false;
    }
    emitter.status("OpenCode session idle");
    return { idle: true, error: false };
  }

  if (payload.type === "session.status" && payload.properties?.status?.type) {
    emitter.status(`OpenCode session ${payload.properties.status.type}`);
  }

  return { idle: false, error: false };
}

function classifyOpenCodeSpawnError(error) {
  const code = error && error.code;
  const msg = String((error && error.message) || error || "");
  return {
    isSpawnEnoent: code === "ENOENT" || /ENOENT/i.test(msg) || /not found/i.test(msg),
    message: msg,
  };
}

function withOpenCodeProcessEnv(env, binPath, fn) {
  const previous = {};
  const next = { ...(env || {}) };
  if (binPath) {
    next.OPENCODE_BIN = binPath;
    const binDir = path.dirname(binPath);
    next.PATH = [binDir, next.PATH || process.env.PATH || ""].filter(Boolean).join(path.delimiter);
  }
  for (const [key, value] of Object.entries(next)) {
    previous[key] = process.env[key];
    process.env[key] = String(value);
  }
  return Promise.resolve()
    .then(fn)
    .finally(() => {
      for (const key of Object.keys(next)) {
        if (previous[key] === undefined) delete process.env[key];
        else process.env[key] = previous[key];
      }
    });
}

async function createDefaultOpenCode(options, env, binPath) {
  let sdk;
  try { sdk = await import("@opencode-ai/sdk"); } catch {
    throw new Error("OpenCode SDK not installed. Run: npm install @opencode-ai/sdk");
  }
  return withOpenCodeProcessEnv(env, binPath, () => sdk.createOpencode(options));
}

async function runOpenCodeTurn({
  prompt, attachments, cwd, model, injectedMcpServers, toolIntegrationMode,
  resumeSessionId, env, binPath, emitter, abortController, openCodeFactory,
}) {
  const config = buildOpenCodeConfig({ model, injectedMcpServers, toolIntegrationMode });
  let opencode = null;
  let sessionId = resumeSessionId || null;
  let hasContent = false;
  let failed = false;
  const state = { reasoningOpen: false };

  try {
    const factory = openCodeFactory || ((options) => createDefaultOpenCode(options, env, binPath));
    opencode = await factory({ config, signal: abortController?.signal });
    const { client } = opencode;
    const events = await client.global.event();

    if (!sessionId) {
      const created = await client.session.create({
        body: { title: "Netcatty OpenCode" },
        query: cwd ? { directory: cwd } : undefined,
      });
      sessionId = created?.data?.id || created?.id || null;
    }
    if (!sessionId) throw new Error("OpenCode did not create a session");
    emitter.sessionId(sessionId);

    const eventLoop = (async () => {
      for await (const event of events.stream) {
        if (abortController?.signal?.aborted) break;
        const eventSessionId = getOpenCodeSessionIdFromEvent(event);
        if (eventSessionId && eventSessionId !== sessionId) continue;
        const result = translateOpenCodeEvent(event, emitter, state);
        if (event?.payload?.type === "message.part.updated") hasContent = true;
        if (result.error) {
          failed = true;
          break;
        }
        if (result.idle) break;
      }
    })();

    const body = {
      parts: buildOpenCodePromptParts(prompt, attachments),
    };
    const parsedModel = parseOpenCodeModel(model);
    if (parsedModel) body.model = parsedModel;

    await client.session.promptAsync({
      path: { id: sessionId },
      query: cwd ? { directory: cwd } : undefined,
      body,
    });

    await eventLoop;

    if (abortController?.signal?.aborted) {
      try { await client.session.abort({ path: { id: sessionId }, query: cwd ? { directory: cwd } : undefined }); } catch {}
    }

    if (!hasContent && !failed && !abortController?.signal?.aborted) {
      emitter.emitError("OpenCode returned an empty response. Run `opencode` in a terminal to configure authentication and models.");
      return { sessionId };
    }
    if (!failed) emitter.emitDone();
    return { sessionId };
  } catch (error) {
    const classified = classifyOpenCodeSpawnError(error);
    if (classified.isSpawnEnoent) {
      emitter.emitError("OpenCode CLI not found or not runnable. Install OpenCode and ensure `opencode` is on PATH, or set a custom path in Settings.");
    } else {
      emitter.emitError(classified.message || "OpenCode turn failed");
    }
    return { sessionId };
  } finally {
    try { opencode?.server?.close?.(); } catch {}
  }
}

function mapOpenCodeModels(response) {
  const providers = Array.isArray(response?.providers) ? response.providers : [];
  const models = [];
  for (const provider of providers) {
    const providerId = provider?.id || provider?.providerID;
    if (!providerId || !provider?.models || typeof provider.models !== "object") continue;
    for (const [modelId, info] of Object.entries(provider.models)) {
      models.push({
        id: `${providerId}/${modelId}`,
        name: `${provider.name || providerId} ${info?.name || modelId}`,
      });
    }
  }
  return models;
}

async function listOpenCodeModels({ env, binPath, openCodeFactory } = {}) {
  let opencode = null;
  try {
    const factory = openCodeFactory || ((options) => createDefaultOpenCode(options, env, binPath));
    opencode = await factory({ config: { autoupdate: false }, timeout: 10000 });
    const response = await opencode.client.config.providers();
    return mapOpenCodeModels(response?.data || response);
  } catch {
    return [];
  } finally {
    try { opencode?.server?.close?.(); } catch {}
  }
}

module.exports = {
  buildOpenCodeConfig,
  buildOpenCodePromptParts,
  classifyOpenCodeSpawnError,
  listOpenCodeModels,
  mapOpenCodeModels,
  parseOpenCodeModel,
  runOpenCodeTurn,
  toOpenCodeMcpConfig,
  translateOpenCodeEvent,
};
