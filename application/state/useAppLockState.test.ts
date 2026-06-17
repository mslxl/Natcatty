import assert from "node:assert/strict";
import test from "node:test";

import {
  createAppLockPasswordVerifier,
  type AppLockSettings,
} from "../../domain/appLock.ts";
import {
  getIdleLockDelayMs,
  resolveUnlockAttempt,
  shouldLockAfterIdle,
  shouldLockOnStartup,
} from "./useAppLockState.ts";

test("shouldLockOnStartup locks only when enabled with a verifier", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");
  const enabled: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 15,
    passwordVerifier: verifier,
  };

  assert.equal(shouldLockOnStartup(enabled), true);
  assert.equal(shouldLockOnStartup({ ...enabled, enabled: false }), false);
  assert.equal(shouldLockOnStartup({ ...enabled, passwordVerifier: null }), false);
});

test("shouldLockAfterIdle honors the configured timeout", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");
  const settings: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 5,
    passwordVerifier: verifier,
  };

  assert.equal(shouldLockAfterIdle(settings, 1_000, 1_000 + 5 * 60_000 - 1), false);
  assert.equal(shouldLockAfterIdle(settings, 1_000, 1_000 + 5 * 60_000), true);
  assert.equal(shouldLockAfterIdle({ ...settings, enabled: false }, 1_000, 1_000 + 60 * 60_000), false);
  assert.equal(shouldLockAfterIdle({ ...settings, passwordVerifier: null }, 1_000, 1_000 + 60 * 60_000), false);
});

test("getIdleLockDelayMs schedules the next check after remaining idle time", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");
  const settings: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 5,
    passwordVerifier: verifier,
  };

  assert.equal(getIdleLockDelayMs(settings, 1_000, 1_000), 5 * 60_000);
  assert.equal(getIdleLockDelayMs(settings, 1_000, 1_000 + 4 * 60_000), 60_000);
  assert.equal(getIdleLockDelayMs(settings, 1_000, 1_000 + 5 * 60_000), 0);
  assert.equal(getIdleLockDelayMs({ ...settings, enabled: false }, 1_000, 1_000), null);
  assert.equal(getIdleLockDelayMs({ ...settings, passwordVerifier: null }, 1_000, 1_000), null);
});


test("resolveUnlockAttempt validates empty, incorrect, and correct passwords", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");

  assert.deepEqual(await resolveUnlockAttempt("", verifier), { ok: false, error: "empty" });
  assert.deepEqual(await resolveUnlockAttempt("wrong", verifier), { ok: false, error: "incorrect" });
  assert.deepEqual(await resolveUnlockAttempt("secret", verifier), { ok: true });
  assert.deepEqual(await resolveUnlockAttempt("secret", null), { ok: false, error: "incorrect" });
});
