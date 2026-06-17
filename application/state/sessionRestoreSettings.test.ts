import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  DEFAULT_RESTORE_TERMINAL_CWD,
  DEFAULT_RESTORE_PREVIOUS_SESSION,
  resolveRestoreTerminalCwdSetting,
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

test("restore terminal cwd setting defaults off", () => {
  assert.equal(DEFAULT_RESTORE_TERMINAL_CWD, false);
  assert.equal(resolveRestoreTerminalCwdSetting(null), false);
});

test("restore terminal cwd setting preserves explicit stored values", () => {
  assert.equal(resolveRestoreTerminalCwdSetting(true), true);
  assert.equal(resolveRestoreTerminalCwdSetting(false), false);
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

test("disabling restore previous session clears the stored restore snapshot", () => {
  const settingsSource = readFileSync(new URL("./useSettingsState.ts", import.meta.url), "utf8");
  const importIndex = settingsSource.indexOf("sessionRestoreStorage");
  const setterIndex = settingsSource.indexOf("const setRestorePreviousSession = useCallback");
  const clearIndex = settingsSource.indexOf("sessionRestoreStorage.clear()", setterIndex);
  const writeIndex = settingsSource.indexOf("localStorageAdapter.writeBoolean(STORAGE_KEY_RESTORE_PREVIOUS_SESSION", setterIndex);

  assert.notEqual(importIndex, -1);
  assert.notEqual(setterIndex, -1);
  assert.notEqual(clearIndex, -1);
  assert.notEqual(writeIndex, -1);
  assert.ok(
    writeIndex < clearIndex,
    "the setting should be persisted before clearing the restore snapshot",
  );
});

test("restore terminal cwd setting participates in cross-window settings sync", () => {
  const storageSyncSource = readFileSync(new URL("./settingsStorageSync.ts", import.meta.url), "utf8");
  const ipcSyncSource = readFileSync(new URL("./settingsIpcSync.ts", import.meta.url), "utf8");

  assert.match(storageSyncSource, /STORAGE_KEY_RESTORE_TERMINAL_CWD/);
  assert.match(storageSyncSource, /setRestoreTerminalCwdState/);
  assert.match(storageSyncSource, /e\.key === STORAGE_KEY_RESTORE_TERMINAL_CWD/);

  assert.match(ipcSyncSource, /STORAGE_KEY_RESTORE_TERMINAL_CWD/);
  assert.match(ipcSyncSource, /setRestoreTerminalCwdState/);
  assert.match(ipcSyncSource, /key === STORAGE_KEY_RESTORE_TERMINAL_CWD/);
});
