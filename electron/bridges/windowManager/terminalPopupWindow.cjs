/* eslint-disable no-undef */

function createTerminalPopupWindowApi(ctx) {
  with (ctx) {
    const terminalPopupWindows = new Map();

    function isLiveWindow(win) {
      return Boolean(win && typeof win.isDestroyed === "function" && !win.isDestroyed());
    }

    async function openTerminalPopupWindow(electronModule, options, payload) {
      const { BrowserWindow, shell } = electronModule;
      const { preload, devServerUrl, isDev, appIcon, isMac, electronDir, sourceWindow } = options;

      const osTheme = electronModule?.nativeTheme?.shouldUseDarkColors ? "dark" : "light";
      const effectiveTheme = currentTheme === "dark" || currentTheme === "light" ? currentTheme : osTheme;
      const frontendBackground = resolveFrontendBackgroundColor(electronDir || __dirname, effectiveTheme);
      const backgroundColor = frontendBackground || "#1a1a1a";

      const popupWidth = 920;
      const popupHeight = 580;
      const { x: popupX, y: popupY } = resolveSettingsWindowBounds(electronModule, {
        sourceWindow: sourceWindow || mainWindow,
        settingsWidth: popupWidth,
        settingsHeight: popupHeight,
      });

      const title = typeof payload?.title === "string" && payload.title.trim()
        ? payload.title.trim()
        : "Terminal";

      const win = new BrowserWindow({
        title,
        width: popupWidth,
        height: popupHeight,
        ...(popupX !== undefined && popupY !== undefined ? { x: popupX, y: popupY } : {}),
        minWidth: 480,
        minHeight: 320,
        backgroundColor,
        icon: appIcon,
        show: false,
        frame: isMac,
        titleBarStyle: isMac ? "hiddenInset" : undefined,
        trafficLightPosition: isMac ? { x: 12, y: 12 } : undefined,
        webPreferences: {
          preload,
          contextIsolation: true,
          nodeIntegration: false,
          sandbox: false,
          v8CacheOptions: V8_CACHE_OPTIONS,
        },
      });

      const popupId = String(payload?.popupId || Date.now());
      terminalPopupWindows.set(popupId, win);

      try {
        win.webContents?.setWindowOpenHandler?.(
          createExternalOnlyWindowOpenHandler(shell),
        );
      } catch {
        // ignore
      }

      win.on("closed", () => {
        terminalPopupWindows.delete(popupId);
      });

      win.on("page-title-updated", (e) => { e.preventDefault(); });

      try {
        win.setBackgroundColor(backgroundColor);
      } catch {
        // ignore
      }

      applyWindowOpacityToWindow(win);

      const popupPath = "/#/terminal-popup";

      if (isDev) {
        try {
          const baseUrl = getDevRendererBaseUrl(devServerUrl);
          await win.loadURL(`${baseUrl}${popupPath}`);
        } catch (e) {
          console.warn("[TerminalPopup] Dev server not reachable", e);
          await win.loadURL(`app://netcatty/index.html${popupPath}`);
        }
      } else {
        await win.loadURL(`app://netcatty/index.html${popupPath}`);
      }

      const delivery = await sendWhenRendererReady(
        win,
        "netcatty:window:terminalPopupConfig",
        { ...payload, popupId },
        { timeoutMs: 10000 },
      );

      if (!delivery.success) {
        try { win.destroy(); } catch { /* ignore */ }
        terminalPopupWindows.delete(popupId);
        return { success: false, error: delivery.error || "Popup failed to receive config" };
      }

      showAndFocusWindow(win);
      return { success: true, popupId };
    }

    function closeTerminalPopupWindow(popupId) {
      const win = terminalPopupWindows.get(popupId);
      if (isLiveWindow(win)) {
        try { win.close(); } catch { /* ignore */ }
      }
      terminalPopupWindows.delete(popupId);
    }

    return {
      openTerminalPopupWindow,
      closeTerminalPopupWindow,
    };
  }
}

module.exports = { createTerminalPopupWindowApi };
