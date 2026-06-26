import assert from "node:assert/strict";
import test from "node:test";

import type { Terminal as XTerm } from "@xterm/xterm";

import { createOutputFlowController } from "./outputFlowController.ts";
import {
  prioritizeTerminalInput,
  releaseTerminalFlowOutputForTerm,
  teardownTerminalOutputPipeline,
} from "./terminalOutputPipeline.ts";
import { FLOW_LOW_WATER_MARK } from "./terminalFlowConstants.ts";
import { enqueueTerminalWrite } from "./terminalWriteQueue.ts";
import { accumulateDeferredTerminalWriteAck } from "./terminalWriteAckDeferral.ts";
import { clearTerminalSessionFlowAck } from "./terminalFlowAckBuffer.ts";

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

test("releaseTerminalFlowOutputForTerm resumes renderer pause without a flow controller", () => {
  const term = createFakeTerm();
  const events: string[] = [];
  const backend = {
    setSessionFlowPaused: (_sessionId: string, paused: boolean) => {
      events.push(paused ? "ipc-pause" : "ipc-resume");
    },
    ackSessionFlow: () => {},
  };

  releaseTerminalFlowOutputForTerm(term, backend, "sess-1", undefined);

  assert.deepEqual(events, ["ipc-resume"]);
});

test("prioritizeTerminalInput flushes batched ack remainders after dropping bytes", () => {
  const term = createFakeTerm();
  const acked: number[] = [];
  const flow = createOutputFlowController({
    highWaterMark: 100,
    lowWaterMark: 20,
    onPause: () => {},
    onResume: () => {},
  });
  const backend = {
    setSessionFlowPaused: () => {},
    ackSessionFlow: (_sessionId: string, bytes: number) => {
      acked.push(bytes);
    },
  };

  flow.received(FLOW_LOW_WATER_MARK + 80);
  enqueueTerminalWrite(term, 50, () => {});
  enqueueTerminalWrite(term, 30, () => {});
  prioritizeTerminalInput(term, "sess-1", flow, backend);

  assert.deepEqual(acked, [30]);
});

test("prioritizeTerminalInput flushes deferred xterm write ack bytes", () => {
  clearTerminalSessionFlowAck("sess-1");
  const term = createFakeTerm();
  const acked: number[] = [];
  const flow = createOutputFlowController({
    highWaterMark: 100,
    lowWaterMark: 20,
    onPause: () => {},
    onResume: () => {},
  });
  const backend = {
    ackSessionFlow: (_sessionId: string, bytes: number) => {
      acked.push(bytes);
    },
    setSessionFlowPaused: () => {},
  };

  accumulateDeferredTerminalWriteAck(term, 42);
  prioritizeTerminalInput(term, "sess-1", flow, backend);

  assert.deepEqual(acked, [42]);
  assert.equal(flow.pendingBytes(), 0);
  clearTerminalSessionFlowAck("sess-1");
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