const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const {
  GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS,
  buildGrokCliArgs,
  buildGrokMcpServerTomlSection,
  createLineBuffer,
  formatGrokErrorForUser,
  listGrokModels,
  mergeWorkspaceGrokMcpToml,
  parseGrokModelsOutput,
  resetGrokMcpMergeRefcountsForTests,
  resolveGrokPermissionFlags,
  resolveGrokToolIntegrationFlags,
  runGrokTurn,
  stripGrokMcpServerSection,
  translateGrokStreamEvent,
} = require("./grokDriver.cjs");

function makeEmitter() {
  const calls = [];
  return {
    calls,
    text: (value) => calls.push(["text", value]),
    reasoning: (value) => calls.push(["reasoning", value]),
    reasoningEnd: () => calls.push(["reasoningEnd"]),
    toolCall: (name, args, id) => calls.push(["toolCall", name, args, id]),
    toolResult: (id, result, name) => calls.push(["toolResult", id, result, name]),
    sessionId: (id) => calls.push(["sessionId", id]),
    planUpdate: (itemId, items, status) => calls.push(["planUpdate", itemId, items, status]),
    usage: (usage) => calls.push(["usage", usage]),
    emitDone: () => calls.push(["done"]),
    emitError: (message) => calls.push(["error", message]),
  };
}

test("resolveGrokPermissionFlags maps observer to plan and others to always-approve", () => {
  assert.deepEqual(resolveGrokPermissionFlags("observer"), ["--permission-mode", "plan"]);
  assert.deepEqual(resolveGrokPermissionFlags("confirm"), ["--always-approve"]);
  assert.deepEqual(resolveGrokPermissionFlags("auto"), ["--always-approve"]);
});

test("buildGrokCliArgs uses streaming-json and optional model/resume/cwd", () => {
  assert.deepEqual(
    buildGrokCliArgs({
      prompt: "hi",
      model: "grok-4.5",
      cwd: "/repo",
      resumeSessionId: "sess-1",
      permissionMode: "observer",
      toolIntegrationMode: "skills",
    }),
    [
      "--no-auto-update",
      "-p",
      "hi",
      "--output-format",
      "streaming-json",
      "-m",
      "grok-4.5",
      "--cwd",
      "/repo",
      "-r",
      "sess-1",
      "--permission-mode",
      "plan",
    ],
  );

  const autoArgs = buildGrokCliArgs({
    prompt: "go",
    permissionMode: "auto",
    toolIntegrationMode: "skills",
  });
  assert.ok(autoArgs.includes("--always-approve"));
  assert.ok(autoArgs.includes("--no-auto-update"));
  assert.ok(!autoArgs.includes("-m"));
});

test("resolveGrokToolIntegrationFlags locks local side-effect tools only in MCP mode", () => {
  assert.deepEqual(resolveGrokToolIntegrationFlags("skills"), []);
  assert.deepEqual(resolveGrokToolIntegrationFlags("mcp"), [
    "--disallowed-tools",
    GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS.join(","),
  ]);
  // Default/unknown → MCP lockdown (align with Claude MCP-mode empty local tools).
  assert.deepEqual(resolveGrokToolIntegrationFlags(undefined), [
    "--disallowed-tools",
    GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS.join(","),
  ]);
  assert.ok(GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS.includes("run_terminal_command"));
  assert.ok(GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS.includes("search_replace"));
  assert.ok(GROK_MCP_MODE_DISALLOWED_LOCAL_TOOLS.includes("write"));
});

test("buildGrokCliArgs applies MCP-mode local-tool lockdown via real builder", () => {
  const mcpArgs = buildGrokCliArgs({
    prompt: "list sessions",
    permissionMode: "auto",
    toolIntegrationMode: "mcp",
  });
  const denyIdx = mcpArgs.indexOf("--disallowed-tools");
  assert.ok(denyIdx >= 0, "MCP mode must pass --disallowed-tools");
  const denied = String(mcpArgs[denyIdx + 1] || "");
  assert.match(denied, /run_terminal_command/);
  assert.match(denied, /search_replace/);
  assert.match(denied, /write/);
  // MCP meta-tools must not appear in the deny list (Netcatty remote path).
  assert.doesNotMatch(denied, /mcp|netcatty/i);

  const skillsArgs = buildGrokCliArgs({
    prompt: "list sessions",
    permissionMode: "auto",
    toolIntegrationMode: "skills",
  });
  assert.ok(!skillsArgs.includes("--disallowed-tools"), "skills mode must not apply MCP lockdown");
});

