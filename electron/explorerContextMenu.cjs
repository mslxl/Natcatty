const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const EXPLORER_CONTEXT_MENU_PREFERENCES_FILE = "explorer-context-menu-preferences.json";
const SHELL_VERB = "Netcatty";
const DIRECTORY_SHELL_KEY = `Software\\Classes\\Directory\\shell\\${SHELL_VERB}`;
const DIRECTORY_BACKGROUND_SHELL_KEY = `Software\\Classes\\Directory\\Background\\shell\\${SHELL_VERB}`;
const MENU_LABEL = "Open in Netcatty";
const OPEN_TERMINAL_PATH_ARG = "--open-terminal-path";

function isWindowsPlatform(platform = process.platform) {
  return platform === "win32";
}

function getExplorerContextMenuPreferencePath({
  app,
  pathModule = path,
} = {}) {
  if (!app || typeof app.getPath !== "function") return null;
  try {
    return pathModule.join(app.getPath("userData"), EXPLORER_CONTEXT_MENU_PREFERENCES_FILE);
  } catch {
    return null;
  }
}

function readExplorerContextMenuEnabledPreference({
  app,
  fsModule = fs,
  pathModule = path,
  logWarn = console.warn,
} = {}) {
  const filePath = getExplorerContextMenuPreferencePath({ app, pathModule });
  if (!filePath) return null;
  try {
    if (!fsModule.existsSync(filePath)) return null;
    const parsed = JSON.parse(fsModule.readFileSync(filePath, "utf8"));
    if (typeof parsed?.enabled !== "boolean") return null;
    return parsed.enabled;
  } catch (err) {
    logWarn?.("[Main] Failed to read Explorer context menu preference:", err);
    return null;
  }
}

function writeExplorerContextMenuEnabledPreference({
  app,
  enabled,
  fsModule = fs,
  pathModule = path,
  logWarn = console.warn,
} = {}) {
  const filePath = getExplorerContextMenuPreferencePath({ app, pathModule });
  if (!filePath) return false;
  try {
    fsModule.mkdirSync(pathModule.dirname(filePath), { recursive: true });
    fsModule.writeFileSync(filePath, JSON.stringify({ enabled: enabled !== false }, null, 2));
    return true;
  } catch (err) {
    logWarn?.("[Main] Failed to write Explorer context menu preference:", err);
    return false;
  }
}

function buildExplorerContextMenuCommand(executablePath, pathPlaceholder) {
  const exe = String(executablePath || "").trim();
  const placeholder = String(pathPlaceholder || "").trim();
  if (!exe || !placeholder) return null;
  // Put app args after `--` so Chromium does not consume them, and keep the
  // path in the same token (`=`) so spaces survive CommandLineToArgvW.
  // Trailing `.` avoids the classic `"C:\"` quote-escape bug for drive roots.
  return `"${exe}" -- ${OPEN_TERMINAL_PATH_ARG}="${placeholder}."`;
}

function runReg(args, {
  spawnSyncImpl = spawnSync,
  logWarn = console.warn,
} = {}) {
  try {
    const result = spawnSyncImpl("reg.exe", args, {
      encoding: "utf8",
      windowsHide: true,
    });
    return {
      status: typeof result.status === "number" ? result.status : 1,
      stdout: String(result.stdout || ""),
      stderr: String(result.stderr || ""),
      error: result.error || null,
    };
  } catch (err) {
    logWarn?.("[Main] Failed to run reg.exe:", err);
    return {
      status: 1,
      stdout: "",
      stderr: err?.message || String(err),
      error: err,
    };
  }
}

function regKeyExists(hive, keyPath, options = {}) {
  const result = runReg(["query", `${hive}\\${keyPath}`], options);
  return result.status === 0;
}

function deleteRegKey(hive, keyPath, options = {}) {
  if (!regKeyExists(hive, keyPath, options)) return true;
  const result = runReg(["delete", `${hive}\\${keyPath}`, "/f"], options);
  return result.status === 0;
}

function writeRegStr(hive, keyPath, valueName, value, options = {}) {
  const args = ["add", `${hive}\\${keyPath}`, "/f"];
  if (valueName) {
    args.push("/v", valueName);
  } else {
    args.push("/ve");
  }
  args.push("/t", "REG_SZ", "/d", value);
  const result = runReg(args, options);
  return result.status === 0;
}

function writeShellVerb(hive, keyPath, {
  executablePath,
  pathPlaceholder,
  iconPath,
}, options = {}) {
  const command = buildExplorerContextMenuCommand(executablePath, pathPlaceholder);
  if (!command) return false;
  const icon = `${iconPath || executablePath},0`;
  return (
    writeRegStr(hive, keyPath, "MUIVerb", MENU_LABEL, options)
    && writeRegStr(hive, keyPath, "Icon", icon, options)
    && writeRegStr(hive, `${keyPath}\\command`, "", command, options)
  );
}

function isExplorerContextMenuRegistered({
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) return false;
  const options = { spawnSyncImpl, logWarn };
  const hives = ["HKCU", "HKLM"];
  for (const hive of hives) {
    if (
      regKeyExists(hive, DIRECTORY_SHELL_KEY, options)
      || regKeyExists(hive, DIRECTORY_BACKGROUND_SHELL_KEY, options)
    ) {
      return true;
    }
  }
  return false;
}

