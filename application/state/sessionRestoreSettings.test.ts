import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  DEFAULT_RESTORE_PREVIOUS_SESSION,
  resolveRestorePreviousSessionSetting,
} from "./sessionRestoreSettings.ts";

test("restore previous session setting defaults on", () => {
  assert.equal(DEFAULT_RESTORE_PREVIOUS_SESSION, true);
  assert.equal(resolveRestorePreviousSessionSetting(null), true);
});

test("restore previous session setting preserves explicit stored values", () => {
  assert.equal(resolveRestorePreviousSessionSetting(true), true);
  assert.equal(resolveRestorePreviousSessionSetting(false), false);
});

test("restore previous session setting participates in cross-window settings sync", () => {
  const storageSyncSource = readFileSync(new URL("./settingsStorageSync.ts", import.meta.url), "utf8");
  const ipcSyncSource = readFileSync(new URL("./settingsIpcSync.ts", import.meta.url), "utf8");

  assert.match(storageSyncSource, /STORAGE_KEY_RESTORE_PREVIOUS_SESSION/);
  assert.match(storageSyncSource, /setRestorePreviousSessionState/);
  assert.match(storageSyncSource, /e\.key === STORAGE_KEY_RESTORE_PREVIOUS_SESSION/);

  assert.match(ipcSyncSource, /STORAGE_KEY_RESTORE_PREVIOUS_SESSION/);
  assert.match(ipcSyncSource, /setRestorePreviousSessionState/);
  assert.match(ipcSyncSource, /key === STORAGE_KEY_RESTORE_PREVIOUS_SESSION/);
});
