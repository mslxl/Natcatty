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

test("TCP latency deduplicates concurrent probes and reuses a recent result", async () => {
  const sockets = [];
  const connectCallbacks = [];
  const probeTimes = [100, 104, 200, 205];
  let clock = 1_000;
  const measure = createTcpConnectLatencyProbe({
    net: {
      isIP: () => 4,
      createConnection(_options, callback) {
        const socket = createSocket();
        sockets.push(socket);
        connectCallbacks.push(callback);
        return socket;
      },
    },
    now: () => probeTimes.shift(),
    cacheNow: () => clock,
    cacheTtlMs: 30_000,
  });

  const first = measure({ hostname: "192.0.2.30", port: 22 });
  const concurrent = measure({ hostname: "192.0.2.30", port: 22 });
  assert.equal(first, concurrent);
  assert.equal(sockets.length, 1);

  connectCallbacks[0]();
  assert.equal(await first, 4);
  assert.equal(await measure({ hostname: "192.0.2.30", port: 22 }), 4);
  assert.equal(sockets.length, 1);

  clock += 30_001;
  const refreshed = measure({ hostname: "192.0.2.30", port: 22 });
  assert.equal(sockets.length, 2);
  connectCallbacks[1]();
  assert.equal(await refreshed, 5);
});
