"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  assertPluginContextKey,
  evaluateContextKeyExpression,
  parseContextKeyExpression,
} = require("./contextKeys.cjs");

test("Context Keys evaluate a bounded host-owned grammar", () => {
  const context = {
    "terminal.connected": true,
    "terminal.kind": "ssh",
    "com.example.mode": "advanced",
    allowed: ["ssh", "telnet"],
  };
  assert.equal(evaluateContextKeyExpression(
    "terminal.connected && (terminal.kind == 'ssh' || com.example.mode == 'safe')",
    context,
  ), true);
  assert.equal(evaluateContextKeyExpression("terminal.kind in allowed", context), true);
  assert.equal(evaluateContextKeyExpression("terminal.kind not in allowed", context), false);
});

test("Context Keys fail closed for invalid or over-complex expressions", () => {
  assert.equal(evaluateContextKeyExpression("globalThis.constructor", {}), false);
  assert.equal(evaluateContextKeyExpression("key &&", { key: true }), false);
  assert.throws(() => parseContextKeyExpression("x || ".repeat(300) + "x"), /length|complex/u);
});

test("plugin-created Context Keys must use the owning namespace", () => {
  assert.equal(assertPluginContextKey("com.example.plugin", "com.example.plugin.ready"), "com.example.plugin.ready");
  assert.throws(() => assertPluginContextKey("com.example.plugin", "terminal.connected"), /namespaced/u);
});
