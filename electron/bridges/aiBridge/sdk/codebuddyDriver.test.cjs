const test = require("node:test");
const assert = require("node:assert/strict");
const {
  buildCodebuddyQueryOptions,
  buildCodebuddyCanUseTool,
  buildCodebuddyPromptInput,
  codebuddyBuiltinTools,
  mapCodebuddyModels,
  runCodebuddyTurn,
  translateCodebuddyMessage,
  buildCodebuddyHooks,
  buildCodebuddyElicitation,
  toSdkMcpServers,
} = require("./codebuddyDriver.cjs");

function collector() {
  const events = [];
  const emitter = {
    text: (t) => events.push({ k: "text", t }),
    reasoning: (d) => events.push({ k: "reasoning", d }),
    toolCall: (name, args, id) => events.push({ k: "toolCall", name, args, id }),
    toolResult: (id, out, name) => events.push({ k: "toolResult", id, out, name }),
    status: (m) => events.push({ k: "status", m }),
    sessionId: (s) => events.push({ k: "sessionId", s }),
    emitDone: () => events.push({ k: "done" }),
    emitError: (m) => events.push({ k: "error", m }),
  };
  return { events, emitter };
}

test("buildCodebuddyQueryOptions wires SDK options in isolated mode", () => {
  const ac = new AbortController();
  const opts = buildCodebuddyQueryOptions({
    cwd: "/tmp",
    model: "codebuddy-1",
    env: { PATH: "/usr/bin", CODEBUDDY_INTERNET_ENVIRONMENT: "ioa" },
    pathToCodebuddyCode: "/opt/codebuddy/bin/codebuddy",
    abortController: ac,
    resume: "sess-1",
    injectedMcpServers: [{
      name: "netcatty-remote-hosts",
      command: "/abs/electron",
      args: ["/abs/server.cjs"],
      env: [{ name: "NETCATTY_MCP_PORT", value: "1" }],
    }],
  });

  assert.equal(opts.cwd, "/tmp");
  assert.equal(opts.model, "codebuddy-1");
  assert.equal(opts.includePartialMessages, true);
  assert.equal(opts.permissionMode, "bypassPermissions");
  assert.equal(opts.allowDangerouslySkipPermissions, true);
  assert.deepEqual(opts.extraArgs, { "dangerously-skip-permissions": null });
  assert.deepEqual(opts.settingSources, []);
  assert.equal(opts.env.CODEBUDDY_INTERNET_ENVIRONMENT, "ioa");
  assert.equal(opts.pathToCodebuddyCode, "/opt/codebuddy/bin/codebuddy");
  assert.equal(opts.abortController, ac);
  assert.equal(opts.resume, "sess-1");
  assert.deepEqual(opts.tools, []);
  // allowedTools must stay unset in mcp mode: tools:[] disables built-ins, while
  // allowedTools:[] would prevent injected Netcatty MCP tools from running.
  assert.ok(!("allowedTools" in opts));
  assert.ok(opts.disallowedTools.includes("AskUserQuestion"));
  assert.equal(opts.mcpServers["netcatty-remote-hosts"].type, "stdio");
  assert.deepEqual(opts.mcpServers["netcatty-remote-hosts"].env, { NETCATTY_MCP_PORT: "1" });
});

test("built-in tools are mode-aware", () => {
  assert.deepEqual(codebuddyBuiltinTools("mcp"), []);
  assert.deepEqual(codebuddyBuiltinTools(undefined), []);
  assert.deepEqual(codebuddyBuiltinTools("skills"), ["Bash"]);
});

test("translateCodebuddyMessage emits assistant text fallback", () => {
  const { events, emitter } = collector();
  translateCodebuddyMessage(
    { type: "assistant", message: { content: [{ type: "text", text: "hello" }] } },
    emitter,
  );
  assert.deepEqual(events, [{ k: "text", t: "hello" }]);
});

test("translateCodebuddyMessage can skip consolidated assistant text after stream deltas", () => {
  const { events, emitter } = collector();
  translateCodebuddyMessage(
    { type: "assistant", message: { content: [{ type: "text", text: "consolidated" }] } },
    emitter,
    { skipAssistantText: true },
  );
  assert.deepEqual(events, []);
});

