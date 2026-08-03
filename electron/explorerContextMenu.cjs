const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const EXPLORER_CONTEXT_MENU_PREFERENCES_FILE = "explorer-context-menu-preferences.json";
// Bump when the shell verb command/label/icon contract changes so warm starts
// re-apply registry entries once after upgrade, then stay query-free again.
const EXPLORER_CONTEXT_MENU_SCHEMA_VERSION = 1;
const SHELL_VERB = "Netcatty";
const DIRECTORY_SHELL_KEY = `Software\\Classes\\Directory\\shell\\${SHELL_VERB}`;
const DIRECTORY_BACKGROUND_SHELL_KEY = `Software\\Classes\\Directory\\Background\\shell\\${SHELL_VERB}`;
const MENU_LABEL = "Open in Netcatty";
const OPEN_TERMINAL_PATH_ARG = "--open-terminal-path";
// Hides a shell verb from Explorer while keeping the key present. Used as a
// per-user override when per-machine (HKLM) keys cannot be deleted without elevation.
const SUPPRESSION_VALUE = "ProgrammaticAccessOnly";

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

function readExplorerContextMenuPreferenceRecord({
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
    const schemaVersion = Number.isInteger(parsed.schemaVersion)
      ? parsed.schemaVersion
      : 0;
    return {
      enabled: parsed.enabled,
      schemaVersion,
    };
  } catch (err) {
    logWarn?.("[Main] Failed to read Explorer context menu preference:", err);
    return null;
  }
}

