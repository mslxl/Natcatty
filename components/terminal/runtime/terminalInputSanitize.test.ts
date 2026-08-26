import { test } from "node:test";
import assert from "node:assert/strict";

import { sanitizeTerminalInput } from "./terminalInputSanitize";

test("sanitizeTerminalInput strips zero-width space (U+200B)", () => {
  assert.equal(sanitizeTerminalInput("ls\u200b"), "ls");
  assert.equal(sanitizeTerminalInput("\u200bls"), "ls");
  assert.equal(sanitizeTerminalInput("l\u200bs"), "ls");
});

test("sanitizeTerminalInput strips BOM / ZWNBSP (U+FEFF)", () => {
  assert.equal(sanitizeTerminalInput("\ufeffls"), "ls");
  assert.equal(sanitizeTerminalInput("ls\ufeff"), "ls");
});

test("sanitizeTerminalInput strips soft hyphen (U+00AD)", () => {
  assert.equal(sanitizeTerminalInput("ls\u00ad"), "ls");
});

test("sanitizeTerminalInput strips ZWNJ (U+200C) and ZWJ (U+200D)", () => {
  assert.equal(sanitizeTerminalInput("a\u200cb"), "ab");
  assert.equal(sanitizeTerminalInput("a\u200db"), "ab");
});

test("sanitizeTerminalInput strips directional marks (U+200E, U+200F)", () => {
  assert.equal(sanitizeTerminalInput("\u200els\u200f"), "ls");
});

test("sanitizeTerminalInput strips word joiner and invisible operators (U+2060-2064)", () => {
  assert.equal(sanitizeTerminalInput("ls\u2060"), "ls");
  assert.equal(sanitizeTerminalInput("ls\u2061"), "ls");
  assert.equal(sanitizeTerminalInput("ls\u2062"), "ls");
  assert.equal(sanitizeTerminalInput("ls\u2063"), "ls");
  assert.equal(sanitizeTerminalInput("ls\u2064"), "ls");
});

test("sanitizeTerminalInput returns empty string for zero-width-only input", () => {
  assert.equal(sanitizeTerminalInput("\u200b"), "");
  assert.equal(sanitizeTerminalInput("\u200b\ufeff\u200c\u200d"), "");
});

test("sanitizeTerminalInput preserves regular ASCII and control characters", () => {
  assert.equal(sanitizeTerminalInput("ls\r"), "ls\r");
  assert.equal(sanitizeTerminalInput("\r"), "\r");
  assert.equal(sanitizeTerminalInput("\n"), "\n");
  assert.equal(sanitizeTerminalInput("\u0003"), "\u0003"); // Ctrl+C
  assert.equal(sanitizeTerminalInput("\u007f"), "\u007f"); // DEL
});

test("sanitizeTerminalInput preserves CJK and emoji characters", () => {
  assert.equal(sanitizeTerminalInput("你好"), "你好");
  assert.equal(sanitizeTerminalInput("😀"), "😀");
  // Full-width punctuation (common CJK IME output) is preserved
  assert.equal(sanitizeTerminalInput("，。！？"), "，。！");
});

test("sanitizeTerminalInput preserves Kitty escape sequences", () => {
  const kittySeq = "\u001b[0;;97:97u";
  assert.equal(sanitizeTerminalInput(kittySeq), kittySeq);
});

test("sanitizeTerminalInput handles empty and falsy input", () => {
  assert.equal(sanitizeTerminalInput(""), "");
});

test("sanitizeTerminalInput strips multiple interspersed zero-width characters", () => {
  assert.equal(sanitizeTerminalInput("\u200bl\u200bs\u200b \u200b-l\u200ba\u200b"), "ls -la");
});

test("sanitizeTerminalInput is stable across repeated calls", () => {
  const input = "ls\u200b\r";
  const first = sanitizeTerminalInput(input);
  const second = sanitizeTerminalInput(first);
  assert.equal(first, second);
  assert.equal(first, "ls\r");
});