test("translateCodebuddyMessage maps stream deltas, tool calls, and tool results", () => {
  const { events, emitter } = collector();
  translateCodebuddyMessage(
    { type: "stream_event", event: { type: "content_block_delta", delta: { type: "text_delta", text: "hi" } } },
    emitter,
  );
  translateCodebuddyMessage(
    { type: "stream_event", event: { type: "content_block_delta", delta: { type: "thinking_delta", thinking: "why" } } },
    emitter,
  );
  translateCodebuddyMessage(
    { type: "assistant", message: { content: [{ type: "tool_use", id: "tu-1", name: "Bash", input: { command: "ls" } }] } },
    emitter,
  );
  translateCodebuddyMessage(
    { type: "user", message: { content: [{ type: "tool_result", tool_use_id: "tu-1", content: "ok" }] } },
    emitter,
  );
  assert.deepEqual(events, [
    { k: "text", t: "hi" },
    { k: "reasoning", d: "why" },
    { k: "toolCall", name: "Bash", args: { command: "ls" }, id: "tu-1" },
    { k: "toolResult", id: "tu-1", out: "ok", name: undefined },
  ]);
});

test("translateCodebuddyMessage emits system session id and status text", () => {
  const { events, emitter } = collector();
  translateCodebuddyMessage(
    { type: "system", session_id: "sess-1", message: "initializing" },
    emitter,
  );
  assert.deepEqual(events, [
    { k: "sessionId", s: "sess-1" },
    { k: "status", m: "initializing" },
  ]);
});

test("runCodebuddyTurn does not duplicate assistant text after streamed text", async () => {
  const { events, emitter } = collector();
  async function* fakeQuery() {
    yield { type: "system", session_id: "sess-1" };
    yield { type: "stream_event", event: { type: "content_block_delta", delta: { type: "text_delta", text: "hello" } } };
    yield { type: "assistant", message: { content: [{ type: "text", text: "hello" }] } };
  }

  const result = await runCodebuddyTurn({
    prompt: "say hi",
    options: { abortController: new AbortController() },
    emitter,
    queryFn: () => fakeQuery(),
  });

  assert.deepEqual(result, { sessionId: "sess-1" });
  assert.deepEqual(events, [
    { k: "sessionId", s: "sess-1" },
    { k: "text", t: "hello" },
    { k: "done" },
  ]);
});

test("runCodebuddyTurn interrupts the SDK query as soon as abort is signaled", async () => {
  const events = [];
  let sawSession;
  const sessionSeen = new Promise((resolve) => { sawSession = resolve; });
  const emitter = {
    text: (t) => events.push({ k: "text", t }),
    reasoning: (d) => events.push({ k: "reasoning", d }),
    toolCall: (name, args, id) => events.push({ k: "toolCall", name, args, id }),
    toolResult: (id, out, name) => events.push({ k: "toolResult", id, out, name }),
    status: (m) => events.push({ k: "status", m }),
    sessionId: (s) => { events.push({ k: "sessionId", s }); sawSession(); },
    emitDone: () => events.push({ k: "done" }),
    emitError: (m) => events.push({ k: "error", m }),
  };
  const ac = new AbortController();
  let interruptCount = 0;
  let release;

  const fakeQuery = () => ({
    interrupt: async () => { interruptCount += 1; release?.(); },
    async *[Symbol.asyncIterator]() {
      yield { type: "system", session_id: "sess-1" };
      await new Promise((resolve) => { release = resolve; });
    },
  });

  const turn = runCodebuddyTurn({
    prompt: "wait",
    options: { abortController: ac },
    emitter,
    queryFn: fakeQuery,
  });

  await sessionSeen;
  ac.abort();
  const result = await turn;

  assert.deepEqual(result, { sessionId: "sess-1" });
  assert.ok(interruptCount >= 1);
  assert.deepEqual(events, [
    { k: "sessionId", s: "sess-1" },
    { k: "done" },
  ]);
});

test("buildCodebuddyPromptInput sends supported images as native image blocks", async () => {
  const input = buildCodebuddyPromptInput("describe this", [
    { filename: "shot.png", mediaType: "image/png", filePath: "/tmp/shot.png", base64Data: "abc" },
    { filename: "bad.svg", mediaType: "image/svg+xml", filePath: "/tmp/bad.svg", base64Data: "def" },
  ]);
  const messages = [];
  for await (const message of input) messages.push(message);
  assert.deepEqual(messages, [{
    type: "user",
    message: {
      role: "user",
      content: [
        { type: "text", text: "describe this" },
        { type: "image", source: { type: "base64", media_type: "image/png", data: "abc" } },
      ],
    },
    parent_tool_use_id: null,
  }]);
});

