const test = require("node:test");
const assert = require("node:assert/strict");
const { CodebuddySessionManager, computeOptionsFingerprint } = require("./codebuddySessionManager.cjs");

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
    emitEvent: (ev) => events.push({ k: "event", ev }),
  };
  return { events, emitter };
}

/** Create a fake V2 session that yields predefined messages. */
function fakeSession(messages, opts = {}) {
  let sentMessages = [];
  let closed = false;
  return {
    sessionId: opts.sessionId || "fake-sess-1",
    sentMessages,
    get closed() { return closed; },
    async connect() {},
    async send(msg) { sentMessages.push(msg); },
    async *stream() { for (const m of messages) yield m; },
    async interrupt() {},
    async setModel(model) { this._model = model; },
    setHooks(hooks) { this._hooks = hooks; },
    setCanUseTool(handler) { this._canUseTool = handler; },
    close() { closed = true; },
  };
}

test("getOrCreateSession reuses existing session when options match", async () => {
  const mgr = new CodebuddySessionManager();
  const session = fakeSession([], { sessionId: "existing-sess" });
  const opts = { cwd: "/tmp", model: "glm-5" };
  mgr.sessions.set("reuse-key", { session, fingerprint: computeOptionsFingerprint(opts) });

  const result = await mgr.getOrCreateSession({
    sessionKey: "reuse-key",
    sessionOptions: opts,
  });
  assert.equal(result, session);
});

test("getOrCreateSession refreshes turn-scoped callbacks on a reused session", async () => {
  let createdOptions;
  const session = fakeSession([], { sessionId: "callback-session" });
  const mgr = new CodebuddySessionManager({
    loadSdk: async () => ({
      unstable_v2_createSession: (options) => {
        createdOptions = options;
        return session;
      },
      unstable_v2_resumeSession: () => session,
    }),
  });
  const firstEvents = [];
  const secondEvents = [];
  const firstOptions = {
    cwd: "/tmp",
    hooks: { Notification: [{ hooks: [() => firstEvents.push("hook")] }] },
    canUseTool: async () => ({ behavior: "allow", updatedInput: {} }),
    elicitation: {
      create: async () => {
        firstEvents.push("elicitation");
        return { action: "accept" };
      },
    },
  };
  const secondOptions = {
    cwd: "/tmp",
    hooks: { Notification: [{ hooks: [() => secondEvents.push("hook")] }] },
    canUseTool: async () => ({ behavior: "deny", message: "second turn" }),
    elicitation: {
      create: async () => {
        secondEvents.push("elicitation");
        return { action: "decline" };
      },
    },
  };

  const first = await mgr.getOrCreateSession({
    sessionKey: "callback-key",
    sessionOptions: firstOptions,
  });
  const second = await mgr.getOrCreateSession({
    sessionKey: "callback-key",
    sessionOptions: secondOptions,
  });

  assert.equal(first, session);
  assert.equal(second, session);
  assert.equal(session._hooks, secondOptions.hooks);
  assert.equal(session._canUseTool, secondOptions.canUseTool);
  assert.notEqual(createdOptions.elicitation, firstOptions.elicitation);
  await session._hooks.Notification[0].hooks[0]();
  assert.deepEqual(await session._canUseTool(), {
    behavior: "deny",
    message: "second turn",
  });
  assert.deepEqual(
    await createdOptions.elicitation.create({}, { signal: new AbortController().signal }),
    { action: "decline" },
  );
  assert.deepEqual(firstEvents, []);
  assert.deepEqual(secondEvents, ["hook", "elicitation"]);
});