function removeExplorerContextMenu({
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) {
    return { success: true, enabled: false, supported: false };
  }

  const options = { spawnSyncImpl, logWarn };
  const hives = ["HKCU", "HKLM"];
  let success = true;
  for (const hive of hives) {
    const folderOk = deleteRegKey(hive, DIRECTORY_SHELL_KEY, options);
    const backgroundOk = deleteRegKey(hive, DIRECTORY_BACKGROUND_SHELL_KEY, options);
    // HKLM may require elevation; treat leftover HKLM keys as failure only when
    // they still exist after the delete attempt.
    if (!folderOk || !backgroundOk) {
      if (
        regKeyExists(hive, DIRECTORY_SHELL_KEY, options)
        || regKeyExists(hive, DIRECTORY_BACKGROUND_SHELL_KEY, options)
      ) {
        success = false;
      }
    }
  }

  return {
    success,
    enabled: isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn }),
    supported: true,
  };
}

function installExplorerContextMenu({
  executablePath = process.execPath,
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) {
    return { success: true, enabled: false, supported: false };
  }

  const exe = String(executablePath || "").trim();
  if (!exe) {
    return { success: false, enabled: false, supported: true };
  }

  const options = { spawnSyncImpl, logWarn };
  // Always write HKCU so the toggle works without elevation. If the installer
  // already created HKLM keys, refresh those too when permitted.
  const hives = ["HKCU"];
  if (
    regKeyExists("HKLM", DIRECTORY_SHELL_KEY, options)
    || regKeyExists("HKLM", DIRECTORY_BACKGROUND_SHELL_KEY, options)
  ) {
    hives.push("HKLM");
  }

  let success = true;
  for (const hive of hives) {
    const folderOk = writeShellVerb(hive, DIRECTORY_SHELL_KEY, {
      executablePath: exe,
      pathPlaceholder: "%1",
      iconPath: exe,
    }, options);
    const backgroundOk = writeShellVerb(hive, DIRECTORY_BACKGROUND_SHELL_KEY, {
      executablePath: exe,
      pathPlaceholder: "%V",
      iconPath: exe,
    }, options);
    if (!folderOk || !backgroundOk) success = false;
  }

  return {
    success,
    enabled: isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn }),
    supported: true,
  };
}

function applyExplorerContextMenuPreference({
  enabled,
  executablePath = process.execPath,
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) {
    return { success: true, enabled: false, supported: false };
  }
  if (enabled === false) {
    return removeExplorerContextMenu({ platform, spawnSyncImpl, logWarn });
  }
  return installExplorerContextMenu({
    executablePath,
    platform,
    spawnSyncImpl,
    logWarn,
  });
}

function updateExplorerContextMenuEnabledPreference({
  currentEnabled = true,
  enabled = true,
  applyPreference = () => ({ success: false, enabled: currentEnabled }),
  writePreference = () => false,
} = {}) {
  const nextEnabled = enabled !== false;
  if (nextEnabled === currentEnabled) {
    return { enabled: currentEnabled, success: true, supported: true };
  }

  const applied = applyPreference(nextEnabled) || {};
  if (applied.success !== true) {
    return {
      enabled: typeof applied.enabled === "boolean" ? applied.enabled : currentEnabled,
      success: false,
      supported: applied.supported !== false,
    };
  }

  const writeSucceeded = writePreference(nextEnabled) === true;
  if (!writeSucceeded) {
    const rolledBack = applyPreference(currentEnabled) || {};
    return {
      enabled: typeof rolledBack.enabled === "boolean" ? rolledBack.enabled : nextEnabled,
      success: false,
      supported: true,
    };
  }

  return {
    enabled: nextEnabled,
    success: true,
    supported: true,
  };
}

function resolveExplorerContextMenuEnabled({
  app,
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  fsModule = fs,
  pathModule = path,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) {
    return { enabled: false, supported: false };
  }

  const preferred = readExplorerContextMenuEnabledPreference({
    app,
    fsModule,
    pathModule,
    logWarn,
  });
  if (typeof preferred === "boolean") {
    return { enabled: preferred, supported: true };
  }

  return {
    enabled: isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn }),
    supported: true,
  };
}

function applyInitialExplorerContextMenuPreference({
  app,
  executablePath = process.execPath,
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  fsModule = fs,
  pathModule = path,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) {
    return { enabled: false, success: true, supported: false };
  }

  const preferred = readExplorerContextMenuEnabledPreference({
    app,
    fsModule,
    pathModule,
    logWarn,
  });

  // No saved preference: keep installer/portable state, but refresh the command
  // path when the menu is already registered so upgrades keep working.
  if (preferred === null) {
    if (isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn })) {
      const refreshed = installExplorerContextMenu({
        executablePath,
        platform,
        spawnSyncImpl,
        logWarn,
      });
      return {
        enabled: refreshed.enabled === true,
        success: refreshed.success === true,
        supported: true,
      };
    }
    return { enabled: false, success: true, supported: true };
  }

  const applied = applyExplorerContextMenuPreference({
    enabled: preferred,
    executablePath,
    platform,
    spawnSyncImpl,
    logWarn,
  });
  return {
    enabled: applied.enabled === true,
    success: applied.success === true,
    supported: true,
  };
}

module.exports = {
  DIRECTORY_BACKGROUND_SHELL_KEY,
  DIRECTORY_SHELL_KEY,
  EXPLORER_CONTEXT_MENU_PREFERENCES_FILE,
  MENU_LABEL,
  applyExplorerContextMenuPreference,
  applyInitialExplorerContextMenuPreference,
  buildExplorerContextMenuCommand,
  getExplorerContextMenuPreferencePath,
  installExplorerContextMenu,
  isExplorerContextMenuRegistered,
  isWindowsPlatform,
  readExplorerContextMenuEnabledPreference,
  removeExplorerContextMenu,
  resolveExplorerContextMenuEnabled,
  updateExplorerContextMenuEnabledPreference,
  writeExplorerContextMenuEnabledPreference,
};
