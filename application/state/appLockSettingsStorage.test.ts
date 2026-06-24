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
    systemUnlockEnabled: false,
    passwordVerifier: verifier,
  };

  assert.deepEqual(
    await applyAppLockEnabledChange(disabled, true),
    {
      enabled: true,
      timeoutMinutes: 15,
      systemUnlockEnabled: false,
      passwordVerifier: verifier,
    },
  );

  assert.deepEqual(
    await applyAppLockEnabledChange({ ...disabled, passwordVerifier: null }, true),
    {
      enabled: false,
      timeoutMinutes: 15,
      systemUnlockEnabled: false,
      passwordVerifier: null,
    },
  );
});

test("applyAppLockEnabledChange requires current password before disabling an existing lock", async () => {
  const verifier = await createAppLockPasswordVerifier("secret");
  const enabled: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 30,
    systemUnlockEnabled: true,
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
      systemUnlockEnabled: false,
      passwordVerifier: null,
    },
  );
});

test("replaceAppLockPassword requires current password when replacing an existing verifier", async () => {
  const verifier = await createAppLockPasswordVerifier("old secret");
  const enabled: AppLockSettings = {
    enabled: true,
    timeoutMinutes: 5,
    systemUnlockEnabled: true,
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
  assert.equal(replaced.systemUnlockEnabled, true);
  assert.notEqual(replaced.passwordVerifier?.hash, verifier.hash);
  assert.equal(await verifyAppLockPassword("new secret", replaced.passwordVerifier), true);
});

test("replaceAppLockPassword rejects empty new passwords", async () => {
  const settings: AppLockSettings = {
    enabled: false,
    timeoutMinutes: 60,
    systemUnlockEnabled: false,
    passwordVerifier: null,
  };

  assert.deepEqual(
    await replaceAppLockPassword(settings, {
      nextPassword: " ",
    }),
    { ok: false, error: "empty-next" },
  );
});

test("replaceAppLockPassword enables app lock when creating the first password", async () => {
  const settings: AppLockSettings = {
    enabled: false,
    timeoutMinutes: 15,
    systemUnlockEnabled: false,
    passwordVerifier: null,
  };

  const saved = await replaceAppLockPassword(settings, {
    nextPassword: "first secret",
  });

  assert.equal("ok" in saved, false);
  assert.equal(saved.enabled, true);
  assert.equal(saved.timeoutMinutes, 15);
  assert.equal(saved.systemUnlockEnabled, false);
  assert.equal(await verifyAppLockPassword("first secret", saved.passwordVerifier), true);
});