test("mapCodebuddyModels maps model ids and drops invalid entries", () => {
  assert.deepEqual(mapCodebuddyModels([
    // Real CLI wire shape ({id,name}) — must NOT be dropped.
    { id: "glm-5.1", name: "GLM-5.1" },
    { modelId: "cb-1", name: "CodeBuddy 1", description: "default" },
    { value: "cb-2", displayName: "CodeBuddy 2" },
    { name: "missing id" },
  ]), [
    { id: "glm-5.1", name: "GLM-5.1", description: undefined },
    { id: "cb-1", name: "CodeBuddy 1", description: "default" },
    { id: "cb-2", name: "CodeBuddy 2", description: undefined },
  ]);
  assert.deepEqual(mapCodebuddyModels(null), []);
});

// ---------------------------------------------------------------------------
// SDK 0.3.230 options
// ---------------------------------------------------------------------------

test("buildCodebuddyQueryOptions passes SDK 0.3.230 options", () => {
  const opts = buildCodebuddyQueryOptions({
    cwd: "/tmp",
    env: {},
    systemPrompt: "You are a server admin assistant.",
    effort: "high",
    maxTurns: 10,
    maxBudgetUsd: 0.5,
    fallbackModel: "glm-4",
    sandbox: { enabled: true, autoAllowBashIfSandboxed: true },
    agents: { auditor: { description: "Security auditor", prompt: "Audit", tools: ["Bash"] } },
    outputFormat: { type: "json_schema", schema: { type: "object" } },
    enableFileCheckpointing: true,
    traceId: "trace-123",
    parentSpanId: "span-456",
    persistSession: false,
    sessionId: "custom-sess",
  });

  assert.deepEqual(opts.systemPrompt, { append: "You are a server admin assistant." });
  assert.equal(opts.effort, "high");
  assert.equal(opts.maxTurns, 10);
  assert.equal(opts.maxBudgetUsd, 0.5);
  assert.equal(opts.fallbackModel, "glm-4");
  assert.deepEqual(opts.sandbox, { enabled: true, autoAllowBashIfSandboxed: true });
  assert.deepEqual(opts.agents, { auditor: { description: "Security auditor", prompt: "Audit", tools: ["Bash"] } });
  assert.deepEqual(opts.outputFormat, { type: "json_schema", schema: { type: "object" } });
  assert.equal(opts.enableFileCheckpointing, true);
  assert.equal(opts.traceId, "trace-123");
  assert.equal(opts.parentSpanId, "span-456");
  assert.equal(opts.persistSession, false);
  assert.equal(opts.sessionId, "custom-sess");
});

test("buildCodebuddyQueryOptions does not set maxThinkingTokens (deprecated removed)", () => {
  const opts = buildCodebuddyQueryOptions({
    cwd: "/tmp",
    env: { NETCATTY_CODEBUDDY_THINKING: "enabled:8000" },
  });
  assert.deepEqual(opts.thinking, { type: "enabled", budgetTokens: 8000 });
  assert.ok(!("maxThinkingTokens" in opts));
});

test("buildCodebuddyQueryOptions accepts object systemPrompt directly", () => {
  const opts = buildCodebuddyQueryOptions({
    cwd: "/tmp",
    env: {},
    systemPrompt: { append: "custom append" },
  });
  assert.deepEqual(opts.systemPrompt, { append: "custom append" });
});

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

test("buildCodebuddyHooks returns hook matchers that emit events", async () => {
  const { events, emitter } = collector();
  emitter.emitEvent = (ev) => events.push({ k: "event", ev });
  const hooks = buildCodebuddyHooks(emitter);

  assert.ok(Array.isArray(hooks.PreToolUse));
  assert.ok(Array.isArray(hooks.PostToolUse));
  assert.ok(Array.isArray(hooks.PostToolUseFailure));
  assert.ok(Array.isArray(hooks.SessionEnd));
  assert.ok(Array.isArray(hooks.Notification));

  // Invoke PreToolUse hook callback
  const preHook = hooks.PreToolUse[0].hooks[0];
  const result = await preHook(
    { tool_name: "Bash", tool_input: { command: "ls" }, tool_use_id: "tu-1" },
    "tu-1",
    { signal: new AbortController().signal },
  );
  assert.deepEqual(result, { continue: true });
  assert.equal(events.length, 1);
  assert.equal(events[0].ev.hookEvent, "PreToolUse");
  assert.equal(events[0].ev.toolName, "Bash");
});

// ---------------------------------------------------------------------------
// Elicitation
// ---------------------------------------------------------------------------

