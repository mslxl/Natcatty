"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { MessageChannel } = require("node:worker_threads");

const { PluginRpcError, PluginRpcRouter, RPC_ERRORS } = require("./rpcRouter.cjs");

test("runtime peer exposes secure host capabilities over the canonical RPC transport", async () => {
  const { startPluginRuntime } = await import("./runtime/runtimePeer.mjs");
  const { port1, port2 } = new MessageChannel();
  const values = new Map();
  const calls = [];
  const deadlines = [];
  const host = new PluginRpcRouter({
    pluginId: "com.example.peer",
    send(message) { port1.postMessage(message); },
    handlers: {
      "storage.get": async () => {
        throw new PluginRpcError(RPC_ERRORS.invalidArgument, "Invalid storage key");
      },
      "storage.set": async ({ key, value }) => { values.set(key, value); return null; },
      "secrets.set": async ({ key, value }) => {
        calls.push(["secret", key, value]);
        return {
          secret: { kind: "secret", id: "secret-reference-0000000000000000", key },
        };
      },
      "secrets.get": async () => ({
        found: true,
        secret: { kind: "secret", id: "secret-reference-0000000000000000", key: "api-key" },
      }),
      "credentials.createLease": async ({ operationId }) => ({
        kind: "secret-lease",
        id: "l".repeat(32),
        operationId,
        expiresAt: 10_000,
      }),
      "network.request": async ({ url, timeoutMs }, requestContext) => {
        deadlines.push(["network", requestContext.deadlineMs, timeoutMs]);
        return {
          url,
          status: 200,
          headers: {},
          body: { encoding: "base64", data: "b2s=" },
        };
      },
      "filesystem.readFile": async ({ path }) => ({ data: `read:${path}` }),
      "filesystem.writeFile": async ({ path, data }) => { calls.push(["write", path, data]); return null; },
      "filesystem.stat": async () => ({ kind: "file", size: 2, modifiedAt: 1 }),
      "filesystem.readDirectory": async () => ({ entries: [{ name: "file", kind: "file" }] }),
      "companion.start": async () => ({ handleId: "companion-handle-000000000000" }),
      "companion.request": async ({ method, params, timeoutMs }, requestContext) => {
        deadlines.push(["companion", requestContext.deadlineMs, timeoutMs]);
        return { method, params };
      },
      "companion.stop": async ({ handleId }) => { calls.push(["stop", handleId]); return null; },
      "log.write": async () => null,
    },
  });
  port1.on("message", (message) => host.accept(message));
  const lifecycle = [];
  const peer = await startPluginRuntime({
    port: port2,
    config: {
      pluginId: "com.example.peer",
      pluginVersion: "1.0.0",
      netcattyVersion: "1.0.0",
      apiVersion: "0.1.0-internal",
      enabledFeatures: [],
    },
    async loadPlugin() {
      return {
        default: {
          async activate(context) {
            lifecycle.push("activate");
            await assert.rejects(
              context.storage.get("invalid"),
              (error) => error?.code === "invalid_argument"
                && error?.message === "Invalid storage key",
            );
            await context.storage.set("answer", 42);
            const secret = await context.secrets.set("api-key", "value");
            const lease = await context.credentials.createLease(secret, {
              operationId: "network:login",
              purpose: "Authenticate",
            });
            assert.equal(lease.operationId, "network:login");
            const vaultLease = await context.credentials.createLease({
              kind: "credential",
              id: "vault-credential-1",
            }, {
              operationId: "connection:login",
              purpose: "Authenticate connection",
            });
            assert.equal(vaultLease.operationId, "connection:login");
            assert.equal((await context.network.request({
              url: "https://example.com",
              timeoutMs: 45_000,
            })).status, 200);
            assert.equal(await context.filesystem.readFile("/tmp/file"), "read:/tmp/file");
            await context.filesystem.writeFile("/tmp/file", "ok", { overwrite: true });
            assert.equal((await context.filesystem.stat("/tmp/file")).kind, "file");
            assert.equal((await context.filesystem.readDirectory("/tmp")).length, 1);
            const companion = await context.companions.start("com.example.peer.helper");
            assert.deepEqual(await companion.request("echo", { value: 1 }, { timeoutMs: 50_000 }), {
              method: "echo",
              params: { value: 1 },
            });
            await companion.stop();
          },
          async deactivate() { lifecycle.push("deactivate"); },
        },
      };
    },
  });
  const initialized = await host.request("plugin.initialize", {
    netcattyVersion: "1.0.0",
    apiVersion: "0.1.0-internal",
    supportedFeatures: [],
  });
  assert.equal(initialized.pluginId, "com.example.peer");
  const unhandledStream = await host.streams.openOutgoing("unhandled", 1024);
  await new Promise((resolve) => setTimeout(resolve, 10));
  await assert.rejects(unhandledStream.write(new Uint8Array([1])), /closed/);
  await assert.rejects(
    host.request("plugin.unknown", {}),
    (error) => error?.code === -32012 && error?.data?.pluginCode === "unsupported",
  );
  await host.request("plugin.activate", {});
  assert.equal(values.get("answer"), 42);
  assert.deepEqual(calls, [
    ["secret", "api-key", "value"],
    ["write", "/tmp/file", "ok"],
    ["stop", "companion-handle-000000000000"],
  ]);
  assert.deepEqual(deadlines, [
    ["network", 45_000, 45_000],
    ["companion", 50_000, 50_000],
  ]);
  await host.request("plugin.deactivate", {});
  await peer.dispose();
  host.close();
  assert.deepEqual(lifecycle, ["activate", "deactivate"]);
});

