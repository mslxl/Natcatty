"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");

const {
  DEFAULT_PROBE_TIMEOUT_MS,
  isLocalNetworkHostname,
  resolveFirstTcpEndpoint,
  annotateMacLocalNetworkErrorMessage,
  createMacLocalNetworkAccessGate,
} = require("./macLocalNetworkAccess.cjs");

test("DEFAULT_PROBE_TIMEOUT_MS is a few seconds, not tens of seconds", () => {
  assert.ok(DEFAULT_PROBE_TIMEOUT_MS >= 500);
  assert.ok(DEFAULT_PROBE_TIMEOUT_MS <= 5_000);
  const gate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    forceElectron: true,
  });
  assert.equal(gate.getProbeTimeoutMs(), DEFAULT_PROBE_TIMEOUT_MS);
});

test("isLocalNetworkHostname recognizes RFC1918, link-local, and CGNAT", () => {
  assert.equal(isLocalNetworkHostname("192.168.5.22"), true);
  assert.equal(isLocalNetworkHostname("10.0.0.1"), true);
  assert.equal(isLocalNetworkHostname("172.16.9.1"), true);
  assert.equal(isLocalNetworkHostname("172.31.255.1"), true);
  assert.equal(isLocalNetworkHostname("169.254.1.1"), true);
  assert.equal(isLocalNetworkHostname("100.64.0.1"), true);
  assert.equal(isLocalNetworkHostname("[fd12::1]"), true);
  assert.equal(isLocalNetworkHostname("fe80::1"), true);
  assert.equal(isLocalNetworkHostname("::ffff:192.168.1.9"), true);
});

test("isLocalNetworkHostname rejects public, loopback, empty, and non-IP hostnames", () => {
  assert.equal(isLocalNetworkHostname("8.8.8.8"), false);
  assert.equal(isLocalNetworkHostname("1.1.1.1"), false);
  assert.equal(isLocalNetworkHostname("172.15.0.1"), false);
  assert.equal(isLocalNetworkHostname("172.32.0.1"), false);
  assert.equal(isLocalNetworkHostname("127.0.0.1"), false);
  assert.equal(isLocalNetworkHostname("localhost"), false);
  assert.equal(isLocalNetworkHostname("example.com"), false);
  assert.equal(isLocalNetworkHostname("nas.local"), false);
  // Must not treat public names that merely start with fc/fd as ULA.
  assert.equal(isLocalNetworkHostname("fda.gov"), false);
  assert.equal(isLocalNetworkHostname("fdcorp.example"), false);
  assert.equal(isLocalNetworkHostname("fc-host.example"), false);
  assert.equal(isLocalNetworkHostname(""), false);
  assert.equal(isLocalNetworkHostname(null), false);
});

test("resolveFirstTcpEndpoint prefers proxy, then jump host, then target", () => {
  assert.deepEqual(
    resolveFirstTcpEndpoint({
      hostname: "10.0.0.9",
      port: 2222,
      proxy: { type: "socks5", host: "192.168.0.2", port: 1080 },
      jumpHosts: [{ hostname: "192.168.1.1", port: 2200 }],
    }),
    { hostname: "192.168.0.2", port: 1080 },
  );
  assert.deepEqual(
    resolveFirstTcpEndpoint({
      hostname: "10.0.0.9",
      port: 2222,
      jumpHosts: [{ hostname: "192.168.1.1", port: 2200 }],
    }),
    { hostname: "192.168.1.1", port: 2200 },
  );
  assert.deepEqual(
    resolveFirstTcpEndpoint({ hostname: "192.168.5.22", port: 22 }),
    { hostname: "192.168.5.22", port: 22 },
  );
  assert.deepEqual(
    resolveFirstTcpEndpoint({ host: "10.1.2.3" }),
    { hostname: "10.1.2.3", port: 22 },
  );
});

test("annotateMacLocalNetworkErrorMessage only rewrites LAN unreachability on darwin", () => {
  const base = "connect EHOSTUNREACH 192.168.5.22:22";
  const annotated = annotateMacLocalNetworkErrorMessage(base, {
    platform: "darwin",
    hostname: "192.168.5.22",
  });
  assert.match(annotated, /Local Network/i);
  assert.match(annotated, /System Settings/i);
  assert.equal(
    annotateMacLocalNetworkErrorMessage(base, {
      platform: "linux",
      hostname: "192.168.5.22",
    }),
    base,
  );
  assert.equal(
    annotateMacLocalNetworkErrorMessage(base, {
      platform: "darwin",
      hostname: "8.8.8.8",
    }),
    base,
  );
  assert.equal(
    annotateMacLocalNetworkErrorMessage("All configured authentication methods failed", {
      platform: "darwin",
      hostname: "192.168.5.22",
    }),
    "All configured authentication methods failed",
  );
  // Annotate when first hop is LAN even if final target is public.
  const viaJump = annotateMacLocalNetworkErrorMessage(base, {
    platform: "darwin",
    hostname: "8.8.8.8",
    firstHopHostname: "192.168.1.1",
  });
  assert.match(viaJump, /Local Network/i);
});

