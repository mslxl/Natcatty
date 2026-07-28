import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { act, create, type ReactTestRenderer } from "react-test-renderer";

import {
  MAX_PENDING_SCRIPT_RECORDING_INPUT_CHARS,
  SCRIPT_RECORDING_LIMIT_EVENT,
  useScriptRecorder,
} from "./useScriptRecorder";

test("oversized unsubmitted recording input stops explicitly and preserves earlier steps", async (t) => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  const eventTarget = new EventTarget() as EventTarget & Record<string, unknown>;
  let stopCalls = 0;
  Object.assign(eventTarget, {
    netcatty: {
      scriptRecordingStart: async () => ({ ok: true }),
      scriptRecordingStop: async () => {
        stopCalls += 1;
        return {
          steps: [{ type: "send", value: "kept" }],
          code: "await nct.screen.sendLine('kept');",
        };
      },
    },
    setInterval,
    clearInterval,
  });
  Object.defineProperty(globalThis, "window", { configurable: true, value: eventTarget });
  t.after(() => {
    if (previousWindow) Object.defineProperty(globalThis, "window", previousWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });

  let recorder: ReturnType<typeof useScriptRecorder> | null = null;
  let renderer: ReactTestRenderer | null = null;
  let limitDetail: { sessionId: string; code: string } | null = null;
  eventTarget.addEventListener(SCRIPT_RECORDING_LIMIT_EVENT, (event) => {
    limitDetail = (event as CustomEvent<{ sessionId: string; code: string }>).detail;
  });

  function Probe() {
    recorder = useScriptRecorder("session-1");
    return null;
  }

  await act(async () => { renderer = create(React.createElement(Probe)); });
  await act(async () => { await recorder!.startRecording(); });
  assert.equal(recorder!.isRecording, true);

  await act(async () => {
    recorder!.recordInput("x".repeat(MAX_PENDING_SCRIPT_RECORDING_INPUT_CHARS + 1));
    await new Promise((resolve) => setImmediate(resolve));
  });

  assert.equal(stopCalls, 1);
  assert.equal(recorder!.isRecording, false);
  assert.equal(limitDetail?.sessionId, "session-1");
  assert.equal(limitDetail?.code, "await nct.screen.sendLine('kept');");
  await act(async () => renderer!.unmount());
});
