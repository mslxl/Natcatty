const test = require("node:test");
const assert = require("node:assert/strict");
const {
  ACP_PROTOCOL_VERSION,
  buildGrokAcpInitializeParams,
  buildGrokAcpPromptParams,
  buildGrokAcpSessionNewParams,
  buildGrokAcpSpawnArgs,
  createJsonRpcClient,
  handleGrokAcpMessage,
  runGrokAcpTurn,
  toAcpMcpEnvPairs,
  toAcpMcpServers,
  translateGrokAcpUpdate,
} = require("./grokAcpDriver.cjs");
const { getDriver, listBackends } = require("./index.cjs");

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
    emitDone: () => calls.push(["done"]),
    emitError: (message) => calls.push(["error", message]),
  };
}

test("buildGrokAcpSpawnArgs uses agent stdio and always-approve for non-observer", () => {
  assert.deepEqual(
    buildGrokAcpSpawnArgs({ model: "grok-4.5", permissionMode: "auto" }),
    ["agent", "--always-approve", "-m", "grok-4.5", "stdio"],
  );
  assert.deepEqual(
    buildGrokAcpSpawnArgs({ permissionMode: "observer" }),
    ["agent", "stdio"],
  );
});

test("toAcpMcpEnvPairs keeps Grok session/new pair-array shape", () => {
  // Live Grok rejects plain object env (Invalid params / McpServer enum).
  assert.deepEqual(
    toAcpMcpEnvPairs([{ name: "NETCATTY_MCP_PORT", value: "9" }]),
    [{ name: "NETCATTY_MCP_PORT", value: "9" }],
  );
  // If a plain object sneaks in, still emit pairs (not a map).
  assert.deepEqual(
    toAcpMcpEnvPairs({ NETCATTY_MCP_PORT: "9", NETCATTY_MCP_TOKEN: "t" }),
    [
      { name: "NETCATTY_MCP_PORT", value: "9" },
      { name: "NETCATTY_MCP_TOKEN", value: "t" },
    ],
  );
  assert.deepEqual(toAcpMcpEnvPairs(undefined), []);
});

test("toAcpMcpServers maps injectMcp env as name/value pairs for session/new", () => {
  assert.deepEqual(
    toAcpMcpServers([{
      name: "netcatty-remote-hosts",
      command: "node",
      args: ["mcp.cjs"],
      env: [{ name: "NETCATTY_MCP_PORT", value: "9" }, { name: "NETCATTY_MCP_TOKEN", value: "t" }],
    }]),
    [{
      name: "netcatty-remote-hosts",
      type: "stdio",
      command: "node",
      args: ["mcp.cjs"],
      env: [
        { name: "NETCATTY_MCP_PORT", value: "9" },
        { name: "NETCATTY_MCP_TOKEN", value: "t" },
      ],
    }],
  );
  // Must never emit object-map env (Grok session/new rejects it).
  const mapped = toAcpMcpServers([{
    name: "x",
    command: "node",
    args: [],
    env: { A: "1" },
  }]);
  assert.ok(Array.isArray(mapped[0].env));
  assert.equal(mapped[0].type, "stdio");
  assert.deepEqual(mapped[0].env, [{ name: "A", value: "1" }]);
});

test("buildGrokAcpSessionNewParams injects MCP servers and MCP-mode rules", () => {
  const params = buildGrokAcpSessionNewParams({
    cwd: "/repo",
    permissionMode: "auto",
    toolIntegrationMode: "mcp",
    injectedMcpServers: [{
      name: "netcatty-remote-hosts",
      command: "node",
      args: ["mcp.cjs"],
      env: [{ name: "NETCATTY_MCP_PORT", value: "1" }],
    }],
  });
  assert.equal(params.cwd, "/repo");
  assert.equal(params.mcpServers[0].name, "netcatty-remote-hosts");
  assert.equal(params.mcpServers[0].type, "stdio");
  assert.ok(Array.isArray(params.mcpServers[0].env));
  assert.deepEqual(params.mcpServers[0].env, [{ name: "NETCATTY_MCP_PORT", value: "1" }]);
  // Explicitly forbid the broken object-map shape in the shipped builder output.
  assert.equal(typeof params.mcpServers[0].env.NETCATTY_MCP_PORT, "undefined");
  assert.equal(params._meta.yoloMode, true);
  assert.match(String(params._meta.rules || ""), /netcatty-remote-hosts|MCP mode/i);
  assert.match(String(params._meta.rules || ""), /run_terminal_command|search_replace|write/);

  const skills = buildGrokAcpSessionNewParams({
    cwd: "/repo",
    permissionMode: "auto",
    toolIntegrationMode: "skills",
    injectedMcpServers: [],
  });
  assert.equal(skills._meta.yoloMode, true);
  assert.equal(skills._meta.rules, undefined);
});

test("buildGrokAcpInitializeParams and prompt params follow ACP shapes", () => {
  const init = buildGrokAcpInitializeParams();
  assert.equal(init.protocolVersion, ACP_PROTOCOL_VERSION);
  assert.equal(init.clientInfo.name, "netcatty");
  assert.deepEqual(
    buildGrokAcpPromptParams("sess-1", "hello"),
    { sessionId: "sess-1", prompt: [{ type: "text", text: "hello" }] },
  );
});

