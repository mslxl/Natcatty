"use strict";

const path = require("node:path");

const { readContainedFile } = require("./pluginProtocol.cjs");

const MAX_ICON_SOURCE_BYTES = 512 * 1024;
const MAX_ICON_PNG_BYTES = 512 * 1024;
const MAX_ICON_EDGE = 64;
const CONTRIBUTION_KINDS = Object.freeze(["commands", "menus", "views"]);

function isDeclaredPackageIcon(manifest, icon) {
  if (!icon || icon.kind !== "package") return false;
  return CONTRIBUTION_KINDS.some((kind) => (
    (manifest?.contributes?.[kind] ?? []).some((contribution) => (
      contribution?.icon?.kind === "package"
      && contribution.icon.light === icon.light
      && contribution.icon.dark === icon.dark
    ))
  ));
}

class PluginContributionIconService {
  constructor(options) {
    this.database = options.database;
    this.packageStore = options.packageStore;
    this.nativeImage = options.nativeImage;
    this.cache = new Map();
  }

  async resolve(payload) {
    const pluginId = typeof payload?.pluginId === "string" && payload.pluginId.length <= 256
      ? payload.pluginId
      : null;
    const icon = payload?.icon;
    const plugin = pluginId ? this.database.getActivePlugin(pluginId) : null;
    if (!pluginId || !isDeclaredPackageIcon(plugin?.manifest, icon)) {
      throw new TypeError("Plugin contribution icon is not declared by the active plugin");
    }
    if (!plugin?.enabled || plugin.runtime?.quarantinedAt != null || !plugin.activeVersion) {
      throw new Error("Plugin contribution icon is unavailable");
    }
    const cacheKey = JSON.stringify([pluginId, plugin.activeVersion, icon.light, icon.dark ?? null]);
    if (this.cache.has(cacheKey)) return this.cache.get(cacheKey);
    const packageRoot = await this.packageStore.preparePackageRoot(plugin);
    const result = Object.freeze({
      light: await this.#readIcon(packageRoot, icon.light),
      ...(icon.dark ? { dark: await this.#readIcon(packageRoot, icon.dark) } : {}),
    });
    const current = this.database.getActivePlugin(pluginId);
    if (!current?.enabled || current.runtime?.quarantinedAt != null
      || current.activeVersion !== plugin.activeVersion
      || !isDeclaredPackageIcon(current.manifest, icon)) {
      throw new Error("Plugin contribution icon ownership changed while loading");
    }
    if (this.cache.size >= 256) this.cache.clear();
    this.cache.set(cacheKey, result);
    return result;
  }

  async #readIcon(packageRoot, packagePath) {
    const segments = packagePath.split("/");
    const file = await readContainedFile(packageRoot, segments);
    if (file.body.byteLength > MAX_ICON_SOURCE_BYTES) throw new Error("Plugin contribution icon is too large");
    const extension = path.extname(file.filePath).toLowerCase();
    if (![".gif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".webp"].includes(extension)) {
      throw new Error("Plugin contribution icon format is unsupported");
    }
    let image = this.nativeImage.createFromBuffer(file.body, { scaleFactor: 1 });
    if (!image || image.isEmpty()) throw new Error("Plugin contribution icon cannot be decoded");
    const size = image.getSize();
    if (!Number.isSafeInteger(size.width) || !Number.isSafeInteger(size.height)
      || size.width < 1 || size.height < 1) {
      throw new Error("Plugin contribution icon dimensions are invalid");
    }
    if (size.width > MAX_ICON_EDGE || size.height > MAX_ICON_EDGE) {
      const scale = Math.min(MAX_ICON_EDGE / size.width, MAX_ICON_EDGE / size.height);
      image = image.resize({
        width: Math.max(1, Math.round(size.width * scale)),
        height: Math.max(1, Math.round(size.height * scale)),
        quality: "best",
      });
    }
    const png = image.toPNG();
    if (!png.length || png.length > MAX_ICON_PNG_BYTES) throw new Error("Plugin contribution icon output is too large");
    return `data:image/png;base64,${png.toString("base64")}`;
  }
}

module.exports = {
  MAX_ICON_EDGE,
  PluginContributionIconService,
  isDeclaredPackageIcon,
};
