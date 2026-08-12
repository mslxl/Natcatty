"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { retainOwnedSessions } = require("./retainOwnedSessions.cjs");

test("retainOwnedSessions keeps host_open-owned sessions dropped by a full scope replace", () => {
  const previousById = new Map([
    ["sess-original", {
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    }],
    ["sess-opened", {
      hostname: "10.0.0.2",
      label: "server-b",
      protocol: "ssh",
      connected: false,
      hostId: "host-b",
    }],
  ]);

  const retained = retainOwnedSessions({
    incomingSessions: [{
      sessionId: "sess-original",
      hostname: "10.0.0.1",
      label: "server-a",
      connected: true,
      hostId: "host-a",
    }],
    ownedSessionIds: ["sess-opened"],
    previousById,
  });

  const ids = retained.map((entry) => entry.sessionId).sort();
  assert.deepEqual(ids, ["sess-opened", "sess-original"]);
  const opened = retained.find((entry) => entry.sessionId === "sess-opened");
  assert.equal(opened.label, "server-b");
  assert.equal(opened.hostId, "host-b");
  assert.equal(opened.connected, false);
});

test("retainOwnedSessions does not alter authoritative empty replaces", () => {
  const retained = retainOwnedSessions({
    incomingSessions: [],
    ownedSessionIds: ["sess-opened"],
    previousById: new Map([
      ["sess-opened", { hostname: "10.0.0.2", label: "server-b" }],
    ]),
  });
  assert.deepEqual(retained, []);
});

test("retainOwnedSessions falls back to cross-scope metadata when needed", () => {
  const retained = retainOwnedSessions({
    incomingSessions: [{ sessionId: "sess-original", label: "server-a" }],
    ownedSessionIds: ["sess-opened"],
    previousById: new Map(),
    findFallbackMeta: (sessionId) => (
      sessionId === "sess-opened"
        ? { hostname: "10.0.0.2", label: "server-b", hostId: "host-b" }
        : null
    ),
  });
  assert.equal(retained.length, 2);
  assert.equal(
    retained.find((entry) => entry.sessionId === "sess-opened")?.label,
    "server-b",
  );
});

test("retainOwnedSessions ignores owned ids with no recoverable metadata", () => {
  const retained = retainOwnedSessions({
    incomingSessions: [{ sessionId: "sess-original", label: "server-a" }],
    ownedSessionIds: ["sess-ghost"],
    previousById: new Map(),
  });
  assert.deepEqual(retained.map((entry) => entry.sessionId), ["sess-original"]);
});
