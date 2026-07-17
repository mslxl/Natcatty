const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");
const fs = require("node:fs");
const { spawnSync } = require("node:child_process");
const tempDirBridge = require("./tempDirBridge.cjs");

test("getTempFilePath is unique for duplicate names in the same millisecond", () => {
  const originalNow = Date.now;
  Date.now = () => 1234567890;
  try {
    const first = tempDirBridge.getTempFilePath("upload.txt");
    const second = tempDirBridge.getTempFilePath("upload.txt");

    assert.notEqual(first, second);
    assert.equal(path.basename(first).endsWith("_upload.txt"), true);
    assert.equal(path.basename(second).endsWith("_upload.txt"), true);
  } finally {
    Date.now = originalNow;
  }
});

test("Netcatty temp root is a private directory owned by the current user", () => {
  const tempRoot = tempDirBridge.getTempDir();
  const stat = fs.lstatSync(tempRoot);
  assert.equal(stat.isDirectory(), true);
  assert.equal(stat.isSymbolicLink(), false);
  assert.equal(stat.mode & 0o777, 0o700);
  if (typeof process.getuid === "function") assert.equal(stat.uid, process.getuid());
  assert.equal(tempDirBridge.getTempDir(), tempRoot);
});

test("shared system temp roots resolve to a stable path under the user's home", async () => {
  const root = await fs.promises.mkdtemp(path.join(require("node:os").tmpdir(), "netcatty-shared-root-"));
  const fakeHome = path.join(root, "home");
  await fs.promises.mkdir(fakeHome);
  await fs.promises.chmod(root, 0o777);
  try {
    if (typeof process.getuid === "function") {
      assert.equal(tempDirBridge.resolvePrivateTempDir(root, fakeHome), path.join(fakeHome, ".netcatty", "tmp"));
    }
  } finally {
    await fs.promises.rm(root, { recursive: true, force: true });
  }
});

test("cached temp root is recreated after OS cleanup", async () => {
  const root = await fs.promises.mkdtemp(path.join(require("node:os").tmpdir(), "netcatty-private-root-"));
  const fakeHome = path.join(root, "home");
  await fs.promises.mkdir(fakeHome);
  await fs.promises.chmod(root, 0o700);
  try {
    const script = [
      'const fs = require("node:fs");',
      'const bridge = require("./electron/bridges/tempDirBridge.cjs");',
      'const first = bridge.getTempDir();',
      'fs.rmSync(first, { recursive: true, force: true });',
      'const second = bridge.getTempDir();',
      'if (first !== second || !fs.statSync(second).isDirectory()) process.exit(2);',
    ].join("\n");
    const result = spawnSync(process.execPath, ["-e", script], {
      cwd: path.resolve(__dirname, "../.."),
      env: { ...process.env, TMPDIR: root, HOME: fakeHome },
      encoding: "utf8",
    });
    assert.equal(result.status, 0, result.stderr || result.stdout);
  } finally {
    await fs.promises.rm(root, { recursive: true, force: true });
  }
});

test("tool output temp handlers write, read, and delete only Netcatty temp files", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({
    handle(channel, handler) {
      handlers.set(channel, handler);
    },
  });

  const write = handlers.get("netcatty:tempdir:toolOutputWrite");
  const read = handlers.get("netcatty:tempdir:toolOutputRead");
  const remove = handlers.get("netcatty:tempdir:toolOutputDelete");
  const saved = await write({}, { handleId: "tool-output-1", content: "large terminal output" });

  assert.equal(saved.ok, true);
  assert.equal(await read({}, { path: saved.path }), "large terminal output");
  assert.deepEqual(await read({}, {
    path: saved.path,
    request: { mode: "range", offset: 6, maxChars: 8 },
  }), {
    mode: "range",
    content: "terminal",
    totalChars: 21,
    startOffset: 6,
    endOffset: 14,
    nextOffset: 14,
    hasMore: true,
  });
  assert.deepEqual(await remove({}, { path: saved.path }), { ok: true });
  assert.equal(await read({}, { path: saved.path }), null);
  assert.deepEqual(await read({}, { path: "/etc/passwd" }), null);
});

test("tool output temp reader rejects symlinks that point outside Netcatty temp", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const linkPath = tempDirBridge.getTempFilePath("tool-output-link.log");
  await fs.promises.symlink("/etc/hosts", linkPath);
  try {
    assert.equal(await handlers.get("netcatty:tempdir:toolOutputRead")({}, { path: linkPath }), null);
  } finally {
    await fs.promises.unlink(linkPath);
  }
});

test("startup cleanup removes expired orphaned tool output files", async () => {
  const filePath = tempDirBridge.getTempFilePath("tool-output-expired.log");
  await fs.promises.writeFile(filePath, "secret");
  const old = new Date(Date.now() - 31 * 60 * 1_000);
  await fs.promises.utimes(filePath, old, old);
  const deleted = await tempDirBridge.cleanupExpiredToolOutputFiles();
  assert.equal(deleted >= 1, true);
  assert.equal(fs.existsSync(filePath), false);
});

test("persisted tool output search advances only past rendered matches", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const write = handlers.get("netcatty:tempdir:toolOutputWrite");
  const read = handlers.get("netcatty:tempdir:toolOutputRead");
  const remove = handlers.get("netcatty:tempdir:toolOutputDelete");
  const saved = await write({}, { handleId: "search-pagination", content: "match middle match tail" });

  try {
    const first = await read({}, {
      path: saved.path,
      request: { mode: "search", query: "match", maxChars: 1 },
    });
    assert.doesNotMatch(first.content, /No matches found/);
    assert.deepEqual(first.matchOffsets, [0]);
    assert.equal(first.nextOffset, 5);
    assert.equal(first.hasMore, true);

    const second = await read({}, {
      path: saved.path,
      request: { mode: "search", query: "match", offset: first.nextOffset, maxChars: 30 },
    });
    assert.deepEqual(second.matchOffsets, [13]);
  } finally {
    await remove({}, { path: saved.path });
  }
});
