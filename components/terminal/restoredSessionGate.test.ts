import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  getInitialTerminalStatus,
  shouldStartTerminalBackend,
} from "./restoredSessionGate.ts";

test("restored disconnected sessions initialize as disconnected", () => {
  assert.equal(
    getInitialTerminalStatus({ status: "disconnected", restoreState: "restored-disconnected" }),
    "disconnected",
  );
});

test("normal sessions initialize as connecting", () => {
  assert.equal(getInitialTerminalStatus({ status: "connecting" }), "connecting");
  assert.equal(getInitialTerminalStatus({ status: "disconnected" }), "connecting");
});

test("restored disconnected sessions do not start terminal backend", () => {
  assert.equal(
    shouldStartTerminalBackend({ status: "disconnected", restoreState: "restored-disconnected" }),
    false,
  );
  assert.equal(shouldStartTerminalBackend({ status: "connecting" }), true);
});

test("restored disconnected sessions still create a terminal runtime before skipping backend startup", () => {
  const source = readFileSync(new URL("./useTerminalEffects.ts", import.meta.url), "utf8");
  const runtimeIndex = source.indexOf("const runtime = createXTermRuntime");
  const backendGateIndex = source.indexOf("if (!shouldStartTerminalBackend({ status, restoreState }))");

  assert.notEqual(runtimeIndex, -1);
  assert.notEqual(backendGateIndex, -1);
  assert.ok(
    runtimeIndex < backendGateIndex,
    "restored placeholders need an xterm runtime so manual reconnect has a terminal to reuse",
  );
});

test("manual reconnect captures restore cwd intent before clearing restored state", () => {
  const source = readFileSync(new URL("../Terminal.tsx", import.meta.url), "utf8");
  const importIndex = source.indexOf("resolveRestoreCwdIntent");
  const refIndex = source.indexOf("const restoreCwdIntentRef = useRef");
  const contextIndex = source.indexOf("restoreCwdIntentRef,");
  const captureCallIndex = source.indexOf("const restoreCwdIntent = resolveRestoreCwdIntent");
  const captureAssignIndex = source.indexOf("restoreCwdIntentRef.current = restoreCwdIntent", captureCallIndex);
  const bootActiveIndex = source.indexOf("isBootActiveRef.current = true", captureAssignIndex);
  const connectingIndex = source.indexOf('updateStatus("connecting")');
  const startNewSessionIndex = source.indexOf("const startNewSession = () =>", connectingIndex);

  assert.notEqual(importIndex, -1);
  assert.notEqual(refIndex, -1);
  assert.notEqual(contextIndex, -1);
  assert.notEqual(captureCallIndex, -1);
  assert.notEqual(captureAssignIndex, -1);
  assert.notEqual(bootActiveIndex, -1);
  assert.notEqual(connectingIndex, -1);
  assert.notEqual(startNewSessionIndex, -1);
  assert.ok(
    captureCallIndex < captureAssignIndex && captureAssignIndex < connectingIndex,
    "manual retry must capture cwd intent while restoreState is still available",
  );
  assert.ok(
    bootActiveIndex < startNewSessionIndex,
    "manual retry must reactivate the boot guard before opening a backend session",
  );
});
