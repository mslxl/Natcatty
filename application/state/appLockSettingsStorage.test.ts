import assert from "node:assert/strict";
import test from "node:test";

import {
  createAppLockPasswordVerifier,
  verifyAppLockPassword,
  type AppLockSettings,
} from "../../domain/appLock.ts";
import {
  applyAppLockEnabledChange,
  replaceAppLockPassword,
} from "./appLockSettingsStorage.ts";

test("applyAppLockEnabledChange enables only when a verifier exists", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");
  const disabled: AppLockSettings = {
    enabled: false,
    timeoutMinutes: 15,
    passwordVerifier: verifier,
  };

  assert.deepEqual(
    await applyAppLockEnabledChange(disabled, true),
    {
      enabled: true,
      timeoutMinutes: 15,
      passwordVerifier: verifier,
    },
  );

  assert.deepEqual(
    await applyAppLockEnabledChange({ ...disabled, passwordVerifier: null }, true),
    {
      enabled: false,
      timeoutMinutes: 15,
      passwordVerifier: null,
    },
  );
});

test("applyAppLockEnabledChange requires current password before disabling an existing lock", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");
  const enabled: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 30,
    passwordVerifier: verifier,
  };

  assert.deepEqual(
    await applyAppLockEnabledChange(enabled, false, "wrong"),
    { ok: false, error: "incorrect" },
  );

  assert.deepEqual(
    await applyAppLockEnabledChange(enabled, false, "secret"),
    {
      enabled: false,
      timeoutMinutes: 30,
      passwordVerifier: verifier,
    },
  );
});

test("replaceAppLockPassword requires current password when replacing an existing verifier", async () => {
  const verifier = await createAppLockPasswordVerifier("old secret");
  const enabled: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 5,
    passwordVerifier: verifier,
  };

  assert.deepEqual(
    await replaceAppLockPassword(enabled, {
      currentPassword: "",
      nextPassword: "new secret",
    }),
    { ok: false, error: "empty-current" },
  );
  assert.deepEqual(
    await replaceAppLockPassword(enabled, {
      currentPassword: "wrong",
      nextPassword: "new secret",
    }),
    { ok: false, error: "incorrect" },
  );

  const replaced = await replaceAppLockPassword(enabled, {
    currentPassword: "old secret",
    nextPassword: "new secret",
  });

  assert.equal("ok" in replaced, false);
  assert.equal(replaced.enabled, true);
  assert.equal(replaced.timeoutMinutes, 5);
  assert.notEqual(replaced.passwordVerifier?.hash, verifier.hash);
  assert.equal(await verifyAppLockPassword("new secret", replaced.passwordVerifier), true);
});

test("replaceAppLockPassword rejects empty new passwords", async () => {
  const settings: AppLockSettings = {
    enabled: false,
    timeoutMinutes: 60,
    passwordVerifier: null,
  };

  assert.deepEqual(
    await replaceAppLockPassword(settings, {
      nextPassword: " ",
    }),
    { ok: false, error: "empty-next" },
  );
});
