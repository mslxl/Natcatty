import assert from "node:assert/strict";
import test from "node:test";
import { pluginAuthenticationResponseErrorMessage } from "./usePluginAuthenticationChallenges";

test("plugin authentication response errors are bounded before display", () => {
  assert.equal(pluginAuthenticationResponseErrorMessage(new Error("bridge down")), "bridge down");
  assert.equal(pluginAuthenticationResponseErrorMessage("  failed  "), "failed");
  assert.equal(pluginAuthenticationResponseErrorMessage({}), "");
  assert.equal(pluginAuthenticationResponseErrorMessage(new Error("x".repeat(600))).length, 512);
});
