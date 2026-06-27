import type { Terminal as XTerm } from "@xterm/xterm";

import type { TerminalSessionStartersContext } from "./createTerminalSessionStarters.types";
import { FLOW_LOW_WATER_MARK } from "./terminalFlowConstants";
import type { OutputFlowController } from "./outputFlowController";
import {
  abortTerminalWriteCoalescer,
  resetTerminalWriteCoalescer,
} from "./terminalWriteCoalescer";
import {
  clearDeferredTerminalWriteAck,
  getDeferredTerminalWriteAckBytes,
} from "./terminalWriteAckDeferral";
import {
  abortTerminalWriteQueue,
  getTerminalWriteQueueDepth,
} from "./terminalWriteQueue";
import {
  ackTerminalSessionFlow,
  clearTerminalSessionFlowAck,
  flushTerminalSessionFlowAck,
} from "./terminalFlowAckBuffer";

type FlowBackend = {
  setSessionFlowPaused?: (sessionId: string, paused: boolean) => void;
  ackSessionFlow?: (sessionId: string, bytes: number) => void;
};

type ResumeScheduler = (callback: () => void) => void;

export type TerminalInputPrioritySnapshot = {
  sessionId: string | null;
  backlogBytes: number;
  writeQueueDepth: number;
  deferredAckBytes: number;
  ackAfterInputBytes: number;
  scheduledBackendResume: boolean;
  skippedReason?: "missing-session" | "below-threshold";
};

const scheduleAfterCurrentInput: ResumeScheduler = (callback) => {
  setTimeout(callback, 0);
};

const acknowledgeDroppedBytes = (
  flow: OutputFlowController | undefined,
  bytes: number,
  backend: FlowBackend,
  sessionId: string | null,
) => {
  if (bytes <= 0) return;
  flow?.written(bytes);
  ackTerminalSessionFlow(backend, sessionId, bytes);
  if (sessionId) {
    flushTerminalSessionFlowAck(sessionId);
    backend.setSessionFlowPaused?.(sessionId, false);
  }
};

export const releaseTerminalFlowOutputForTerm = (
  term: XTerm,
  backend: FlowBackend,
  sessionId: string | null,
  flow: OutputFlowController | undefined,
  options: { resumeBackend?: boolean } = {},
): void => {
  const resumeBackend = options.resumeBackend !== false;
  const onDropped = (bytes: number) => {
    acknowledgeDroppedBytes(flow, bytes, backend, sessionId);
  };

  abortTerminalWriteCoalescer(term, onDropped);
  abortTerminalWriteQueue(term, onDropped);
  const deferredAck = clearDeferredTerminalWriteAck(term);
  if (deferredAck > 0) {
    ackTerminalSessionFlow(backend, sessionId, deferredAck);
  }
  flow?.reset({ resume: resumeBackend });
  if (sessionId) {
    flushTerminalSessionFlowAck(sessionId);
    if (resumeBackend) {
      backend.setSessionFlowPaused?.(sessionId, false);
    }
    clearTerminalSessionFlowAck(sessionId);
  }
  resetTerminalWriteCoalescer(term);
};

export const teardownTerminalOutputPipeline = (
  ctx: TerminalSessionStartersContext,
  term: XTerm,
  sessionId: string | null,
  flow: OutputFlowController,
): void => {
  releaseTerminalFlowOutputForTerm(term, ctx.terminalBackend, sessionId, flow);
};

export const prioritizeTerminalInput = (
  term: XTerm,
  sessionId: string | null,
  flow: OutputFlowController | undefined,
  backend: FlowBackend,
  scheduleResume: ResumeScheduler = scheduleAfterCurrentInput,
): TerminalInputPrioritySnapshot => {
  if (!sessionId) {
    return {
      sessionId,
      backlogBytes: 0,
      writeQueueDepth: 0,
      deferredAckBytes: 0,
      ackAfterInputBytes: 0,
      scheduledBackendResume: false,
      skippedReason: "missing-session",
    };
  }

  const backlog = flow?.pendingBytes() ?? 0;
  const queueDepth = getTerminalWriteQueueDepth(term);
  const deferredAck = getDeferredTerminalWriteAckBytes(term);
  if (backlog <= FLOW_LOW_WATER_MARK && queueDepth === 0 && deferredAck === 0) {
    return {
      sessionId,
      backlogBytes: backlog,
      writeQueueDepth: queueDepth,
      deferredAckBytes: deferredAck,
      ackAfterInputBytes: 0,
      scheduledBackendResume: false,
      skippedReason: "below-threshold",
    };
  }

  let ackAfterInput = 0;

  const onDropped = (bytes: number) => {
    if (bytes <= 0) return;
    ackAfterInput += bytes;
  };

  abortTerminalWriteCoalescer(term, onDropped);
  abortTerminalWriteQueue(term, onDropped);
  const flushedDeferredAck = clearDeferredTerminalWriteAck(term);
  if (flushedDeferredAck > 0) {
    ackAfterInput += flushedDeferredAck;
  }
  flow?.reset({ resume: false });
  scheduleResume(() => {
    if (ackAfterInput > 0) {
      ackTerminalSessionFlow(backend, sessionId, ackAfterInput);
    }
    flushTerminalSessionFlowAck(sessionId);
    backend.setSessionFlowPaused?.(sessionId, false);
  });

  return {
    sessionId,
    backlogBytes: backlog,
    writeQueueDepth: queueDepth,
    deferredAckBytes: deferredAck,
    ackAfterInputBytes: ackAfterInput,
    scheduledBackendResume: true,
  };
};
