"use strict";

/* global process, __dirname, console */

if (!process.versions.electron) {
  const test = require("node:test");
  test("xterm keeps dense keyword-style decorations responsive", {
    skip: "run with Electron so the real DOM renderer is available",
  }, () => {});
} else {
  const assert = require("node:assert/strict");
  const fs = require("node:fs");
  const os = require("node:os");
  const path = require("node:path");
  const electron = require("electron");

  const appRoot = path.resolve(__dirname, "..");
  const userData = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-xterm-decoration-perf-"));
  electron.app.setPath("userData", userData);
  electron.app.on("window-all-closed", () => {});

  const cleanup = (exitCode) => {
    fs.rmSync(userData, { recursive: true, force: true });
    electron.app.exit(exitCode);
  };

  void electron.app.whenReady().then(async () => {
    const window = new electron.BrowserWindow({
      show: false,
      width: 900,
      height: 560,
      paintWhenInitiallyHidden: true,
      webPreferences: {
        backgroundThrottling: false,
        contextIsolation: false,
        nodeIntegration: true,
        sandbox: false,
      },
    });
    await window.loadURL(
      "data:text/html;charset=utf-8," + encodeURIComponent(
        "<!doctype html><style>html,body,#terminal{width:800px;height:480px;margin:0}</style><div id=terminal></div>",
      ),
    );

    const xtermPath = require.resolve("@xterm/xterm", { paths: [appRoot] });
    const result = await window.webContents.executeJavaScript(`(async () => {
      const { Terminal } = require(${JSON.stringify(xtermPath)});
      const term = new Terminal({
        allowProposedApi: true,
        cols: 80,
        cursorBlink: false,
        rendererType: "dom",
        rows: 30,
        scrollback: 1000,
      });
      term.open(document.getElementById("terminal"));

      const waitForRender = (trigger, label) => new Promise((resolve, reject) => {
        const startedAt = performance.now();
        const timeout = setTimeout(() => {
          disposable.dispose();
          reject(new Error("timed out waiting for terminal render: " + label));
        }, 15000);
        const disposable = term.onRender(() => {
          clearTimeout(timeout);
          disposable.dispose();
          resolve(performance.now() - startedAt);
        });
        trigger();
      });
      const writeAndWait = data => new Promise(resolve => term.write(data, resolve));

      let history = "";
      for (let line = 0; line < 500; line += 1) {
        history += "INFO WARN ERROR SUCCESS DEBUG completed failed critical\\r\\n";
      }
      await writeAndWait(history);

      const cursorLine = term.buffer.normal.baseY + term.buffer.normal.cursorY;
      const markers = [];
      const decorations = [];
      for (let line = term.buffer.normal.length - 500; line < term.buffer.normal.length; line += 1) {
        const marker = term.registerMarker(line - cursorLine);
        if (!marker) continue;
        markers.push(marker);
        for (let x = 0; x < term.cols; x += 2) {
          const decoration = term.registerDecoration({
            marker,
            x,
            width: 1,
            foregroundColor: "#f87171",
          });
          if (decoration) decorations.push(decoration);
        }
      }

      const registrationPaintMs = await waitForRender(
        () => term.refresh(0, term.rows - 1),
        "registration paint",
      );
      const refreshMs = [];
      for (let iteration = 0; iteration < 3; iteration += 1) {
        refreshMs.push(await waitForRender(
          () => term.refresh(0, term.rows - 1),
          "measured refresh " + iteration,
        ));
      }

      const state = {
        decorationCount: decorations.length,
        markerCount: markers.length,
        registrationPaintMs,
        refreshMs,
        worstRefreshMs: Math.max(...refreshMs),
      };
      decorations.forEach(decoration => decoration.dispose());
      markers.forEach(marker => marker.dispose());
      term.dispose();
      return state;
    })()`);

    assert.equal(result.markerCount, 500, JSON.stringify(result));
    assert.equal(result.decorationCount, 20000, JSON.stringify(result));
    assert.ok(
      result.worstRefreshMs < 150,
      `dense keyword-style decorations blocked terminal repaint: ${JSON.stringify(result)}`,
    );
    process.stdout.write(`XTERM_DECORATION_PERFORMANCE_OK ${JSON.stringify(result)}\n`);
    window.destroy();
    cleanup(0);
  }).catch((error) => {
    console.error(error);
    cleanup(1);
  });
}
