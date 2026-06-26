/**
 * Coalesces PTY output chunks into one xterm.write() per animation frame.
 *
 * Agent CLIs (Codex, Claude Code) emit full-screen repaints as many small PTY
 * chunks. Writing each chunk individually triggers an xterm parse/render cycle
 * per chunk, which can tear TUI frames (missing box borders, clipped bottom
 * rows). Batching to the display refresh rate keeps rendering atomic per frame.
 *
 * Ported from superset-sh/superset (issues #2241 / #2244):
 * apps/desktop/src/renderer/lib/terminal/write-coalescer.ts
 */

/** Pending-byte ceiling when rAF is throttled (hidden window). */
export const MAX_PENDING_WRITE_COALESCE_BYTES = 1024 * 1024;

export type WriteCoalescer = {
  push(chunk: string): void;
  /** Flush pending bytes synchronously before ordered writes (exit notices). */
  flushSync(): void;
  /** Drop pending bytes without writing (flood recovery / teardown). */
  abort(onDropped?: (bytes: number) => void): void;
  pendingBytes(): number;
  dispose(): void;
};

type ScheduleWriteFrame = (callback: () => void) => (() => void) | null;

const scheduleWriteFrame = (callback: () => void): (() => void) | null => {
  if (typeof globalThis.requestAnimationFrame === "function") {
    const frameId = globalThis.requestAnimationFrame(callback);
    return () => {
      if (typeof globalThis.cancelAnimationFrame === "function") {
        globalThis.cancelAnimationFrame(frameId);
      }
    };
  }

  return null;
};

export const createWriteCoalescer = (
  write: (data: string) => void,
  options: { scheduleFrame?: ScheduleWriteFrame } = {},
): WriteCoalescer => {
  let pending: string[] = [];
  let pendingBytes = 0;
  let cancelPendingFrame: (() => void) | null = null;
  let disposed = false;
  const scheduleFrame = options.scheduleFrame ?? scheduleWriteFrame;

  const cancelScheduledFrame = (): void => {
    if (cancelPendingFrame !== null) {
      cancelPendingFrame();
      cancelPendingFrame = null;
    }
  };

  const flushSync = (): void => {
    cancelScheduledFrame();
    if (pendingBytes === 0) {
      return;
    }
    const batch = pending.length === 1 ? pending[0]! : pending.join("");
    pending = [];
    pendingBytes = 0;
    write(batch);
  };

  const abort = (onDropped?: (bytes: number) => void): void => {
    cancelScheduledFrame();
    if (pendingBytes === 0) {
      return;
    }
    const dropped = pendingBytes;
    pending = [];
    pendingBytes = 0;
    onDropped?.(dropped);
  };

  const push = (chunk: string): void => {
    if (disposed || chunk.length === 0) {
      return;
    }
    pending.push(chunk);
    pendingBytes += chunk.length;
    if (pendingBytes > MAX_PENDING_WRITE_COALESCE_BYTES) {
      flushSync();
      return;
    }
    if (cancelPendingFrame === null) {
      const cancelFrame = scheduleFrame(() => {
        cancelPendingFrame = null;
        flushSync();
      });
      if (cancelFrame === null) {
        flushSync();
        return;
      }
      cancelPendingFrame = cancelFrame;
    }
  };

  return {
    push,
    flushSync,
    abort,
    pendingBytes: () => pendingBytes,
    dispose() {
      if (disposed) {
        return;
      }
      flushSync();
      disposed = true;
    },
  };
};
