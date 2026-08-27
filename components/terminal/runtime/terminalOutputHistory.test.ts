import assert from "node:assert/strict";
import test from "node:test";

import {
  DEFAULT_OUTPUT_HISTORY_MAX_LINES,
  createTerminalOutputHistoryPreview,
  nextOutputHistoryPreviewTop,
  stripTerminalDisplayToPlainText,
  wrapOutputHistoryLineToRows,
} from "./terminalOutputHistory.ts";

test("display chunks reduce to plain transcript text", () => {
  assert.deepEqual(
    stripTerminalDisplayToPlainText("\x1b[32mhello\x1b[0m world\r\n"),
    { text: "hello world\r\n", pending: "" },
  );
  assert.equal(
    stripTerminalDisplayToPlainText("\x1b]0;title\x07tail").text,
    "tail",
  );
  assert.equal(
    stripTerminalDisplayToPlainText("\x1b]0;title\x1b\\tail").text,
    "tail",
  );
  assert.equal(stripTerminalDisplayToPlainText("a\x07b\x00c").text, "abc");
});

test("escape sequences split across chunks are not leaked into the transcript", () => {
  const first = stripTerminalDisplayToPlainText("ok\x1b[3");
  assert.equal(first.text, "ok");
  assert.equal(first.pending, "\x1b[3");

  const second = stripTerminalDisplayToPlainText("1mred", first.pending);
  assert.equal(second.text, "red");
  assert.equal(second.pending, "");
});

test("escape sequences with intermediate bytes are consumed through their final byte", () => {
  // ESC ( B designates G0 (ncurses / terminal reset emit it constantly).
  assert.equal(stripTerminalDisplayToPlainText("\x1b(Bplain").text, "plain");
  // ESC # 8 is DECALN.
  assert.equal(stripTerminalDisplayToPlainText("\x1b#8plain").text, "plain");
  // Two-byte escapes without intermediates keep working.
  assert.equal(stripTerminalDisplayToPlainText("\x1bMplain").text, "plain");

  const first = stripTerminalDisplayToPlainText("ok\x1b(");
  assert.equal(first.text, "ok");
  assert.equal(first.pending, "\x1b(");
  assert.equal(stripTerminalDisplayToPlainText("Btail", first.pending).text, "tail");
});

test("a cursor-addressed frame without newlines stays inside the character budget", () => {
  const history = createTerminalOutputHistoryPreview({ maxChars: 64 });
  for (let frame = 0; frame < 200; frame += 1) {
    history.append(`\x1b[Hframe ${frame} ${"x".repeat(80)}`);
  }

  const transcript = [...history.getLines()].join("");
  assert.ok(transcript.length <= 2 * 64, `unbounded open line: ${transcript.length}`);
  // The newest frame still lands in the retained tail.
  assert.ok(transcript.includes("frame 199"), transcript.slice(-200));
});

test("bare carriage returns overwrite the line they restart", () => {
  const history = createTerminalOutputHistoryPreview();
  history.append("downloading 10%\r");
  history.append("downloading 55%\r");
  history.append("downloading 100%\r\n");
  history.append("done\n");
  assert.deepEqual([...history.getLines()], ["downloading 100%", "done"]);
});

test("history keeps a bounded tail of lines", () => {
  const history = createTerminalOutputHistoryPreview({ maxLines: 3 });
  for (let index = 0; index < 6; index += 1) history.append(`line ${index}\n`);
  assert.deepEqual(
    [...history.getLines()],
    ["line 3", "line 4", "line 5"],
  );
});

test("preview rows wrap long lines and flag the continuation rows", () => {
  const history = createTerminalOutputHistoryPreview();
  history.append("ok\n");
  history.append("abcdefgh\n");

  const window = history.getPreviewRows({ cols: 4, rows: 3, top: 0 });
  assert.equal(window.totalRows, 3);
  assert.deepEqual(
    window.rows.map((row) => row.text),
    ["ok", "abcd", "efgh"],
  );
  assert.deepEqual(
    window.rows.map((row) => row.isWrapped),
    [false, false, true],
  );
});

test("preview windows clamp to the retained rows and pad short output", () => {
  const history = createTerminalOutputHistoryPreview();
  history.append("one\ntwo\n");

  assert.deepEqual(
    history.getPreviewRows({ cols: 10, rows: 2, top: 99 }).rows.map((row) => row.text),
    ["one", "two"],
  );
  assert.deepEqual(
    history.getPreviewRows({ cols: 10, rows: 4, top: 0 }).rows.map((row) => row.text),
    ["one", "two", "", ""],
  );
});

test("preview rows keep wide characters intact at a column boundary", () => {
  // Four columns hold two CJK cells per row; the glyphs are never split.
  assert.deepEqual(wrapOutputHistoryLineToRows("中文中文中文", 4), ["中文", "中文", "中文"]);
  const history = createTerminalOutputHistoryPreview();
  history.append("中文中文中文\n");
  assert.deepEqual(
    history.getPreviewRows({ cols: 4, rows: 3, top: 0 }).rows.map((row) => row.text),
    ["中文", "中文", "中文"],
  );
});

test("wheel steps walk the preview from the newest row upwards", () => {
  const history = createTerminalOutputHistoryPreview();
  for (let index = 0; index < 6; index += 1) history.append(`row ${index}\n`);

  const totalRows = history.getPreviewRows({ cols: 20, rows: 2, top: 0 }).totalRows;
  assert.equal(totalRows, 6);

  const bottom = nextOutputHistoryPreviewTop({
    currentTop: null,
    lines: 0,
    rows: 2,
    totalRows,
  });
  assert.equal(bottom, 4);
  assert.deepEqual(
    history.getPreviewRows({ cols: 20, rows: 2, top: bottom }).rows.map((row) => row.text),
    ["row 4", "row 5"],
  );

  const up = nextOutputHistoryPreviewTop({ currentTop: bottom, lines: -3, rows: 2, totalRows });
  assert.equal(up, 1);
  const top = nextOutputHistoryPreviewTop({ currentTop: up, lines: -30, rows: 2, totalRows });
  assert.equal(top, 0);
});

test("clear drops retained transcript and pending escapes", () => {
  const history = createTerminalOutputHistoryPreview();
  history.append(`keep\n`);
  history.clear();
  history.append("\x1b[3");
  assert.deepEqual([...history.getLines()], []);
  assert.equal(
    history.getPreviewRows({ cols: 10, rows: 2, top: 0 }).totalRows,
    0,
  );
});

test("default retention bounds the preview history", () => {
  assert.equal(DEFAULT_OUTPUT_HISTORY_MAX_LINES > 0, true);

  const history = createTerminalOutputHistoryPreview({ maxLines: 2, maxChars: 6 });
  history.append("aaaaaaaa\n");
  history.append("bbbbbbbb\n");
  history.append("cccccccc\n");
  // Lines longer than the character budget are clipped to it.
  assert.deepEqual([...history.getLines()], ["cccccc"]);
});