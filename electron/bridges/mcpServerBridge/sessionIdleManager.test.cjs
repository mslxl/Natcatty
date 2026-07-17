"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { createSessionIdleManager } = require("./sessionIdleManager.cjs");

function createClock() {
  let now = 0;
  let nextId = 1;
  const timers = new Map();

  function runDueTimers() {
    while (true) {
      const due = Array.from(timers.entries())
        .filter(([, timer]) => timer.at <= now)
        .sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      timers.delete(due[0]);
      due[1].callback();
    }
  }

  return {
    Date: { now: () => now },
    setTimeout(callback, delay) {
      const id = nextId++;
      timers.set(id, { callback, at: now + delay });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id);
    },
    advance(ms) {
      now += ms;
      runDueTimers();
    },
    timerCount() {
      return timers.size;
    },
  };
}

test("idle sessions close independently and activity renews only the matching session", async () => {
  const clock = createClock();
  const closed = [];
  const manager = createSessionIdleManager({
    ...clock,
    timeoutMinutes: 1,
    onIdle: async (entry) => closed.push(entry),
  });

  manager.track("chat-a", "session-1");
  manager.track("chat-a", "session-2");

  clock.advance(30_000);
  await Promise.resolve();
  assert.deepEqual(closed, []);
  manager.touch("chat-a", "session-1");

  clock.advance(30_000);
  await Promise.resolve();
  assert.deepEqual(closed, [{ chatSessionId: "chat-a", sessionId: "session-2" }]);

  clock.advance(30_000);
  await Promise.resolve();
  assert.deepEqual(closed, [
    { chatSessionId: "chat-a", sessionId: "session-2" },
    { chatSessionId: "chat-a", sessionId: "session-1" },
  ]);
});

test("an in-flight operation cannot time out and gets a fresh idle window when it ends", async () => {
  const clock = createClock();
  const closed = [];
  const manager = createSessionIdleManager({
    ...clock,
    timeoutMinutes: 1,
    onIdle: async (entry) => closed.push(entry),
  });

  manager.track("chat-a", "session-1");
  assert.equal(manager.beginActivity("chat-a", "session-1"), true);
  clock.advance(120_000);
  await Promise.resolve();
  assert.deepEqual(closed, []);
  assert.equal(clock.timerCount(), 0);

  manager.endActivity("chat-a", "session-1");
  clock.advance(59_999);
  await Promise.resolve();
  assert.deepEqual(closed, []);
  clock.advance(1);
  await Promise.resolve();
  assert.deepEqual(closed, [{ chatSessionId: "chat-a", sessionId: "session-1" }]);
});

test("a failed close resumes tracking while a successful close can be forgotten", async () => {
  const clock = createClock();
  let attempts = 0;
  const manager = createSessionIdleManager({
    ...clock,
    timeoutMinutes: 1,
    onIdle: async ({ sessionId }) => {
      attempts += 1;
      if (attempts === 1) manager.resume(sessionId);
      else manager.forgetSession(sessionId);
    },
  });

  manager.track("chat-a", "session-1");
  clock.advance(60_000);
  await Promise.resolve();
  assert.equal(attempts, 1);
  assert.equal(manager.isTracked("session-1"), true);

  clock.advance(60_000);
  await Promise.resolve();
  assert.equal(attempts, 2);
  assert.equal(manager.isTracked("session-1"), false);
  assert.equal(clock.timerCount(), 0);
});

test("clearing an AI scope does not discard its idle cleanup fallback", async () => {
  const clock = createClock();
  const closed = [];
  const manager = createSessionIdleManager({
    ...clock,
    timeoutMinutes: 1,
    onIdle: async (entry) => closed.push(entry),
  });

  manager.track("chat-a", "session-1");
  manager.scopeCleared("chat-a");
  clock.advance(60_000);
  await Promise.resolve();

  assert.deepEqual(closed, [{ chatSessionId: "chat-a", sessionId: "session-1" }]);
});
