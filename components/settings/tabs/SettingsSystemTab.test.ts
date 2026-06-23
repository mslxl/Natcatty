import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

test("disabling app lock does not trigger a second renderer-side unlock request", () => {
  const source = readFileSync(new URL("./SettingsSystemTab.tsx", import.meta.url), "utf8");

  assert.doesNotMatch(source, /unlockApp\?\.\(/);
  assert.doesNotMatch(source, /unlockApp\?:/);
});
