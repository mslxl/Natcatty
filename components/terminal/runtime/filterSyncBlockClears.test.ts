import assert from "node:assert/strict";
import test from "node:test";

import {
  createSyncBlockFilterState,
  filterSyncBlockClears,
  isTerminalViewportScrolledUp,
} from "./filterSyncBlockClears.ts";

const SYNC_START = "\x1b[?2026h";
const SYNC_END = "\x1b[?2026l";
const CLEAR = "\x1b[2J";

test("passes through data with no synchronized-output sequences", () => {
  const state = createSyncBlockFilterState();
  const input = "hello\r\n\x1b[2Jworld\r\n";

  assert.equal(filterSyncBlockClears(input, state), input);
  assert.equal(state.inSyncBlock, false);
});

test("strips clear-screen inside a synchronized-output block", () => {
  const state = createSyncBlockFilterState();
  const input = `${SYNC_START}${CLEAR}frame${SYNC_END}`;

  assert.equal(filterSyncBlockClears(input, state), `${SYNC_START}frame${SYNC_END}`);
  assert.equal(state.inSyncBlock, false);
});

test("does not strip clear-screen outside synchronized-output blocks", () => {
  const state = createSyncBlockFilterState();

  assert.equal(filterSyncBlockClears(CLEAR, state), CLEAR);
  assert.equal(state.inSyncBlock, false);
});

test("tracks synchronized-output state across chunks", () => {
  const state = createSyncBlockFilterState();

  assert.equal(filterSyncBlockClears(SYNC_START, state), SYNC_START);
  assert.equal(state.inSyncBlock, true);

  assert.equal(filterSyncBlockClears(`${CLEAR}partial`, state), "partial");
  assert.equal(state.inSyncBlock, true);

  assert.equal(filterSyncBlockClears(`${CLEAR}done${SYNC_END}`, state), `done${SYNC_END}`);
  assert.equal(state.inSyncBlock, false);
});

test("leaves non-clear redraw sequences inside synchronized-output blocks intact", () => {
  const state = createSyncBlockFilterState();
  const cursorHome = "\x1b[H";
  const input = `${SYNC_START}${cursorHome}${CLEAR}text${SYNC_END}`;

  assert.equal(
    filterSyncBlockClears(input, state),
    `${SYNC_START}${cursorHome}text${SYNC_END}`,
  );
});

test("handles sync start marker split across chunks", () => {
  const state = createSyncBlockFilterState();
  const startPrefix = SYNC_START.slice(0, -1);
  const startSuffix = SYNC_START.slice(-1);

  assert.equal(filterSyncBlockClears(startPrefix, state), "");
  assert.equal(state.pending, startPrefix);
  assert.equal(state.inSyncBlock, false);

  assert.equal(
    filterSyncBlockClears(`${startSuffix}${CLEAR}frame${SYNC_END}`, state),
    `${SYNC_START}frame${SYNC_END}`,
  );
  assert.equal(state.inSyncBlock, false);
  assert.equal(state.pending, "");
});

test("handles clear-screen marker split across chunks inside sync block", () => {
  const state = createSyncBlockFilterState();
  const clearPrefix = CLEAR.slice(0, -1);
  const clearSuffix = CLEAR.slice(-1);

  assert.equal(filterSyncBlockClears(SYNC_START, state), SYNC_START);
  assert.equal(state.inSyncBlock, true);

  assert.equal(filterSyncBlockClears(`${clearPrefix}`, state), "");
  assert.equal(state.pending, clearPrefix);

  assert.equal(filterSyncBlockClears(`${clearSuffix}frame${SYNC_END}`, state), `frame${SYNC_END}`);
  assert.equal(state.inSyncBlock, false);
  assert.equal(state.pending, "");
});

test("handles sync end marker split across chunks", () => {
  const state = createSyncBlockFilterState();
  const endPrefix = SYNC_END.slice(0, -1);
  const endSuffix = SYNC_END.slice(-1);

  assert.equal(filterSyncBlockClears(`${SYNC_START}frame${endPrefix}`, state), `${SYNC_START}frame`);
  assert.equal(state.inSyncBlock, true);
  assert.equal(state.pending, endPrefix);

  assert.equal(filterSyncBlockClears(endSuffix, state), SYNC_END);
  assert.equal(state.inSyncBlock, false);
  assert.equal(state.pending, "");
});

test("releases a trailing ESC when the next chunk is ordinary text", () => {
  const state = createSyncBlockFilterState();

  assert.equal(filterSyncBlockClears("prompt\x1b", state), "prompt");
  assert.equal(state.pending, "\x1b");

  assert.equal(filterSyncBlockClears("more output", state), "\x1bmore output");
  assert.equal(state.pending, "");
});

test("keeps clear-screen inside sync blocks when stripping is disabled", () => {
  const state = createSyncBlockFilterState();
  const input = `${SYNC_START}${CLEAR}frame${SYNC_END}`;

  assert.equal(
    filterSyncBlockClears(input, state, { stripClearsInSyncBlock: false }),
    input,
  );
});

test("isTerminalViewportScrolledUp is false on alternate-screen buffers", () => {
  const term = {
    rows: 24,
    buffer: { active: { type: "alternate", baseY: 0, length: 24 } },
  } as never;

  assert.equal(isTerminalViewportScrolledUp(term), false);
});

test("isTerminalViewportScrolledUp detects normal-buffer scrollback", () => {
  const atBottom = {
    rows: 24,
    buffer: { active: { type: "normal", baseY: 76, length: 100 } },
  } as never;
  const scrolledUp = {
    rows: 24,
    buffer: { active: { type: "normal", baseY: 10, length: 100 } },
  } as never;

  assert.equal(isTerminalViewportScrolledUp(atBottom), false);
  assert.equal(isTerminalViewportScrolledUp(scrolledUp), true);
});