test("buildCodebuddyElicitation forwards create and resolves on response", async () => {
  const { events, emitter } = collector();
  emitter.emitEvent = (ev) => events.push({ k: "event", ev });
  const pendingMap = new Map();
  const handler = buildCodebuddyElicitation(emitter, pendingMap);

  const createPromise = handler.create(
    { _meta: { "codebuddy.ai": { elicitationId: "el-1" } }, message: "Confirm?" },
    { signal: new AbortController().signal },
  );

  // Should have emitted elicitation-create event
  assert.equal(events.length, 1);
  assert.equal(events[0].ev.type, "elicitation-create");
  assert.equal(events[0].ev.elicitationId, "el-1");

  // Resolve the pending elicitation
  assert.ok(pendingMap.has("el-1"));
  pendingMap.get("el-1").resolve({ action: "accept", content: { confirmed: true } });
  const response = await createPromise;
  assert.deepEqual(response, { action: "accept", content: { confirmed: true } });
});

// ---------------------------------------------------------------------------
// MCP SSE/HTTP support
// ---------------------------------------------------------------------------

test("toSdkMcpServers supports sse, http, and sdk transport types", () => {
  const fakeInstance = { __brand: "sdk-mcp" };
  const map = toSdkMcpServers([
    { name: "stdio-server", command: "/bin/server", args: ["--port", "0"], env: [] },
    { name: "sse-server", type: "sse", url: "http://localhost:3000/sse", headers: { Authorization: "Bearer x" } },
    { name: "http-server", type: "http", url: "http://localhost:4000/mcp" },
    { name: "sdk-server", type: "sdk", instance: fakeInstance },
  ]);
  assert.equal(map["stdio-server"].type, "stdio");
  assert.equal(map["stdio-server"].command, "/bin/server");
  assert.equal(map["sse-server"].type, "sse");
  assert.equal(map["sse-server"].url, "http://localhost:3000/sse");
  assert.deepEqual(map["sse-server"].headers, { Authorization: "Bearer x" });
  assert.equal(map["http-server"].type, "http");
  assert.equal(map["http-server"].url, "http://localhost:4000/mcp");
  assert.equal(map["sdk-server"].type, "sdk");
  assert.equal(map["sdk-server"].name, "sdk-server");
  assert.equal(map["sdk-server"].instance, fakeInstance);
});

// ---------------------------------------------------------------------------
// Permission handler (canUseTool)
// ---------------------------------------------------------------------------

test("buildCodebuddyCanUseTool auto mode allows without prompting", async () => {
  const handler = buildCodebuddyCanUseTool({ permissionMode: "auto" });
  const result = await handler("Bash", { command: "rm -rf /tmp/x" }, {});
  assert.deepEqual(result, { behavior: "allow" });
});

test("buildCodebuddyCanUseTool observer mode denies with message", async () => {
  const handler = buildCodebuddyCanUseTool({ permissionMode: "observer" });
  const result = await handler("Bash", { command: "ls" }, {});
  assert.equal(result.behavior, "deny");
  assert.ok(result.message.includes("Observer mode"));
});

test("buildCodebuddyCanUseTool confirm mode forwards to approval UI and allows on approve", async () => {
  const calls = [];
  const requestApproval = async (toolName, args, chatSessionId) => {
    calls.push({ toolName, args, chatSessionId });
    return true;
  };
  const handler = buildCodebuddyCanUseTool({
    permissionMode: "confirm",
    chatSessionId: "chat-1",
    requestApproval,
  });
  const result = await handler("Bash", { command: "apt install nginx" }, {});
  assert.deepEqual(result, { behavior: "allow" });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].toolName, "Bash");
  assert.deepEqual(calls[0].args, { command: "apt install nginx" });
  assert.equal(calls[0].chatSessionId, "chat-1");
});

test("buildCodebuddyCanUseTool confirm mode denies on user rejection", async () => {
  const handler = buildCodebuddyCanUseTool({
    permissionMode: "confirm",
    chatSessionId: "chat-1",
    requestApproval: async () => false,
  });
  const result = await handler("Bash", { command: "reboot" }, {});
  assert.equal(result.behavior, "deny");
  assert.ok(result.message.includes("User denied"));
});

test("buildCodebuddyCanUseTool confirm mode denies when no approval channel", async () => {
  const handler = buildCodebuddyCanUseTool({ permissionMode: "confirm" });
  const result = await handler("Bash", {}, {});
  assert.equal(result.behavior, "deny");
  assert.ok(result.message.includes("no approval channel"));
});

test("buildCodebuddyQueryOptions attaches canUseTool handler", () => {
  const handler = async () => ({ behavior: "allow" });
  const opts = buildCodebuddyQueryOptions({ cwd: "/tmp", env: {}, canUseTool: handler });
  assert.equal(opts.canUseTool, handler);
});
