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
      assert.equal(tempDirBridge.resolvePrivateTempDir(root, fakeHome), path.join(fakeHome, ".netcatty", "tmp", "Netcatty"));
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
  const restore = handlers.get("netcatty:tempdir:toolOutputRestore");
  const remove = handlers.get("netcatty:tempdir:toolOutputDelete");
  const record = {
    schemaVersion: 1,
    handleId: "tool-output-1",
    chatSessionId: "chat-1",
    capabilityId: "terminal.execute",
    totalChars: 21,
    storedChars: 21,
    sourceTruncated: false,
    preview: "large terminal output",
    storedAt: Date.now(),
    accessedAt: Date.now(),
  };
  const saved = await write({}, { record, content: "large terminal output" });

  assert.equal(saved.ok, true);
  assert.deepEqual(await restore({}, { handleId: "tool-output-1", chatSessionId: "chat-other" }), null);
  const restored = await restore({}, { handleId: "tool-output-1", chatSessionId: "chat-1" });
  assert.equal(restored.path, saved.path);
  assert.deepEqual({ ...restored.record, accessedAt: record.accessedAt }, record);
  assert.equal(restored.record.accessedAt >= record.accessedAt, true);
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
  assert.equal(await restore({}, { handleId: "tool-output-1", chatSessionId: "chat-1" }), null);
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

test("tool output temp writer rejects output arriving after terminal close", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const terminalSessionId = "terminal-closed-before-write";
  await handlers.get("netcatty:tempdir:toolOutputDeleteTerminal")({}, { terminalSessionId });
  const now = Date.now();
  const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {
    record: {
      schemaVersion: 1,
      handleId: "late-terminal-output",
      chatSessionId: "chat-late-terminal",
      capabilityId: "terminal.execute",
      terminalSessionId,
      totalChars: 4,
      storedChars: 4,
      sourceTruncated: false,
      preview: "late",
      storedAt: now,
      accessedAt: now,
    },
    content: "late",
  });

  assert.equal(saved.ok, false);
  assert.match(saved.error, /already closed/);
});

test("terminal close wins when a persisted output read is already in flight", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const terminalSessionId = "terminal-close-during-read";
  const now = Date.now();
  const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {
    record: {
      schemaVersion: 1,
      handleId: "read-close-race",
      chatSessionId: "chat-read-close-race",
      capabilityId: "terminal.execute",
      terminalSessionId,
      totalChars: 7,
      storedChars: 7,
      sourceTruncated: false,
      preview: "private",
      storedAt: now,
      accessedAt: now,
    },
    content: "private",
  });
  const originalOpen = fs.promises.open;
  let signalReadStarted;
  const readStarted = new Promise(resolve => { signalReadStarted = resolve; });
  let releaseRead;
  const readReleased = new Promise(resolve => { releaseRead = resolve; });
  fs.promises.open = async (...args) => {
    const handle = await originalOpen(...args);
    if (path.resolve(String(args[0])) !== path.resolve(saved.path)) return handle;
    return {
      stat: handle.stat.bind(handle),
      close: handle.close.bind(handle),
      read: handle.read.bind(handle),
      readFile: async (...readArgs) => {
        signalReadStarted();
        await readReleased;
        return handle.readFile(...readArgs);
      },
    };
  };

  try {
    const reading = handlers.get("netcatty:tempdir:toolOutputRead")({}, { path: saved.path });
    await readStarted;
    await handlers.get("netcatty:tempdir:toolOutputDeleteTerminal")({}, { terminalSessionId });
    releaseRead();

    assert.equal(await reading, null);
    assert.equal(fs.existsSync(saved.path), false);
    assert.equal(fs.existsSync(saved.manifestPath), false);
  } finally {
    fs.promises.open = originalOpen;
    releaseRead?.();
    await Promise.allSettled([
      fs.promises.unlink(saved.path),
      fs.promises.unlink(saved.manifestPath),
    ]);
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

test("startup cleanup removes abandoned pending manifests", async () => {
  const filePath = tempDirBridge.getTempFilePath("tool-output-abandoned.manifest.pending");
  await fs.promises.writeFile(filePath, "pending");
  const old = new Date(Date.now() - 31 * 60 * 1_000);
  await fs.promises.utimes(filePath, old, old);

  const deleted = await tempDirBridge.cleanupExpiredToolOutputFiles();

  assert.equal(deleted >= 1, true);
  assert.equal(fs.existsSync(filePath), false);
});

test("managed tool output survives short restarts and expires after thirty days", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const now = Date.now();
  const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {
    record: {
      schemaVersion: 1,
      handleId: "tool-output-ttl",
      chatSessionId: "chat-ttl",
      capabilityId: "terminal.execute",
      totalChars: 7,
      storedChars: 7,
      sourceTruncated: false,
      preview: "durable",
      storedAt: now,
      accessedAt: now,
    },
    content: "durable",
  });
  const thirtyOneMinutesAgo = new Date(Date.now() - 31 * 60 * 1_000);
  await Promise.all([
    fs.promises.utimes(saved.path, thirtyOneMinutesAgo, thirtyOneMinutesAgo),
    fs.promises.utimes(saved.manifestPath, thirtyOneMinutesAgo, thirtyOneMinutesAgo),
  ]);

  await tempDirBridge.cleanupExpiredToolOutputFiles();
  assert.equal(fs.existsSync(saved.path), true);
  assert.equal(fs.existsSync(saved.manifestPath), true);

  const forgedFutureMtime = new Date(now + 365 * 24 * 60 * 60 * 1_000);
  await fs.promises.utimes(saved.manifestPath, forgedFutureMtime, forgedFutureMtime);
  await tempDirBridge.cleanupExpiredToolOutputFiles(now + 31 * 24 * 60 * 60 * 1_000);
  assert.equal(fs.existsSync(saved.path), false);
  assert.equal(fs.existsSync(saved.manifestPath), false);
});

