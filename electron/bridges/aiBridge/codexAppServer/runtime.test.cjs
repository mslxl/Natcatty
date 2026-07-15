const test = require("node:test");
const assert = require("node:assert/strict");
const {
  CodexAppServerRuntime,
  buildTurnInput,
  mapAppServerModels,
  normalizeFileChanges,
  resolveCodexPermissionConfig,
} = require("./runtime.cjs");

class FakeConnection {
  constructor(options) {
    this.options = options;
    this.requests = [];
    this.responses = [];
    this.threadId = "thread-1";
    this.turnId = "turn-1";
  }
  async start() { return this; }
  async request(method, params) {
    this.requests.push({ method, params });
    if (method === "thread/start" || method === "thread/resume") {
      return { thread: { id: this.threadId } };
    }
    if (method === "turn/start") {
      if (this.turnStartGate) await this.turnStartGate;
      return { turn: { id: this.turnId } };
    }
    if (method === "turn/steer") {
      if (this.turnSteerGate) await this.turnSteerGate;
      if (this.turnSteerError) throw this.turnSteerError;
      return { turnId: this.turnId };
    }
    if (method === "turn/interrupt") return {};
    if (method === "model/list") {
      return {
        data: [
          {
            id: "gpt-first",
            displayName: "GPT First",
            description: "First model in the catalog",
            hidden: false,
            supportedReasoningEfforts: [{ reasoningEffort: "low" }],
            defaultReasoningEffort: "low",
            isDefault: false,
          },
          {
            id: "gpt-test",
            displayName: "GPT Test",
            description: "Server default model",
            hidden: false,
            supportedReasoningEfforts: [{ reasoningEffort: "low" }, { reasoningEffort: "high" }],
            defaultReasoningEffort: "high",
            isDefault: true,
          },
        ],
        nextCursor: null,
      };
    }
    return {};
  }
  respond(id, result) { this.responses.push({ id, result }); }
  respondError(id, code, message) { this.responses.push({ id, error: { code, message } }); }
  notify(message) { this.options.onNotification(message); }
  serverRequest(message) { return this.options.onServerRequest(message, this); }
  close() {}
}

function createEmitter() {
  const events = [];
  return {
    events,
    emitDone: () => events.push(["done"]),
    sessionId: (id) => events.push(["session", id]),
    text: (text) => events.push(["text", text]),
    reasoning: (text) => events.push(["reasoning", text]),
    reasoningEnd: () => events.push(["reasoning-end"]),
    toolCall: (name, args, id) => events.push(["tool-call", name, args, id]),
    toolResult: (id, output, name) => events.push(["tool-result", id, output, name]),
    fileChange: (id, changes, status) => events.push(["file-change", id, changes, status]),
    webSearch: (id, query, status) => events.push(["web-search", id, query, status]),
    planUpdate: (id, items, status) => events.push(["plan", id, items, status]),
    warning: (id, message) => events.push(["warning", id, message]),
    usage: (usage) => events.push(["usage", usage]),
  };
}

async function waitFor(predicate) {
  for (let index = 0; index < 50; index += 1) {
    if (predicate()) return;
    await new Promise((resolve) => setImmediate(resolve));
  }
  throw new Error("condition not reached");
}

test("permission modes map to fail-closed Codex policies", () => {
  assert.deepEqual(resolveCodexPermissionConfig("observer"), {
    approvalPolicy: "never",
    approvalsReviewer: "user",
    sandbox: "read-only",
    sandboxPolicy: { type: "readOnly", networkAccess: false },
  });
  assert.equal(resolveCodexPermissionConfig("confirm").approvalPolicy, "on-request");
  assert.equal(resolveCodexPermissionConfig("confirm").sandbox, "read-only");
  assert.equal(resolveCodexPermissionConfig("auto").sandbox, "danger-full-access");
});

test("turn input uses text plus local images only", () => {
  assert.deepEqual(buildTurnInput("hello", [
    { filePath: "/tmp/a.png", mediaType: "image/png" },
    { filePath: "/tmp/a.txt", mediaType: "text/plain" },
  ]), [
    { type: "text", text: "hello", text_elements: [] },
    { type: "localImage", path: "/tmp/a.png" },
  ]);
});

