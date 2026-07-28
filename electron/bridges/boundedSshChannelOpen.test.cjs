"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const {
  openBoundedForwardIn,
  openBoundedForwardOut,
  openBoundedSshShell,
} = require("./boundedSshChannelOpen.cjs");

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
  const client = {
    shell(_window, _options, next) { callback = next; },
    destroy() { invalidations += 1; },
  };
  const controller = new AbortController();
  const pending = openBoundedSshShell(client, {}, {}, { signal: controller.signal });
  controller.abort(new Error("cancelled"));
  await assert.rejects(pending, /cancelled/);
  assert.equal(invalidations, 1);

  const stream = new EventEmitter();
  stream.closed = 0;
  stream.close = () => { stream.closed += 1; };
  callback(null, stream);
  assert.equal(stream.closed, 1);
});
