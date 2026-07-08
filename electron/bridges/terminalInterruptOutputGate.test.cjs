const test = require("node:test");
const assert = require("node:assert/strict");

const {
  armTerminalInterruptOutputGate,
  filterTerminalInterruptOutput,
  stashPendingInterruptOutputMeta,
  takePendingInterruptOutputMeta,
} = require("./terminalInterruptOutputGate.cjs");

test("pending interrupt metadata clears stale alternate-screen action on later unknown risk", () => {
  const session = {};

  stashPendingInterruptOutputMeta(session, {
    droppedOutputMayAffectTerminalState: true,
    droppedOutputAlternateScreenAction: "leave",
  });
  stashPendingInterruptOutputMeta(session, {
    droppedOutputMayAffectTerminalState: true,
  });

  assert.deepEqual(takePendingInterruptOutputMeta(session), {
    droppedOutputMayAffectTerminalState: true,
  });
});

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

test("accepts an immediate prompt when the remote does not echo Ctrl+C", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 2500,
    quietMs: 80,
    maxDrainMs: 1000,
  });

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "$ ", { now: 2501 }),
    { accepted: true, data: "$ ", droppedBytes: 0, reason: "prompt-candidate" },
  );

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "next output", { now: 2502 }),
    { accepted: true, data: "next output", droppedBytes: 0, reason: "inactive" },
  );
});

test("resumes output when interrupt echo is split across chunks", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 3500,
    quietMs: 80,
    maxDrainMs: 1000,
  });

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "old output", { now: 3501 }),
    { accepted: false, data: "", droppedBytes: 10, reason: "draining" },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "^", { now: 3502 }),
    { accepted: false, data: "", droppedBytes: 1, reason: "draining" },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "C\r\n$ ", { now: 3503 }),
    { accepted: true, data: "^C\r\n$ ", droppedBytes: 0, reason: "interrupt-echo" },
  );
});

test("prompt gap keeps only the prompt suffix and drops stale prefix", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 3600,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  assert.equal(filterTerminalInterruptOutput(session, "old output", { now: 3601 }).accepted, false);
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "stale flood\r\n$ ", { now: 3700 }),
    { accepted: true, data: "$ ", droppedBytes: 13, reason: "prompt-gap" },
  );
});

test("preserves alternate-screen exit controls while draining stale output", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 3800,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "stale frame\x1b[?1049l", { now: 3801 }),
    {
      accepted: true,
      data: "\x1b[?1049l",
      droppedBytes: "stale frame".length,
      reason: "draining",
    },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "more stale\x1b[?25h\r\n$ ", { now: 3900 }),
    {
      accepted: true,
      data: "\x1b[?25h$ ",
      droppedBytes: "more stale\r\n".length,
      reason: "prompt-gap",
    },
  );
});

test("preserves split alternate-screen exit controls while draining stale output", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 4800,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "stale frame\x1b[?104", { now: 4801 }),
    {
      accepted: false,
      data: "",
      droppedBytes: "stale frame".length,
      reason: "draining",
    },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "9l^C\r\n$ ", { now: 4802 }),
    {
      accepted: true,
      data: "\x1b[?1049l^C\r\n$ ",
      droppedBytes: 0,
      reason: "interrupt-echo",
    },
  );
});

test("does not preserve unsafe combined private modes while draining stale output", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 4900,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  const unsafeSequence = "\x1b[?1049;25h";
  assert.deepEqual(
    filterTerminalInterruptOutput(session, `stale frame${unsafeSequence}^C\r\n$ `, {
      now: 4901,
    }),
    {
      accepted: true,
      data: "^C\r\n$ ",
      droppedBytes: "stale frame".length + unsafeSequence.length,
      reason: "interrupt-echo",
    },
  );
});

