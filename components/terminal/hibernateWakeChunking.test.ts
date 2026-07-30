import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import type { Terminal as XTerm } from "@xterm/xterm";

import { applyHibernateWakeToTerminal } from "./terminalHibernateRuntime.ts";
import { writeTerminalPayloadChunked } from "./terminalReplay.ts";

test("writeTerminalPayloadChunked splits large buffers (shipped wake helper)", async () => {
  const writes: string[] = [];
  const term = {
    write: (data: string, cb: () => void) => {
      writes.push(data);
      cb();
    },
  } as unknown as XTerm;

  const payload = "y".repeat(50_000);
  await writeTerminalPayloadChunked(term, payload, { chunkBytes: 8_192 });
  assert.ok(writes.length >= 2, `expected multiple chunks, got ${writes.length}`);
  assert.equal(writes.join(""), payload);
});

test("applyHibernateWakeToTerminal defers scrollback to idle and chunks viewport first", async () => {
  const writes: string[] = [];
  const term = {
    rows: 24,
    write: (data: string, cb?: () => void) => {
      writes.push(data);
      cb?.();
    },
    refresh: () => {},
  } as unknown as XTerm;

  const runtime = {
    ensureWebglRenderer: () => {},
    clearTextureAtlas: () => {},
  };

  let idleScheduled = false;
  const originalRic = globalThis.requestIdleCallback;
  // @ts-expect-error test override
  globalThis.requestIdleCallback = (cb: () => void) => {
    idleScheduled = true;
    // Do not run immediately — proves scrollback is deferred off the wake path.
    setTimeout(cb, 0);
    return 1;
  };

  try {
    const viewport = "VIEWPORT";
    const scrollback = "S".repeat(40_000);
    await applyHibernateWakeToTerminal(
      term,
      runtime as never,
      {
        snapshot: viewport,
        viewportSnapshot: viewport,
        scrollbackSnapshot: scrollback,
        pendingBuffer: "",
        alternateScreen: false,
      },
      { replayOptions: { chunkBytes: 8_192 } },
    );

    // Viewport written on the wake path; scrollback scheduled idle.
    assert.ok(writes.join("").includes(viewport));
    assert.equal(idleScheduled, true);
    assert.ok(
      !writes.join("").includes(scrollback.slice(0, 1000)),
      "scrollback must not be written synchronously on wake",
    );

    // Flush idle callback.
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.ok(writes.join("").includes(scrollback.slice(0, 100)), "scrollback eventually applied");
  } finally {
    if (originalRic) {
      globalThis.requestIdleCallback = originalRic;
    } else {
      // @ts-expect-error cleanup
      delete globalThis.requestIdleCallback;
    }
  }
});

test("hibernate runtime source schedules scrollback via requestIdleCallback", () => {
  const source = readFileSync(new URL("./terminalHibernateRuntime.ts", import.meta.url), "utf8");
  assert.match(source, /requestIdleCallback|scheduleIdle/);
  assert.match(source, /writeTerminalPayloadChunked\(term, scrollback/);
  assert.match(source, /writeTerminalReplaySequence\(term, \[viewport, payload\.pendingBuffer\]/);
});
