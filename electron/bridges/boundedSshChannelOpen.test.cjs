"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter, getEventListeners } = require("node:events");
const {
  openBoundedForwardIn,
  openBoundedForwardOut,
  openBoundedSshShell,
} = require("./boundedSshChannelOpen.cjs");

function trackedTimerApi() {
  const active = new Set();
  return {
    active,
    setTimeoutFn(callback, delay) {
      let timer;
      timer = setTimeout(() => {
        active.delete(timer);
        callback();
      }, delay);
      active.add(timer);
      return timer;
    },
    clearTimeoutFn(timer) {
      clearTimeout(timer);
      active.delete(timer);
    },
  };
}

function pendingClient(method) {
  const client = new EventEmitter();
  client.pending = [];
  client.invalidations = 0;
  client[method] = (...args) => client.pending.push(args.at(-1));
  client.end = () => {};
  client.destroy = () => {
    client.invalidations += 1;
    client.pending.length = 0;
  };
  return client;
}

test("unresponsive shell and forward opens invalidate transport and release pending callbacks", async () => {
  for (const [method, open] of [
    ["shell", (client) => openBoundedSshShell(client, {}, {}, { timeoutMs: 2 })],
    ["forwardOut", (client) => openBoundedForwardOut(client, "127.0.0.1", 0, "host", 22, { timeoutMs: 2 })],
    ["forwardIn", (client) => openBoundedForwardIn(client, "127.0.0.1", 2222, { timeoutMs: 2 })],
  ]) {
    const client = pendingClient(method);
    await assert.rejects(open(client), /timed out/);
    assert.equal(client.invalidations, 1, method);
    assert.equal(client.pending.length, 0, method);
  }
});

test("cancelled channel open invalidates transport and a late stream is closed", async () => {
  let callback;
  let invalidations = 0;
  const timers = trackedTimerApi();
  const client = {
    shell(_window, _options, next) { callback = next; },
    destroy() { invalidations += 1; },
  };
  const controller = new AbortController();
  const pending = openBoundedSshShell(client, {}, {}, {
    signal: controller.signal,
    ...timers,
  });
  assert.equal(timers.active.size, 1);
  assert.equal([...timers.active][0].hasRef(), true);
  controller.abort(new Error("cancelled"));
  await assert.rejects(pending, /cancelled/);
  assert.equal(invalidations, 1);
  assert.equal(timers.active.size, 0);
  assert.equal(getEventListeners(controller.signal, "abort").length, 0);

  const stream = new EventEmitter();
  stream.closed = 0;
  stream.close = () => { stream.closed += 1; };
  callback(new Error("late open failure"), stream);
  assert.equal(stream.closed, 1);
  assert.equal(timers.active.size, 0);
});

test("channel open keeps its deadline referenced and clears it after success", async () => {
  let callback;
  const timers = trackedTimerApi();
  const controller = new AbortController();
  const client = {
    shell(_window, _options, next) { callback = next; },
  };
  const pending = openBoundedSshShell(client, {}, {}, {
    signal: controller.signal,
    timeoutMs: 1_000,
    ...timers,
  });

  assert.equal(timers.active.size, 1);
  assert.equal([...timers.active][0].hasRef(), true);
  const stream = new EventEmitter();
  callback(null, stream);
  assert.equal(await pending, stream);
  assert.equal(timers.active.size, 0);
  assert.equal(getEventListeners(controller.signal, "abort").length, 0);
});
