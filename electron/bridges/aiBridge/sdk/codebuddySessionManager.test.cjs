const test = require("node:test");
const assert = require("node:assert/strict");
const { CodebuddySessionManager } = require("./codebuddySessionManager.cjs");

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
    close() { closed = true; },
  };
}

test("getOrCreateSession reuses existing session from map", async () => {
  const mgr = new CodebuddySessionManager();
  const session = fakeSession([], { sessionId: "existing-sess" });
  mgr.sessions.set("reuse-key", session);

  const result = await mgr.getOrCreateSession({
    sessionKey: "reuse-key",
    sessionOptions: { cwd: "/tmp" },
  });
  assert.equal(result, session);
});

test("runTurn streams messages via V2 session when available", async () => {
  const mgr = new CodebuddySessionManager();
  const messages = [
    { type: "system", session_id: "sess-v2" },
    { type: "stream_event", event: { type: "content_block_delta", delta: { type: "text_delta", text: "hi from v2" } } },
  ];
  const session = fakeSession(messages, { sessionId: "sess-v2" });
  // Pre-populate the session map to bypass SDK import.
  mgr.sessions.set("preloaded-key", session);

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

test("steer sends follow-up message on existing session", async () => {
  const mgr = new CodebuddySessionManager();
  const messages = [
    { type: "stream_event", event: { type: "content_block_delta", delta: { type: "text_delta", text: "steered" } } },
  ];
  const session = fakeSession(messages);
  mgr.sessions.set("steer-key", session);

  const { events, emitter } = collector();
  const result = await mgr.steer({
    sessionKey: "steer-key",
    prompt: "now do this",
    attachments: [],
    emitter,
  });

  assert.deepEqual(result, { status: "accepted" });
  assert.ok(session.sentMessages.includes("now do this"));
  assert.ok(events.some((e) => e.k === "text" && e.t === "steered"));
  assert.ok(events.some((e) => e.k === "done"));
});

test("closeSession removes and closes the session", () => {
  const mgr = new CodebuddySessionManager();
  const session = fakeSession([]);
  mgr.sessions.set("close-key", session);

  mgr.closeSession("close-key");
  assert.ok(!mgr.sessions.has("close-key"));
  assert.ok(session.closed);
});

test("closeForChat closes all sessions matching the chat prefix", () => {
  const mgr = new CodebuddySessionManager();
  const s1 = fakeSession([]);
  const s2 = fakeSession([]);
  const s3 = fakeSession([]);
  mgr.sessions.set("chat1\u0000codebuddy\u0000/bin/cb\u0000sdk", s1);
  mgr.sessions.set("chat1\u0000codebuddy\u0000/other/cb\u0000sdk", s2);
  mgr.sessions.set("chat2\u0000codebuddy\u0000/bin/cb\u0000sdk", s3);

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
  mgr.sessions.set("a", s1);
  mgr.sessions.set("b", s2);

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
  mgr.sessions.set("model-key", session);

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
