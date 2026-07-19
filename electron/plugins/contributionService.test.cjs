"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { PluginContributionService } = require("./contributionService.cjs");
const { PluginDatabase } = require("./database.cjs");
const { PluginHostRpcRegistry } = require("./hostRpcRegistry.cjs");

function manifest(id = "com.example.contributions", activationEvents = []) {
  return {
    manifestVersion: 1,
    id,
    name: "contributions",
    displayName: { en: "Contributions", "zh-CN": "贡献" },
    version: "1.0.0",
    publisher: "example",
    engines: { netcatty: ">=0.0.0", api: ">=0.1.0-internal <0.2.0" },
    main: { browser: "index.js" },
    activationEvents,
    contributes: {
      settings: [{
        id: `${id}.greeting`,
        label: { en: "Greeting", "zh-CN": "问候" },
        control: "text",
        scope: "application",
        default: "hello",
      }, {
        id: `${id}.token`,
        label: "Token",
        control: "password",
        scope: "application",
        secret: true,
      }],
      commands: [{ id: `${id}.hello`, title: { en: "Say hello", "zh-CN": "打招呼" }, enablement: `${id}.ready` }],
      menus: [{ command: `${id}.hello`, location: "commandPalette", when: `${id}.visible` }],
      views: [{ id: `${id}.view`, title: "Example view", location: "aside", entry: "view.html" }],
    },
  };
}

function setup(context, pluginManifest, options = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-plugin-contributions-"));
  context.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const database = new PluginDatabase(path.join(root, "plugins.sqlite"));
  context.after(() => database.close());
  database.installVersion({
    pluginId: pluginManifest.id,
    version: pluginManifest.version,
    manifest: pluginManifest,
    archiveSha256: "a".repeat(64),
    packageRelativePath: `${pluginManifest.id}/${pluginManifest.version}/package`,
  }, { enable: true });
  const secrets = new Map();
  const secretStore = {
    getReference(pluginId, key) { return secrets.has(`${pluginId}:${key}`) ? secrets.get(`${pluginId}:${key}`).ref : null; },
    set(pluginId, key, value) {
      const ref = { kind: "secret", id: `secret-${"x".repeat(24)}`, key };
      secrets.set(`${pluginId}:${key}`, { ref, value });
      return ref;
    },
    delete(pluginId, key) { secrets.delete(`${pluginId}:${key}`); },
  };
  const calls = [];
  const runtimeSupervisor = options.runtimeSupervisor ?? {
    async start(pluginId) { calls.push(`start:${pluginId}`); },
    async request(pluginId, method, params) { calls.push(`request:${pluginId}:${method}`); return params; },
    async notify(pluginId, method) { calls.push(`notify:${pluginId}:${method}`); },
  };
  const service = new PluginContributionService({
    database,
    runtimeSupervisor,
    secretStore,
    getLocale: () => "zh-CN",
  });
  return { calls, database, secretStore, service, secrets };
}

test("only onStartupFinished plugins activate during contribution initialization", async (context) => {
  const pluginManifest = manifest("com.example.startup", ["onStartupFinished"]);
  const { calls, service } = setup(context, pluginManifest);
  await service.initialize();
  assert.deepEqual(calls, ["start:com.example.startup"]);

  const lazyManifest = manifest("com.example.lazy", ["onCommand:com.example.lazy.hello"]);
  const lazy = setup(context, lazyManifest);
  await lazy.service.initialize();
  assert.deepEqual(lazy.calls, []);
});

test("commands activate lazily and Context Keys are evaluated by the host", async (context) => {
  const pluginManifest = manifest(undefined, ["onCommand:com.example.contributions.hello"]);
  const { calls, service } = setup(context, pluginManifest);
  const registry = new PluginHostRpcRegistry();
  service.registerRpcCapabilities(registry);
  const routes = registry.createRoutes({
    pluginId: pluginManifest.id,
    pluginVersion: pluginManifest.version,
    runtimeId: "runtime-1",
    runtimeKind: "browser",
    manifest: pluginManifest,
  });
  const transport = { signal: new AbortController().signal };
  await routes.requestHandlers["contextKeys.set"]({ key: `${pluginManifest.id}.ready`, value: true }, transport);
  await routes.requestHandlers["contextKeys.set"]({ key: `${pluginManifest.id}.visible`, value: true }, transport);

  const snapshot = service.snapshot();
  assert.equal(snapshot.plugins[0].displayName, "贡献");
  assert.equal(snapshot.plugins[0].commands[0].enabled, true);
  assert.equal(snapshot.plugins[0].menus[0].visible, true);
  assert.equal(snapshot.plugins[0].menus[0].checked, undefined);

  const result = await service.executeCommand(`${pluginManifest.id}.hello`, { name: "Catty" });
  assert.equal(result.command, `${pluginManifest.id}.hello`);
  assert.deepEqual(calls.slice(-2), [
    `start:${pluginManifest.id}`,
    `request:${pluginManifest.id}:plugin.command.execute`,
  ]);
});