test("persisted tool output search advances only past rendered matches", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const write = handlers.get("netcatty:tempdir:toolOutputWrite");
  const read = handlers.get("netcatty:tempdir:toolOutputRead");
  const remove = handlers.get("netcatty:tempdir:toolOutputDelete");
  const now = Date.now();
  const saved = await write({}, {
    record: {
      schemaVersion: 1,
      handleId: "search-pagination",
      chatSessionId: "chat-search",
      capabilityId: "terminal.execute",
      totalChars: 23,
      storedChars: 23,
      sourceTruncated: false,
      preview: "match middle match tail",
      storedAt: now,
      accessedAt: now,
    },
    content: "match middle match tail",
  });

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

test("tool output temp restore rejects content tampering and deletes by chat session", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const now = Date.now();
  const record = {
    schemaVersion: 1,
    handleId: "tool-output-restart",
    chatSessionId: "chat-restart",
    capabilityId: "terminal.execute",
    terminalSessionId: "terminal-1",
    totalChars: 16,
    storedChars: 16,
    sourceTruncated: false,
    preview: "restart content",
    storedAt: now,
    accessedAt: now,
  };
  const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {
    record,
    content: "restart content!",
  });

  assert.ok(saved.manifestPath);
  assert.equal((await fs.promises.stat(saved.path)).mode & 0o777, 0o600);
  assert.equal((await fs.promises.stat(saved.manifestPath)).mode & 0o777, 0o600);

  await fs.promises.writeFile(saved.path, "tampered content", { encoding: "utf16le" });
  assert.equal(await handlers.get("netcatty:tempdir:toolOutputRestore")({}, {
    handleId: record.handleId,
    chatSessionId: record.chatSessionId,
  }), null);
  assert.equal(fs.existsSync(saved.path), false);
  assert.equal(fs.existsSync(saved.manifestPath), false);

  const deleted = await handlers.get("netcatty:tempdir:toolOutputDeleteSession")({}, {
    chatSessionId: record.chatSessionId,
  });
  assert.equal(deleted.deletedCount, 0);
  assert.equal(fs.existsSync(saved.path), false);
  assert.equal(fs.existsSync(saved.manifestPath), false);
});

