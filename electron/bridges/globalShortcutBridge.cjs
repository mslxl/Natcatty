/**
 * Global Shortcut Bridge - Handles global keyboard shortcuts and system tray
 * Implements the "Quake mode" / drop-down terminal feature
 */

const path = require("node:path");
const fs = require("node:fs");

let electronModule = null;
let tray = null;
let closeToTray = false;
let currentHotkey = null;
let hotkeyEnabled = false;

function resolveTrayIconPath() {
  const { app } = electronModule;
  
  // Use different icons for different platforms
  // macOS: template image (black + transparent, system handles color)
  // Windows/Linux: colored icon
  const isMac = process.platform === "darwin";
  const iconName = isMac ? "tray-iconTemplate.png" : "tray-icon.png";
  
  // Security: Only use known packaged icon locations, ignore renderer-provided paths
  const candidates = [
    path.join(app.getAppPath(), "dist", iconName),
    path.join(app.getAppPath(), "public", iconName),
    path.join(__dirname, "../../public", iconName),
    path.join(__dirname, "../../dist", iconName),
  ];

  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  return null;
}

/**
 * Initialize the bridge with dependencies
 */
function init(deps) {
  electronModule = deps.electronModule;
}

/**
 * Get the main window reference
 */
function getMainWindow() {
  const { BrowserWindow } = electronModule;
  const wins = BrowserWindow.getAllWindows();
  // Return the first window (main window)
  return wins && wins.length ? wins[0] : null;
}

/**
 * Convert a hotkey string from frontend format to Electron accelerator format
 * e.g., "⌘ + Space" -> "CommandOrControl+Space"
 *       "Ctrl + `" -> "CommandOrControl+`"
 *       "Alt + Space" -> "Alt+Space"
 */
function toElectronAccelerator(hotkeyStr) {
  if (!hotkeyStr || hotkeyStr === "Disabled" || hotkeyStr === "") {
    return null;
  }

  // Parse the hotkey string
  const parts = hotkeyStr.split("+").map((p) => p.trim());

  // Convert each part to Electron accelerator format
  const acceleratorParts = parts.map((part) => {
    // Mac symbols to Electron format
    if (part === "⌘" || part === "Cmd" || part === "Command") {
      return "CommandOrControl";
    }
    if (part === "⌃" || part === "Ctrl" || part === "Control") {
      return "Control";
    }
    if (part === "⌥" || part === "Alt" || part === "Option") {
      return "Alt";
    }
    if (part === "Shift") {
      return "Shift";
    }
    if (part === "Win" || part === "Super" || part === "Meta") {
      return "Super";
    }
    // Arrow symbols
    if (part === "↑") return "Up";
    if (part === "↓") return "Down";
    if (part === "←") return "Left";
    if (part === "→") return "Right";
    // Special keys
    if (part === "↵" || part === "Enter" || part === "Return") return "Return";
    if (part === "⇥" || part === "Tab") return "Tab";
    if (part === "⌫" || part === "Backspace") return "Backspace";
    if (part === "Del" || part === "Delete") return "Delete";
    if (part === "Esc" || part === "Escape") return "Escape";
    if (part === "Space") return "Space";
    // Backtick/grave accent
    if (part === "`" || part === "~") return "`";
    // Function keys
    if (/^F\d+$/i.test(part)) return part.toUpperCase();
    // Single character - keep as-is
    return part;
  });

  return acceleratorParts.join("+");
}

/**
 * Toggle the main window visibility
 */
function toggleWindowVisibility() {
  const win = getMainWindow();
  if (!win) return;

  try {
    // Check if window is minimized first - minimized windows may still report isVisible() = true
    if (win.isMinimized()) {
      win.restore();
      win.show();
      win.focus();
      const { app } = electronModule;
      try {
        app.focus({ steal: true });
      } catch {
        // ignore
      }
    } else if (win.isVisible()) {
      if (win.isFocused()) {
        // Window is visible and focused - hide it
        win.hide();
      } else {
        // Window is visible but not focused - focus it
        win.focus();
        const { app } = electronModule;
        try {
          app.focus({ steal: true });
        } catch {
          // ignore
        }
      }
    } else {
      // Window is hidden - show and focus it
      win.show();
        win.focus();
      const { app } = electronModule;
      try {
        app.focus({ steal: true });
      } catch {
        // ignore
      }
    }
  } catch (err) {
    console.warn("[GlobalShortcut] Error toggling window visibility:", err);
  }
}

/**
 * Register the global toggle hotkey
 */
function registerGlobalHotkey(hotkeyStr) {
  const { globalShortcut } = electronModule;

  // Unregister existing hotkey first
  unregisterGlobalHotkey();

  if (!hotkeyStr || hotkeyStr === "Disabled" || hotkeyStr === "") {
    hotkeyEnabled = false;
    currentHotkey = null;
    return { success: true, enabled: false };
  }

  const accelerator = toElectronAccelerator(hotkeyStr);
  if (!accelerator) {
    hotkeyEnabled = false;
    currentHotkey = null;
    return { success: false, error: "Invalid hotkey format" };
  }

  try {
    const registered = globalShortcut.register(accelerator, toggleWindowVisibility);
    if (registered) {
      hotkeyEnabled = true;
      currentHotkey = hotkeyStr;
      console.log(`[GlobalShortcut] Registered hotkey: ${accelerator}`);
      return { success: true, enabled: true, accelerator };
    } else {
      console.warn(`[GlobalShortcut] Failed to register hotkey: ${accelerator}`);
      return { success: false, error: "Hotkey may be in use by another application" };
    }
  } catch (err) {
    console.error("[GlobalShortcut] Error registering hotkey:", err);
    return { success: false, error: err.message };
  }
}

