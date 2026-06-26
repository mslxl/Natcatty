"use strict";

/**
 * Coalescing output buffer for terminal/PTY data on its way to the renderer.
 *
 * Incoming shell data is accumulated and delivered to `sendFn` in batches to
 * keep IPC traffic down, but the batch is flushed on the *next event-loop turn*
 * (`setImmediate`) rather than after a fixed time interval. A fixed interval
 * adds that whole interval as latency to interactive echo — every keystroke
 * round-trips through the buffer and waits out the timer before it can paint.
 * Turn-based flushing coalesces only the data that has already arrived in the
 * current turn, so a single echoed keystroke is forwarded almost immediately
 * while bursts of output still collapse into one send.
 *
 * A byte cap still forces an immediate, synchronous flush so a flood of output
 * can't grow the buffer without bound between turns.
 *
 * @param {(data: string) => void} sendFn delivers an accumulated batch
 * @param {{
 *   maxBufferSize?: number,
 *   shouldAcceptOutput?: () => boolean,
 * }} [options]
 * @returns {{ bufferData: (data: string) => void, flush: () => void }}
 */
function createPtyOutputBuffer(sendFn, options = {}) {
  const maxBufferSize = options.maxBufferSize ?? 16384; // 16KB
  const shouldAcceptOutput = options.shouldAcceptOutput ?? (() => true);

  let dataBuffer = "";
  let scheduled = null;

  const cancelScheduled = () => {
    if (scheduled) {
      clearImmediate(scheduled);
      scheduled = null;
    }
  };

  const flushNow = () => {
    scheduled = null;
    if (dataBuffer.length > 0) {
      const pending = dataBuffer;
      dataBuffer = "";
      sendFn(pending);
    }
  };

  const bufferData = (data) => {
    if (!shouldAcceptOutput()) {
      return;
    }
    dataBuffer += data;
    if (dataBuffer.length >= maxBufferSize) {
      // Large enough to ship right now — don't wait for the turn flush.
      cancelScheduled();
      flushNow();
    } else if (!scheduled) {
      scheduled = setImmediate(flushNow);
    }
  };

  const flush = () => {
    cancelScheduled();
    flushNow();
  };

  return { bufferData, flush };
}

module.exports = { createPtyOutputBuffer };
