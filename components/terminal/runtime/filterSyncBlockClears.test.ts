import assert from "node:assert/strict";
import test from "node:test";

import type { Terminal as XTerm } from "@xterm/xterm";

import {
  createSyncBlockFilterState,
  filterSyncBlockClears,
  isTerminalViewportScrolledUp,
} from "./filterSyncBlockClears.ts";

const SYNC_START = "\x1b[?2026h";
const SYNC_END = "\x1b[?2026l";
const CLEAR = "\x1b[2J";
const ALT_SCREEN_ENTER = "\x1b[?1049h";

const createMockTerm = ({
  type = "normal",
  viewportY = 10,
  baseY = 76,
  length = 100,
  rows = 24,
}: {
  type?: "normal" | "alternate";
  viewportY?: number;
  baseY?: number;
  length?: number;
  rows?: number;
} = {}): XTerm => ({
  rows,
  buffer: {
    active: {
      type,
      baseY,
      length,
      viewportY,
    },
  },
} as unknown as XTerm);

const scrolledUpTerm = createMockTerm();
const bottomTerm = createMockTerm({ viewportY: 76, baseY: 76 });

test("passes through data with no synchronized-output sequences", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const input = "hello\r\n\x1b[2Jworld\r\n";

  assert.equal(filterSyncBlockClears(input, state, scrolledUpTerm), input);
  assert.equal(state.inSyncBlock, false);
});

test("strips clear-screen inside a synchronized-output block", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const input = `${SYNC_START}${CLEAR}frame${SYNC_END}`;

  assert.equal(filterSyncBlockClears(input, state, scrolledUpTerm), `${SYNC_START}frame${SYNC_END}`);
  assert.equal(state.inSyncBlock, false);
});

test("does not strip clear-screen outside synchronized-output blocks", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);

  assert.equal(filterSyncBlockClears(CLEAR, state, scrolledUpTerm), CLEAR);
  assert.equal(state.inSyncBlock, false);
});

test("tracks synchronized-output state across chunks", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);

  assert.equal(filterSyncBlockClears(SYNC_START, state, scrolledUpTerm), SYNC_START);
  assert.equal(state.inSyncBlock, true);

  assert.equal(filterSyncBlockClears(`${CLEAR}partial`, state, scrolledUpTerm), "partial");
  assert.equal(state.inSyncBlock, true);

  assert.equal(
    filterSyncBlockClears(`${CLEAR}done${SYNC_END}`, state, scrolledUpTerm),
    `done${SYNC_END}`,
  );
  assert.equal(state.inSyncBlock, false);
});

test("leaves non-clear redraw sequences inside synchronized-output blocks intact", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const cursorHome = "\x1b[H";
  const input = `${SYNC_START}${cursorHome}${CLEAR}text${SYNC_END}`;

  assert.equal(
    filterSyncBlockClears(input, state, scrolledUpTerm),
    `${SYNC_START}${cursorHome}text${SYNC_END}`,
  );
});

test("handles sync start marker split across chunks", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const startPrefix = SYNC_START.slice(0, -1);
  const startSuffix = SYNC_START.slice(-1);

  assert.equal(filterSyncBlockClears(startPrefix, state, scrolledUpTerm), "");
  assert.equal(state.pending, startPrefix);
  assert.equal(state.inSyncBlock, false);

  assert.equal(
    filterSyncBlockClears(`${startSuffix}${CLEAR}frame${SYNC_END}`, state, scrolledUpTerm),
    `${SYNC_START}frame${SYNC_END}`,
  );
  assert.equal(state.inSyncBlock, false);
  assert.equal(state.pending, "");
});

test("handles clear-screen marker split across chunks inside sync block", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const clearPrefix = CLEAR.slice(0, -1);
  const clearSuffix = CLEAR.slice(-1);

  assert.equal(filterSyncBlockClears(SYNC_START, state, scrolledUpTerm), SYNC_START);
  assert.equal(state.inSyncBlock, true);

  assert.equal(filterSyncBlockClears(`${clearPrefix}`, state, scrolledUpTerm), "");
  assert.equal(state.pending, clearPrefix);

  assert.equal(
    filterSyncBlockClears(`${clearSuffix}frame${SYNC_END}`, state, scrolledUpTerm),
    `frame${SYNC_END}`,
  );
  assert.equal(state.inSyncBlock, false);
  assert.equal(state.pending, "");
});

test("handles sync end marker split across chunks", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const endPrefix = SYNC_END.slice(0, -1);
  const endSuffix = SYNC_END.slice(-1);

  assert.equal(
    filterSyncBlockClears(`${SYNC_START}frame${endPrefix}`, state, scrolledUpTerm),
    `${SYNC_START}frame`,
  );
  assert.equal(state.inSyncBlock, true);
  assert.equal(state.pending, endPrefix);

  assert.equal(filterSyncBlockClears(endSuffix, state, scrolledUpTerm), SYNC_END);
  assert.equal(state.inSyncBlock, false);
  assert.equal(state.pending, "");
});

test("releases a trailing ESC when the next chunk is ordinary text", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);

  assert.equal(filterSyncBlockClears("prompt\x1b", state, scrolledUpTerm), "prompt");
  assert.equal(state.pending, "\x1b");

  assert.equal(filterSyncBlockClears("more output", state, scrolledUpTerm), "\x1bmore output");
  assert.equal(state.pending, "");
});

test("preserves clear-screen when alternate-screen entry precedes sync redraw", () => {
  const state = createSyncBlockFilterState(scrolledUpTerm);
  const input = `${ALT_SCREEN_ENTER}${SYNC_START}${CLEAR}frame${SYNC_END}`;

  assert.equal(filterSyncBlockClears(input, state, scrolledUpTerm), input);
  assert.equal(state.inAlternateScreen, true);
});

test("preserves clear-screen inside sync blocks when viewport is at bottom", () => {
  const state = createSyncBlockFilterState(bottomTerm);
  const input = `${SYNC_START}${CLEAR}frame${SYNC_END}`;

  assert.equal(filterSyncBlockClears(input, state, bottomTerm), input);
});

test("isTerminalViewportScrolledUp is false when buffer state is unavailable", () => {
  assert.equal(isTerminalViewportScrolledUp({} as never), false);
});

test("isTerminalViewportScrolledUp is false on alternate-screen buffers", () => {
  assert.equal(isTerminalViewportScrolledUp(createMockTerm({ type: "alternate", viewportY: 0, baseY: 0, length: 24 })), false);
});

test("isTerminalViewportScrolledUp detects normal-buffer scrollback", () => {
  assert.equal(isTerminalViewportScrolledUp(bottomTerm), false);
  assert.equal(isTerminalViewportScrolledUp(scrolledUpTerm), true);
});
