import test from "node:test";
import assert from "node:assert/strict";

import { startPluginConnectionWithBridge } from "./useTerminalBackend";

const startOptions = {
  requestId: "plugin-connection-test-request",
  sessionId: "session-plugin",
  providerId: "com.example.connection",
  configuration: { endpoint: "example.test" },
  columns: 120,
  rows: 32,
} satisfies NetcattyPluginConnectionStartRequest;

test("startPluginConnectionWithBridge uses the caller request ID and strips the renderer-only signal", async () => {
  const controller = new AbortController();
  let validationRequest: NetcattyExtensionProviderRequest | null = null;
  let startRequest: NetcattyPluginConnectionStartRequest | null = null;
  const bridge = {
    async invokePluginExtensionProvider(request: NetcattyExtensionProviderRequest) {
      validationRequest = request;
      return { valid: true };
    },
    async startPluginConnection(request: NetcattyPluginConnectionStartRequest) {
      startRequest = request;
      return {
        sessionId: request.sessionId,
        providerId: request.providerId,
        status: "connected" as const,
        diagnostics: [],
      };
    },
  };

  const opened = await startPluginConnectionWithBridge(bridge, {
    ...startOptions,
    signal: controller.signal,
  });

  assert.equal(validationRequest?.requestId, startOptions.requestId);
  assert.equal(startRequest?.requestId, startOptions.requestId);
  assert.equal("signal" in (startRequest as Record<string, unknown>), false);
  assert.equal(opened.sessionId, startOptions.sessionId);
});

test("startPluginConnectionWithBridge stops before connection start when cancelled after validation", async () => {
  const controller = new AbortController();
  let startCalled = false;
  const bridge = {
    async invokePluginExtensionProvider(request: NetcattyExtensionProviderRequest) {
      assert.equal(request.requestId, startOptions.requestId);
      controller.abort(new DOMException("Terminal closed", "AbortError"));
      return { valid: true };
    },
    async startPluginConnection(_request: NetcattyPluginConnectionStartRequest) {
      startCalled = true;
      throw new Error("connection start should not run after cancellation");
    },
  };

  await assert.rejects(
    startPluginConnectionWithBridge(bridge, {
      ...startOptions,
      signal: controller.signal,
    }),
    /Terminal closed/,
  );
  assert.equal(startCalled, false);
});

test("startPluginConnectionWithBridge rejects promptly when cancelled during validation", async () => {
  const controller = new AbortController();
  let resolveValidationStarted: (() => void) | null = null;
  const validationStarted = new Promise<void>((resolve) => { resolveValidationStarted = resolve; });
  let startCalled = false;
  const bridge = {
    async invokePluginExtensionProvider(request: NetcattyExtensionProviderRequest) {
      assert.equal(request.requestId, startOptions.requestId);
      resolveValidationStarted?.();
      await new Promise(() => {});
      return { valid: true };
    },
    async startPluginConnection(_request: NetcattyPluginConnectionStartRequest) {
      startCalled = true;
      throw new Error("connection start should not run after cancellation");
    },
  };

  const start = startPluginConnectionWithBridge(bridge, {
    ...startOptions,
    signal: controller.signal,
  });
  await validationStarted;
  controller.abort(new DOMException("Terminal closed", "AbortError"));

  await assert.rejects(start, /Terminal closed/);
  assert.equal(startCalled, false);
});