test("tool output ownership metadata cannot be reassigned to another chat", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const now = Date.now();
  const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {
    record: {
      schemaVersion: 1,
      handleId: "tool-output-owned",
      chatSessionId: "chat-owner",
      capabilityId: "terminal.execute",
      totalChars: 7,
      storedChars: 7,
      sourceTruncated: false,
      preview: "private",
      storedAt: now,
      accessedAt: now,
    },
    content: "private",
  });
  const manifest = JSON.parse(await fs.promises.readFile(saved.manifestPath, "utf8"));
  manifest.record.chatSessionId = "chat-attacker";
  await fs.promises.writeFile(saved.manifestPath, JSON.stringify(manifest), "utf8");

  assert.equal(await handlers.get("netcatty:tempdir:toolOutputRestore")({}, {
    handleId: "tool-output-owned",
    chatSessionId: "chat-attacker",
  }), null);
  assert.equal(await handlers.get("netcatty:tempdir:toolOutputRead")({}, { path: saved.path }), null);

  await Promise.allSettled([
    fs.promises.unlink(saved.path),
    fs.promises.unlink(saved.manifestPath),
  ]);
});

test("tool output verification detects same-size edits even when mtime is restored", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers({ handle(channel, handler) { handlers.set(channel, handler); } });
  const now = Date.now();
  const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {
    record: {
      schemaVersion: 1,
      handleId: "tool-output-same-mtime",
      chatSessionId: "chat-same-mtime",
      capabilityId: "terminal.execute",
      totalChars: 8,
      storedChars: 8,
      sourceTruncated: false,
      preview: "original",
      storedAt: now,
      accessedAt: now,
    },
    content: "original",
  });
  const originalStat = await fs.promises.stat(saved.path);
  await fs.promises.writeFile(saved.path, "modified", { encoding: "utf16le" });
  await fs.promises.utimes(saved.path, originalStat.atime, originalStat.mtime);

  assert.equal(await handlers.get("netcatty:tempdir:toolOutputRead")({}, { path: saved.path }), null);
  assert.equal(fs.existsSync(saved.path), false);
  assert.equal(fs.existsSync(saved.manifestPath), false);
});

