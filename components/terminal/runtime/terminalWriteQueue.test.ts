import assert from "node:assert/strict";
import test from "node:test";

import type { Terminal as XTerm } from "@xterm/xterm";

import {
  MAX_WRITE_QUEUE_ITEMS,
  abortTerminalWriteQueue,
  enqueueTerminalWrite,
  getTerminalWriteQueueDepth,
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

test("collapses queued writes when item cap is exceeded", () => {
  const term = createFakeTerm();
  const dropped: number[] = [];
  let releaseFirst: (() => void) | null = null;

  enqueueTerminalWrite(term, 10, (done) => {
    releaseFirst = done;
  });

  for (let index = 0; index < MAX_WRITE_QUEUE_ITEMS + 1; index += 1) {
    enqueueTerminalWrite(
      term,
      10,
      (done) => done(),
      { onDropped: (bytes) => dropped.push(bytes) },
    );
  }

  assert.ok(dropped.length > 0);
  assert.ok(getTerminalWriteQueueDepth(term) <= 1);
  releaseFirst?.();
});

test("setTerminalWriteQueueDropHandler applies to queues created after registration", () => {
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

  assert.ok(dropped.length > 0);
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