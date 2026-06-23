const test = require("node:test");
const assert = require("node:assert/strict");
const {
  buildOpenCodeConfig,
  buildOpenCodePromptParts,
  classifyOpenCodeSpawnError,
  mapOpenCodeModels,
  parseOpenCodeModel,
  runOpenCodeTurn,
  translateOpenCodeEvent,
} = require("./opencodeDriver.cjs");

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

test("parseOpenCodeModel splits provider/model ids", () => {
  assert.deepEqual(parseOpenCodeModel("openai/gpt-5.1"), { providerID: "openai", modelID: "gpt-5.1" });
  assert.deepEqual(parseOpenCodeModel("anthropic/claude-sonnet-4-6"), { providerID: "anthropic", modelID: "claude-sonnet-4-6" });
  assert.equal(parseOpenCodeModel("gpt-5.1"), undefined);
  assert.equal(parseOpenCodeModel(""), undefined);
});

test("buildOpenCodeConfig isolates local tools and injects Netcatty MCP", () => {
  const cfg = buildOpenCodeConfig({
    model: "openai/gpt-5.1",
    injectedMcpServers: [{
      name: "netcatty-remote-hosts",
      command: "/abs/electron",
      args: ["/abs/server.cjs"],
      env: [{ name: "NETCATTY_MCP_PORT", value: "1" }],
    }],
  });

  assert.equal(cfg.model, "openai/gpt-5.1");
  assert.deepEqual(cfg.permission, {
    edit: "deny",
    bash: "deny",
    webfetch: "deny",
    external_directory: "deny",
  });
  assert.deepEqual(cfg.mcp["netcatty-remote-hosts"], {
    type: "local",
    command: ["/abs/electron", "/abs/server.cjs"],
    environment: { NETCATTY_MCP_PORT: "1" },
    enabled: true,
  });
});

test("buildOpenCodePromptParts includes supported images as file parts", () => {
  assert.deepEqual(buildOpenCodePromptParts("describe", [
    { filename: "shot.png", mediaType: "image/png", filePath: "/tmp/shot.png", base64Data: "abc" },
    { filename: "bad.svg", mediaType: "image/svg+xml", filePath: "/tmp/bad.svg", base64Data: "def" },
  ]), [
    { type: "text", text: "describe" },
    { type: "file", mime: "image/png", filename: "shot.png", url: "file:///tmp/shot.png" },
  ]);
});

test("translateOpenCodeEvent maps text, reasoning, tools, errors, and idle", () => {
  const { events, emitter } = collector();
  translateOpenCodeEvent(
    { directory: "/tmp", payload: { type: "message.part.updated", properties: { part: { type: "text", id: "p1", text: "hello" }, delta: "he" } } },
    emitter,
  );
  translateOpenCodeEvent(
    { payload: { type: "message.part.updated", properties: { part: { type: "reasoning", id: "r1", text: "thinking" }, delta: "think" } } },
    emitter,
  );
  translateOpenCodeEvent(
    { payload: { type: "message.part.updated", properties: { part: { type: "tool", callID: "tool-1", tool: "netcatty_run", state: { status: "running", input: { command: "uptime" } } } } } },
    emitter,
  );
  translateOpenCodeEvent(
    { payload: { type: "message.part.updated", properties: { part: { type: "tool", callID: "tool-1", tool: "netcatty_run", state: { status: "completed", input: { command: "uptime" }, output: "ok" } } } } },
    emitter,
  );
  translateOpenCodeEvent(
    { payload: { type: "session.error", properties: { error: { data: { message: "bad key" } } } } },
    emitter,
  );
  translateOpenCodeEvent(
    { payload: { type: "session.idle", properties: { sessionID: "sess-1" } } },
    emitter,
  );

  assert.deepEqual(events, [
    { k: "text", t: "he" },
    { k: "reasoning", d: "think" },
    { k: "toolCall", name: "netcatty_run", args: { command: "uptime" }, id: "tool-1" },
    { k: "toolResult", id: "tool-1", out: "ok", name: "netcatty_run" },
    { k: "error", m: "bad key" },
    { k: "status", m: "OpenCode session idle" },
  ]);
});

test("mapOpenCodeModels flattens providers", () => {
  assert.deepEqual(mapOpenCodeModels({
    providers: [
      { id: "openai", name: "OpenAI", models: { "gpt-5.1": { name: "GPT-5.1" } } },
      { id: "anthropic", models: { "claude-sonnet": {} } },
    ],
  }), [
    { id: "openai/gpt-5.1", name: "OpenAI GPT-5.1" },
    { id: "anthropic/claude-sonnet", name: "anthropic claude-sonnet" },
  ]);
  assert.deepEqual(mapOpenCodeModels(null), []);
});

test("runOpenCodeTurn creates a session, streams event deltas, and returns session id", async () => {
  const { events, emitter } = collector();
  const abortController = new AbortController();
  let eventController;
  const stream = {
    async *[Symbol.asyncIterator]() {
      await new Promise((resolve) => { eventController = resolve; });
      yield { payload: { type: "message.part.updated", properties: { part: { type: "text", id: "p1", text: "hi" }, delta: "hi" } } };
      yield { payload: { type: "session.idle", properties: { sessionID: "sess-1" } } };
    },
  };
  const client = {
    global: { event: async () => ({ stream }) },
    session: {
      create: async (args) => {
        assert.equal(args.query.directory, "/repo");
        return { data: { id: "sess-1" } };
      },
      promptAsync: async (args) => {
        assert.equal(args.path.id, "sess-1");
        assert.deepEqual(args.body.model, { providerID: "openai", modelID: "gpt-5.1" });
        eventController();
        return { data: true };
      },
    },
  };

  const result = await runOpenCodeTurn({
    prompt: "hello",
    cwd: "/repo",
    model: "openai/gpt-5.1",
    emitter,
    abortController,
    openCodeFactory: async () => ({ client, server: { close() {} } }),
  });

  assert.deepEqual(result, { sessionId: "sess-1" });
  assert.deepEqual(events, [
    { k: "sessionId", s: "sess-1" },
    { k: "text", t: "hi" },
    { k: "status", m: "OpenCode session idle" },
    { k: "done" },
  ]);
});

test("classifyOpenCodeSpawnError recognizes missing opencode CLI", () => {
  assert.equal(classifyOpenCodeSpawnError(Object.assign(new Error("spawn opencode ENOENT"), { code: "ENOENT" })).isSpawnEnoent, true);
  assert.equal(classifyOpenCodeSpawnError(new Error("other")).isSpawnEnoent, false);
});
