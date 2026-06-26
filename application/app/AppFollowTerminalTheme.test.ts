import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("../../App.tsx", import.meta.url), "utf8");

test("follow-app terminal theme selection updates the matching UI theme", () => {
  assert.match(source, /const update = getFollowAppTerminalThemeSelectionUpdate\(themeId\)/);
  assert.match(source, /setDarkUiThemeId\(update\.uiThemeId\)/);
  assert.match(source, /setLightUiThemeId\(update\.uiThemeId\)/);
  assert.match(source, /setTheme\(update\.appTheme\)/);
  assert.doesNotMatch(source, /customThemeStore\.getThemeById\(themeId\)/);
});