test("translateGrokAcpUpdate maps text, thought, tools to canonical emitter events", () => {
  const emitter = makeEmitter();
  const state = {};
  translateGrokAcpUpdate({
    sessionUpdate: "agent_thought_chunk",
    content: { text: "plan" },
  }, emitter, state);
  translateGrokAcpUpdate({
    sessionUpdate: "agent_message_chunk",
    content: { text: "Hi" },
  }, emitter, state);
  translateGrokAcpUpdate({
    sessionUpdate: "tool_call",
    toolCallId: "c1",
    toolName: "mcp__netcatty-remote-hosts__get_environment",
    rawInput: { x: 1 },
  }, emitter, state);
  translateGrokAcpUpdate({
    sessionUpdate: "tool_call_update",
    toolCallId: "c1",
    status: "completed",
    rawOutput: { ok: true },
  }, emitter, state);

  assert.deepEqual(emitter.calls, [
    ["reasoning", "plan"],
    ["reasoningEnd"],
    ["text", "Hi"],
    ["toolCall", "mcp__netcatty-remote-hosts__get_environment", { x: 1 }, "c1"],
    ["toolResult", "c1", "{\"ok\":true}", "mcp__netcatty-remote-hosts__get_environment"],
  ]);
});

test("createJsonRpcClient correlates request ids and parses lines", async () => {
  const written = [];
  const client = createJsonRpcClient({
    write: (line) => written.push(line),
    onMessage: (message, pending) => {
      if (message.id != null && pending.has(message.id)) {
        const waiter = pending.get(message.id);
        pending.delete(message.id);
        waiter.resolve(message.result);
      }
    },
  });
  const pending = client.request("initialize", { protocolVersion: 1 });
  assert.match(written[0], /"method":"initialize"/);
  const sent = JSON.parse(written[0]);
  client.handleLine(JSON.stringify({ jsonrpc: "2.0", id: sent.id, result: { ok: true } }));
  assert.deepEqual(await pending, { ok: true });
});

test("handleGrokAcpMessage routes session/update notifications", () => {
  const emitter = makeEmitter();
  const state = {};
  const pending = new Map();
  handleGrokAcpMessage({
    jsonrpc: "2.0",
    method: "session/update",
    params: {
      sessionId: "s1",
      update: {
        sessionUpdate: "agent_message_chunk",
        content: { text: "ok" },
      },
    },
  }, { emitter, state, pending });
  assert.equal(state.sessionId, "s1");
  assert.deepEqual(emitter.calls, [
    ["text", "ok"],
    ["sessionId", "s1"],
  ]);
});

test("runGrokAcpTurn drives initialize/session/new/prompt via fixture RPC", async () => {
  const emitter = makeEmitter();
  const methods = [];
  const result = await runGrokAcpTurn({
    prompt: "hi",
    binPath: "/usr/bin/grok",
    cwd: "/repo",
    permissionMode: "auto",
    toolIntegrationMode: "mcp",
    injectedMcpServers: [{
      name: "netcatty-remote-hosts",
      command: "node",
      args: ["mcp.cjs"],
      env: [{ name: "NETCATTY_MCP_PORT", value: "7" }],
    }],
    emitter,
    rpcClientFactory: ({ emitter: em, state }) => ({
      async request(method, params) {
        methods.push([method, params]);
        if (method === "initialize") return { protocolVersion: ACP_PROTOCOL_VERSION };
        if (method === "session/new") {
          assert.equal(params.cwd, "/repo");
          assert.equal(params.mcpServers[0].name, "netcatty-remote-hosts");
          assert.equal(params.mcpServers[0].type, "stdio");
          assert.ok(Array.isArray(params.mcpServers[0].env));
          assert.deepEqual(
            params.mcpServers[0].env,
            [{ name: "NETCATTY_MCP_PORT", value: "7" }],
          );
          return { sessionId: "acp-sess-1" };
        }
        if (method === "session/prompt") {
          // Simulate streamed ACP updates during the prompt
          translateGrokAcpUpdate({
            sessionUpdate: "agent_message_chunk",
            content: { text: "hello-acp" },
          }, em, state);
          return { stopReason: "end_turn" };
        }
        throw new Error(`unexpected method ${method}`);
      },
    }),
  });

  assert.equal(result.runtime, "acp");
  assert.equal(result.sessionId, "acp-sess-1");
  assert.deepEqual(methods.map((m) => m[0]), ["initialize", "session/new", "session/prompt"]);
  assert.ok(emitter.calls.some((c) => c[0] === "text" && c[1] === "hello-acp"));
  assert.ok(emitter.calls.some((c) => c[0] === "sessionId" && c[1] === "acp-sess-1"));
  assert.ok(emitter.calls.some((c) => c[0] === "done"));
});

test("runGrokAcpTurn reports missing CLI clearly", async () => {
  const emitter = makeEmitter();
  const result = await runGrokAcpTurn({
    prompt: "hi",
    binPath: "",
    emitter,
  });
  assert.equal(result.sessionId, null);
  assert.equal(result.runtime, "acp");
  assert.match(String(emitter.calls[0]?.[1] || ""), /not found/i);
});

test("registry grok backend defaults to ACP path and keeps streaming-json fallback", async () => {
  assert.ok(listBackends().includes("grok"));
  const driver = getDriver("grok");
  assert.equal(typeof driver.runTurn, "function");

  // Fallback runtime uses streaming-json builder path (no real binary).
  const emitter = makeEmitter();
  const fallback = await driver.runTurn({
    prompt: "x",
    binPath: "",
    grokRuntime: "streaming-json",
    emitter,
    env: {},
  });
  assert.equal(fallback.sessionId, null);
  assert.match(String(emitter.calls[0]?.[1] || ""), /not found/i);

  // Default ACP path also surfaces missing CLI without hanging.
  const emitter2 = makeEmitter();
  const acp = await driver.runTurn({
    prompt: "x",
    binPath: "",
    emitter: emitter2,
    env: {},
  });
  assert.equal(acp.runtime, "acp");
  assert.match(String(emitter2.calls[0]?.[1] || ""), /not found/i);
});
