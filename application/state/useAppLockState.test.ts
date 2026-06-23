import assert from "node:assert/strict";
import test from "node:test";
import { createAppLockPasswordVerifier, type AppLockSettings } from "../../domain/appLock.ts";
import {
  createOptimisticUnlockedRuntimeState,
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

test("createOptimisticUnlockedRuntimeState clears stale locked state after successful unlock", () => {
  const nextState = createOptimisticUnlockedRuntimeState(
    {
      initialized: false,
      locked: true,
      reason: "startup",
      version: 7,
      lastLockedAt: 2_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
    5_000,
  );

  assert.deepEqual(nextState, {
    initialized: true,
    locked: false,
    reason: null,
    version: 7,
    lastLockedAt: 2_000,
    lastUnlockedAt: 5_000,
    lastActivityAt: 5_000,
  });
});


test("resolveUnlockAttempt validates empty, incorrect, and correct passwords", async () => {
  const originalWindow = globalThis.window;
  const verifier = await createAppLockPasswordVerifier("secret");
  void verifier;
  globalThis.window = {
    netcatty: {
      requestAppLockUnlock: async (password: string) =>
        password === "secret"
          ? { ok: true as const }
          : { ok: false as const, error: "incorrect" as const },
    },
  } as typeof window;

  try {
    assert.deepEqual(await resolveUnlockAttempt(""), { ok: false, error: "empty" });
    assert.deepEqual(await resolveUnlockAttempt("wrong"), { ok: false, error: "incorrect" });
    assert.deepEqual(await resolveUnlockAttempt("secret"), { ok: true });
  } finally {
    globalThis.window = originalWindow;
  }
});