test("runtime companion handles retry a failed stop request", async () => {
  const { startPluginRuntime } = await import("./runtime/runtimePeer.mjs");
  const { port1, port2 } = new MessageChannel();
  let stopCalls = 0;
  const host = new PluginRpcRouter({
    pluginId: "com.example.retry-stop",
    send(message) { port1.postMessage(message); },
    handlers: {
      "companion.start": async () => ({ handleId: "companion-handle-000000000001" }),
      "companion.stop": async () => {
        stopCalls += 1;
        if (stopCalls === 1) throw new Error("transient stop failure");
        return null;
      },
    },
  });
  port1.on("message", (message) => host.accept(message));
  const peer = await startPluginRuntime({
    port: port2,
    config: {
      pluginId: "com.example.retry-stop",
      pluginVersion: "1.0.0",
      netcattyVersion: "1.0.0",
      apiVersion: "0.1.0-internal",
      enabledFeatures: [],
    },
    async loadPlugin() {
      return {
        default: {
          async activate(context) {
            const companion = await context.companions.start("com.example.retry-stop.helper");
            await assert.rejects(companion.stop());
            await companion.stop();
          },
        },
      };
    },
  });
  await host.request("plugin.initialize", {
    netcattyVersion: "1.0.0",
    apiVersion: "0.1.0-internal",
    supportedFeatures: [],
  });
  await host.request("plugin.activate", {});
  assert.equal(stopCalls, 2);
  await peer.dispose();
  host.close();
});

