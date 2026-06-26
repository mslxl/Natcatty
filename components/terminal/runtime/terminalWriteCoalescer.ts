import type { Terminal as XTerm } from "@xterm/xterm";

import { createWriteCoalescer, type WriteCoalescer } from "./writeCoalescer.ts";

const terminalWriteCoalescers = new WeakMap<XTerm, WriteCoalescer>();
const terminalWriteCoalescerIngress = new WeakMap<XTerm, number>();

const takePendingIngressBytes = (term: XTerm, fallback = 0): number => {
  const pending = terminalWriteCoalescerIngress.get(term) ?? fallback;
  terminalWriteCoalescerIngress.delete(term);
  return pending;
};

export const enqueueCoalescedTerminalWrite = (
  term: XTerm,
  data: string,
  writeNow: (data: string, ingressBytes: number) => void,
  ingressBytes: number = data.length,
): void => {
  terminalWriteCoalescerIngress.set(
    term,
    (terminalWriteCoalescerIngress.get(term) ?? 0) + ingressBytes,
  );

  let coalescer = terminalWriteCoalescers.get(term);
  if (!coalescer) {
    coalescer = createWriteCoalescer((batch) => {
      const batchIngress = takePendingIngressBytes(term, batch.length);
      writeNow(batch, batchIngress);
    });
    terminalWriteCoalescers.set(term, coalescer);
  }
  coalescer.push(data);
};

export const flushTerminalWriteCoalescer = (term: XTerm): void => {
  terminalWriteCoalescers.get(term)?.flushSync();
};

export const resetTerminalWriteCoalescer = (term: XTerm): void => {
  terminalWriteCoalescers.get(term)?.dispose();
  terminalWriteCoalescers.delete(term);
  terminalWriteCoalescerIngress.delete(term);
};

export const getTerminalWriteCoalescerPendingBytes = (term: XTerm): number =>
  terminalWriteCoalescers.get(term)?.pendingBytes() ?? 0;

export const getTerminalWriteCoalescerPendingIngressBytes = (term: XTerm): number =>
  terminalWriteCoalescerIngress.get(term) ?? 0;

export const abortTerminalWriteCoalescer = (
  term: XTerm,
  onDropped?: (bytes: number) => void,
): void => {
  const coalescer = terminalWriteCoalescers.get(term);
  if (!coalescer) return;
  const ingressDropped = takePendingIngressBytes(
    term,
    coalescer.pendingBytes(),
  );
  coalescer.abort();
  if (ingressDropped > 0) {
    onDropped?.(ingressDropped);
  }
};