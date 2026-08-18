"use strict";

/* global process, __dirname, console */

if (!process.versions.electron) {
  const test = require("node:test");
  test("inline keyword highlighting stays responsive", {
    skip: "run with Electron so the real WebGL renderer is available",
  }, () => {});
} else {
  const assert = require("node:assert/strict");
  const fs = require("node:fs");
  const path = require("node:path");
  const electron = require("electron");
  const esbuild = require("esbuild");
  const tempDirBridge = require("../electron/bridges/tempDirBridge.cjs");

  const appRoot = path.resolve(__dirname, "..");
  const showWindow = process.env.NETCATTY_TERMINAL_PERF_SHOW_WINDOW === "1";
  const userData = fs.mkdtempSync(`${tempDirBridge.getTempFilePath("xterm-keyword-highlight-perf")}-`);
  electron.app.setPath("userData", userData);
  electron.app.on("window-all-closed", () => {});
  let window = null;

  const cleanup = (exitCode) => {
    if (window && !window.isDestroyed()) window.destroy();
    try {
      fs.rmSync(userData, { recursive: true, force: true });
    } catch (error) {
      console.warn("Unable to remove xterm performance test data:", error);
    } finally {
      electron.app.exit(exitCode);
    }
  };

  void electron.app.whenReady().then(async () => {
    window = new electron.BrowserWindow({
      show: showWindow,
      width: 1000,
      height: 640,
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
        "<!doctype html><style>html,body,#terminal{width:920px;height:560px;margin:0}</style><div id=terminal></div>",
      ),
    );

    const xtermPath = require.resolve("@xterm/xterm", { paths: [appRoot] });
    const webglPath = require.resolve("@xterm/addon-webgl", { paths: [appRoot] });
    const serializePath = require.resolve("@xterm/addon-serialize", { paths: [appRoot] });
    const highlighterPath = path.join(appRoot, "components/terminal/keywordHighlight.ts");
    const pressurePath = path.join(appRoot, "components/terminal/runtime/terminalOutputPressure.ts");
    const highlighterBundle = esbuild.buildSync({
      stdin: {
        contents: [
          `export * from ${JSON.stringify(highlighterPath)};`,
          `export { noteTerminalOutputPressureData } from ${JSON.stringify(pressurePath)};`,
        ].join("\n"),
        loader: "ts",
        resolveDir: appRoot,
      },
      bundle: true,
      format: "cjs",
      platform: "browser",
      target: "chrome142",
      write: false,
    }).outputFiles[0].text;

    const result = await window.webContents.executeJavaScript(`(async () => {
      const { Terminal } = require(${JSON.stringify(xtermPath)});
      const { WebglAddon } = require(${JSON.stringify(webglPath)});
      const { SerializeAddon } = require(${JSON.stringify(serializePath)});
      const highlighterModule = { exports: {} };
      ((module, exports) => { ${highlighterBundle} })(highlighterModule, highlighterModule.exports);
      const { KeywordHighlighter, noteTerminalOutputPressureData } = highlighterModule.exports;
      const term = new Terminal({
        allowProposedApi: true,
        cols: 120,
        cursorBlink: false,
        rows: 40,
        scrollback: 10000,
      });
      term.open(document.getElementById("terminal"));
      let renderer = "dom";
      try {
        term.loadAddon(new WebglAddon());
        renderer = "webgl";
      } catch {}
      const serializer = new SerializeAddon();
      term.loadAddon(serializer);
      const highlighter = new KeywordHighlighter(term);
      const redRules = [{
        id: "error",
        label: "Error",
        patterns: ["ERROR", "failed", "10\\\\.2\\\\.\\\\d+\\\\.\\\\d+"],
        color: "#F87171",
        enabled: true,
      }];
      const blueRules = redRules.map(rule => ({ ...rule, color: "#60A5FA" }));
      highlighter.setRules(redRules, true);
      const write = data => new Promise(resolve => term.write(data, resolve));
      const waitPaint = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      let history = "";
      for (let line = 0; line < 10000; line += 1) {
        history += "2026-08-13 worker=" + (line % 32) + " ERROR failed from 10.2." + (line % 255) + "." + ((line * 7) % 255) + "\\r\\n";
      }

      const initialStart = performance.now();
      noteTerminalOutputPressureData(term, history);
      await write(history);
      await waitPaint();
      const initialWriteMs = performance.now() - initialStart;
      await new Promise(resolve => setTimeout(resolve, 650));
      await highlighter.whenSettled();
      const rebuildsBeforeEnter = highlighter.rebuildCount;

      const enterStart = performance.now();
      const prompt = "\\r\\nplain prompt # ";
      noteTerminalOutputPressureData(term, prompt);
      await write(prompt);
      await waitPaint();
      const enterWriteMs = performance.now() - enterStart;
      await new Promise(resolve => setTimeout(resolve, 650));
      await highlighter.whenSettled();
      const enterRebuildCount = highlighter.rebuildCount;

      const rebuildStart = performance.now();
      highlighter.setRules(blueRules, true);
      await highlighter.whenSettled();
      await waitPaint();
      const rebuildMs = performance.now() - rebuildStart;

      const serialized = serializer.serialize({ scrollback: 10000 });
      const pristine = highlighter.serializeAddon.serialize({ scrollback: 10000 });
      const state = {
        renderer,
        rawChars: history.length,
        initialWriteMs,
        rebuildsBeforeEnter,
        enterWriteMs,
        enterRebuildCount,
        rebuildMs,
        rebuildCount: highlighter.rebuildCount,
        blueMatchCount: (serialized.match(/38;2;96;165;250m/g) || []).length,
        pristineHasNetcattyColor: /38;2;(248;113;113|96;165;250)m/.test(pristine),
      };
      highlighter.dispose();
      term.dispose();
      return state;
    })()`);

    if (process.env.NETCATTY_TERMINAL_PERF_REQUIRE_WEBGL === "1") {
      assert.equal(result.renderer, "webgl", JSON.stringify(result));
    }
    assert.equal(result.enterRebuildCount, result.rebuildsBeforeEnter, JSON.stringify(result));
    assert.equal(result.rebuildCount, result.rebuildsBeforeEnter + 1, JSON.stringify(result));
    assert.ok(result.blueMatchCount >= 10000, JSON.stringify(result));
    assert.equal(result.pristineHasNetcattyColor, false, JSON.stringify(result));
    assert.ok(result.enterWriteMs < 150, `Enter write regressed: ${JSON.stringify(result)}`);
    assert.ok(result.rebuildMs < 1000, `10k-line rule rebuild regressed: ${JSON.stringify(result)}`);
    process.stdout.write(`XTERM_KEYWORD_HIGHLIGHT_PERFORMANCE_OK ${JSON.stringify(result)}\n`);
    cleanup(0);
  }).catch((error) => {
    console.error(error);
    cleanup(1);
  });
}
