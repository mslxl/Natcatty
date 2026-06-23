import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

test("disabling app lock does not trigger a second renderer-side unlock request", () => {
  const source = readFileSync(new URL("./SettingsSystemTab.tsx", import.meta.url), "utf8");

  assert.doesNotMatch(source, /unlockApp\?\.\(/);
  assert.doesNotMatch(source, /unlockApp\?:/);
});

test("app lock setup starts with password setup instead of a misleading enable toggle", () => {
  const source = readFileSync(new URL("./SettingsSystemTab.tsx", import.meta.url), "utf8");

  assert.match(source, /!hasAppLockPassword \? \(/);
  assert.match(source, /settings\.appLock\.setupTitle/);
  assert.match(source, /settings\.appLock\.setupDescription/);
  assert.match(source, /settings\.appLock\.manageTitle/);
  assert.doesNotMatch(
    source,
    /hasAppLockPassword \? t\("settings\.appLock\.enableDesc"\) : t\("settings\.appLock\.enableAfterPassword"\)/,
  );
});