test("runtime maps lifecycle, activities, usage, and retry warnings", async () => {
  let connection;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => (connection = new FakeConnection(options)),
  });
  const emitter = createEmitter();
  const run = runtime.runTurn({
    requestId: "request-1",
    chatSessionId: "chat-1",
    prompt: "hello",
    cwd: "/repo",
    model: "gpt-test/high",
    permissionMode: "confirm",
    env: { HOME: "/home" },
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter,
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));

  connection.notify({ method: "item/agentMessage/delta", params: { threadId: "thread-1", turnId: "turn-1", itemId: "msg-1", delta: "Hi" } });
  connection.notify({ method: "turn/plan/updated", params: { threadId: "thread-1", turnId: "turn-1", plan: [{ step: "Inspect", status: "completed" }] } });
  connection.notify({ method: "item/started", params: { threadId: "thread-1", turnId: "turn-1", item: { type: "webSearch", id: "search-1", query: "Netcatty" } } });
  connection.notify({ method: "item/completed", params: { threadId: "thread-1", turnId: "turn-1", item: { type: "fileChange", id: "file-1", status: "completed", changes: [{ path: "a.ts", kind: { type: "add" } }] } } });
  connection.notify({ method: "thread/tokenUsage/updated", params: { threadId: "thread-1", turnId: "turn-1", tokenUsage: { last: { inputTokens: 10, cachedInputTokens: 2, outputTokens: 3, reasoningOutputTokens: 1, totalTokens: 13 } } } });
  connection.notify({ method: "error", params: { threadId: "thread-1", turnId: "turn-1", willRetry: true, error: { message: "network" } } });
  connection.notify({ method: "warning", params: { message: "global warning" } });
  connection.notify({ method: "turn/completed", params: { threadId: "thread-1", turn: { id: "turn-1", status: "completed", error: null } } });
  await run;

  assert.ok(emitter.events.some((event) => event[0] === "text" && event[1] === "Hi"));
  assert.ok(emitter.events.some((event) => event[0] === "plan"));
  assert.ok(emitter.events.some((event) => event[0] === "web-search" && event[3] === "running"));
  assert.ok(emitter.events.some((event) => event[0] === "file-change" && event[3] === "completed"));
  assert.ok(emitter.events.some((event) => event[0] === "usage" && event[1].cachedInputTokens === 2));
  assert.ok(emitter.events.some((event) => event[0] === "warning" && /retrying/.test(event[2])));
  assert.ok(emitter.events.some((event) => event[0] === "warning" && event[2] === "global warning"));
  assert.ok(emitter.events.some((event) => event[0] === "done"));
});

test("runtime delegates injected MCP approvals to Netcatty's policy gate", async () => {
  let connection;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => (connection = new FakeConnection(options)),
  });
  const run = runtime.runTurn({
    requestId: "request-mcp-policy",
    chatSessionId: "chat-mcp-policy",
    prompt: "inspect the terminal",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [{
      name: "netcatty-remote-hosts",
      command: "/abs/electron",
      args: ["/abs/server.cjs"],
      env: [{ name: "NETCATTY_MCP_PERMISSION_MODE", value: "confirm" }],
    }],
    emitter: createEmitter(),
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));

  const threadStart = connection.requests.find((request) => request.method === "thread/start");
  assert.deepEqual(threadStart.params.config.mcp_servers["netcatty-remote-hosts"], {
    command: "/abs/electron",
    args: ["/abs/server.cjs"],
    env: { NETCATTY_MCP_PERMISSION_MODE: "confirm" },
    default_tools_approval_mode: "approve",
  });

  connection.notify({
    method: "turn/completed",
    params: { threadId: "thread-1", turn: { id: "turn-1", status: "completed", error: null } },
  });
  await run;
});

