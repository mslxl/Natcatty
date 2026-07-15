"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const { createPortForwardService } = require("./portforwardService.cjs");

test("portforward service lists active tunnels from main bridge", async () => {
  const service = createPortForwardService({
    invokeVaultAgent: async () => ({ ok: true, rules: [] }),
  });
  const result = await service.listTunnels();
  assert.equal(result.ok, true);
  assert.ok(Array.isArray(result.tunnels));
});

test("portforward start delegates to vault agent bridge after approval path", async () => {
  let invokedOp = null;
  const service = createPortForwardService({
    invokeVaultAgent: async (op, params) => {
      invokedOp = op;
      return { ok: true, ruleId: params.ruleId };
    },
  });
  const result = await service.start({ ruleId: "rule-1", chatSessionId: "chat-1" });
  assert.equal(invokedOp, "portforward.start");
  assert.equal(result.ok, true);
});

test("portforward rule mutations delegate to the renderer vault", async () => {
  const calls = [];
  const service = createPortForwardService({
    invokeVaultAgent: async (op, params) => {
      calls.push({ op, params });
      return { ok: true };
    },
  });
  await service.createRule({ label: "Web" });
  await service.updateRule({ ruleId: "rule-1", localPort: 8081 });
  await service.duplicateRule({ ruleId: "rule-1" });
  await service.deleteRule({ ruleId: "rule-1" });
  assert.deepEqual(calls.map((call) => call.op), [
    "portforward.rules.create", "portforward.rules.update", "portforward.rules.duplicate", "portforward.rules.delete",
  ]);
});