test("getOrCreateSession closes stale session when options change", async () => {
  const oldSession = fakeSession([], { sessionId: "old-sess" });
  const replacementSession = fakeSession([], { sessionId: "new-sess" });
  const mgr = new CodebuddySessionManager({
    loadSdk: async () => ({
      unstable_v2_createSession: () => replacementSession,
      unstable_v2_resumeSession: () => replacementSession,
    }),
  });
  const oldOpts = { cwd: "/tmp", model: "glm-4" };
  const newOpts = { cwd: "/tmp", model: "glm-5" };
  mgr.sessions.set("stale-key", {
    session: oldSession,
    fingerprint: computeOptionsFingerprint(oldOpts),
  });

  const result = await mgr.getOrCreateSession({
    sessionKey: "stale-key",
    sessionOptions: newOpts,
  });

  assert.ok(oldSession.closed);
  assert.equal(result, replacementSession);
  assert.equal(mgr.sessions.get("stale-key").session, replacementSession);
  assert.equal(
    mgr.sessions.get("stale-key").fingerprint,
    computeOptionsFingerprint(newOpts),
  );
});

test("computeOptionsFingerprint detects option changes", () => {
  const base = { cwd: "/tmp", model: "glm-5", maxTurns: 10, effort: "high" };
  const same = { cwd: "/tmp", model: "glm-5", maxTurns: 10, effort: "high" };
  const diffModel = { cwd: "/tmp", model: "glm-4", maxTurns: 10, effort: "high" };
  const diffMaxTurns = { cwd: "/tmp", model: "glm-5", maxTurns: 20, effort: "high" };
  const diffEffort = { cwd: "/tmp", model: "glm-5", maxTurns: 10, effort: "low" };
  assert.equal(computeOptionsFingerprint(base), computeOptionsFingerprint(same));
  assert.notEqual(computeOptionsFingerprint(base), computeOptionsFingerprint(diffModel));
  assert.notEqual(computeOptionsFingerprint(base), computeOptionsFingerprint(diffMaxTurns));
  assert.notEqual(computeOptionsFingerprint(base), computeOptionsFingerprint(diffEffort));
});

test("runTurn streams messages via V2 session when available", async () => {
  const mgr = new CodebuddySessionManager();
  const messages = [
    { type: "system", session_id: "sess-v2" },
    { type: "stream_event", event: { type: "content_block_delta", delta: { type: "text_delta", text: "hi from v2" } } },
  ];
  const session = fakeSession(messages, { sessionId: "sess-v2" });
  // Pre-populate the session map to bypass SDK import.
  mgr.sessions.set("preloaded-key", { session, fingerprint: computeOptionsFingerprint({}) });

  const { events, emitter } = collector();
  const result = await mgr.runTurn({
    sessionKey: "preloaded-key",
    prompt: "say hi",
    attachments: [],
    options: { abortController: new AbortController() },
    emitter,
    sessionOptions: {},
  });

  assert.deepEqual(result, { sessionId: "sess-v2", usedV2: true });
  assert.ok(events.some((e) => e.k === "text" && e.t === "hi from v2"));
  assert.ok(events.some((e) => e.k === "done"));
  assert.ok(session.sentMessages.includes("say hi"));
});

test("steer returns unsupported when no session exists", async () => {
  const mgr = new CodebuddySessionManager();
  const { emitter } = collector();
  const result = await mgr.steer({
    sessionKey: "nonexistent",
    prompt: "follow up",
    attachments: [],
    emitter,
  });
  assert.deepEqual(result, { status: "unsupported" });
});

test("steer sends into the active session and leaves streaming to runTurn", async () => {
  const mgr = new CodebuddySessionManager();
  const session = fakeSession([]);
  let streamCalls = 0;
  let markStreamStarted;
  let releaseOriginalStream;
  const streamStarted = new Promise((resolve) => { markStreamStarted = resolve; });
  const originalStreamGate = new Promise((resolve) => { releaseOriginalStream = resolve; });
  session.stream = async function* stream() {
    streamCalls += 1;
    if (streamCalls === 1) {
      markStreamStarted();
      yield {
        type: "stream_event",
        event: { type: "content_block_delta", delta: { type: "text_delta", text: "before steer" } },
      };
      await originalStreamGate;
      yield {
        type: "stream_event",
        event: { type: "content_block_delta", delta: { type: "text_delta", text: "after steer" } },
      };
      return;
    }
    yield {
      type: "stream_event",
      event: { type: "content_block_delta", delta: { type: "text_delta", text: "second consumer" } },
    };
  };
  mgr.sessions.set("steer-key", {
    session,
    fingerprint: computeOptionsFingerprint({}),
  });

  const { events, emitter } = collector();
  const run = mgr.runTurn({
    sessionKey: "steer-key",
    prompt: "start",
    attachments: [],
    options: { abortController: new AbortController() },
    emitter,
    sessionOptions: {},
  });
  await streamStarted;

  const result = await mgr.steer({
    sessionKey: "steer-key",
    prompt: "now do this",
    attachments: [],
    emitter,
  });

  assert.deepEqual(result, { status: "accepted" });
  assert.ok(session.sentMessages.includes("now do this"));
  assert.equal(streamCalls, 1);
  assert.equal(events.filter((event) => event.k === "done").length, 0);

  releaseOriginalStream();
  await run;

  assert.deepEqual(
    events.filter((event) => event.k === "text").map((event) => event.t),
    ["before steer", "after steer"],
  );
  assert.equal(events.filter((event) => event.k === "done").length, 1);
});