test("runtime routes native approvals and request_user_input responses", async () => {
  let connection;
  let interaction;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => (connection = new FakeConnection(options)),
    sendInteractionRequest: (payload) => { interaction = payload; return true; },
  });
  const run = runtime.runTurn({
    requestId: "request-2",
    chatSessionId: "chat-2",
    prompt: "change it",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter: createEmitter(),
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));

  await connection.serverRequest({
    id: 70,
    method: "item/commandExecution/requestApproval",
    params: {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "cmd-1",
      command: "npm test",
      cwd: "/repo",
      availableDecisions: ["accept", "acceptForSession", "decline", "cancel"],
    },
  });
  assert.equal(interaction.kind, "command");
  assert.deepEqual(interaction.availableDecisions, ["accept", "acceptForSession", "decline", "cancel"]);
  runtime.respondInteraction(interaction.interactionId, { decision: "session" });
  assert.deepEqual(connection.responses.at(-1), { id: 70, result: { decision: "acceptForSession" } });

  await connection.serverRequest({
    id: 72,
    method: "item/permissions/requestApproval",
    params: {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "permissions-1",
      permissions: { network: { enabled: true }, fileSystem: null },
      cwd: "/repo",
    },
  });
  runtime.respondInteraction(interaction.interactionId, { decision: "once" });
  assert.deepEqual(connection.responses.at(-1), {
    id: 72,
    result: { permissions: { network: { enabled: true } }, scope: "turn" },
  });

  await connection.serverRequest({
    id: 71,
    method: "item/tool/requestUserInput",
    params: { threadId: "thread-1", turnId: "turn-1", itemId: "question-1", questions: [{ id: "choice", question: "Choose", header: "Mode", isOther: true, isSecret: false, options: null }] },
  });
  runtime.respondInteraction(interaction.interactionId, { answers: { choice: { answers: ["safe"] } } });
  assert.deepEqual(connection.responses.at(-1), { id: 71, result: { answers: { choice: { answers: ["safe"] } } } });

  connection.notify({ method: "turn/completed", params: { threadId: "thread-1", turn: { id: "turn-1", status: "completed", error: null } } });
  await run;
});

test("runtime steers the active turn with text, local images, and a stable user message id", async () => {
  let connection;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => (connection = new FakeConnection(options)),
  });
  const run = runtime.runTurn({
    requestId: "request-steer",
    chatSessionId: "chat-steer",
    prompt: "initial",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter: createEmitter(),
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));

  const result = await runtime.steerTurn("request-steer", {
    chatSessionId: "chat-steer",
    prompt: "use this image",
    attachments: [
      { filePath: "/tmp/image.png", mediaType: "image/png" },
      { filePath: "/tmp/notes.txt", mediaType: "text/plain" },
    ],
    clientUserMessageId: "user-steer-1",
  });

  assert.deepEqual(result, { status: "accepted" });
  assert.deepEqual(connection.requests.find((request) => request.method === "turn/steer"), {
    method: "turn/steer",
    params: {
      threadId: "thread-1",
      expectedTurnId: "turn-1",
      input: [
        { type: "text", text: "use this image", text_elements: [] },
        { type: "localImage", path: "/tmp/image.png" },
      ],
      clientUserMessageId: "user-steer-1",
    },
  });

  connection.notify({ method: "turn/completed", params: { threadId: "thread-1", turn: { id: "turn-1", status: "completed", error: null } } });
  await run;
});

test("runtime serializes steering and classifies non-steerable turns", async () => {
  let connection;
  let releaseSteer;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => {
      connection = new FakeConnection(options);
      connection.turnSteerGate = new Promise((resolve) => { releaseSteer = resolve; });
      return connection;
    },
  });
  const run = runtime.runTurn({
    requestId: "request-steer-busy",
    chatSessionId: "chat-steer-busy",
    prompt: "initial",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter: createEmitter(),
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));

  const first = runtime.steerTurn("request-steer-busy", {
    chatSessionId: "chat-steer-busy",
    prompt: "first",
    clientUserMessageId: "user-first",
  });
  await waitFor(() => connection.requests.some((request) => request.method === "turn/steer"));
  assert.equal((await runtime.steerTurn("request-steer-busy", {
    chatSessionId: "chat-steer-busy",
    prompt: "second",
    clientUserMessageId: "user-second",
  })).status, "busy");
  releaseSteer();
  assert.equal((await first).status, "accepted");

  const error = new Error("active turn cannot be steered");
  error.data = { activeTurnNotSteerable: { turnKind: "review" } };
  connection.turnSteerError = error;
  const rejected = await runtime.steerTurn("request-steer-busy", {
    chatSessionId: "chat-steer-busy",
    prompt: "review change",
    clientUserMessageId: "user-review",
  });
  assert.deepEqual(rejected, {
    status: "not-steerable",
    turnKind: "review",
    message: "active turn cannot be steered",
  });

  connection.notify({ method: "turn/completed", params: { threadId: "thread-1", turn: { id: "turn-1", status: "completed", error: null } } });
  await run;
});

