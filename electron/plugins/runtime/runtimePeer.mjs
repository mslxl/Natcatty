import {
  CancellationTokenSource,
  DisposableStore,
  PluginError,
  PLUGIN_ERROR_WIRE_CODES,
  pluginErrorToRpcError,
} from "@netcatty/plugin-sdk";
import { createMessagePortStreamEnvelope } from "@netcatty/plugin-contract";

const RPC_ERRORS = {
  methodNotFound: -32601,
  invalidParams: -32602,
  internal: -32603,
  cancelled: -32001,
  unsupported: -32012,
};

const PLUGIN_ERROR_NAMES_BY_WIRE_CODE = new Map(
  Object.entries(PLUGIN_ERROR_WIRE_CODES).map(([name, code]) => [code, name]),
);

function pluginErrorNameFromRpcError(error) {
  if (typeof error?.data?.pluginCode === "string") return error.data.pluginCode;
  if (error?.code === RPC_ERRORS.methodNotFound) return "unsupported";
  if (error?.code === RPC_ERRORS.invalidParams) return "invalid_argument";
  if (error?.code === RPC_ERRORS.internal) return "internal";
  return PLUGIN_ERROR_NAMES_BY_WIRE_CODE.get(error?.code) ?? "unknown";
}

function messageData(value) {
  return value && typeof value === "object" && "data" in value ? value.data : value;
}

