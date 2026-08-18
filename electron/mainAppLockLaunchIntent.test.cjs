const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const test = require("node:test");

const source = readFileSync(require.resolve("./main.cjs"), "utf8");

function readFunction(name, nextName) {
  const start = source.indexOf(`async function ${name}`);
  const end = source.indexOf(`function ${nextName}`, start + 1);
  assert.notEqual(start, -1, `${name} must exist`);
  assert.notEqual(end, -1, `${nextName} must exist after ${name}`);
  return source.slice(start, end);
}

test("locked startup launch intents wait for unlock instead of expiring", () => {
  for (const [name, nextName] of [
    ["deliverJmsDeepLink", "flushPendingJmsDeepLinks"],
    ["deliverTelnetDeepLink", "flushPendingSshDeepLinks"],
    ["deliverOpenTerminalPath", "flushPendingOpenTerminalPaths"],
  ]) {
    const functionSource = readFunction(name, nextName);
    assert.match(functionSource, /timeoutMs: 0/);
    assert.doesNotMatch(functionSource, /15000|30000/);
  }
});

test("failed locked launch-intent deliveries return to their queues", () => {
  const jmsFlush = readFunction("flushPendingJmsDeepLinks", "deliverSshDeepLink");
  const telnetFlush = readFunction("flushPendingTelnetDeepLinks", "deliverOpenTerminalPath");
  const terminalFlush = readFunction("flushPendingOpenTerminalPaths", "hasPendingColdStartLaunchIntents");

  assert.match(jmsFlush, /pendingJmsDeepLinkUrls\.unshift\(rawUrl\)/);
  assert.match(telnetFlush, /pendingTelnetDeepLinkUrls\.unshift\(rawUrl\)/);
  assert.match(terminalFlush, /pendingOpenTerminalPaths\.unshift\(targetPath\)/);
});
