const test = require("node:test");
const assert = require("node:assert/strict");
const {
  getDriver,
  listBackends,
  hasCodebuddyQueryOnlyOptions,
} = require("./index.cjs");

test("registry exposes SDK backends", () => {
  assert.deepEqual(listBackends().sort(), ["claude", "codebuddy", "codex", "copilot", "cursor", "opencode"]);
});

test("getDriver returns a driver with runTurn", () => {
  for (const key of ["claude", "codebuddy", "codex", "copilot", "cursor", "opencode"]) {
    const d = getDriver(key);
    assert.equal(typeof d.runTurn, "function", `${key} must expose runTurn`);
  }
});

test("getDriver throws on unknown backend", () => {
  assert.throws(() => getDriver("gemini"), /No SDK driver registered for backend: gemini/);
});

test("SDK drivers expose listModels; codex returns [] (no catalog)", async () => {
  for (const key of ["claude", "codebuddy", "codex", "copilot", "cursor", "opencode"]) {
    assert.equal(typeof getDriver(key).listModels, "function", `${key} must expose listModels`);
  }
  assert.deepEqual(await getDriver("codex").listModels({}), []);
});

test("CodeBuddy keeps V2 for SessionOptions fields and falls back for query-only fields", () => {
  assert.equal(hasCodebuddyQueryOnlyOptions({
    agents: { reviewer: { description: "Reviews changes", prompt: "Review" } },
    thinking: { type: "adaptive" },
    effort: "high",
  }), false);
  assert.equal(hasCodebuddyQueryOnlyOptions({ maxBudgetUsd: 1 }), true);
  assert.equal(hasCodebuddyQueryOnlyOptions({ sandbox: { enabled: true } }), true);
  assert.equal(hasCodebuddyQueryOnlyOptions({ fallbackModel: "fallback" }), true);
  assert.equal(hasCodebuddyQueryOnlyOptions({ enableFileCheckpointing: false }), true);
  assert.equal(hasCodebuddyQueryOnlyOptions({ outputFormat: { type: "json_schema" } }), true);
});