function createTransportAdapter(port) {
  const listeners = new Set();
  const handle = (event) => {
    const value = messageData(event);
    for (const listener of listeners) listener(value);
  };
  if (typeof port.addEventListener === "function") port.addEventListener("message", handle);
  else port.on("message", handle);
  port.start?.();
  return {
    post(message, transfer = []) {
      port.postMessage(message, transfer);
    },
    onMessage(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    close() {
      listeners.clear();
      port.close?.();
    },
  };
}

function makeRpcFailure(id, code, message, data) {
  return {
    jsonrpc: "2.0",
    id,
    error: { code, message, ...(data === undefined ? {} : { data }) },
  };
}

function cancelUnhandledStream(transport, message) {
  const keys = Reflect.ownKeys(message);
  if (keys.some((key) => typeof key !== "string" || (key !== "frame" && key !== "transfer"))) {
    throw new TypeError("Plugin stream envelope contains unknown properties");
  }
  for (const key of keys) {
    const descriptor = Object.getOwnPropertyDescriptor(message, key);
    if (!descriptor || !("value" in descriptor) || !descriptor.enumerable) {
      throw new TypeError("Plugin stream envelope must contain enumerable data properties");
    }
  }
  const envelope = createMessagePortStreamEnvelope(message.frame, message.transfer);
  if (envelope.frame.kind !== "open") {
    throw new Error(`Unknown incoming plugin stream: ${envelope.frame.streamId}`);
  }
  transport.post(createMessagePortStreamEnvelope({
    streamId: envelope.frame.streamId,
    sequence: 1,
    kind: "cancel",
  }));
}

function normalizeError(error) {
  if (error instanceof PluginError) {
    return pluginErrorToRpcError(error);
  }
  return { code: RPC_ERRORS.internal, message: "Plugin operation failed" };
}

function createHostClient(transport) {
  let nextId = 0;
  const pending = new Map();
  function accept(message) {
    if (!message || typeof message !== "object" || !Object.hasOwn(message, "id")) return false;
    if (!Object.hasOwn(message, "result") && !Object.hasOwn(message, "error")) return false;
    const request = pending.get(`${typeof message.id}:${String(message.id)}`);
    if (!request) return false;
    pending.delete(`${typeof message.id}:${String(message.id)}`);
    if (message.error) {
      request.reject(new PluginError(
        pluginErrorNameFromRpcError(message.error),
        message.error.message,
        message.error.data?.details,
      ));
    } else request.resolve(message.result);
    return true;
  }
  function request(method, params, options = {}) {
    const id = nextId;
    nextId = nextId === Number.MAX_SAFE_INTEGER ? 0 : nextId + 1;
    return new Promise((resolve, reject) => {
      pending.set(`${typeof id}:${String(id)}`, { resolve, reject });
      transport.post({
        jsonrpc: "2.0",
        id,
        method,
        params,
        ...(options.deadlineMs === undefined ? {} : { deadlineMs: options.deadlineMs }),
      });
    });
  }
  function notify(method, params) {
    transport.post({ jsonrpc: "2.0", method, params });
  }
  function close() {
    const error = new PluginError("unavailable", "Plugin host disconnected");
    for (const request of pending.values()) request.reject(error);
    pending.clear();
  }
  return { accept, request, notify, close };
}

function assertStorageKey(key) {
  if (typeof key !== "string" || key.length < 1 || key.length > 256 || key.includes("\0")) {
    throw new PluginError("invalid_argument", "Plugin storage key is invalid");
  }
  return key;
}

function assertCredentialRef(credential) {
  if (
    !credential
    || typeof credential !== "object"
    || (credential.kind !== "secret" && credential.kind !== "credential")
    || typeof credential.id !== "string"
    || credential.id.length < 16
    || credential.id.length > 256
  ) throw new PluginError("invalid_argument", "Credential reference is invalid");
  if (
    credential.kind === "secret"
    && (
      typeof credential.key !== "string"
      || credential.key.length < 1
      || credential.key.length > 256
      || credential.key.includes("\0")
    )
  ) throw new PluginError("invalid_argument", "Credential reference is invalid");
  return credential.kind === "secret"
    ? { kind: "secret", id: credential.id, key: credential.key }
    : { kind: "credential", id: credential.id };
}

function forwardedDeadline(value, maximum) {
  return Number.isSafeInteger(value) && value >= 1 && value <= maximum ? value : undefined;
}

function assertCompanionId(companionId) {
  if (typeof companionId !== "string" || companionId.length < 5 || companionId.length > 192) {
    throw new PluginError("invalid_argument", "Companion ID is invalid");
  }
  return companionId;
}

function assertOwnedContributionId(pluginId, id, label) {
  if (typeof id !== "string" || !id.startsWith(`${pluginId}.`) || id.length > 256) {
    throw new PluginError("invalid_argument", `${label} ID is invalid`);
  }
  return id;
}

function createEmitter() {
  const listeners = new Set();
  return {
    event(listener) {
      if (typeof listener !== "function") throw new PluginError("invalid_argument", "Plugin event listener must be a function");
      listeners.add(listener);
      return Object.freeze({ dispose: () => listeners.delete(listener) });
    },
    fire(value) {
      for (const listener of [...listeners]) {
        try { listener(value); } catch {}
      }
    },
    clear() { listeners.clear(); },
  };
}

function createPluginContext(config, client, runtimeApi) {
  const subscriptions = new DisposableStore();
  const storage = {
    get: (key) => client.request("storage.get", { key: assertStorageKey(key) })
      .then((result) => result?.found ? result.value : undefined),
    set: (key, value) => client.request("storage.set", { key: assertStorageKey(key), value })
      .then(() => undefined),
    delete: (key) => client.request("storage.delete", { key: assertStorageKey(key) })
      .then(() => undefined),
    keys: () => client.request("storage.keys", {}).then((result) => result?.keys ?? []),
  };
  const secrets = {
    get: (key) => client.request("secrets.get", { key: assertStorageKey(key) })
      .then((result) => result?.found ? result.secret : undefined),
    set: (key, value) => client.request("secrets.set", {
      key: assertStorageKey(key),
      value,
    }).then((result) => result.secret),
    delete: (key) => client.request("secrets.delete", { key: assertStorageKey(key) })
      .then(() => undefined),
  };
  const credentials = {
    createLease: (secret, options) => client.request("credentials.createLease", {
      secret: assertCredentialRef(secret),
      operationId: options?.operationId,
      purpose: options?.purpose,
      ...(options?.ttlMs === undefined ? {} : { ttlMs: options.ttlMs }),
    }),
  };
  const network = {
    request: (request) => client.request("network.request", request, {
      deadlineMs: forwardedDeadline(request?.timeoutMs, 300_000),
    }),
  };
  const filesystem = {
    readFile: (filePath, options = {}) => client.request("filesystem.readFile", {
      path: filePath,
      ...options,
    }).then((result) => result.data),
    writeFile: (filePath, data, options = {}) => client.request("filesystem.writeFile", {
      path: filePath,
      data,
      ...options,
    }).then(() => undefined),
    stat: (filePath) => client.request("filesystem.stat", { path: filePath }),
    readDirectory: (directoryPath) => client.request("filesystem.readDirectory", { path: directoryPath })
      .then((result) => result.entries),
  };
  const companions = {
    start: async (companionId) => {
      const result = await client.request("companion.start", {
        companionId: assertCompanionId(companionId),
      });
      let stopped = false;
      let stopPromise = null;
      const stop = () => {
        if (stopped) return Promise.resolve();
        if (stopPromise) return stopPromise;
        stopPromise = client.request("companion.stop", { handleId: result.handleId })
          .then(() => { stopped = true; })
          .finally(() => { stopPromise = null; });
        return stopPromise;
      };
      return Object.freeze({
        id: result.handleId,
        request: (method, params, options = {}) => client.request(
          "companion.request",
          {
            handleId: result.handleId,
            method,
            ...(params === undefined ? {} : { params }),
            ...options,
          },
          { deadlineMs: forwardedDeadline(options.timeoutMs, 60_000) },
        ),
        stop,
        dispose() { void stop().catch(() => {}); },
      });
    },
  };
  const settings = {
    get: (settingId, options = {}) => client.request("settings.get", {
      settingId: assertOwnedContributionId(config.pluginId, settingId, "Plugin setting"),
      ...(options.scopeId === undefined ? {} : { scopeId: options.scopeId }),
    }).then((result) => result?.found ? result.value : undefined),
    update: (settingId, value, options = {}) => client.request("settings.update", {
      settingId: assertOwnedContributionId(config.pluginId, settingId, "Plugin setting"),
      value,
      ...(options.scopeId === undefined ? {} : { scopeId: options.scopeId }),
    }).then((result) => result ?? { restartRequired: false }),
    onDidChange: runtimeApi.settingsChanged.event,
  };
  const commands = {
    registerCommand(commandId, handler) {
      const id = assertOwnedContributionId(config.pluginId, commandId, "Plugin command");
      if (typeof handler !== "function") throw new PluginError("invalid_argument", "Plugin command handler must be a function");
      if (runtimeApi.commandHandlers.has(id)) throw new PluginError("already_exists", `Plugin command is already registered: ${id}`);
      runtimeApi.commandHandlers.set(id, handler);
      return Object.freeze({ dispose: () => runtimeApi.commandHandlers.delete(id) });
    },
    executeCommand: (commandId, args) => client.request("commands.execute", {
      command: assertOwnedContributionId(config.pluginId, commandId, "Plugin command"),
      ...(args === undefined ? {} : { args }),
    }),
  };
  const contextKeys = {
    set: (key, value) => client.request("contextKeys.set", {
      key: assertOwnedContributionId(config.pluginId, key, "Plugin Context Key"),
      value,
    }).then(() => undefined),
  };
  const views = {
    onDidReceiveMessage(viewId, listener) {
      const id = assertOwnedContributionId(config.pluginId, viewId, "Plugin view");
      let emitter = runtimeApi.viewMessages.get(id);
      if (!emitter) {
        emitter = createEmitter();
        runtimeApi.viewMessages.set(id, emitter);
      }
      return emitter.event(listener);
    },
    postMessage: (viewId, message) => client.notify("views.postMessage", {
      viewId: assertOwnedContributionId(config.pluginId, viewId, "Plugin view"),
      message,
    }),
    getState: (viewId, scopeId) => client.request("views.getState", {
      viewId: assertOwnedContributionId(config.pluginId, viewId, "Plugin view"),
      scopeId,
    }).then((result) => result?.state),
    setState: (viewId, scopeId, state) => client.request("views.setState", {
      viewId: assertOwnedContributionId(config.pluginId, viewId, "Plugin view"),
      scopeId,
      state,
    }).then(() => undefined),
  };
  const environment = {
    get locale() { return runtimeApi.environment.locale ?? "en"; },
    get theme() { return runtimeApi.environment.theme ?? "system"; },
    get reducedMotion() { return runtimeApi.environment.reducedMotion === true; },
    get highContrast() { return runtimeApi.environment.highContrast === true; },
    onDidChange: runtimeApi.environmentChanged.event,
  };
  const logger = Object.fromEntries(["debug", "info", "warn", "error"].map((level) => [
    level,
    (message, fields) => client.notify("log.write", {
      level,
      message: String(message).slice(0, 2_048),
      ...(fields === undefined ? {} : { fields }),
    }),
  ]));
  return {
    pluginId: config.pluginId,
    netcattyVersion: config.netcattyVersion,
    apiVersion: config.apiVersion,
    enabledFeatures: new Set(config.enabledFeatures),
    subscriptions,
    storage,
    settings,
    commands,
    contextKeys,
    views,
    environment,
    secrets,
    credentials,
    network,
    filesystem,
    companions,
    logger,
  };
}

export async function startPluginRuntime({ port, config, loadPlugin }) {
  const transport = createTransportAdapter(port);
  const client = createHostClient(transport);
  const cancellation = new Map();
  let plugin;
  let context;
  let activated = false;
  let deactivated = false;
  const runtimeApi = {
    commandHandlers: new Map(),
    settingsChanged: createEmitter(),
    environmentChanged: createEmitter(),
    environment: {},
    viewMessages: new Map(),
  };
  const pluginModule = await loadPlugin(config.entryUrl);
  plugin = pluginModule?.default;
  if (!plugin || typeof plugin.activate !== "function") {
    throw new Error("Plugin entrypoint must default-export a plugin with activate(context)");
  }

  async function handleRequest(message) {
    if (message.method === "plugin.initialize") {
      if (context) throw new PluginError("failed_precondition", "Plugin is already initialized");
      context = createPluginContext(config, client, runtimeApi);
      return {
        pluginId: config.pluginId,
        pluginVersion: config.pluginVersion,
        apiVersion: config.apiVersion,
        enabledFeatures: [...config.enabledFeatures],
      };
    }
    if (message.method === "plugin.activate") {
      if (!context) throw new PluginError("failed_precondition", "Plugin must be initialized first");
      if (!activated) {
        const disposable = await plugin.activate(context);
        if (disposable && typeof disposable.dispose === "function") context.subscriptions.add(disposable);
        activated = true;
      }
      return null;
    }
    if (message.method === "plugin.deactivate") {
      if (!deactivated) {
        deactivated = true;
        await plugin.deactivate?.();
        context?.subscriptions.dispose();
      }
      return null;
    }
    if (message.method === "plugin.command.execute") {
      if (!activated || !context) throw new PluginError("failed_precondition", "Plugin is not activated");
      const command = assertOwnedContributionId(config.pluginId, message.params?.command, "Plugin command");
      const handler = runtimeApi.commandHandlers.get(command);
      if (!handler) throw new PluginError("failed_precondition", `Plugin command has no registered handler: ${command}`);
      return await handler(message.params?.args, message.params?.invocation);
    }
    throw new PluginError("unsupported", `Unsupported host method: ${message.method}`);
  }

  function handleNotification(message) {
    if (message.method === "plugin.settings.changed") {
      runtimeApi.settingsChanged.fire(message.params);
      return true;
    }
    if (message.method === "plugin.environment.changed") {
      runtimeApi.environment = { ...(message.params ?? {}) };
      runtimeApi.environmentChanged.fire(runtimeApi.environment);
      return true;
    }
    if (message.method === "plugin.view.message") {
      const viewId = assertOwnedContributionId(config.pluginId, message.params?.viewId, "Plugin view");
      runtimeApi.viewMessages.get(viewId)?.fire(message.params?.message);
      return true;
    }
    return false;
  }

  const dispose = transport.onMessage((message) => {
    if (message && typeof message === "object" && Object.hasOwn(message, "frame")) {
      try { cancelUnhandledStream(transport, message); }
      catch { transport.close(); }
      return;
    }
    if (client.accept(message)) return;
    if (!message || message.jsonrpc !== "2.0") return;
    if (message.method === "$/cancelRequest") {
      cancellation.get(message.params?.cancellationId)?.cancel();
      return;
    }
    if (!Object.hasOwn(message, "id")) {
      try { handleNotification(message); } catch { transport.close(); }
      return;
    }
    const cancellationId = message.cancellationId;
    const source = new CancellationTokenSource();
    if (cancellationId) cancellation.set(cancellationId, source);
    void handleRequest(message).then(
      (result) => transport.post({ jsonrpc: "2.0", id: message.id, result }),
      (error) => {
        const rpcError = normalizeError(error);
        transport.post(makeRpcFailure(message.id, rpcError.code, rpcError.message, rpcError.data));
      },
    ).finally(() => {
      if (cancellationId) cancellation.delete(cancellationId);
      source.dispose();
    });
  });

  return {
    async dispose() {
      dispose();
      client.close();
      for (const source of cancellation.values()) source.cancel();
      runtimeApi.commandHandlers.clear();
      runtimeApi.settingsChanged.clear();
      runtimeApi.environmentChanged.clear();
      for (const emitter of runtimeApi.viewMessages.values()) emitter.clear();
      cancellation.clear();
      if (!deactivated) {
        await plugin.deactivate?.();
        context?.subscriptions.dispose();
      }
      transport.close();
    },
  };
}
