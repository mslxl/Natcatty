"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { PluginContributionIconService } = require("./contributionIconService.cjs");

function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-plugin-icon-"));
  fs.mkdirSync(path.join(root, "assets"));
  fs.writeFileSync(path.join(root, "assets", "light.png"), "light");
  fs.writeFileSync(path.join(root, "assets", "dark.png"), "dark");
  const icon = { kind: "package", light: "assets/light.png", dark: "assets/dark.png" };
  const plugin = {
    id: "com.example.icon",
    activeVersion: "1.0.0",
    enabled: true,
    runtime: { quarantinedAt: null },
    manifest: { contributes: { views: [{ id: "com.example.icon.view", icon }] } },
  };
  const decoded = [];
  const service = new PluginContributionIconService({
    database: { getActivePlugin: (id) => id === plugin.id ? plugin : null },
    packageStore: { preparePackageRoot: async () => root },
    nativeImage: {
      createFromBuffer(buffer) {
        decoded.push(buffer.toString());
        return {
          isEmpty: () => false,
          getSize: () => ({ width: 128, height: 64 }),
          resize: ({ width, height }) => ({
            toPNG: () => Buffer.from(`png:${width}x${height}`),
          }),
        };
      },
    },
  });
  return { decoded, icon, plugin, root, service };
}

test("package contribution icons are declaration-bound, integrity-prepared, and rasterized", async (t) => {
  const { decoded, icon, plugin, root, service } = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  assert.deepEqual(await service.resolve({ pluginId: plugin.id, icon }), {
    light: `data:image/png;base64,${Buffer.from("png:64x32").toString("base64")}`,
    dark: `data:image/png;base64,${Buffer.from("png:64x32").toString("base64")}`,
  });
  assert.deepEqual(decoded, ["light", "dark"]);
});

test("package contribution icon lookup rejects paths not declared by the active manifest", async (t) => {
  const { plugin, root, service } = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  await assert.rejects(service.resolve({
    pluginId: plugin.id,
    icon: { kind: "package", light: "assets/other.png" },
  }), /not declared/);
});

test("package contribution icon lookup rejects version replacement during decoding", async (t) => {
  const { icon, plugin, root } = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  let current = plugin;
  const service = new PluginContributionIconService({
    database: { getActivePlugin: () => current },
    packageStore: {
      async preparePackageRoot() {
        current = { ...plugin, activeVersion: "2.0.0" };
        return root;
      },
    },
    nativeImage: {
      createFromBuffer() {
        return {
          isEmpty: () => false,
          getSize: () => ({ width: 16, height: 16 }),
          toPNG: () => Buffer.from("png"),
        };
      },
    },
  });

  await assert.rejects(service.resolve({ pluginId: plugin.id, icon }), /ownership changed/);
});
