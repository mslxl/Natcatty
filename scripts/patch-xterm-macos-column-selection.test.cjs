"use strict";

const assert = require("node:assert/strict");
const path = require("node:path");
const test = require("node:test");

const {
  PATCHES,
  invalidateViteCache,
  patchXtermSource,
  replacementForPatch,
  sameLengthExpression,
} = require("./patch-xterm-macos-column-selection.cjs");

test("patches both distributed bundles without shifting source-map offsets", () => {
  for (const patch of PATCHES.filter((entry) => entry.preserveLength)) {
    const input = `before:${patch.original}:after`;
    const result = patchXtermSource(input, patch);
    assert.equal(result.changed, true);
    assert.equal(result.source.length, input.length);
    assert.equal(result.source.includes(patch.original), false);
    assert.equal(
      result.source.includes(sameLengthExpression(patch.replacement, patch.original.length)),
      true,
    );
  }
});

test("patches readable xterm sources and source-map content", () => {
  for (const patch of PATCHES.filter((entry) => !entry.preserveLength)) {
    const input = `before:${patch.original}:after`;
    const result = patchXtermSource(input, patch);
    assert.equal(result.changed, true);
    assert.equal(result.source, `before:${replacementForPatch(patch)}:after`);
    assert.equal(result.source.length, input.length);
  }
});

test("is idempotent and fails closed on an unknown package shape", () => {
  for (const patch of PATCHES) {
    const replacement = replacementForPatch(patch);
    assert.deepEqual(patchXtermSource(`before:${replacement}:after`, patch), {
      source: `before:${replacement}:after`,
      changed: false,
    });
    assert.throws(
      () => patchXtermSource("unrecognized source", patch),
      /expected exactly one original or patched selection predicate/,
    );
  }
});

test("fails closed when a generated-bundle replacement would shift offsets", () => {
  assert.throws(
    () => sameLengthExpression("replacement is longer", 4),
    /replacement expression is too long/,
  );
});

test("invalidates Vite's optimized dependency cache after patching xterm", () => {
  const calls = [];
  const cachePath = invalidateViteCache("/repo", {
    rmSync(target, options) {
      calls.push({ target, options });
    },
  });

  assert.equal(cachePath, path.resolve("/repo/node_modules/.vite"));
  assert.deepEqual(calls, [{
    target: cachePath,
    options: { recursive: true, force: true },
  }]);
});
