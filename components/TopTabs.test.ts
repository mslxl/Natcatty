import assert from "node:assert/strict";
import test from "node:test";

const storage = new Map<string, string>();
Object.defineProperty(globalThis, "localStorage", {
  configurable: true,
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  },
});

const { computeHostTreeTabGutter } = await import("./TopTabs.tsx");

test("host tree tab gutter fills the remaining sidebar width", () => {
  assert.equal(computeHostTreeTabGutter(280, 120), 160);
});

test("host tree tab gutter never goes negative", () => {
  assert.equal(computeHostTreeTabGutter(120, 280), 0);
});
