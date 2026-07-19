"use strict";

const { isPluginDevelopmentEnabled } = require("./constants.cjs");

const CHANNELS = Object.freeze({
  status: "netcatty:plugins:status",
  list: "netcatty:plugins:list",
  install: "netcatty:plugins:install",
  setEnabled: "netcatty:plugins:set-enabled",
  restart: "netcatty:plugins:restart",
  uninstall: "netcatty:plugins:uninstall",
  contributions: "netcatty:plugins:contributions",
  contributionsChanged: "netcatty:plugins:contributions-changed",
  executeCommand: "netcatty:plugins:execute-command",
  updateSetting: "netcatty:plugins:update-setting",
  resetSetting: "netcatty:plugins:reset-setting",
  setEnvironment: "netcatty:plugins:set-environment",
  openView: "netcatty:plugins:open-view",
  closeView: "netcatty:plugins:close-view",
  setViewBounds: "netcatty:plugins:set-view-bounds",
  viewMessage: "netcatty:plugins:view-message",
  viewMessagePosted: "netcatty:plugins:view-message-posted",
});

function createTrustedPluginBridgeSender(options = {}) {
  const devServerOrigin = options.devServerUrl ? new URL(options.devServerUrl).origin : null;
  return (event) => {
    const senderUrl = event?.senderFrame?.url || event?.sender?.getURL?.() || "";
    try {
      const url = new URL(senderUrl);
      if (url.protocol === "app:" && url.hostname === "netcatty") return true;
      return Boolean(devServerOrigin && url.origin === devServerOrigin);
    } catch {
      return false;
    }
  };
}

function registerPluginBridge(ipcMain, options) {
  const manager = options.manager;
  const contributionService = options.contributionService;
  const viewHost = options.viewHost;
  const env = options.env ?? process.env;
  const isTrustedSender = options.isTrustedSender;
  const configured = isPluginDevelopmentEnabled(env)
    && Boolean(manager)
    && typeof manager.initialize === "function";
  const resolveManager = async () => {
    if (!configured) throw new Error("Plugin development runtime is disabled or unavailable");
    try {
      await manager.initialize();
    } catch (cause) {
      throw new Error("Plugin development runtime is disabled or unavailable", { cause });
    }
    return manager;
  };
  const handle = (channel, callback) => {
    ipcMain.handle(channel, async (event, payload) => {
      if (!isTrustedSender(event)) throw new Error("Untrusted plugin management sender");
      const activeManager = await resolveManager();
      return callback(activeManager, payload, event);
    });
  };
  ipcMain.handle(CHANNELS.status, async (event) => {
    if (!isTrustedSender(event)) throw new Error("Untrusted plugin management sender");
    let available = false;
    try {
      await resolveManager();
      available = true;
    } catch {}
    return { available, experimental: true };
  });
  handle(CHANNELS.list, async (activeManager) => activeManager.list());
  handle(CHANNELS.install, async (activeManager, payload) => activeManager.install(
    payload?.archivePath,
    { enable: payload?.enable === true },
  ));
  handle(CHANNELS.setEnabled, async (activeManager, payload) => activeManager.setEnabled(
    payload?.pluginId,
    payload?.enabled === true,
  ));
  handle(CHANNELS.restart, async (activeManager, payload) => activeManager.restart(payload?.pluginId));
  handle(CHANNELS.uninstall, async (activeManager, payload) => activeManager.uninstall(payload?.pluginId));
  handle(CHANNELS.contributions, async (_activeManager, payload) => {
    if (!contributionService) throw new Error("Plugin contributions are unavailable");
    return contributionService.snapshot(payload ?? {});
  });
  handle(CHANNELS.executeCommand, async (_activeManager, payload) => {
    if (!contributionService) throw new Error("Plugin contributions are unavailable");
    return contributionService.executeCommand(payload?.command, payload?.args, {
      source: "renderer",
      context: payload?.context,
    });
  });
  handle(CHANNELS.updateSetting, async (_activeManager, payload) => {
    if (!contributionService) throw new Error("Plugin contributions are unavailable");
    return contributionService.updateSetting(
      payload?.pluginId,
      payload?.settingId,
      payload?.value,
      payload?.scopeId,
      { source: "host" },
    );
  });
  handle(CHANNELS.resetSetting, async (_activeManager, payload) => {
    if (!contributionService) throw new Error("Plugin contributions are unavailable");
    return contributionService.resetSetting(payload?.pluginId, payload?.settingId, payload?.scopeId);
  });
  handle(CHANNELS.setEnvironment, async (_activeManager, payload) => {
    if (!contributionService) throw new Error("Plugin contributions are unavailable");
    await contributionService.setEnvironment(payload ?? {});
    viewHost?.setEnvironment?.(payload ?? {});
    return null;
  });
  handle(CHANNELS.openView, async (_activeManager, payload, event) => {
    if (!viewHost) throw new Error("Plugin views are unavailable");
    return viewHost.open(payload, event.sender);
  });
  handle(CHANNELS.closeView, async (_activeManager, payload, event) => {
    if (!viewHost) throw new Error("Plugin views are unavailable");
    await viewHost.close(payload?.instanceId, event.sender);
    return null;
  });
  handle(CHANNELS.setViewBounds, async (_activeManager, payload, event) => {
    if (!viewHost) throw new Error("Plugin views are unavailable");
    viewHost.setBounds(payload?.instanceId, payload?.bounds, event.sender);
    return null;
  });
  handle(CHANNELS.viewMessage, async (_activeManager, payload, event) => {
    if (!viewHost) throw new Error("Plugin views are unavailable");
    await viewHost.postMessage(payload?.instanceId, payload?.message, event.sender);
    return null;
  });
  contributionService?.onDidChange?.((event) => options.broadcast?.(CHANNELS.contributionsChanged, event));
  contributionService?.onDidPostViewMessage?.((event) => options.broadcast?.(CHANNELS.viewMessagePosted, event));
}

module.exports = { CHANNELS, createTrustedPluginBridgeSender, registerPluginBridge };
