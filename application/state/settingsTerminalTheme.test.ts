import test from "node:test";
import assert from "node:assert/strict";

import { resolveCurrentTerminalTheme } from "./settingsTerminalTheme";

const commonArgs = {
  terminalThemeId: "netcatty-dark",
  customThemes: [],
  resolvedTheme: "dark" as const,
  lightUiThemeId: "snow",
  darkUiThemeId: "github",
  accentMode: "theme" as const,
  customAccent: "",
};

test("follow-app terminal theme ignores manual per-mode terminal picks", () => {
  const theme = resolveCurrentTerminalTheme({
    ...commonArgs,
    followAppTerminalTheme: true,
    terminalThemeDarkId: "dracula",
    terminalThemeLightId: "solarized-light",
  });

  assert.equal(theme.id, "system-github-dark");
});

test("manual terminal theme uses the active per-mode pick", () => {
  const theme = resolveCurrentTerminalTheme({
    ...commonArgs,
    followAppTerminalTheme: false,
    terminalThemeDarkId: "dracula",
    terminalThemeLightId: "solarized-light",
  });

  assert.equal(theme.id, "dracula");
});
