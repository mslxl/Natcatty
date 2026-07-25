import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const pluginConnectionSectionSource = readFileSync(new URL("../PluginConnectionSection.tsx", import.meta.url), "utf8");
const pluginAuthenticationHostSource = readFileSync(new URL("./PluginAuthenticationHost.tsx", import.meta.url), "utf8");
const connectionHookSource = readFileSync(new URL("../../application/state/usePluginConnectionSectionState.ts", import.meta.url), "utf8");
const authenticationHookSource = readFileSync(new URL("../../application/state/usePluginAuthenticationChallenges.ts", import.meta.url), "utf8");

test("plugin connection section delegates provider discovery and credential catalog state to application state", () => {
  assert.match(connectionHookSource, /pluginExtensionBridge\.listProviders\("connection"\)/u);
  assert.match(connectionHookSource, /pluginExtensionBridge\.subscribeCredentialCatalog/u);
  assert.doesNotMatch(pluginConnectionSectionSource, /pluginExtensionBridge/u);
});

test("plugin authentication host delegates challenge lifecycle effects to application state", () => {
  assert.match(authenticationHookSource, /pluginExtensionBridge\.onAuthenticationChallenge/u);
  assert.match(authenticationHookSource, /pluginExtensionBridge\.respondAuthenticationChallenge/u);
  assert.doesNotMatch(pluginAuthenticationHostSource, /pluginExtensionBridge/u);
});
