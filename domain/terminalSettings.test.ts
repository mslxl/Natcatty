import test from "node:test";
import assert from "node:assert/strict";

import { normalizeTerminalSettings } from "./models";

test("normalizeTerminalSettings disables prompt line breaks by default", () => {
  const settings = normalizeTerminalSettings();

  assert.equal(settings.forcePromptNewLine, false);
});

test("normalizeTerminalSettings defaults startupCommandDelayMs to 600", () => {
  assert.equal(normalizeTerminalSettings().startupCommandDelayMs, 600);
});

test("normalizeTerminalSettings preserves a provided startupCommandDelayMs", () => {
  assert.equal(normalizeTerminalSettings({ startupCommandDelayMs: 0 }).startupCommandDelayMs, 0);
  assert.equal(normalizeTerminalSettings({ startupCommandDelayMs: 1500 }).startupCommandDelayMs, 1500);
});

