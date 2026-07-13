import assert from "node:assert/strict";
import test from "node:test";
import {
  getNormalizedTerminalSelection,
  type SelectionBufferLine,
  type SelectionTerminal,
} from "./normalizeTerminalSelection.ts";

function makeLine(text: string, isWrapped = false): SelectionBufferLine {
  return {
    isWrapped,
    length: text.length,
    translateToString(trimRight = false, startColumn = 0, endColumn = text.length) {
      const start = Math.max(0, startColumn);
      const end = Math.max(start, Math.min(endColumn, text.length));
      let slice = text.slice(start, end);
      if (trimRight) {
        slice = slice.replace(/\s+$/u, "");
      }
      return slice;
    },
  };
}

function makeTerm(
  lines: Array<{ text: string; isWrapped?: boolean }>,
  range: { start: { x: number; y: number }; end: { x: number; y: number } } | null,
  rawSelection = "",
): SelectionTerminal {
  const bufferLines = lines.map((line) => makeLine(line.text, line.isWrapped ?? false));
  return {
    getSelection: () => rawSelection,
    getSelectionPosition: () => range,
    buffer: {
      active: {
        getLine: (y) => bufferLines[y],
      },
    },
  };
}

test("joins soft-wrapped rows and trims display padding", () => {
  // Simulates a soft-wrapped sentence padded to terminal width, then a hard line.
  const term = makeTerm(
    [
      { text: "Pi: use /copy is the most" + "   " },
      { text: "reliable option          " + "   ", isWrapped: true },
      { text: "next hard line           " + "   " },
    ],
    { start: { x: 0, y: 0 }, end: { x: 28, y: 2 } },
    "raw fallback",
  );

  assert.equal(
    getNormalizedTerminalSelection(term),
    "Pi: use /copy is the mostreliable option\nnext hard line",
  );
});

test("preserves hard line breaks between non-wrapped rows", () => {
  const term = makeTerm(
    [
      { text: "line one   " },
      { text: "line two   " },
      { text: "line three " },
    ],
    { start: { x: 0, y: 0 }, end: { x: 11, y: 2 } },
  );

  assert.equal(getNormalizedTerminalSelection(term), "line one\nline two\nline three");
});

test("respects partial column selection on first and last rows", () => {
  const term = makeTerm(
    [
      { text: "xxhello worldyy" },
      { text: "continued here!", isWrapped: true },
    ],
    { start: { x: 2, y: 0 }, end: { x: 10, y: 1 } },
  );

  // first row from col 2: "hello worldyy"; last row cols 0..10: "continued "
  // with trimRight → "continued"
  assert.equal(getNormalizedTerminalSelection(term), "hello worldyycontinued");
});

test("falls back to getSelection when position is unavailable", () => {
  const term = makeTerm([{ text: "abc" }], null, "fallback text");
  assert.equal(getNormalizedTerminalSelection(term), "fallback text");
});

test("returns empty string for empty range and normalizes inverted ranges", () => {
  const empty = makeTerm([{ text: "abc" }], { start: { x: 1, y: 0 }, end: { x: 1, y: 0 } });
  assert.equal(getNormalizedTerminalSelection(empty), "");

  // Inverted (bottom-right → top-left) is rewritten to buffer order.
  const inverted = makeTerm(
    [
      { text: "alpha " },
      { text: "beta  " },
    ],
    { start: { x: 6, y: 1 }, end: { x: 0, y: 0 } },
  );
  assert.equal(getNormalizedTerminalSelection(inverted), "alpha\nbeta");
});

test("handles multi-row soft wrap chains", () => {
  const term = makeTerm(
    [
      { text: "aaa" },
      { text: "bbb", isWrapped: true },
      { text: "ccc", isWrapped: true },
      { text: "ddd" },
    ],
    { start: { x: 0, y: 0 }, end: { x: 3, y: 3 } },
  );

  assert.equal(getNormalizedTerminalSelection(term), "aaabbbccc\nddd");
});
