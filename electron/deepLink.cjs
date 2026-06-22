const SSH_DEEP_LINK_CHANNEL = "netcatty:deepLink:ssh";
const SSH_PROTOCOL = "ssh";

function isSshDeepLinkUrl(rawUrl) {
  if (typeof rawUrl !== "string" || !rawUrl.trim()) return false;
  try {
    return new URL(rawUrl).protocol.toLowerCase() === `${SSH_PROTOCOL}:`;
  } catch {
    return false;
  }
}

function collectSshDeepLinkUrls(argv) {
  if (!Array.isArray(argv)) return [];
  return argv.filter(isSshDeepLinkUrl);
}

function registerSshProtocolClient({ app, isDev, argv = process.argv, execPath = process.execPath, logWarn = console.warn } = {}) {
  if (!app || typeof app.setAsDefaultProtocolClient !== "function") return false;
  try {
    if (isDev && process.defaultApp && argv[1]) {
      return app.setAsDefaultProtocolClient(SSH_PROTOCOL, execPath, [argv[1]]);
    }
    return app.setAsDefaultProtocolClient(SSH_PROTOCOL);
  } catch (err) {
    logWarn("[Main] Failed to register ssh:// protocol handler:", err);
    return false;
  }
}

module.exports = {
  SSH_DEEP_LINK_CHANNEL,
  collectSshDeepLinkUrls,
  isSshDeepLinkUrl,
  registerSshProtocolClient,
};
