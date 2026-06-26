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
): void => {
  const onDropped = (bytes: number) => {
    acknowledgeDroppedBytes(flow, bytes, backend, sessionId);
  };

  abortTerminalWriteCoalescer(term, onDropped);
  abortTerminalWriteQueue(term, onDropped);
  const deferredAck = clearDeferredTerminalWriteAck(term);
  if (deferredAck > 0) {
    flow?.written(deferredAck);
    ackTerminalSessionFlow(backend, sessionId, deferredAck);
  }
  flow?.reset();
  if (sessionId) {
    flushTerminalSessionFlowAck(sessionId);
    backend.setSessionFlowPaused?.(sessionId, false);
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
): void => {
  if (!sessionId) return;

  const backlog = flow?.pendingBytes() ?? 0;
  const queueDepth = getTerminalWriteQueueDepth(term);
  const deferredAck = getDeferredTerminalWriteAckBytes(term);
  if (backlog <= FLOW_LOW_WATER_MARK && queueDepth === 0 && deferredAck === 0) {
    return;
  }

  const onDropped = (bytes: number) => {
    acknowledgeDroppedBytes(flow, bytes, backend, sessionId);
  };

  abortTerminalWriteCoalescer(term, onDropped);
  abortTerminalWriteQueue(term, onDropped);
  const flushedDeferredAck = clearDeferredTerminalWriteAck(term);
  if (flushedDeferredAck > 0) {
    flow?.written(flushedDeferredAck);
    ackTerminalSessionFlow(backend, sessionId, flushedDeferredAck);
  }
  flow?.reset();
  flushTerminalSessionFlowAck(sessionId);
  backend.setSessionFlowPaused?.(sessionId, false);
};