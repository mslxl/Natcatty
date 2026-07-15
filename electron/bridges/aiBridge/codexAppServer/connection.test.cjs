const test = require("node:test");
const assert = require("node:assert/strict");
const { EventEmitter, once } = require("node:events");
const { PassThrough } = require("node:stream");
const {
  CodexAppServerConnection,
  buildCodexAppServerKey,
  buildCodexAppServerLaunch,
} = require("./connection.cjs");

function createFakeChild() {
  const child = new EventEmitter();
  child.stdin = new PassThrough();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.killed = false;
  child.kill = () => { child.killed = true; };
  return child;
}

async function readJsonLine(stream) {
  const [chunk] = await once(stream, "data");
  return JSON.parse(String(chunk).trim());
}

test("buildCodexAppServerLaunch runs JS entries through Node without a shell", () => {
  assert.deepEqual(
    buildCodexAppServerLaunch("/opt/codex/bin/codex.js", ["app-server", "--help"], { nodePath: "/usr/bin/node" }),
    {
      command: "/usr/bin/node",
      args: ["/opt/codex/bin/codex.js", "app-server", "--help"],
      env: { ELECTRON_RUN_AS_NODE: "1" },
    },
  );
  assert.deepEqual(
    buildCodexAppServerLaunch("/usr/local/bin/codex"),
    { command: "/usr/local/bin/codex", args: ["app-server", "--stdio"] },
  );
  assert.throws(() => buildCodexAppServerLaunch("C:\\npm\\codex.cmd"), /shell shim/);
});

test("App Server connection initializes once and correlates JSONL requests", async () => {
  const child = createFakeChild();
  const notifications = [];
  const connection = new CodexAppServerConnection({
    binPath: "/usr/bin/codex",
    env: { HOME: "/tmp/home" },
    appVersion: "1.2.3",
    spawnImpl: () => child,
    onNotification: (message) => notifications.push(message),
  });

  const startPromise = connection.start();
  const initialize = await readJsonLine(child.stdin);
  assert.equal(initialize.method, "initialize");
  assert.equal(initialize.params.clientInfo.name, "netcatty");
  assert.equal(initialize.params.capabilities.experimentalApi, true);
  child.stdout.write(`${JSON.stringify({ id: initialize.id, result: { userAgent: "codex" } })}\n`);
  await startPromise;
  const initialized = await readJsonLine(child.stdin);
  assert.equal(initialized.method, "initialized");

  const requestPromise = connection.request("model/list", { limit: 100 });
  const request = await readJsonLine(child.stdin);
  assert.equal(request.method, "model/list");
  child.stdout.write(`${JSON.stringify({ id: request.id, result: { data: [], nextCursor: null } })}\n`);
  assert.deepEqual(await requestPromise, { data: [], nextCursor: null });

  child.stdout.write(`${JSON.stringify({ method: "warning", params: { message: "heads up" } })}\n`);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(notifications[0].method, "warning");
  connection.close();
});

test("App Server connection rejects pending RPCs when the process exits", async () => {
  const child = createFakeChild();
  let fatal;
  const connection = new CodexAppServerConnection({
    binPath: "/usr/bin/codex",
    env: {},
    spawnImpl: () => child,
    onFatal: (error) => { fatal = error; },
  });
  const startPromise = connection.start();
  const initialize = await readJsonLine(child.stdin);
  child.stdout.write(`${JSON.stringify({ id: initialize.id, result: {} })}\n`);
  await startPromise;
  await readJsonLine(child.stdin); // initialized notification

  const request = connection.request("thread/start", {});
  await readJsonLine(child.stdin);
  child.emit("exit", 1, null);
  await assert.rejects(request, /exited unexpectedly/);
  assert.match(fatal.message, /code 1/);
});

test("App Server process keys include executable and environment identity", () => {
  assert.notEqual(
    buildCodexAppServerKey("/a/codex", { HOME: "/a" }),
    buildCodexAppServerKey("/b/codex", { HOME: "/a" }),
  );
  assert.notEqual(
    buildCodexAppServerKey("/a/codex", { HOME: "/a" }),
    buildCodexAppServerKey("/a/codex", { HOME: "/b" }),
  );
});
