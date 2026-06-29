import assert from "node:assert/strict";
import test from "node:test";

import type { Terminal as XTerm } from "@xterm/xterm";

import {
  MAX_WRITE_QUEUE_ITEMS,
  abortTerminalWriteQueue,
  enqueueTerminalWrite,
  getTerminalWriteQueueDepth,
  isTerminalWriteQueueInFloodMode,
  setTerminalWriteQueueDropHandler,
} from "./terminalWriteQueue.ts";

const createFakeTerm = () => ({}) as XTerm;

test("enqueueTerminalWrite serializes writes in order", () => {
  const term = createFakeTerm();
  const order: number[] = [];

  enqueueTerminalWrite(term, 1, (done) => {
    order.push(1);
    done();
  });
  enqueueTerminalWrite(term, 1, (done) => {
    order.push(2);
    done();
  });

  assert.deepEqual(order, [1, 2]);
});

test("marks flood mode without dropping queued writes when item cap is exceeded", () => {
  const term = createFakeTerm();
  const dropped: number[] = [];
  let releaseFirst: (() => void) | null = null;
  let completed = 0;

  enqueueTerminalWrite(term, 10, (done) => {
    releaseFirst = done;
  });

  for (let index = 0; index < MAX_WRITE_QUEUE_ITEMS + 1; index += 1) {
    enqueueTerminalWrite(
      term,
      10,
      (done) => {
        completed += 1;
        done();
      },
      { onDropped: (bytes) => dropped.push(bytes) },
    );
  }

  assert.deepEqual(dropped, []);
  assert.equal(isTerminalWriteQueueInFloodMode(term), true);
  assert.equal(getTerminalWriteQueueDepth(term), MAX_WRITE_QUEUE_ITEMS + 1);
  releaseFirst?.();
  assert.equal(completed, MAX_WRITE_QUEUE_ITEMS + 1);
});

test("setTerminalWriteQueueDropHandler only reports explicit queue aborts", () => {
  const term = createFakeTerm();
  const dropped: number[] = [];
  let releaseFirst: (() => void) | null = null;

  setTerminalWriteQueueDropHandler(term, (bytes) => dropped.push(bytes));
  enqueueTerminalWrite(term, 10, (done) => {
    releaseFirst = done;
  });

  for (let index = 0; index < MAX_WRITE_QUEUE_ITEMS + 1; index += 1) {
    enqueueTerminalWrite(term, 10, (done) => done());
  }

  assert.deepEqual(dropped, []);
  assert.equal(isTerminalWriteQueueInFloodMode(term), true);
  abortTerminalWriteQueue(term);
  assert.deepEqual(dropped, [MAX_WRITE_QUEUE_ITEMS * 10 + 10]);
  releaseFirst?.();
});

test("abortTerminalWriteQueue drops pending bytes and reports dropped count", () => {
  const term = createFakeTerm();
  const dropped: number[] = [];
  let started = false;

  enqueueTerminalWrite(term, 40, () => {
    started = true;
  });
  enqueueTerminalWrite(term, 60, () => {}, { onDropped: (bytes) => dropped.push(bytes) });
  abortTerminalWriteQueue(term, (bytes) => dropped.push(bytes));

  assert.equal(started, true);
  assert.deepEqual(dropped, [60]);
});
