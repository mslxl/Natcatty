import assert from "node:assert/strict";
import test from "node:test";

import {
  CancellationError,
  CancellationTokenSource,
  definePlugin,
  DisposableStore,
  PluginError,
  throwIfCancellationRequested,
} from "./index.ts";

test("definePlugin preserves the exact plugin object", () => {
  const plugin = definePlugin({ activate() {} });
  assert.equal(typeof plugin.activate, "function");
});

test("DisposableStore disposes every item once", () => {
  const store = new DisposableStore();
  const calls: string[] = [];
  store.add({ dispose: () => calls.push("first") });
  store.add({ dispose: () => calls.push("second") });

  store.dispose();
  store.dispose();

  assert.deepEqual(calls, ["first", "second"]);
});

test("DisposableStore disposes rejected late additions", () => {
  const store = new DisposableStore();
  store.dispose();
  let disposed = false;

  assert.throws(
    () => store.add({ dispose: () => { disposed = true; } }),
    (error) => error instanceof PluginError && error.code === "unavailable",
  );
  assert.equal(disposed, true);
});

test("CancellationTokenSource notifies listeners once", () => {
  const source = new CancellationTokenSource();
  let count = 0;
  source.token.onCancellationRequested(() => count += 1);

  source.cancel();
  source.cancel();

  assert.equal(count, 1);
  assert.equal(source.token.isCancellationRequested, true);
  assert.throws(
    () => throwIfCancellationRequested(source.token),
    CancellationError,
  );
});