test("closeSession removes and closes the session", () => {
  const mgr = new CodebuddySessionManager();
  const session = fakeSession([]);
  mgr.sessions.set("close-key", { session, fingerprint: null });

  mgr.closeSession("close-key");
  assert.ok(!mgr.sessions.has("close-key"));
  assert.ok(session.closed);
});

test("closeForChat closes all sessions matching the chat prefix", () => {
  const mgr = new CodebuddySessionManager();
  const s1 = fakeSession([]);
  const s2 = fakeSession([]);
  const s3 = fakeSession([]);
  mgr.sessions.set("chat1\u0000codebuddy\u0000/bin/cb\u0000sdk", { session: s1, fingerprint: null });
  mgr.sessions.set("chat1\u0000codebuddy\u0000/other/cb\u0000sdk", { session: s2, fingerprint: null });
  mgr.sessions.set("chat2\u0000codebuddy\u0000/bin/cb\u0000sdk", { session: s3, fingerprint: null });

  mgr.closeForChat("chat1");
  assert.ok(!mgr.sessions.has("chat1\u0000codebuddy\u0000/bin/cb\u0000sdk"));
  assert.ok(!mgr.sessions.has("chat1\u0000codebuddy\u0000/other/cb\u0000sdk"));
  assert.ok(mgr.sessions.has("chat2\u0000codebuddy\u0000/bin/cb\u0000sdk"));
  assert.ok(s1.closed);
  assert.ok(s2.closed);
  assert.ok(!s3.closed);
});

test("closeAll closes every session", () => {
  const mgr = new CodebuddySessionManager();
  const s1 = fakeSession([]);
  const s2 = fakeSession([]);
  mgr.sessions.set("a", { session: s1, fingerprint: null });
  mgr.sessions.set("b", { session: s2, fingerprint: null });

  mgr.closeAll();
  assert.equal(mgr.sessions.size, 0);
  assert.ok(s1.closed);
  assert.ok(s2.closed);
});

test("setModel returns false when session does not exist", async () => {
  const mgr = new CodebuddySessionManager();
  const result = await mgr.setModel("missing", "new-model");
  assert.equal(result, false);
});

test("setModel delegates to the session", async () => {
  const mgr = new CodebuddySessionManager();
  const session = fakeSession([]);
  mgr.sessions.set("model-key", { session, fingerprint: null });

  const result = await mgr.setModel("model-key", "glm-5");
  assert.equal(result, true);
  assert.equal(session._model, "glm-5");
});

test("resolveElicitation resolves pending and returns true", () => {
  const mgr = new CodebuddySessionManager();
  let resolved;
  mgr.elicitationPending.set("el-1", {
    resolve: (v) => { resolved = v; },
    reject: () => {},
  });

  const ok = mgr.resolveElicitation("el-1", { action: "accept" });
  assert.equal(ok, true);
  assert.deepEqual(resolved, { action: "accept" });
  assert.ok(!mgr.elicitationPending.has("el-1"));
});

test("resolveElicitation returns false for unknown id", () => {
  const mgr = new CodebuddySessionManager();
  const ok = mgr.resolveElicitation("unknown", { action: "cancel" });
  assert.equal(ok, false);
});
