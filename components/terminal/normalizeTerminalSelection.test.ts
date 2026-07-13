import assert from "node:assert/strict";
import test from "node:test";
import {
  getNormalizedTerminalSelection,
  trimWrittenPadding,
  type SelectionBufferLine,
  type SelectionTerminal,
} from "./normalizeTerminalSelection.ts";

/**
 * Fake line that matches real xterm translateToString(true) semantics:
 * trimRight only drops *empty* cells (trailing chars that were never written),
 * not written ASCII spaces used as display padding.
 */
function makeLine(
  text: string,
  options: { isWrapped?: boolean; emptyCells?: number } = {},
): SelectionBufferLine {
  const emptyCells = options.emptyCells ?? 0;
  const full = text + "\0".repeat(emptyCells);
  return {
    isWrapped: options.isWrapped ?? false,
    length: full.length,
    translateToString(trimRight = false, startColumn = 0, endColumn = full.length) {
      let end = Math.max(startColumn, Math.min(endColumn, full.length));
      if (trimRight) {
        // Drop only empty (never-written) cells on the right — not spaces.
        while (end > startColumn && full[end - 1] === "\0") {
          end -= 1;
        }
      }
      const start = Math.max(0, startColumn);
      return full.slice(start, end).replace(/\0/g, " ");
    },
  };
}

function makeTerm(
  lines: Array<{ text: string; isWrapped?: boolean; emptyCells?: number }>,
  range: { start: { x: number; y: number }; end: { x: number; y: number } } | null,
  options: {
    rawSelection?: string;
    columnSelect?: boolean;
  } = {},
): SelectionTerminal {
  const bufferLines = lines.map((line) =>
    makeLine(line.text, { isWrapped: line.isWrapped, emptyCells: line.emptyCells }),
  );
  return {
    getSelection: () => options.rawSelection ?? "",
    getSelectionPosition: () => range,
    buffer: {
      active: {
        getLine: (y) => bufferLines[y],
      },
    },
    _core: options.columnSelect
      ? { _selectionService: { _activeSelectionMode: 3 } }
      : { _selectionService: { _activeSelectionMode: 0 } },
  };
}

test("trimWrittenPadding removes written trailing spaces but keeps internal ones", () => {
  assert.equal(trimWrittenPadding("hello   "), "hello");
  assert.equal(trimWrittenPadding("  hello  world  "), "  hello  world");
  assert.equal(trimWrittenPadding("tabs\t\t"), "tabs");
});

test("joins soft-wrapped rows and strips written display padding", () => {
  // TUI pads each physical row with real spaces; empty cells alone are not the problem.
  const term = makeTerm(
    [
      { text: "Pi: use /copy is the most   " },
      { text: "reliable option             ", isWrapped: true },
      { text: "next hard line              " },
    ],
    { start: { x: 0, y: 0 }, end: { x: 28, y: 2 } },
  );

  assert.equal(
    getNormalizedTerminalSelection(term),
    "Pi: use /copy is the mostreliable option\nnext hard line",
  );
});

test("preserves hard line breaks between non-wrapped rows while trimming padding", () => {
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

test("empty-cell trim from xterm still applies before written-space trim", () => {
  const term = makeTerm(
    [{ text: "hello", emptyCells: 10 }],
    { start: { x: 0, y: 0 }, end: { x: 15, y: 0 } },
  );
  assert.equal(getNormalizedTerminalSelection(term), "hello");
});

test("respects partial column selection on first and last rows", () => {
  const term = makeTerm(
    [
      { text: "xxhello worldyy" },
      { text: "continued here!", isWrapped: true },
    ],
    { start: { x: 2, y: 0 }, end: { x: 10, y: 1 } },
  );

  // first row from col 2 to line end (multi-row): "hello worldyy"
  // last row cols 0..10: "continued " → written-space trim → "continued"
  assert.equal(getNormalizedTerminalSelection(term), "hello worldyycontinued");
});

test("falls back to getSelection when position is unavailable", () => {
  const term = makeTerm([{ text: "abc" }], null, { rawSelection: "fallback text" });
  assert.equal(getNormalizedTerminalSelection(term), "fallback text");
});

test("returns empty string for empty range and normalizes inverted ranges", () => {
  const empty = makeTerm([{ text: "abc" }], { start: { x: 1, y: 0 }, end: { x: 1, y: 0 } });
  assert.equal(getNormalizedTerminalSelection(empty), "");

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

test("preserves rectangular column selection column bounds on every row", () => {
  const term = makeTerm(
    [
      { text: "abcdefghij" },
      { text: "0123456789" },
      { text: "ABCDEFGHIJ" },
    ],
    { start: { x: 2, y: 0 }, end: { x: 5, y: 2 } },
    { columnSelect: true },
  );

  // Columns 2..5 on each row (not linear first-row-to-end / last-row-from-start).
  assert.equal(getNormalizedTerminalSelection(term), "cde\n234\nCDE");
});

test("converts non-breaking spaces to regular spaces", () => {
  const term = makeTerm(
    [{ text: "hello\u00a0world  " }],
    { start: { x: 0, y: 0 }, end: { x: 13, y: 0 } },
  );
  assert.equal(getNormalizedTerminalSelection(term), "hello world");
});
