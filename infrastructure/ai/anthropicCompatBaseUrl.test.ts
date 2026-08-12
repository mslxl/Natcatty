import assert from "node:assert/strict";
import test from "node:test";

import {
  anthropicBaseIncludesV1,
  normalizeAnthropicSdkBaseURL,
  stripTrailingSlashes,
} from "./anthropicCompatBaseUrl";

test("stripTrailingSlashes trims and drops trailing slashes", () => {
  assert.equal(stripTrailingSlashes("  https://host/v1/  "), "https://host/v1");
  assert.equal(stripTrailingSlashes(""), "");
});

test("anthropicBaseIncludesV1 detects AI SDK style bases", () => {
  assert.equal(anthropicBaseIncludesV1("https://api.anthropic.com/v1"), true);
  assert.equal(anthropicBaseIncludesV1("https://api.anthropic.com/v1/"), true);
  assert.equal(anthropicBaseIncludesV1("https://gateway.example/api/v1"), true);
  assert.equal(anthropicBaseIncludesV1("https://api.anthropic.com"), false);
  assert.equal(anthropicBaseIncludesV1("https://gateway.example/"), false);
});

test("normalizeAnthropicSdkBaseURL accepts Claude Code and AI SDK conventions", () => {
  assert.equal(
    normalizeAnthropicSdkBaseURL("https://api.anthropic.com"),
    "https://api.anthropic.com/v1",
  );
  assert.equal(
    normalizeAnthropicSdkBaseURL("https://gateway.example/"),
    "https://gateway.example/v1",
  );
  assert.equal(
    normalizeAnthropicSdkBaseURL("https://gateway.example/v1"),
    "https://gateway.example/v1",
  );
  assert.equal(
    normalizeAnthropicSdkBaseURL("https://gateway.example/v1/"),
    "https://gateway.example/v1",
  );
  assert.equal(normalizeAnthropicSdkBaseURL("   "), "");
});