test("createMacLocalNetworkAccessGate probes from the main process once per LAN endpoint", async () => {
  const connects = [];
  class FakeSocket extends EventEmitter {
    constructor() {
      super();
      this.destroyed = false;
    }
    setTimeout() { return this; }
    destroy() { this.destroyed = true; }
  }

  const gate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    forceElectron: true,
    net: {
      connect(options, onConnect) {
        connects.push(options);
        const socket = new FakeSocket();
        queueMicrotask(() => onConnect && onConnect());
        return socket;
      },
    },
  });

  await gate.ensureAccess({ hostname: "192.168.5.22", port: 22 });
  await gate.ensureAccess({ hostname: "192.168.5.22", port: 22 });
  assert.equal(connects.length, 1);
  assert.deepEqual(connects[0], { host: "192.168.5.22", port: 22 });
});

test("createMacLocalNetworkAccessGate skips non-darwin and non-LAN hosts", async () => {
  let connects = 0;
  const linuxGate = createMacLocalNetworkAccessGate({
    platform: "linux",
    forceElectron: true,
    net: {
      connect() {
        connects += 1;
        throw new Error("should not connect");
      },
    },
  });
  await linuxGate.ensureAccess({ hostname: "192.168.5.22", port: 22 });

  const darwinGate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    forceElectron: true,
    net: {
      connect() {
        connects += 1;
        throw new Error("should not connect");
      },
    },
  });
  await darwinGate.ensureAccess({ hostname: "example.com", port: 22 });
  await darwinGate.ensureAccess({ hostname: "fda.gov", port: 22 });

  const bareNodeGate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    versions: {},
    net: {
      connect() {
        connects += 1;
        throw new Error("should not connect outside Electron");
      },
    },
  });
  await bareNodeGate.ensureAccess({ hostname: "192.168.5.22", port: 22 });
  assert.equal(connects, 0);
});

test("createMacLocalNetworkAccessGate treats connect errors as a completed probe", async () => {
  class FakeSocket extends EventEmitter {
    setTimeout() { return this; }
    destroy() {}
  }
  const gate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    forceElectron: true,
    net: {
      connect() {
        const socket = new FakeSocket();
        queueMicrotask(() => socket.emit("error", Object.assign(new Error("connect EHOSTUNREACH"), { code: "EHOSTUNREACH" })));
        return socket;
      },
    },
  });
  await gate.ensureAccess({
    hostname: "10.0.0.8",
    port: 22,
    jumpHosts: [{ hostname: "192.168.1.50", port: 2200 }],
  });
  // Second call must be cached even though the first connect failed.
  let secondConnects = 0;
  const cachedGate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    forceElectron: true,
    probedKeys: new Set(["192.168.1.50:2200"]),
    net: {
      connect() {
        secondConnects += 1;
        throw new Error("should use cache");
      },
    },
  });
  await cachedGate.ensureAccess({
    jumpHosts: [{ hostname: "192.168.1.50", port: 2200 }],
  });
  assert.equal(secondConnects, 0);
});

test("createMacLocalNetworkAccessGate applies the configured probe timeout", async () => {
  const timeouts = [];
  class FakeSocket extends EventEmitter {
    setTimeout(ms, onTimeout) {
      timeouts.push(ms);
      queueMicrotask(() => onTimeout && onTimeout());
      return this;
    }
    destroy() {}
  }
  const gate = createMacLocalNetworkAccessGate({
    platform: "darwin",
    forceElectron: true,
    probeTimeoutMs: DEFAULT_PROBE_TIMEOUT_MS,
    net: {
      connect() {
        return new FakeSocket();
      },
    },
  });
  await gate.ensureAccess({ hostname: "10.0.0.1", port: 22 });
  assert.deepEqual(timeouts, [DEFAULT_PROBE_TIMEOUT_MS]);
  assert.ok(DEFAULT_PROBE_TIMEOUT_MS <= 5_000);
});