/**
 * Unregister the global toggle hotkey
 */
function unregisterGlobalHotkey() {
  if (!hotkeyEnabled || !currentHotkey) return;

  const { globalShortcut } = electronModule;
  const accelerator = toElectronAccelerator(currentHotkey);

  if (accelerator) {
    try {
      globalShortcut.unregister(accelerator);
      console.log(`[GlobalShortcut] Unregistered hotkey: ${accelerator}`);
    } catch (err) {
      console.warn("[GlobalShortcut] Error unregistering hotkey:", err);
    }
  }

  hotkeyEnabled = false;
  currentHotkey = null;
}

/**
 * Create the system tray icon
 */
function createTray() {
  const { Tray, Menu, app, nativeImage } = electronModule;

  if (tray) {
    // Tray already exists
    return;
  }

  try {
    // Load the tray icon
    let trayIcon;
    const resolvedIconPath = resolveTrayIconPath();
    if (resolvedIconPath) {
      trayIcon = nativeImage.createFromPath(resolvedIconPath);
      // Resize for tray (16x16 on most platforms, 22x22 on some Linux)
      if (process.platform === "darwin") {
        trayIcon = trayIcon.resize({ width: 16, height: 16 });
        trayIcon.setTemplateImage(true);
      } else {
        trayIcon = trayIcon.resize({ width: 16, height: 16 });
      }
    }

    tray = new Tray(trayIcon || nativeImage.createEmpty());
    tray.setToolTip("Netcatty");

    // Create context menu
    const contextMenu = Menu.buildFromTemplate([
      {
        label: "Show/Hide Window",
        click: toggleWindowVisibility,
      },
      { type: "separator" },
      {
        label: "Quit",
        click: () => {
          // Force quit - bypass close-to-tray
          closeToTray = false;
          app.quit();
        },
      },
    ]);

    tray.setContextMenu(contextMenu);

    // Click on tray icon shows/hides window
    tray.on("click", toggleWindowVisibility);

    // Double-click also shows window (Windows behavior)
    tray.on("double-click", () => {
      const win = getMainWindow();
      if (win) {
        win.show();
        win.focus();
      }
    });

    console.log("[GlobalShortcut] System tray created");
  } catch (err) {
    console.error("[GlobalShortcut] Error creating tray:", err);
  }
}

/**
 * Destroy the system tray icon
 */
function destroyTray() {
  if (tray) {
    try {
      tray.destroy();
      tray = null;
      console.log("[GlobalShortcut] System tray destroyed");
    } catch (err) {
      console.warn("[GlobalShortcut] Error destroying tray:", err);
    }
  }
}

/**
 * Set close-to-tray behavior
 */
function setCloseToTray(enabled) {
  closeToTray = !!enabled;

  if (closeToTray) {
    // Create tray if it doesn't exist
    if (!tray) {
      createTray();
    }
  } else {
    // Destroy tray if it exists
    destroyTray();
  }

  return { success: true, enabled: closeToTray };
}

/**
 * Check if close-to-tray is enabled
 */
function isCloseToTrayEnabled() {
  return closeToTray;
}

/**
 * Get current hotkey status
 */
function getHotkeyStatus() {
  return {
    enabled: hotkeyEnabled,
    hotkey: currentHotkey,
  };
}

/**
 * Handle window close event - hide to tray instead of closing
 */
function handleWindowClose(event, win) {
  if (closeToTray && tray) {
    event.preventDefault();
    win.hide();
    return true; // Prevented close
  }
  return false; // Allow close
}

/**
 * Register IPC handlers
 */
function registerHandlers(ipcMain) {
  // Register global toggle hotkey
  ipcMain.handle("netcatty:globalHotkey:register", async (_event, { hotkey }) => {
    return registerGlobalHotkey(hotkey);
  });

  // Unregister global toggle hotkey
  ipcMain.handle("netcatty:globalHotkey:unregister", async () => {
    unregisterGlobalHotkey();
    return { success: true };
  });

  // Get current hotkey status
  ipcMain.handle("netcatty:globalHotkey:status", async () => {
    return getHotkeyStatus();
  });

  // Set close-to-tray behavior
  ipcMain.handle("netcatty:tray:setCloseToTray", async (_event, { enabled }) => {
    return setCloseToTray(enabled);
  });

  // Get close-to-tray status
  ipcMain.handle("netcatty:tray:isCloseToTray", async () => {
    return { enabled: closeToTray };
  });

  console.log("[GlobalShortcut] IPC handlers registered");
}

/**
 * Cleanup on app quit
 */
function cleanup() {
  unregisterGlobalHotkey();
  destroyTray();
}

module.exports = {
  init,
  registerHandlers,
  registerGlobalHotkey,
  unregisterGlobalHotkey,
  setCloseToTray,
  isCloseToTrayEnabled,
  handleWindowClose,
  toggleWindowVisibility,
  getHotkeyStatus,
  cleanup,
};
