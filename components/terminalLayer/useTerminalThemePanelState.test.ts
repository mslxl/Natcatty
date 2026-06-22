import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("./useTerminalThemePanelState.ts", import.meta.url), "utf8");

test("follow-app side panel theme changes update the global terminal theme", () => {
  assert.match(source, /onUpdateFollowAppTerminalThemeId\?\.\(themeId\)/);
  assert.match(source, /if \(followAppTerminalTheme\) \{/);
});

test("theme previews update the host tree sidebar in the same pass", () => {
  assert.match(source, /applyHostTreePreviewVars\(themeId\)/);
  assert.match(source, /clearHostTreePreviewVars\(\)/);
});
