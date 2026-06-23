import assert from "node:assert/strict";
import test from "node:test";

import {
  shouldNotifyAppLockGateRendererReady,
  shouldRenderAppLockGateChildren,
} from "./AppLockGate.tsx";

test("shouldRenderAppLockGateChildren withholds startup-locked route children until unlock", () => {
  assert.equal(
    shouldRenderAppLockGateChildren({
      initialized: true,
      locked: true,
      lockReason: "startup",
      hasRenderedChildren: false,
    }),
    false,
  );
  assert.equal(
    shouldRenderAppLockGateChildren({
      initialized: true,
      locked: false,
      lockReason: null,
      hasRenderedChildren: false,
    }),
    true,
  );
});

test("shouldRenderAppLockGateChildren withholds children before runtime initialization", () => {
  assert.equal(
    shouldRenderAppLockGateChildren({
      initialized: false,
      locked: false,
      lockReason: null,
      hasRenderedChildren: false,
    }),
    false,
  );
});

test("shouldRenderAppLockGateChildren keeps mounted children for manual and idle locks", () => {
  assert.equal(
    shouldRenderAppLockGateChildren({
      initialized: true,
      locked: true,
      lockReason: "manual",
      hasRenderedChildren: true,
    }),
    true,
  );
  assert.equal(
    shouldRenderAppLockGateChildren({
      initialized: true,
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
      initialized: true,
      locked: true,
      lockReason: "startup",
      hasRenderedChildren: true,
    }),
    true,
  );
});

test("shouldNotifyAppLockGateRendererReady waits until startup-locked children can mount", () => {
  assert.equal(
    shouldNotifyAppLockGateRendererReady({
      notifyRendererReady: true,
      renderChildren: false,
    }),
    false,
  );
  assert.equal(
    shouldNotifyAppLockGateRendererReady({
      notifyRendererReady: true,
      renderChildren: true,
    }),
    true,
  );
  assert.equal(
    shouldNotifyAppLockGateRendererReady({
      notifyRendererReady: false,
      renderChildren: true,
    }),
    false,
  );
});
