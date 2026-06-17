import assert from "node:assert/strict";
import test from "node:test";

import {
  APP_LOCK_TIMEOUT_OPTIONS_MINUTES,
  DEFAULT_APP_LOCK_SETTINGS,
  canEnableAppLock,
  createAppLockPasswordVerifier,
  normalizeAppLockSettings,
  normalizeAppLockTimeoutMinutes,
  verifyAppLockPassword,
} from "./appLock.ts";

test("normalizeAppLockTimeoutMinutes accepts only supported timeout options", () => {
  assert.deepEqual(APP_LOCK_TIMEOUT_OPTIONS_MINUTES, [1, 5, 15, 30, 60]);
  assert.equal(normalizeAppLockTimeoutMinutes(1), 1);
  assert.equal(normalizeAppLockTimeoutMinutes("5"), 5);
  assert.equal(normalizeAppLockTimeoutMinutes(60), 60);
  assert.equal(normalizeAppLockTimeoutMinutes(2), DEFAULT_APP_LOCK_SETTINGS.timeoutMinutes);
  assert.equal(normalizeAppLockTimeoutMinutes(""), DEFAULT_APP_LOCK_SETTINGS.timeoutMinutes);
});

test("normalizeAppLockSettings defaults disabled and clears verifier when disabled", () => {
  const normalized = normalizeAppLockSettings({
    enabled: false,
    timeoutMinutes: 30,
    passwordVerifier: {
      version: 1,
      algorithm: "PBKDF2-SHA256",
      iterations: 210000,
      salt: "abc",
      hash: "def",
    },
  });

  assert.deepEqual(normalized, {
    enabled: false,
    timeoutMinutes: 30,
    passwordVerifier: null,
  });
});

test("normalizeAppLockSettings refuses enabled state without a valid verifier", () => {
  assert.deepEqual(
    normalizeAppLockSettings({
      enabled: true,
      timeoutMinutes: 5,
      passwordVerifier: {
        version: 1,
        algorithm: "PBKDF2-SHA256",
        iterations: 0,
        salt: "",
        hash: "",
      },
    }),
    {
      enabled: false,
      timeoutMinutes: 5,
      passwordVerifier: null,
    },
  );
});

test("createAppLockPasswordVerifier stores a verifier and verifies password attempts", async () => {
  const verifier = await createAppLockPasswordVerifier("correct horse battery staple");

  assert.equal(verifier.version, 1);
  assert.equal(verifier.algorithm, "PBKDF2-SHA256");
  assert.ok(verifier.iterations >= 100000);
  assert.notEqual(verifier.salt, "");
  assert.notEqual(verifier.hash, "");
  assert.ok(!verifier.hash.includes("correct horse battery staple"));

  assert.equal(canEnableAppLock({ enabled: false, timeoutMinutes: 15, passwordVerifier: verifier }), true);
  assert.equal(await verifyAppLockPassword("correct horse battery staple", verifier), true);
  assert.equal(await verifyAppLockPassword("wrong password", verifier), false);
  assert.equal(await verifyAppLockPassword("", verifier), false);
  assert.equal(await verifyAppLockPassword("correct horse battery staple", null), false);
});

test("createAppLockPasswordVerifier rejects empty passwords", async () => {
  await assert.rejects(
    () => createAppLockPasswordVerifier("  "),
    /password/i,
  );
});
