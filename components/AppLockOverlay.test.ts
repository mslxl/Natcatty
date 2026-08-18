import assert from "node:assert/strict";
import test from "node:test";

import {
  getAppLockErrorMessageKey,
} from "./AppLockOverlay.tsx";

test("getAppLockErrorMessageKey maps unlock errors to localized message keys", () => {
  assert.equal(getAppLockErrorMessageKey("empty"), "appLock.error.emptyPassword");
  assert.equal(getAppLockErrorMessageKey("incorrect"), "appLock.error.incorrectPassword");
  assert.equal(getAppLockErrorMessageKey(null), null);
});