test("createLineBuffer rejects and releases an unterminated oversized message", () => {
  const lines = [];
  const lineBuffer = createLineBuffer((line) => lines.push(line), 8);
  lineBuffer.push(Buffer.from("12345678"));
  assert.throws(
    () => lineBuffer.push(Buffer.from("9")),
    (error) => error?.code === "GROK_LINE_LIMIT",
  );
  lineBuffer.flush();
  assert.deepEqual(lines, []);
});

test("formatGrokErrorForUser maps auth failures without over-matching bare login strings", () => {
  assert.match(
    formatGrokErrorForUser("Not authenticated"),
    /not logged in/i,
  );
  assert.equal(
    formatGrokErrorForUser("Failed to run login form validation"),
    "Failed to run login form validation",
  );
});

test("translateGrokStreamEvent maps thought, text, tools, usage, end", () => {
  const emitter = makeEmitter();
  const state = {};

  translateGrokStreamEvent({ type: "thought", data: "plan" }, emitter, state);
  translateGrokStreamEvent({ type: "text", data: "Hi" }, emitter, state);
  translateGrokStreamEvent({
    type: "tool_call",
    toolCallId: "c1",
    toolName: "read_file",
    status: "in_progress",
    rawInput: { path: "a.ts" },
  }, emitter, state);
  translateGrokStreamEvent({
    type: "tool_call_update",
    toolCallId: "c1",
    status: "completed",
    rawOutput: { lines: 2 },
  }, emitter, state);
  translateGrokStreamEvent({
    type: "usage",
    usage: {
      input_tokens: 10,
      output_tokens: 3,
      cache_read_input_tokens: 1,
      reasoning_tokens: 2,
      total_tokens: 16,
    },
  }, emitter, state);
  translateGrokStreamEvent({
    type: "end",
    stopReason: "end_turn",
    sessionId: "s1",
    usage: { input_tokens: 10, output_tokens: 3, total_tokens: 13 },
  }, emitter, state);

  assert.deepEqual(emitter.calls, [
    ["reasoning", "plan"],
    ["reasoningEnd"],
    ["text", "Hi"],
    ["toolCall", "read_file", { path: "a.ts" }, "c1"],
    ["toolResult", "c1", "{\"lines\":2}", "read_file"],
    ["usage", {
      inputTokens: 10,
      cachedInputTokens: 1,
      outputTokens: 3,
      reasoningTokens: 2,
      totalTokens: 16,
    }],
    ["sessionId", "s1"],
    ["usage", {
      inputTokens: 10,
      cachedInputTokens: undefined,
      outputTokens: 3,
      reasoningTokens: undefined,
      totalTokens: 13,
    }],
  ]);
  assert.equal(state.sessionId, "s1");
  assert.equal(state.streamedAssistantText, true);
});

test("translateGrokStreamEvent maps error events to emitError and stop", () => {
  const emitter = makeEmitter();
  const state = {};
  const stop = translateGrokStreamEvent(
    { type: "error", message: "Couldn't start session" },
    emitter,
    state,
  );
  assert.equal(stop, true);
  assert.equal(state.failed, true);
  assert.deepEqual(emitter.calls, [["error", "Couldn't start session"]]);
});

