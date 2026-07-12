const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");

const { createTcpConnectLatencyProbe } = require("./tcpConnectLatency.cjs");

function createSocket() {
  const socket = new EventEmitter();
  socket.setTimeout = (_ms, callback) => { socket.timeoutCallback = callback; };
  socket.destroy = () => { socket.destroyedByProbe = true; };
  return socket;
}

test("TCP latency stops timing as soon as the socket connects", async () => {
  const socket = createSocket();
  let connected;
  const times = [100, 102.4];
  const measure = createTcpConnectLatencyProbe({
    net: {
      createConnection(options, callback) {
        assert.equal(options.host, "vm.test");
        assert.equal(options.port, 22);
        options.lookup("vm.test", {}, () => {});
        connected = callback;
        return socket;
      },
    },
    lookup: (_host, _options, callback) => callback(null, "192.0.2.10", 4),
    now: () => times.shift(),
  });

  const pending = measure({ hostname: "vm.test", port: 22 });
  connected();

  assert.equal(await pending, 2);
  assert.equal(socket.destroyedByProbe, true);
});

test("TCP latency excludes hostname lookup time", async () => {
  const socket = createSocket();
  let connected;
  let finishLookup;
  const times = [500, 503];
  const measure = createTcpConnectLatencyProbe({
    net: {
      createConnection(options, callback) {
        options.lookup("slow-dns.test", {}, () => {});
        connected = callback;
        return socket;
      },
    },
    lookup: (_host, _options, callback) => { finishLookup = callback; },
    now: () => times.shift(),
  });

  const pending = measure({ hostname: "slow-dns.test", port: 22 });
  finishLookup(null, "192.0.2.20", 4);
  connected();

  assert.equal(await pending, 3);
});

test("TCP latency returns no value when the endpoint cannot be reached", async () => {
  const socket = createSocket();
  const measure = createTcpConnectLatencyProbe({
    net: { createConnection: () => socket },
    now: () => 100,
  });

  const pending = measure({ hostname: "offline.test", port: 22 });
  socket.emit("error", new Error("unreachable"));

  assert.equal(await pending, null);
  assert.equal(socket.destroyedByProbe, true);
});