test("runtime peer exposes contribution APIs and routes host UI events", async () => {
  const { startPluginRuntime } = await import("./runtime/runtimePeer.mjs");
  const { port1, port2 } = new MessageChannel();
  const calls = [];
  const host = new PluginRpcRouter({
    pluginId: "com.example.ui",
    send(message) { port1.postMessage(message); },
    handlers: {
      "settings.get": async ({ settingId }) => ({ found: true, value: `value:${settingId}` }),
      "settings.update": async ({ settingId, value }) => {
        calls.push(["setting", settingId, value]);
        return { restartRequired: false };
      },
      "commands.execute": async ({ command, args }) => {
        calls.push(["execute", command, args]);
        return "host-result";
      },
      "contextKeys.set": async ({ key, value }) => { calls.push(["context", key, value]); return null; },
      "views.getState": async ({ viewId, scopeId }) => ({ state: { viewId, scopeId } }),
      "views.setState": async ({ viewId, scopeId, state }) => {
        calls.push(["view-state", viewId, scopeId, state]);
        return null;
      },
      "views.postMessage": async ({ viewId, message }) => { calls.push(["view-message", viewId, message]); },
    },
  });
  port1.on("message", (message) => host.accept(message));
  const events = [];
  let pluginContext;
  const peer = await startPluginRuntime({
    port: port2,
    config: {
      pluginId: "com.example.ui",
      pluginVersion: "1.0.0",
      netcattyVersion: "1.0.0",
      apiVersion: "0.1.0-internal",
      enabledFeatures: [],
      environment: {
        locale: "en-GB",
        theme: "light",
        reducedMotion: false,
        highContrast: true,
        themeTokens: { "--background": "initial" },
      },
    },
    async loadPlugin() {
      return {
        default: {
          async activate(context) {
            pluginContext = context;
            assert.equal(context.environment.locale, "fr-FR");
            assert.equal(context.environment.theme, "dark");
            assert.equal(context.environment.highContrast, true);
            assert.deepEqual(context.environment.themeTokens, { "--background": "active" });
            assert.throws(() => {
              context.environment.themeTokens["--background"] = "mutated";
            }, /read only/u);
            context.settings.onDidChange((event) => events.push(["settings", event.settingId]));
            context.environment.onDidChange((event) => events.push([
              "environment",
              event.locale,
              event.theme,
              event.themeTokens["--background"],
            ]));
            context.views.onDidReceiveMessage("com.example.ui.view", (message) => events.push(["view", message]));
            context.commands.registerCommand("com.example.ui.hello", async (args, invocation) => ({ args, source: invocation.source }));
            context.commands.registerCommand("com.example.ui.void", async () => undefined);
            const staleDisposable = context.commands.registerCommand("com.example.ui.replaceable", async () => "old");
            staleDisposable.dispose();
            context.commands.registerCommand("com.example.ui.replaceable", async () => "new");
            staleDisposable.dispose();
            assert.equal(await context.settings.get("com.example.ui.greeting"), "value:com.example.ui.greeting");
            assert.deepEqual(await context.settings.update("com.example.ui.greeting", "hello"), { restartRequired: false });
            assert.equal(await context.commands.executeCommand("com.example.ui.hello", { nested: true }), "host-result");
            await context.contextKeys.set("com.example.ui.ready", true);
            assert.deepEqual(await context.views.getState("com.example.ui.view", "window-1"), {
              viewId: "com.example.ui.view",
              scopeId: "window-1",
            });
            await context.views.setState("com.example.ui.view", "window-1", { selected: 2 });
            context.views.postMessage("com.example.ui.view", { ready: true });
          },
        },
      };
    },
  });
  await host.request("plugin.initialize", {
    netcattyVersion: "1.0.0",
    apiVersion: "0.1.0-internal",
    supportedFeatures: [],
  });
  await host.request("plugin.activate", {
    environment: {
      locale: "fr-FR",
      theme: "dark",
      reducedMotion: true,
      highContrast: true,
      themeTokens: { "--background": "active" },
    },
  });
  assert.deepEqual(await host.request("plugin.command.execute", {
    command: "com.example.ui.hello",
    args: { name: "Catty" },
    invocation: { source: "palette" },
  }), { args: { name: "Catty" }, source: "palette" });
  assert.equal(await host.request("plugin.command.execute", {
    command: "com.example.ui.void",
    invocation: { source: "palette" },
  }), null);
  assert.equal(await host.request("plugin.command.execute", {
    command: "com.example.ui.replaceable",
    invocation: { source: "palette" },
  }), "new");

  host.notify("plugin.settings.changed", { settingId: "com.example.ui.greeting", scope: "application", scopeId: "application", source: "host" });
  host.notify("plugin.environment.changed", {
    locale: "zh-CN",
    theme: "dark",
    reducedMotion: true,
    highContrast: false,
    themeTokens: { "--background": "updated" },
  });
  host.notify("plugin.view.message", { viewId: "com.example.ui.view", message: { ping: true } });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(pluginContext.environment.locale, "zh-CN");
  assert.equal(pluginContext.environment.reducedMotion, true);
  assert.deepEqual(pluginContext.environment.themeTokens, { "--background": "updated" });
  assert.deepEqual(events, [
    ["settings", "com.example.ui.greeting"],
    ["environment", "zh-CN", "dark", "updated"],
    ["view", { ping: true }],
  ]);
  assert.deepEqual(calls, [
    ["setting", "com.example.ui.greeting", "hello"],
    ["execute", "com.example.ui.hello", { nested: true }],
    ["context", "com.example.ui.ready", true],
    ["view-state", "com.example.ui.view", "window-1", { selected: 2 }],
    ["view-message", "com.example.ui.view", { ready: true }],
  ]);
  await peer.dispose();
  host.close();
});
