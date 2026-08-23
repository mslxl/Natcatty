"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  PATCHES,
  patchXtermSource,
  sameLengthTrueExpression,
} = require("./patch-xterm-macos-column-selection.cjs");

test("patches both distributed bundles without shifting source-map offsets", () => {
  for (const patch of PATCHES.filter((entry) => entry.preserveLength)) {
    const input = `before:${patch.original}:after`;
    const result = patchXtermSource(input, patch);
    assert.equal(result.changed, true);
    assert.equal(result.source.length, input.length);
    assert.equal(result.source.includes(patch.original), false);
    assert.equal(
      result.source.includes(sameLengthTrueExpression(patch.original.length)),
      true,
    );
  }
});

test("patches readable xterm sources and source-map content", () => {
  for (const patch of PATCHES.filter((entry) => !entry.preserveLength)) {
    const result = patchXtermSource(`before:${patch.original}:after`, patch);
    assert.equal(result.changed, true);
    assert.equal(result.source, `before:${patch.replacement}:after`);
  }
});

test("is idempotent and fails closed on an unknown package shape", () => {
  for (const patch of PATCHES) {
    const replacement = patch.preserveLength
      ? sameLengthTrueExpression(patch.original.length)
      : patch.replacement;
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
