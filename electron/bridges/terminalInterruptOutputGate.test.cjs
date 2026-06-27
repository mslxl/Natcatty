const test = require("node:test");
const assert = require("node:assert/strict");

const {
  armTerminalInterruptOutputGate,
  filterTerminalInterruptOutput,
} = require("./terminalInterruptOutputGate.cjs");

test("drops flood output after Ctrl+C and resumes from the interrupt echo", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 1000,
    quietMs: 80,
    maxDrainMs: 1000,
  });

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "old output\n", { now: 1001 }),
    { accepted: false, data: "", droppedBytes: 11, reason: "draining" },
  );

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "more old output^C\r\n$ ", { now: 1002 }),
    { accepted: true, data: "^C\r\n$ ", droppedBytes: 15, reason: "interrupt-echo" },
  );

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "next output", { now: 1003 }),
    { accepted: true, data: "next output", droppedBytes: 0, reason: "inactive" },
  );
});

test("resumes output after a quiet gap when no interrupt echo is visible", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 2000,
    quietMs: 80,
    maxDrainMs: 1000,
  });

  assert.equal(filterTerminalInterruptOutput(session, "old output", { now: 2001 }).accepted, false);
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "$ ", { now: 2100 }),
    { accepted: true, data: "$ ", droppedBytes: 0, reason: "prompt-gap" },
  );
});

test("keeps draining large chunks after a short quiet gap", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 3000,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  assert.equal(filterTerminalInterruptOutput(session, "old output", { now: 3001 }).accepted, false);
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "x".repeat(32768), { now: 3100 }),
    { accepted: false, data: "", droppedBytes: 32768, reason: "draining" },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "$ ", { now: 3200 }),
    { accepted: true, data: "$ ", droppedBytes: 0, reason: "prompt-gap" },
  );
});
