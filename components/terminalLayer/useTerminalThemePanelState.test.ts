import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("./useTerminalThemePanelState.ts", import.meta.url), "utf8");

test("follow-app side panel theme changes delegate to ThemeRuntime pickTheme", () => {
  assert.match(source, /pickTheme\(themeId\)/);
  assert.match(source, /if \(followAppTerminalTheme\) \{/);
  assert.doesNotMatch(source, /onUpdateFollowAppTerminalThemeId/);
  assert.doesNotMatch(source, /setThemePreview/);
  assert.doesNotMatch(source, /applyTerminalPreviewVars/);
});

test("manual side panel theme changes persist host overrides and use runtime pick intent", () => {
  assert.match(source, /pickTheme\(themeId, \{ followApp: false \}\)/);
  assert.match(source, /onUpdateHost\(\{ \.\.\.rawFocusedHost, theme: themeId, themeOverride: true \}\)/);
  assert.doesNotMatch(source, /startTransition\(\(\) => \{[\s\S]*onUpdateHost\(\{ \.\.\.rawFocusedHost, theme: themeId/);
});

test("follow-app theme list selection tracks global runtime theme id", () => {
  assert.match(source, /listSelectedThemeId = followAppTerminalTheme/);
  assert.match(source, /terminalTheme\.id/);
});

test("manual theme list selection reads focused appearance from runtime", () => {
  assert.match(source, /resolveFocusedAppearance\(focusedHostScope\)/);
  assert.match(source, /focusedAppearance\.themeId/);
  assert.match(source, /resolvedPreviewTheme = focusedAppearance\.theme/);
});

test("closing the theme tab clears runtime user intent", () => {
  assert.match(source, /if \(activeSidePanelTab !== 'theme'\) \{/);
  assert.match(source, /clearIntent\(\)/);
});
