const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const {
  buildOpenCodeConfig,
  buildOpenCodePromptParts,
  classifyOpenCodeSpawnError,
  createOpenCodeProcessEnv,
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

test("createOpenCodeProcessEnv creates an opencode shim for arbitrary custom binary paths", () => {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-opencode-test-"));
  const fakeBin = path.join(tempRoot, "my-opencode-wrapper");
  fs.writeFileSync(fakeBin, "#!/bin/sh\n");
  fs.chmodSync(fakeBin, 0o755);

  const { env, cleanup } = createOpenCodeProcessEnv(
    { PATH: "/usr/bin" },
    fakeBin,
    {
      platform: "linux",
      getTempFilePath: (name) => path.join(tempRoot, name),
    },
  );

  try {
    const shimDir = env.PATH.split(path.delimiter)[0];
    const shim = path.join(shimDir, "opencode");
    assert.equal(env.OPENCODE_BIN, fakeBin);
    assert.equal(fs.existsSync(shim), true);
    assert.match(fs.readFileSync(shim, "utf8"), new RegExp(`exec "${fakeBin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}"`));
  } finally {
    cleanup();
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
});

test("translateOpenCodeEvent maps text, reasoning, tools, errors, and idle", () => {
  const { events, emitter } = collector();
  const state = {};
  translateOpenCodeEvent(
    { directory: "/tmp", payload: { type: "message.part.updated", properties: { part: { type: "text", id: "p1", text: "hello" }, delta: "he" } } },
    emitter,
    state,
  );
  translateOpenCodeEvent(
    { payload: { type: "message.part.updated", properties: { part: { type: "reasoning", id: "r1", text: "thinking" }, delta: "think" } } },
    emitter,
    state,
  );
  translateOpenCodeEvent(
    { payload: { type: "message.part.updated", properties: { part: { type: "tool", callID: "tool-1", tool: "netcatty_run", state: { status: "running", input: { command: "uptime" } } } } } },
    emitter,
    state,
  );
  translateOpenCodeEvent(
    { payload: { type: "message.part.updated", properties: { part: { type: "tool", callID: "tool-1", tool: "netcatty_run", state: { status: "completed", input: { command: "uptime" }, output: "ok" } } } } },
    emitter,
    state,
  );
  translateOpenCodeEvent(
    { payload: { type: "session.error", properties: { error: { data: { message: "bad key" } } } } },
    emitter,
    state,
  );
  translateOpenCodeEvent(
    { payload: { type: "session.idle", properties: { sessionID: "sess-1" } } },
    emitter,
    state,
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

test("translateOpenCodeEvent also accepts direct OpenCode SDK events", () => {
  const { events, emitter } = collector();
  translateOpenCodeEvent(
    { type: "message.part.updated", properties: { part: { type: "text", sessionID: "sess-1", id: "p1", text: "hello" }, delta: "he" } },
    emitter,
  );
  translateOpenCodeEvent(
    { type: "session.idle", properties: { sessionID: "sess-1" } },
    emitter,
  );

  assert.deepEqual(events, [
    { k: "text", t: "he" },
    { k: "status", m: "OpenCode session idle" },
  ]);
});

test("translateOpenCodeEvent emits a tool call before a result when only completion arrives", () => {
  const { events, emitter } = collector();
  translateOpenCodeEvent(
    { type: "message.part.updated", properties: { part: { type: "tool", callID: "tool-1", tool: "netcatty_run", state: { status: "completed", input: { command: "whoami" }, output: "me" } } } },
    emitter,
  );

  assert.deepEqual(events, [
    { k: "toolCall", name: "netcatty_run", args: { command: "whoami" }, id: "tool-1" },
    { k: "toolResult", id: "tool-1", out: "me", name: "netcatty_run" },
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

test("runOpenCodeTurn ignores events from other OpenCode sessions", async () => {
  const { events, emitter } = collector();
  const abortController = new AbortController();
  const stream = {
    async *[Symbol.asyncIterator]() {
      yield { type: "message.part.updated", properties: { part: { type: "text", sessionID: "other", id: "p0", text: "wrong" }, delta: "wrong" } };
      yield { type: "message.part.updated", properties: { part: { type: "text", sessionID: "sess-1", id: "p1", text: "right" }, delta: "right" } };
      yield { type: "session.idle", properties: { sessionID: "sess-1" } };
    },
  };
  const client = {
    global: { event: async () => ({ stream }) },
    session: {
      create: async () => ({ data: { id: "sess-1" } }),
      promptAsync: async () => ({ data: true }),
    },
  };

  await runOpenCodeTurn({
    prompt: "hello",
    emitter,
    abortController,
    openCodeFactory: async () => ({ client, server: { close() {} } }),
  });

  assert.deepEqual(events.filter((event) => event.k === "text"), [
    { k: "text", t: "right" },
  ]);
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

test("runOpenCodeTurn returns promptly when aborted while the event stream is quiet", async () => {
  const { events, emitter } = collector();
  const abortController = new AbortController();
  let closeCount = 0;
  let abortCount = 0;
  const stream = {
    async *[Symbol.asyncIterator]() {
      await new Promise(() => {});
    },
  };
  const client = {
    global: { event: async () => ({ stream }) },
    session: {
      create: async () => ({ data: { id: "sess-1" } }),
      promptAsync: async () => ({ data: true }),
      abort: async () => { abortCount += 1; },
    },
  };

  const running = runOpenCodeTurn({
    prompt: "hello",
    emitter,
    abortController,
    openCodeFactory: async () => ({ client, server: { close() { closeCount += 1; } } }),
  });
  await new Promise((resolve) => setImmediate(resolve));
  abortController.abort();

  const result = await Promise.race([
    running,
    new Promise((resolve) => setTimeout(() => resolve("timed-out"), 50)),
  ]);

  assert.notEqual(result, "timed-out");
  assert.deepEqual(result, { sessionId: "sess-1" });
  assert.equal(abortCount, 1);
  assert.equal(closeCount >= 1, true);
  assert.equal(events.some((event) => event.k === "done"), false);
});

test("runOpenCodeTurn passes an explicit non-default port to the OpenCode SDK factory", async () => {
  const { emitter } = collector();
  const abortController = new AbortController();
  let capturedPort;
  const stream = {
    async *[Symbol.asyncIterator]() {
      yield { type: "message.part.updated", properties: { part: { type: "text", sessionID: "sess-1", id: "p1", text: "ok" }, delta: "ok" } };
      yield { type: "session.idle", properties: { sessionID: "sess-1" } };
    },
  };
  const client = {
    global: { event: async () => ({ stream }) },
    session: {
      create: async () => ({ data: { id: "sess-1" } }),
      promptAsync: async () => ({ data: true }),
    },
  };

  await runOpenCodeTurn({
    prompt: "hello",
    emitter,
    abortController,
    openCodeFactory: async (options) => {
      capturedPort = options.port;
      return { client, server: { close() {} } };
    },
  });

  assert.equal(typeof capturedPort, "number");
  assert.notEqual(capturedPort, 4096);
});

test("runOpenCodeTurn waits for the OpenCode event stream before prompting", async () => {
  const { emitter } = collector();
  const abortController = new AbortController();
  let releaseServerConnected;
  let serverConnectedSeen = false;
  const stream = {
    async *[Symbol.asyncIterator]() {
      await new Promise((resolve) => { releaseServerConnected = resolve; });
      yield { payload: { type: "server.connected", properties: {} } };
      serverConnectedSeen = true;
      yield { payload: { type: "message.part.updated", properties: { part: { type: "text", sessionID: "sess-1", id: "p1", text: "ok" }, delta: "ok" } } };
      yield { payload: { type: "session.idle", properties: { sessionID: "sess-1" } } };
    },
  };
  const client = {
    global: { event: async () => ({ stream }) },
    session: {
      create: async () => ({ data: { id: "sess-1" } }),
      promptAsync: async () => {
        assert.equal(serverConnectedSeen, true);
        return { data: true };
      },
    },
  };

  const running = runOpenCodeTurn({
    prompt: "hello",
    model: "openai/gpt-5.1",
    emitter,
    abortController,
    openCodeFactory: async () => ({ client, server: { close() {} } }),
  });

  await new Promise((resolve) => setImmediate(resolve));
  releaseServerConnected();
  const result = await running;

  assert.deepEqual(result, { sessionId: "sess-1" });
});

test("listOpenCodeModels passes an explicit non-default port to the OpenCode SDK factory", async () => {
  let capturedPort;
  const models = await require("./opencodeDriver.cjs").listOpenCodeModels({
    openCodeFactory: async (options) => {
      capturedPort = options.port;
      return {
        client: { config: { providers: async () => ({ providers: [], default: { openai: "gpt-5.1" } }) } },
        server: { close() {} },
      };
    },
  });

  assert.deepEqual(models, { currentModelId: "openai/gpt-5.1", models: [] });
  assert.equal(typeof capturedPort, "number");
  assert.notEqual(capturedPort, 4096);
});

test("classifyOpenCodeSpawnError recognizes missing opencode CLI", () => {
  assert.equal(classifyOpenCodeSpawnError(Object.assign(new Error("spawn opencode ENOENT"), { code: "ENOENT" })).isSpawnEnoent, true);
  assert.equal(classifyOpenCodeSpawnError(new Error("other")).isSpawnEnoent, false);
});
