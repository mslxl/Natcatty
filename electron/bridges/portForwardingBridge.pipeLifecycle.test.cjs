const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const test = require("node:test");

const {
  _attachTunnelPipeStreamForTests: attachTunnelPipeStream,
  _destroyTunnelPipesForTests: destroyTunnelPipes,
  _trackTunnelPipeForTests: trackTunnelPipe,
} = require("./portForwardingBridge.cjs");

function fakeEndpoint() {
  const endpoint = new EventEmitter();
  endpoint.destroyedByTest = false;
  endpoint.destroy = () => { endpoint.destroyedByTest = true; };
  endpoint.end = () => { endpoint.destroyedByTest = true; };
  endpoint.close = () => { endpoint.destroyedByTest = true; };
  return endpoint;
}

test("accepted sockets are tracked before forwardOut finishes and stop destroys them", () => {
  const tunnel = { cancelled: false, activePipes: new Set() };
  const socket = fakeEndpoint();
  trackTunnelPipe(tunnel, socket);
  assert.equal(tunnel.activePipes.size, 1);

  destroyTunnelPipes(tunnel);
  assert.equal(socket.destroyedByTest, true);
  assert.equal(tunnel.activePipes.size, 0);
});

test("a late forwardOut stream after stop is rejected and destroyed", () => {
  const tunnel = { cancelled: false, activePipes: new Set() };
  const socket = fakeEndpoint();
  const entry = trackTunnelPipe(tunnel, socket);
  tunnel.cancelled = true;
  const stream = fakeEndpoint();

  assert.equal(attachTunnelPipeStream(tunnel, entry, stream), false);
  assert.equal(socket.destroyedByTest, true);
  assert.equal(stream.destroyedByTest, true);
  assert.equal(tunnel.activePipes.size, 0);
});

test("closing either side tears down the paired endpoint and releases tracking", () => {
  const tunnel = { cancelled: false, activePipes: new Set() };
  const socket = fakeEndpoint();
  const stream = fakeEndpoint();
  const entry = trackTunnelPipe(tunnel, socket);
  assert.equal(attachTunnelPipeStream(tunnel, entry, stream), true);

  socket.emit("close");
  assert.equal(stream.destroyedByTest, true);
  assert.equal(tunnel.activePipes.size, 0);
});
