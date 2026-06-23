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

test("app lock disable handler uses its inline current password field", () => {
  const source = readFileSync(new URL("./SettingsSystemTab.tsx", import.meta.url), "utf8");
  const handlerStart = source.indexOf("const handleDisableAppLock = useCallback");
  const handlerEnd = source.indexOf("const handleSaveAppLockPassword", handlerStart);
  const handlerSource = source.slice(handlerStart, handlerEnd);

  assert.match(handlerSource, /requestAppLockDisable\(appLockDisablePassword\)/);
  assert.match(handlerSource, /appLockDisablePassword,/);
  assert.doesNotMatch(source, /handleAppLockEnabledChange/);
});

test("app lock management separates disabling from replacing the password", () => {
  const source = readFileSync(new URL("./SettingsSystemTab.tsx", import.meta.url), "utf8");
  const appLockSectionStart = source.indexOf('<SectionHeader title={t("settings.appLock.title")} />');
  const nextSectionStart = source.indexOf("<SectionHeader", appLockSectionStart + 1);
  const appLockSection = source.slice(appLockSectionStart, nextSectionStart);

  assert.doesNotMatch(appLockSection, /<Toggle/);
  assert.match(source, /settings\.appLock\.disableTitle/);
  assert.match(source, /settings\.appLock\.disableDescription/);
  assert.match(source, /settings\.appLock\.disable/);
  assert.match(source, /settings\.appLock\.changePasswordTitle/);
  assert.match(source, /settings\.appLock\.currentPasswordForDisablePlaceholder/);
  assert.match(source, /settings\.appLock\.currentPasswordForChangePlaceholder/);
});

test("app lock disable explains that turning it off removes the saved password", () => {
  const englishLocale = readFileSync(new URL("../../../application/i18n/locales/en/core.ts", import.meta.url), "utf8");

  assert.match(englishLocale, /saved password will be removed/);
  assert.match(englishLocale, /requires creating a new one/);
});