function readExplorerContextMenuEnabledPreference({
  app,
  fsModule = fs,
  pathModule = path,
  logWarn = console.warn,
} = {}) {
  const record = readExplorerContextMenuPreferenceRecord({
    app,
    fsModule,
    pathModule,
    logWarn,
  });
  return record ? record.enabled : null;
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
    fsModule.writeFileSync(
      filePath,
      JSON.stringify({
        enabled: enabled !== false,
        schemaVersion: EXPLORER_CONTEXT_MENU_SCHEMA_VERSION,
      }, null, 2),
    );
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

function regValueExists(hive, keyPath, valueName, options = {}) {
  const result = runReg(["query", `${hive}\\${keyPath}`, "/v", valueName], options);
  return result.status === 0;
}

function parseRegSzValue(stdout) {
  const lines = String(stdout || "").split(/\r?\n/);
  for (const line of lines) {
    const match = line.match(/\bREG_SZ\s+(.*)$/i);
    if (match) return match[1];
  }
  return null;
}

function readRegStr(hive, keyPath, valueName, options = {}) {
  const args = valueName
    ? ["query", `${hive}\\${keyPath}`, "/v", valueName]
    : ["query", `${hive}\\${keyPath}`, "/ve"];
  const result = runReg(args, options);
  if (result.status !== 0) return null;
  return parseRegSzValue(result.stdout);
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

function shellVerbIsCurrent(hive, keyPath, {
  executablePath,
  pathPlaceholder,
  iconPath,
}, options = {}) {
  if (!regKeyExists(hive, keyPath, options)) return false;
  // Suppression keys are not a real install even if the path exists.
  if (hive === "HKCU" && isSuppressionKey(hive, keyPath, options)) return false;

  const expectedCommand = buildExplorerContextMenuCommand(executablePath, pathPlaceholder);
  if (!expectedCommand) return false;
  const expectedIcon = `${iconPath || executablePath},0`;

  const currentCommand = readRegStr(hive, `${keyPath}\\command`, "", options);
  if (currentCommand !== expectedCommand) return false;

  const currentLabel = readRegStr(hive, keyPath, "MUIVerb", options);
  if (currentLabel !== MENU_LABEL) return false;

  const currentIcon = readRegStr(hive, keyPath, "Icon", options);
  if (currentIcon !== expectedIcon) return false;

  return true;
}

function writeShellVerb(hive, keyPath, {
  executablePath,
  pathPlaceholder,
  iconPath,
}, options = {}) {
  const command = buildExplorerContextMenuCommand(executablePath, pathPlaceholder);
  if (!command) return false;
  // Skip reg.exe writes when the verb is already current (common warm-start path).
  if (shellVerbIsCurrent(hive, keyPath, { executablePath, pathPlaceholder, iconPath }, options)) {
    return true;
  }
  const icon = `${iconPath || executablePath},0`;
  return (
    writeRegStr(hive, keyPath, "MUIVerb", MENU_LABEL, options)
    && writeRegStr(hive, keyPath, "Icon", icon, options)
    && writeRegStr(hive, `${keyPath}\\command`, "", command, options)
  );
}

function isSuppressionKey(hive, keyPath, options = {}) {
  return regKeyExists(hive, keyPath, options)
    && regValueExists(hive, keyPath, SUPPRESSION_VALUE, options);
}

function isUserSuppressed(options = {}) {
  return (
    isSuppressionKey("HKCU", DIRECTORY_SHELL_KEY, options)
    || isSuppressionKey("HKCU", DIRECTORY_BACKGROUND_SHELL_KEY, options)
  );
}

function writeUserSuppression(options = {}) {
  // HKCU Classes values override HKLM for the same key path. Marking the verb
  // ProgrammaticAccessOnly hides it in Explorer for this user without elevation.
  const folderOk = writeRegStr("HKCU", DIRECTORY_SHELL_KEY, SUPPRESSION_VALUE, "", options);
  const backgroundOk = writeRegStr(
    "HKCU",
    DIRECTORY_BACKGROUND_SHELL_KEY,
    SUPPRESSION_VALUE,
    "",
    options,
  );
  return folderOk && backgroundOk;
}

function clearUserSuppression(options = {}) {
  // Only delete HKCU keys that are suppressions so we do not wipe a real
  // per-user install when refreshing machine keys.
  let ok = true;
  for (const keyPath of [DIRECTORY_SHELL_KEY, DIRECTORY_BACKGROUND_SHELL_KEY]) {
    if (!isSuppressionKey("HKCU", keyPath, options)) continue;
    if (!deleteRegKey("HKCU", keyPath, options)) ok = false;
  }
  return ok;
}

function hasMachineRegistration(options = {}) {
  return (
    regKeyExists("HKLM", DIRECTORY_SHELL_KEY, options)
    || regKeyExists("HKLM", DIRECTORY_BACKGROUND_SHELL_KEY, options)
  );
}

function hasActiveShellKey(hive, keyPath, options = {}) {
  if (!regKeyExists(hive, keyPath, options)) return false;
  // Suppression keys are not an active menu entry.
  if (hive === "HKCU" && isSuppressionKey(hive, keyPath, options)) return false;
  return true;
}

function isExplorerContextMenuRegistered({
  platform = process.platform,
  spawnSyncImpl = spawnSync,
  logWarn = console.warn,
} = {}) {
  if (!isWindowsPlatform(platform)) return false;
  const options = { spawnSyncImpl, logWarn };
  // Per-user suppression hides machine registration for this user.
  if (isUserSuppressed(options)) return false;
  for (const hive of ["HKCU", "HKLM"]) {
    if (
      hasActiveShellKey(hive, DIRECTORY_SHELL_KEY, options)
      || hasActiveShellKey(hive, DIRECTORY_BACKGROUND_SHELL_KEY, options)
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

  // The Settings toggle is a per-user preference (stored under this user's
  // userData). Never delete HKLM verbs here — even when elevated — so other
  // accounts keep the installer-created menu. Machine-wide cleanup belongs to
  // the NSIS uninstaller. Per-user disable is: drop HKCU install keys, then
  // suppress any remaining HKLM verbs via ProgrammaticAccessOnly.
  let success = true;
  for (const keyPath of [DIRECTORY_SHELL_KEY, DIRECTORY_BACKGROUND_SHELL_KEY]) {
    if (!deleteRegKey("HKCU", keyPath, options)) {
      if (regKeyExists("HKCU", keyPath, options)) success = false;
    }
  }

  if (hasMachineRegistration(options)) {
    if (!writeUserSuppression(options)) {
      success = false;
    }
  }

  const stillActive = isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn });
  return {
    success: success && !stillActive,
    enabled: stillActive,
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

  // Drop any per-user hide before enabling again.
  clearUserSuppression(options);

  const machineRegistered = hasMachineRegistration(options);

  // When the installer already registered per-machine (HKLM) verbs, refresh
  // those only. Do NOT mirror them into HKCU: the NSIS uninstaller only cleans
  // SHCTX (HKLM in all-users mode), so an HKCU copy would survive uninstall.
  const hives = machineRegistered ? ["HKLM"] : ["HKCU"];

  let writesOk = true;
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
    if (!folderOk || !backgroundOk) writesOk = false;
  }

  // Machine keys that we could not refresh still show the menu (installer path).
  // Treat that as success so the Settings toggle can stay enabled without elevation.
  const enabled = isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn });
  const success = enabled && (writesOk || machineRegistered);

  return {
    success,
    enabled,
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

  const record = readExplorerContextMenuPreferenceRecord({
    app,
    fsModule,
    pathModule,
    logWarn,
  });

  // No saved preference: keep installer/portable state, but repair the command
  // path once when the menu is already registered (upgrade migration). Persist
  // the outcome so later startups skip the registry refresh path entirely.
  if (record === null) {
    if (isExplorerContextMenuRegistered({ platform, spawnSyncImpl, logWarn })) {
      const refreshed = installExplorerContextMenu({
        executablePath,
        platform,
        spawnSyncImpl,
        logWarn,
      });
      if (refreshed.success === true && refreshed.enabled === true) {
        writeExplorerContextMenuEnabledPreference({
          app,
          enabled: true,
          fsModule,
          pathModule,
          logWarn,
        });
      }
      return {
        enabled: refreshed.enabled === true,
        success: refreshed.success === true,
        supported: true,
      };
    }
    return { enabled: false, success: true, supported: true };
  }

  const preferred = record.enabled;
  // Healthy warm start: a current schemaVersion means the last successful apply
  // already wrote/suppressed the correct verbs. Skip reg.exe entirely.
  if (record.schemaVersion === EXPLORER_CONTEXT_MENU_SCHEMA_VERSION) {
    return { enabled: preferred, success: true, supported: true };
  }

  // Schema bump (command contract change): re-apply once and rewrite preference.
  const applied = applyExplorerContextMenuPreference({
    enabled: preferred,
    executablePath,
    platform,
    spawnSyncImpl,
    logWarn,
  });
  if (applied.success === true) {
    writeExplorerContextMenuEnabledPreference({
      app,
      enabled: applied.enabled === true,
      fsModule,
      pathModule,
      logWarn,
    });
  }
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
  EXPLORER_CONTEXT_MENU_SCHEMA_VERSION,
  MENU_LABEL,
  SUPPRESSION_VALUE,
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
