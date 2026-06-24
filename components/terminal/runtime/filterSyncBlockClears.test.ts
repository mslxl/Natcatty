import assert from "node:assert/strict";
import test from "node:test";

import {
  createSyncBlockFilterState,
  filterSyncBlockClears,
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
