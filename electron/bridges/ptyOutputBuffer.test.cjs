const test = require("node:test");
const assert = require("node:assert/strict");

const { createPtyOutputBuffer } = require("./ptyOutputBuffer.cjs");

/** Resolve after one event-loop turn (immediates have run). */
const tick = () => new Promise((resolve) => setImmediate(resolve));

test("coalesces data buffered within the same turn into a single send", async () => {
  const sends = [];
  const buffer = createPtyOutputBuffer((data) => sends.push(data));

  buffer.bufferData("a");
  buffer.bufferData("b");
  buffer.bufferData("c");

  // Nothing is sent synchronously while still in the same turn.
  assert.equal(sends.length, 0);

  await tick();

  assert.deepEqual(sends, ["abc"]);
});

test("flushes within a single event-loop turn (not on a fixed delay)", async () => {
  const sends = [];
  const buffer = createPtyOutputBuffer((data) => sends.push(data));

  buffer.bufferData("x");

  // A fixed-interval (e.g. 8ms) buffer would NOT have flushed after one
  // immediate turn. Turn-based flushing must have delivered it by now.
  await tick();

  assert.deepEqual(sends, ["x"]);
});

test("flushes immediately and synchronously once the size cap is reached", async () => {
  const sends = [];
  const buffer = createPtyOutputBuffer((data) => sends.push(data), {
    maxBufferSize: 4,
  });

  buffer.bufferData("ab");
  assert.equal(sends.length, 0); // under cap, still pending

  buffer.bufferData("cd"); // now "abcd" hits the 4-byte cap

  // Cap flush happens synchronously, without waiting for the turn.
  assert.deepEqual(sends, ["abcd"]);

  // The pending turn flush must have been cancelled — no empty/duplicate send.
  await tick();
  assert.deepEqual(sends, ["abcd"]);
});

test("flush() forces a synchronous send and cancels the pending turn", async () => {
  const sends = [];
  const buffer = createPtyOutputBuffer((data) => sends.push(data));

  buffer.bufferData("hello");
  buffer.flush();

  assert.deepEqual(sends, ["hello"]);

  await tick();
  assert.deepEqual(sends, ["hello"]); // not sent twice
});

test("flush() with an empty buffer does not send", async () => {
  const sends = [];
  const buffer = createPtyOutputBuffer((data) => sends.push(data));

  buffer.flush();

  assert.equal(sends.length, 0);
});

test("drops incoming data when shouldAcceptOutput returns false", async () => {
  const sends = [];
  let accept = true;
  const buffer = createPtyOutputBuffer((data) => sends.push(data), {
    shouldAcceptOutput: () => accept,
  });

  buffer.bufferData("before");
  accept = false;
  buffer.bufferData("dropped");
  await tick();

  assert.deepEqual(sends, ["before"]);
});

test("keeps batching after a flush", async () => {
  const sends = [];
  const buffer = createPtyOutputBuffer((data) => sends.push(data));

  buffer.bufferData("first");
  await tick();

  buffer.bufferData("second");
  await tick();

  assert.deepEqual(sends, ["first", "second"]);
});
