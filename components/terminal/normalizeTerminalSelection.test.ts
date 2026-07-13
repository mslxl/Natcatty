import assert from "node:assert/strict";
import test from "node:test";
import {
  getNormalizedTerminalSelection,
  joinSoftWrappedRows,
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
});

test("joinSoftWrappedRows keeps a single trailing space as a word separator", () => {
  assert.equal(joinSoftWrappedRows("hello ", "world"), "hello world");
});

test("joinSoftWrappedRows concatenates mid-word wraps tightly", () => {
  assert.equal(joinSoftWrappedRows("hel", "lo world"), "hello world");
});

test("joinSoftWrappedRows does not invent spaces inside URL/path tokens", () => {
  assert.equal(
    joinSoftWrappedRows("https://example.com/very/long/   ", "path"),
    "https://example.com/very/long/path",
  );
});

test("joinSoftWrappedRows does not invent spaces before CJK punctuation", () => {
  assert.equal(joinSoftWrappedRows("你好   ", "，世界"), "你好，世界");
});

test("joinSoftWrappedRows collapses prose padding to one space", () => {
  assert.equal(joinSoftWrappedRows("shifts   ", "across"), "shifts across");
  assert.equal(joinSoftWrappedRows("most   ", "reliable"), "most reliable");
});

test("joinSoftWrappedRows does not invent spaces between CJK characters", () => {
  assert.equal(joinSoftWrappedRows("最   ", "稳"), "最稳");
});

test("joins soft-wrapped rows, strips padding, keeps word separators", () => {
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
    "Pi: use /copy is the most reliable option\nnext hard line",
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

test("preserves explicitly selected trailing spaces on a partial last row", () => {
  // "abc  def" — select columns 0..5 → "abc  " must keep the spaces.
  const term = makeTerm(
    [{ text: "abc  def" }],
    { start: { x: 0, y: 0 }, end: { x: 5, y: 0 } },
  );
  assert.equal(getNormalizedTerminalSelection(term), "abc  ");
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

  // Last-row end.x=10 selects "continued " (including the space); keep it.
  assert.equal(getNormalizedTerminalSelection(term), "hello worldyycontinued ");
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

test("preserves rectangular column selection including right-edge spaces", () => {
  const term = makeTerm(
    [
      { text: "ab  efghij" },
      { text: "01  56789x" },
      { text: "AB  EFGHIJ" },
    ],
    { start: { x: 2, y: 0 }, end: { x: 5, y: 2 } },
    { columnSelect: true },
  );

  // Columns 2..5 include the intentional spaces.
  assert.equal(getNormalizedTerminalSelection(term), "  e\n  5\n  E");
});

test("converts non-breaking spaces to regular spaces", () => {
  const term = makeTerm(
    [{ text: "hello\u00a0world  " }],
    { start: { x: 0, y: 0 }, end: { x: 13, y: 0 } },
  );
  assert.equal(getNormalizedTerminalSelection(term), "hello world");
});

test("soft-wrapped CJK with padding joins without inserted spaces", () => {
  const term = makeTerm(
    [
      { text: "Pi: 用 /copy 最   " },
      { text: "稳                ", isWrapped: true },
    ],
    { start: { x: 0, y: 0 }, end: { x: 18, y: 1 } },
  );
  assert.equal(getNormalizedTerminalSelection(term), "Pi: 用 /copy 最稳");
});
