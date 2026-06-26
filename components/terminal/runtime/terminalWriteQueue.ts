import type { Terminal as XTerm } from "@xterm/xterm";

export const MAX_WRITE_QUEUE_ITEMS = 32;
export const MAX_WRITE_QUEUE_BYTES = 512 * 1024;

type QueuedWrite = {
  bytes: number;
  run: () => void;
};

type TerminalWriteQueue = {
  writing: boolean;
  pending: QueuedWrite[];
  pendingBytes: number;
  floodMode: boolean;
  onDropped?: (bytes: number) => void;
};

const terminalWriteQueues = new WeakMap<XTerm, TerminalWriteQueue>();
const terminalWriteQueueDropHandlers = new WeakMap<XTerm, (bytes: number) => void>();

const getOrCreateQueue = (term: XTerm): TerminalWriteQueue => {
  let queue = terminalWriteQueues.get(term);
  if (!queue) {
    queue = {
      writing: false,
      pending: [],
      pendingBytes: 0,
      floodMode: false,
      onDropped: terminalWriteQueueDropHandlers.get(term),
    };
    terminalWriteQueues.set(term, queue);
  }
  return queue;
};

const scheduleNextTerminalWrite = (term: XTerm, queue: TerminalWriteQueue) => {
  const next = queue.pending.shift();
  if (!next) {
    queue.writing = false;
    queue.floodMode = false;
    terminalWriteQueues.delete(term);
    return;
  }

  queue.pendingBytes -= next.bytes;
  if (queue.pendingBytes < 0) queue.pendingBytes = 0;
  queue.writing = true;
  next.run();
};

const collapseFloodQueue = (queue: TerminalWriteQueue) => {
  if (queue.pending.length <= 1) {
    return;
  }
  let droppedBytes = 0;
  const latest = queue.pending[queue.pending.length - 1]!;
  for (let index = 0; index < queue.pending.length - 1; index += 1) {
    droppedBytes += queue.pending[index]!.bytes;
  }
  queue.pending = [latest];
  queue.pendingBytes = latest.bytes;
  queue.floodMode = true;
  if (droppedBytes > 0) {
    queue.onDropped?.(droppedBytes);
  }
};

export const setTerminalWriteQueueDropHandler = (
  term: XTerm,
  onDropped?: (bytes: number) => void,
): void => {
  if (onDropped) {
    terminalWriteQueueDropHandlers.set(term, onDropped);
  } else {
    terminalWriteQueueDropHandlers.delete(term);
  }
  const queue = terminalWriteQueues.get(term);
  if (queue && onDropped) {
    queue.onDropped = onDropped;
  }
};

export const getTerminalWriteQueueDepth = (term: XTerm): number =>
  terminalWriteQueues.get(term)?.pending.length ?? 0;

export const isTerminalWriteQueueInFloodMode = (term: XTerm): boolean =>
  terminalWriteQueues.get(term)?.floodMode ?? false;

export const enqueueTerminalWrite = (
  term: XTerm,
  bytes: number,
  write: (done: () => void) => void,
  options: { onDropped?: (bytes: number) => void } = {},
): void => {
  const queue = getOrCreateQueue(term);
  if (options.onDropped) {
    queue.onDropped = options.onDropped;
  } else if (!queue.onDropped) {
    queue.onDropped = terminalWriteQueueDropHandlers.get(term);
  }

  const shouldCollapse = queue.floodMode
    || queue.pending.length >= MAX_WRITE_QUEUE_ITEMS
    || queue.pendingBytes + bytes > MAX_WRITE_QUEUE_BYTES;

  queue.pending.push({
    bytes,
    run: () => {
      write(() => scheduleNextTerminalWrite(term, queue));
    },
  });
  queue.pendingBytes += bytes;

  if (shouldCollapse) {
    collapseFloodQueue(queue);
  }

  if (!queue.writing) {
    scheduleNextTerminalWrite(term, queue);
  }
};

/** Drop queued output frames without writing them to xterm. */
export const abortTerminalWriteQueue = (
  term: XTerm,
  onDropped?: (bytes: number) => void,
): void => {
  const queue = terminalWriteQueues.get(term);
  if (!queue) return;

  let droppedBytes = queue.pendingBytes;
  if (queue.writing) {
    // The in-flight write is not counted in pendingBytes once dequeued.
    droppedBytes = queue.pending.reduce((sum, item) => sum + item.bytes, 0);
  }

  queue.pending = [];
  queue.pendingBytes = 0;
  queue.writing = false;
  queue.floodMode = false;
  terminalWriteQueues.delete(term);

  if (droppedBytes > 0) {
    onDropped?.(droppedBytes);
  }
};