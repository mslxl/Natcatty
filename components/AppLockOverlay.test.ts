import assert from "node:assert/strict";
import test from "node:test";

import {
  getAppLockErrorMessageKey,
  getAppLockReasonMessageKey,
} from "./AppLockOverlay.tsx";

test("getAppLockReasonMessageKey maps lock reasons to localized message keys", () => {
  assert.equal(getAppLockReasonMessageKey("startup"), "appLock.reason.startup");
  assert.equal(getAppLockReasonMessageKey("idle"), "appLock.reason.idle");
  assert.equal(getAppLockReasonMessageKey("manual"), "appLock.reason.manual");
  assert.equal(getAppLockReasonMessageKey(null), "appLock.reason.default");
});

test("getAppLockErrorMessageKey maps unlock errors to localized message keys", () => {
  assert.equal(getAppLockErrorMessageKey("empty"), "appLock.error.emptyPassword");
  assert.equal(getAppLockErrorMessageKey("incorrect"), "appLock.error.incorrectPassword");
  assert.equal(getAppLockErrorMessageKey(null), null);
});
