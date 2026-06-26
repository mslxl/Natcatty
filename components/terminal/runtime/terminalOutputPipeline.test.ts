import assert from "node:assert/strict";
import test from "node:test";

import type { Terminal as XTerm } from "@xterm/xterm";

import { createOutputFlowController } from "./outputFlowController.ts";
import {
  prioritizeTerminalInput,
  teardownTerminalOutputPipeline,
} from "./terminalOutputPipeline.ts";
import { FLOW_LOW_WATER_MARK } from "./terminalFlowConstants.ts";
import { enqueueTerminalWrite } from "./terminalWriteQueue.ts";

const createFakeTerm = () => ({}) as XTerm;

test("teardownTerminalOutputPipeline resumes renderer pause and clears backlog", () => {
  const term = createFakeTerm();
  const events: string[] = [];
  const flow = createOutputFlowController({
    highWaterMark: 50,
    lowWaterMark: 10,
    onPause: () => events.push("pause"),
    onResume: () => events.push("resume"),
  });
  const backend = {
    setSessionFlowPaused: (_sessionId: string, paused: boolean) => {
      events.push(paused ? "ipc-pause" : "ipc-resume");
    },
    ackSessionFlow: () => {},
  };

  flow.received(60);
  enqueueTerminalWrite(term, 20, (done) => done());
  teardownTerminalOutputPipeline(
    { terminalBackend: backend, sessionRef: { current: "sess-1" } } as never,
    term,
    "sess-1",
    flow,
  );

  assert.deepEqual(events, ["pause", "resume", "ipc-resume"]);
});

test("prioritizeTerminalInput drains backlog before user input is forwarded", () => {
  const term = createFakeTerm();
  const events: string[] = [];
  const flow = createOutputFlowController({
    highWaterMark: 100,
    lowWaterMark: 20,
    onPause: () => events.push("pause"),
    onResume: () => events.push("resume"),
  });
  const backend = {
    setSessionFlowPaused: (_sessionId: string, paused: boolean) => {
      events.push(paused ? "ipc-pause" : "ipc-resume");
    },
    ackSessionFlow: () => {},
  };

  flow.received(FLOW_LOW_WATER_MARK + 1024);
  let release: (() => void) | null = null;
  enqueueTerminalWrite(term, 30, (done) => {
    release = done;
  });
  prioritizeTerminalInput(term, "sess-1", flow, backend);
  release?.();

  assert.ok(events.includes("ipc-resume"));
});