test("settings validate declared controls and secrets never enter settings storage", async (context) => {
  const pluginManifest = manifest();
  const { database, secrets, service } = setup(context, pluginManifest);
  assert.equal(await service.getSetting(pluginManifest.id, `${pluginManifest.id}.greeting`), "hello");
  await service.updateSetting(pluginManifest.id, `${pluginManifest.id}.greeting`, "bonjour");
  assert.equal(await service.getSetting(pluginManifest.id, `${pluginManifest.id}.greeting`), "bonjour");
  await assert.rejects(
    service.updateSetting(pluginManifest.id, `${pluginManifest.id}.greeting`, 42),
    /must be a string/u,
  );

  await service.updateSetting(pluginManifest.id, `${pluginManifest.id}.token`, "plaintext");
  const reference = await service.getSetting(pluginManifest.id, `${pluginManifest.id}.token`);
  assert.equal(reference.kind, "secret");
  assert.equal(database.listSettings(pluginManifest.id).some((entry) => entry.settingId.endsWith(".token")), false);
  assert.equal([...secrets.values()][0].value, "plaintext");
  await service.resetSetting(pluginManifest.id, `${pluginManifest.id}.greeting`);
  assert.equal(await service.getSetting(pluginManifest.id, `${pluginManifest.id}.greeting`), "hello");
});

test("user settings and view state survive package removal", async (context) => {
  const pluginManifest = manifest();
  const { database, service } = setup(context, pluginManifest);
  await service.updateSetting(pluginManifest.id, `${pluginManifest.id}.greeting`, "preserved");
  database.setViewState(pluginManifest.id, `${pluginManifest.id}.view`, "window-1", { selected: 2 });
  database.removePlugin(pluginManifest.id);
  assert.equal(database.getSetting(pluginManifest.id, `${pluginManifest.id}.greeting`, "application", "application"), "preserved");
  assert.deepEqual(database.getViewState(pluginManifest.id, `${pluginManifest.id}.view`, "window-1"), { selected: 2 });
});

test("structured settings use the host restricted schema and unsafe text patterns fail closed", async (context) => {
  const pluginManifest = manifest();
  pluginManifest.contributes.settings.push({
    id: `${pluginManifest.id}.servers`,
    label: "Servers",
    control: "table",
    scope: "application",
    valueSchema: {
      type: "array",
      maxItems: 4,
      items: {
        type: "object",
        properties: {
          name: { type: "string", minLength: 1, maxLength: 32 },
          port: { type: "integer", minimum: 1, maximum: 65_535 },
        },
        required: ["name", "port"],
        additionalProperties: false,
      },
    },
  }, {
    id: `${pluginManifest.id}.patterned`,
    label: "Patterned",
    control: "text",
    scope: "application",
    pattern: "^[a-z]+$",
  }, {
    id: `${pluginManifest.id}.unsafe-pattern`,
    label: "Unsafe pattern",
    control: "text",
    scope: "application",
    pattern: "^(a+)+$",
  });
  const { service } = setup(context, pluginManifest);
  await service.updateSetting(pluginManifest.id, `${pluginManifest.id}.servers`, [{ name: "ssh", port: 22 }]);
  await assert.rejects(
    service.updateSetting(pluginManifest.id, `${pluginManifest.id}.servers`, [{ name: "ssh", port: 70_000 }]),
    /does not match/u,
  );
  await service.updateSetting(pluginManifest.id, `${pluginManifest.id}.patterned`, "netcatty");
  await assert.rejects(
    service.updateSetting(pluginManifest.id, `${pluginManifest.id}.unsafe-pattern`, "aaaa"),
    /unsafe pattern/u,
  );
});

test("Provider contributions expose a PR 5 lazy activation seam", async (context) => {
  const pluginManifest = manifest("com.example.provider", ["onProvider:com.example.provider.completion"]);
  pluginManifest.contributes.providers = [{
    id: "com.example.provider.completion",
    label: "Completion",
    kind: "terminal.completion",
  }];
  const { calls, service } = setup(context, pluginManifest);
  const result = await service.activateProvider("com.example.provider.completion");
  assert.equal(result.provider.id, "com.example.provider.completion");
  assert.deepEqual(calls, ["start:com.example.provider"]);
});