test("buildGrokMcpServerTomlSection escapes paths and env", () => {
  const section = buildGrokMcpServerTomlSection({
    name: "netcatty-remote-hosts",
    command: "C:\\Program Files\\node.exe",
    args: ["mcp.cjs", "--flag"],
    env: [{ name: "TOKEN", value: 'a"b' }],
  });
  assert.match(section, /\[mcp_servers\.netcatty-remote-hosts\]/);
  assert.match(section, /command = "C:\\\\Program Files\\\\node\.exe"/);
  assert.match(section, /args = \["mcp\.cjs", "--flag"\]/);
  assert.match(section, /TOKEN = "a\\"b"/);
  assert.match(section, /enabled = true/);
});

test("stripGrokMcpServerSection removes only the named server block", () => {
  const input = [
    "[ui]",
    "compact_mode = true",
    "",
    "[mcp_servers.other]",
    'command = "echo"',
    "",
    "[mcp_servers.netcatty-remote-hosts]",
    'command = "node"',
    "enabled = true",
    "",
    "[mcp_servers.other.nested]",
    "x = 1",
  ].join("\n");

  const stripped = stripGrokMcpServerSection(input, "netcatty-remote-hosts");
  assert.match(stripped, /\[mcp_servers\.other\]/);
  assert.match(stripped, /\[ui\]/);
  assert.doesNotMatch(stripped, /netcatty-remote-hosts/);
});

test("mergeWorkspaceGrokMcpToml upserts netcatty without dropping other servers", () => {
  resetGrokMcpMergeRefcountsForTests();
  const path = require("node:path");
  const repo = path.join("repo-fixture");
  const grokDir = path.join(repo, ".grok");
  const configPath = path.join(grokDir, "config.toml");
  const original = [
    "[mcp_servers.other]",
    'command = "echo"',
    "enabled = true",
    "",
  ].join("\n");
  const files = new Map();
  files.set(configPath, original);

  const handle = mergeWorkspaceGrokMcpToml(repo, [{
    name: "netcatty-remote-hosts",
    command: "node",
    args: ["mcp.cjs"],
    env: [{ name: "TOKEN", value: "x" }],
  }], {
    existsSync: (p) => files.has(p) || p === grokDir,
    readFileSync: (p) => files.get(p),
    writeFileSync: (p, data) => { files.set(p, data); },
    mkdirSync: () => {},
    unlinkSync: (p) => { files.delete(p); },
  });

  const written = files.get(configPath);
  assert.match(written, /\[mcp_servers\.other\]/);
  assert.match(written, /\[mcp_servers\.netcatty-remote-hosts\]/);
  assert.match(written, /TOKEN = "x"/);

  handle.restore();
  assert.equal(files.get(configPath), original);
});

test("parseGrokModelsOutput reads default and bullet list", () => {
  const parsed = parseGrokModelsOutput([
    "You are logged in with grok.com.",
    "",
    "Default model: grok-4.5",
    "",
    "Available models:",
    "  * grok-4.5 (default)",
    "  * grok-code-fast",
  ].join("\n"));
  assert.equal(parsed.currentModelId, "grok-4.5");
  assert.deepEqual(parsed.models, [
    { id: "grok-4.5", name: "grok-4.5" },
    { id: "grok-code-fast", name: "grok-code-fast" },
  ]);
});

test("runGrokTurn streams fixture lines and emits done", async () => {
  const emitter = makeEmitter();
  const child = new EventEmitter();
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.pid = 4242;
  child.kill = () => {};

  const spawnImpl = (bin, args) => {
    assert.equal(bin, "/usr/bin/grok");
    assert.ok(args.includes("streaming-json"));
    assert.ok(args.includes("--always-approve"));
    queueMicrotask(() => {
      child.stdout.emit("data", Buffer.from(
        [
          '{"type":"thought","data":"thinking"}',
          '{"type":"text","data":"hello"}',
          '{"type":"end","sessionId":"sess-xyz","stopReason":"end_turn"}',
          "",
        ].join("\n"),
      ));
      child.emit("close", 0);
    });
    return child;
  };

  const result = await runGrokTurn({
    prompt: "hi",
    binPath: "/usr/bin/grok",
    cwd: "/repo",
    permissionMode: "auto",
    injectedMcpServers: [],
    emitter,
    spawnImpl,
    mergeMcp: () => ({ restore() {} }),
  });

  assert.equal(result.sessionId, "sess-xyz");
  assert.ok(emitter.calls.some((c) => c[0] === "text" && c[1] === "hello"));
  assert.ok(emitter.calls.some((c) => c[0] === "done"));
  assert.ok(emitter.calls.some((c) => c[0] === "sessionId" && c[1] === "sess-xyz"));
});

test("runGrokTurn reports missing CLI clearly", async () => {
  const emitter = makeEmitter();
  const result = await runGrokTurn({
    prompt: "hi",
    binPath: "",
    emitter,
  });
  assert.equal(result.sessionId, null);
  assert.match(String(emitter.calls[0]?.[1] || ""), /not found/i);
});

test("listGrokModels parses spawn stdout", async () => {
  const child = new EventEmitter();
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.pid = 1;
  child.kill = () => {};

  const spawnImpl = () => {
    queueMicrotask(() => {
      child.stdout.emit("data", Buffer.from("Default model: grok-4.5\n* grok-4.5 (default)\n"));
      child.emit("close", 0);
    });
    return child;
  };

  const result = await listGrokModels({
    binPath: "/usr/bin/grok",
    spawnImpl,
  });
  assert.equal(result.currentModelId, "grok-4.5");
  assert.equal(result.models[0].id, "grok-4.5");
});