test("accepts prompt candidates with OSC title and spaces after stale output", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 5000,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  assert.equal(filterTerminalInterruptOutput(session, "old output", { now: 5001 }).accepted, false);
  const prompt = "\x1b]0;~/My Project\x07~/My Project$ ";
  assert.deepEqual(
    filterTerminalInterruptOutput(session, prompt, { now: 5100 }),
    {
      accepted: true,
      data: prompt,
      droppedBytes: 0,
      reason: "prompt-gap",
    },
  );
});

test("accepts split OSC prompt candidates after stale output", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 5200,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 1000,
  });

  assert.deepEqual(
    filterTerminalInterruptOutput(session, "old output\x1b]0;~/My ", { now: 5201 }),
    {
      accepted: false,
      data: "",
      droppedBytes: "old output".length,
      reason: "draining",
    },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "Project\x07~/My Project$ ", { now: 5300 }),
    {
      accepted: true,
      data: "\x1b]0;~/My Project\x07~/My Project$ ",
      droppedBytes: 0,
      reason: "prompt-gap",
    },
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

test("accepts a sudo password prompt while draining after Ctrl+C (#2010)", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 6000,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 2500,
  });

  assert.equal(
    filterTerminalInterruptOutput(session, "Reading package lists...\n", { now: 6001 }).accepted,
    false,
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "[sudo] password for alice: ", { now: 6100 }),
    {
      accepted: true,
      data: "[sudo] password for alice: ",
      droppedBytes: 0,
      reason: "prompt-gap",
    },
  );
});

test("accepts bare and localized password prompts while draining (#2010)", () => {
  for (const prompt of [
    "Password: ",
    "[sudo] alice 的密码：",
    "输入密码",
    "Input Password",
  ]) {
    const session = {};
    armTerminalInterruptOutputGate(session, {
      now: 7000,
      quietMs: 500,
      promptQuietMs: 80,
      maxDrainMs: 2500,
    });
    assert.equal(filterTerminalInterruptOutput(session, "stale\n", { now: 7001 }).accepted, false);
    assert.deepEqual(
      filterTerminalInterruptOutput(session, prompt, { now: 7100 }),
      {
        accepted: true,
        data: prompt,
        droppedBytes: 0,
        reason: "prompt-gap",
      },
      `expected password prompt to resume drain: ${JSON.stringify(prompt)}`,
    );
  }
});

test("does not treat password mentions in ordinary output as prompts", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 8000,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 2500,
  });

  assert.equal(filterTerminalInterruptOutput(session, "stale\n", { now: 8001 }).accepted, false);
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "the password was changed", { now: 8100 }),
    { accepted: false, data: "", droppedBytes: 24, reason: "draining" },
  );
});

test("holds a split Password: prompt across chunks while draining (#2010)", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 9000,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 2500,
  });

  assert.equal(filterTerminalInterruptOutput(session, "stale\n", { now: 9001 }).accepted, false);
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "Pass", { now: 9002 }),
    { accepted: false, data: "", droppedBytes: 0, reason: "draining" },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "word: ", { now: 9003 }),
    {
      accepted: true,
      data: "Password: ",
      droppedBytes: 0,
      reason: "prompt-gap",
    },
  );
});

test("holds a split [sudo] password prompt across chunks while draining (#2010)", () => {
  const session = {};

  armTerminalInterruptOutputGate(session, {
    now: 9100,
    quietMs: 500,
    promptQuietMs: 80,
    maxDrainMs: 2500,
  });

  assert.equal(filterTerminalInterruptOutput(session, "stale\n", { now: 9101 }).accepted, false);
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "[sudo] pass", { now: 9102 }),
    { accepted: false, data: "", droppedBytes: 0, reason: "draining" },
  );
  assert.deepEqual(
    filterTerminalInterruptOutput(session, "word for alice: ", { now: 9103 }),
    {
      accepted: true,
      data: "[sudo] password for alice: ",
      droppedBytes: 0,
      reason: "prompt-gap",
    },
  );
});
