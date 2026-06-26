import type { Terminal as XTerm } from "@xterm/xterm";

import { XTERM_WRITE_CALLBACK_BATCH_BYTES } from "./terminalFlowConstants";

const deferredAckBytesByTerm = new WeakMap<XTerm, number>();

export const getDeferredTerminalWriteAckBytes = (term: XTerm): number =>
  deferredAckBytesByTerm.get(term) ?? 0;

export const accumulateDeferredTerminalWriteAck = (
  term: XTerm,
  bytes: number,
): number => {
  if (bytes <= 0) return getDeferredTerminalWriteAckBytes(term);
  const next = getDeferredTerminalWriteAckBytes(term) + bytes;
  deferredAckBytesByTerm.set(term, next);
  return next;
};

export const clearDeferredTerminalWriteAck = (term: XTerm): number => {
  const bytes = deferredAckBytesByTerm.get(term) ?? 0;
  deferredAckBytesByTerm.delete(term);
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
  deferredAckBytesByTerm.delete(term);
};