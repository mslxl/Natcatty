import assert from "node:assert/strict";
import test from "node:test";

import {
  scheduleChromeLayoutAnimation,
} from "./useActiveChromeTheme.ts";

function createRafRoot() {
  const callbacks = new Map<number, FrameRequestCallback>();
  let nextId = 1;
  const view = {
    requestAnimationFrame: (callback: FrameRequestCallback) => {
      const id = nextId++;
      callbacks.set(id, callback);
      return id;
    },
    cancelAnimationFrame: (id: number) => {
      callbacks.delete(id);
    },
  };
  const root = {
    ownerDocument: { defaultView: view },
  } as unknown as HTMLElement;

  const flushFrame = () => {
    const [id, callback] = callbacks.entries().next().value ?? [];
    if (!id || !callback) return false;
    callbacks.delete(id);
    callback(0);
    return true;
  };

  return { root, flushFrame };
}

test("chrome layout animations wait until theme settle frames complete", () => {
  const { root, flushFrame } = createRafRoot();
  let ran = false;

  const cancel = scheduleChromeLayoutAnimation(() => {
    ran = true;
  }, root);

  while (!ran && flushFrame()) {
    // Drain scheduled animation frames.
  }
  assert.equal(ran, true);
  cancel();
});
