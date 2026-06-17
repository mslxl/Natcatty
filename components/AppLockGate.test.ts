import assert from "node:assert/strict";
import test from "node:test";

import { shouldRenderAppLockGateChildren } from "./AppLockGate.tsx";

test("shouldRenderAppLockGateChildren withholds startup-locked route children until unlock", () => {
  assert.equal(
    shouldRenderAppLockGateChildren({
      locked: true,
      lockReason: "startup",
      hasRenderedChildren: false,
    }),
    false,
  );
  assert.equal(
    shouldRenderAppLockGateChildren({
      locked: false,
      lockReason: null,
      hasRenderedChildren: false,
    }),
    true,
  );
});

test("shouldRenderAppLockGateChildren keeps mounted children for manual and idle locks", () => {
  assert.equal(
    shouldRenderAppLockGateChildren({
      locked: true,
      lockReason: "manual",
      hasRenderedChildren: true,
    }),
    true,
  );
  assert.equal(
    shouldRenderAppLockGateChildren({
      locked: true,
      lockReason: "idle",
      hasRenderedChildren: true,
    }),
    true,
  );
});

test("shouldRenderAppLockGateChildren keeps existing children mounted for reopen locks", () => {
  assert.equal(
    shouldRenderAppLockGateChildren({
      locked: true,
      lockReason: "startup",
      hasRenderedChildren: true,
    }),
    true,
  );
});
