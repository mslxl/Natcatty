import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("./useTerminalLayerEffects.ts", import.meta.url), "utf8");

test("follow-app terminal theme preview cleanup does not cancel theme clicks", () => {
  assert.doesNotMatch(source, /\[followAppTerminalTheme, themePreview\.targetSessionId, themePreview\.themeId\]/);
});

test("theme preview cleanup also clears the host tree sidebar preview", () => {
  assert.match(source, /clearHostTreePreviewVars\(\)/);
});

test("follow-app mode changes clear previews in either direction", () => {
  assert.match(source, /const didChangeFollowTheme = followAppTerminalTheme !== previousFollowAppTerminalThemeRef\.current/);
  assert.match(source, /if \(!didChangeFollowTheme\) return/);
});