test("stop during steering cancels the UI result without creating a replacement turn", async () => {
  let connection;
  let releaseSteer;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => {
      connection = new FakeConnection(options);
      connection.turnSteerGate = new Promise((resolve) => { releaseSteer = resolve; });
      return connection;
    },
  });
  const run = runtime.runTurn({
    requestId: "request-steer-stop",
    chatSessionId: "chat-steer-stop",
    prompt: "initial",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter: createEmitter(),
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));
  const steer = runtime.steerTurn("request-steer-stop", {
    chatSessionId: "chat-steer-stop",
    prompt: "too late",
    clientUserMessageId: "user-steer-stop",
  });
  await waitFor(() => connection.requests.some((request) => request.method === "turn/steer"));
  assert.equal(await runtime.cancelTurn("request-steer-stop"), true);
  releaseSteer();
  assert.equal((await steer).status, "cancelled");
  assert.equal(connection.requests.filter((request) => request.method === "turn/start").length, 1);

  connection.notify({ method: "turn/completed", params: { threadId: "thread-1", turn: { id: "turn-1", status: "interrupted", error: null } } });
  await run;
});

test("stop requested while turn/start is pending interrupts the assigned turn", async () => {
  let connection;
  let releaseTurnStart;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => {
      connection = new FakeConnection(options);
      connection.turnStartGate = new Promise((resolve) => { releaseTurnStart = resolve; });
      return connection;
    },
  });
  const emitter = createEmitter();
  const run = runtime.runTurn({
    requestId: "request-stop",
    chatSessionId: "chat-stop",
    prompt: "wait",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter,
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));
  assert.equal(await runtime.cancelTurn("request-stop"), true);
  assert.equal(connection.requests.some((request) => request.method === "turn/interrupt"), false);

  releaseTurnStart();
  await waitFor(() => connection.requests.some((request) => request.method === "turn/interrupt"));
  connection.notify({
    method: "turn/completed",
    params: { threadId: "thread-1", turn: { id: "turn-1", status: "interrupted", error: null } },
  });
  await run;
  assert.equal(connection.requests.filter((request) => request.method === "turn/interrupt").length, 1);
  assert.ok(emitter.events.some((event) => event[0] === "done"));
});

test("unsupported requests fail immediately and warn without hanging", async () => {
  let connection;
  const emitter = createEmitter();
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => (connection = new FakeConnection(options)),
  });
  const run = runtime.runTurn({
    requestId: "request-unsupported",
    chatSessionId: "chat-unsupported",
    prompt: "hello",
    permissionMode: "confirm",
    env: {},
    binPath: "/bin/codex",
    injectedMcpServers: [],
    emitter,
  });
  await waitFor(() => connection?.requests.some((request) => request.method === "turn/start"));
  await connection.serverRequest({
    id: 99,
    method: "item/unknown/requestApproval",
    params: { threadId: "thread-1", turnId: "turn-1" },
  });
  assert.deepEqual(connection.responses.at(-1), {
    id: 99,
    error: { code: -32601, message: "Unsupported Codex App Server request: item/unknown/requestApproval" },
  });
  assert.ok(emitter.events.some((event) => event[0] === "warning"));
  await connection.serverRequest({
    id: 100,
    method: "item/commandExecution/requestApproval",
    params: { threadId: "thread-1", turnId: "turn-1", itemId: "cmd-no-renderer", command: "rm -rf /" },
  });
  assert.deepEqual(connection.responses.at(-1), { id: 100, result: { decision: "decline" } });
  connection.notify({ method: "turn/completed", params: { threadId: "thread-1", turn: { id: "turn-1", status: "completed", error: null } } });
  await run;
});

test("model and file-change normalization preserve UI contract", async () => {
  assert.deepEqual(normalizeFileChanges([
    { path: "a", kind: { type: "add" } },
    { path: "b", kind: { type: "delete" } },
    { path: "c", kind: { type: "update", move_path: null } },
  ]), [
    { path: "a", kind: "add" },
    { path: "b", kind: "delete" },
    { path: "c", kind: "update" },
  ]);
  assert.equal(mapAppServerModels([{ id: "hidden", hidden: true }]).length, 0);

  let connection;
  const runtime = new CodexAppServerRuntime({
    connectionFactory: (options) => (connection = new FakeConnection(options)),
  });
  const catalog = await runtime.listModels({ binPath: "/bin/codex", env: {} });
  assert.equal(connection.requests[0].method, "model/list");
  assert.equal(catalog.currentModelId, "gpt-test/high");
  assert.equal(catalog.models[0].id, "gpt-first");
  assert.deepEqual(catalog.models[1].thinkingLevels, ["low", "high"]);
  assert.equal(catalog.models[1].defaultThinkingLevel, "high");
});
