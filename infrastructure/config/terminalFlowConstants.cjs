"use strict";

// Match VS Code FlowControlConstants (terminal.ts).
const FLOW_HIGH_WATER_MARK = 100_000;
const FLOW_LOW_WATER_MARK = 5_000;
/** Batched IPC ACK size (VS Code CharCountAckSize). Must be <= LOW watermark. */
const FLOW_CHAR_COUNT_ACK_SIZE = 5_000;

/** Normal rAF coalescer ceiling when the display refresh is throttled. */
const MAX_PENDING_WRITE_COALESCE_BYTES = 1024 * 1024;
/** Tighter coalescer cap when renderer flow or write queue is in flood mode. */
const MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD = 128 * 1024;

/** xterm.js flow-control guide: small writes may omit the callback. */
const XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES = 1024;
/** Batch deferred xterm write callbacks / flow acks during floods. */
const XTERM_WRITE_CALLBACK_BATCH_BYTES = 100_000;

module.exports = {
  FLOW_HIGH_WATER_MARK,
  FLOW_LOW_WATER_MARK,
  FLOW_CHAR_COUNT_ACK_SIZE,
  MAX_PENDING_WRITE_COALESCE_BYTES,
  MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD,
  XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES,
  XTERM_WRITE_CALLBACK_BATCH_BYTES,
};