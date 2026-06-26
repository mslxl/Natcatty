import type { Terminal as XTerm } from "@xterm/xterm";

import { XTERM_WRITE_CALLBACK_BATCH_BYTES } from "./terminalFlowConstants";

/** Ingress bytes written to xterm but not yet reported to main-process IPC ACK. */
const deferredIpcAckBytesByTerm = new WeakMap<XTerm, number>();

export const getDeferredTerminalWriteAckBytes = (term: XTerm): number =>
  deferredIpcAckBytesByTerm.get(term) ?? 0;

export const accumulateDeferredTerminalWriteAck = (
  term: XTerm,
  bytes: number,
): number => {
  if (bytes <= 0) return getDeferredTerminalWriteAckBytes(term);
  const next = getDeferredTerminalWriteAckBytes(term) + bytes;
  deferredIpcAckBytesByTerm.set(term, next);
  return next;
};

export const clearDeferredTerminalWriteAck = (term: XTerm): number => {
  const bytes = deferredIpcAckBytesByTerm.get(term) ?? 0;
  deferredIpcAckBytesByTerm.delete(term);
  return bytes;
};

export const shouldDeferTerminalWriteCallback = (
  displayBytes: number,
  deferredIngressBytes: number,
  ingressBytes: number,
  fastPathMaxBytes: number,
  batchBytes: number = XTERM_WRITE_CALLBACK_BATCH_BYTES,
): boolean =>
  displayBytes <= fastPathMaxBytes
  && deferredIngressBytes + ingressBytes < batchBytes;

export const resetDeferredTerminalWriteAck = (term: XTerm): void => {
  deferredIpcAckBytesByTerm.delete(term);
};