test("tool output signing key survives a real process restart", async () => {
  const root = await fs.promises.mkdtemp(path.join(require("node:os").tmpdir(), "netcatty-tool-output-restart-"));
  const fakeHome = path.join(root, "home");
  await fs.promises.mkdir(fakeHome);
  await fs.promises.chmod(root, 0o700);
  const script = [
    'const bridge = require("./electron/bridges/tempDirBridge.cjs");',
    'const handlers = new Map();',
    'const secret = process.env.FAKE_SAFE_STORAGE_SECRET;',
    'const safeStorage = {',
    '  isEncryptionAvailable: () => true,',
    '  encryptString: value => Buffer.from(`${secret}:${value}`, "utf8"),',
    '  decryptString: value => {',
    '    const text = value.toString("utf8");',
    '    if (!text.startsWith(`${secret}:`)) throw new Error("wrong key");',
    '    return text.slice(secret.length + 1);',
    '  },',
    '};',
    'bridge.registerHandlers({ handle: (channel, handler) => handlers.set(channel, handler) }, undefined, { safeStorage });',
    '(async () => {',
    '  if (process.env.PHASE === "write") {',
    '    const now = Date.now();',
    '    const saved = await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {',
    '      record: { schemaVersion: 1, handleId: "real-restart", chatSessionId: "restart-chat", capabilityId: "terminal.execute", terminalSessionId: "terminal-one", totalChars: 14, storedChars: 14, sourceTruncated: false, preview: "across restart", storedAt: now, accessedAt: now },',
    '      content: "across restart",',
    '    });',
    '    await handlers.get("netcatty:tempdir:toolOutputWrite")({}, {',
    '      record: { schemaVersion: 1, handleId: "real-restart-two", chatSessionId: "restart-chat", capabilityId: "terminal.execute", terminalSessionId: "terminal-two", totalChars: 13, storedChars: 13, sourceTruncated: false, preview: "second output", storedAt: now, accessedAt: now },',
    '      content: "second output",',
    '    });',
    '    console.log(`RESULT:${JSON.stringify(saved)}`);',
    '    return;',
    '  }',
    '  if (process.env.PHASE === "delete-terminal") {',
    '    const deleted = await handlers.get("netcatty:tempdir:toolOutputDeleteTerminal")({}, { terminalSessionId: "terminal-one" });',
    '    console.log(`RESULT:${JSON.stringify(deleted)}`);',
    '    return;',
    '  }',
    '  if (process.env.PHASE === "delete-chat") {',
    '    const deleted = await handlers.get("netcatty:tempdir:toolOutputDeleteSession")({}, { chatSessionId: "restart-chat" });',
    '    console.log(`RESULT:${JSON.stringify(deleted)}`);',
    '    return;',
    '  }',
    '  const restored = await handlers.get("netcatty:tempdir:toolOutputRestore")({}, { handleId: "real-restart", chatSessionId: "restart-chat" });',
    '  const content = restored ? await handlers.get("netcatty:tempdir:toolOutputRead")({}, { path: restored.path }) : null;',
    '  await bridge.cleanupExpiredToolOutputFiles();',
    '  console.log(`RESULT:${JSON.stringify({ restored: Boolean(restored), content })}`);',
    '})().catch(error => { console.error(error); process.exit(1); });',
  ].join("\n");
  const run = (phase, secret) => spawnSync(process.execPath, ["-e", script], {
    cwd: path.resolve(__dirname, "../.."),
    env: {
      ...process.env,
      TMPDIR: root,
      HOME: fakeHome,
      PHASE: phase,
      FAKE_SAFE_STORAGE_SECRET: secret,
    },
    encoding: "utf8",
  });
  try {
    const written = run("write", "stable-secret");
    assert.equal(written.status, 0, written.stderr || written.stdout);
    assert.match(written.stdout, /RESULT:.*"ok":true/);

    const reopened = run("read", "stable-secret");
    assert.equal(reopened.status, 0, reopened.stderr || reopened.stdout);
    assert.match(reopened.stdout, /RESULT:.*"restored":true.*"content":"across restart"/);

    const persistedDir = path.join(root, "Netcatty");
    const old = new Date(Date.now() - 31 * 60 * 1_000);
    for (const file of await fs.promises.readdir(persistedDir)) {
      if (file.endsWith(".log") || file.endsWith(".log.meta.json")) {
        await fs.promises.utimes(path.join(persistedDir, file), old, old);
      }
    }
    const wrongKey = run("read", "different-secret");
    assert.equal(wrongKey.status, 0, wrongKey.stderr || wrongKey.stdout);
    assert.match(wrongKey.stdout, /RESULT:\{"restored":false,"content":null\}/);
    const remainingFiles = await fs.promises.readdir(persistedDir);
    assert.equal(remainingFiles.some(file => file.endsWith(".log")), true);
    assert.equal(remainingFiles.some(file => file.endsWith(".log.meta.json")), true);

    const deletedTerminal = run("delete-terminal", "different-secret");
    assert.equal(deletedTerminal.status, 0, deletedTerminal.stderr || deletedTerminal.stdout);
    assert.match(deletedTerminal.stdout, /RESULT:\{"deletedCount":1\}/);
    const afterTerminalDelete = await fs.promises.readdir(persistedDir);
    assert.equal(afterTerminalDelete.filter(file => file.endsWith(".log")).length, 1);

    const deletedChat = run("delete-chat", "different-secret");
    assert.equal(deletedChat.status, 0, deletedChat.stderr || deletedChat.stdout);
    assert.match(deletedChat.stdout, /RESULT:\{"deletedCount":1\}/);
    const afterChatDelete = await fs.promises.readdir(persistedDir);
    assert.equal(afterChatDelete.some(file => file.endsWith(".log") || file.endsWith(".log.meta.json")), false);
  } finally {
    await fs.promises.rm(root, { recursive: true, force: true });
  }
});

test("tool output persistence reports when secure storage is unavailable", async () => {
  const handlers = new Map();
  tempDirBridge.registerHandlers(
    { handle(channel, handler) { handlers.set(channel, handler); } },
    undefined,
    { safeStorage: { isEncryptionAvailable: () => false } },
  );

  assert.deepEqual(
    await handlers.get("netcatty:tempdir:toolOutputPersistenceStatus")(),
    { durable: false, reason: "Secure local storage is unavailable." },
  );
});
