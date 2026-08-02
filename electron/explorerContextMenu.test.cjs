const test = require("node:test");
const assert = require("node:assert/strict");

const {
  buildExplorerContextMenuCommand,
  installExplorerContextMenu,
  isExplorerContextMenuRegistered,
  removeExplorerContextMenu,
  resolveExplorerContextMenuEnabled,
  updateExplorerContextMenuEnabledPreference,
} = require("./explorerContextMenu.cjs");

test("buildExplorerContextMenuCommand puts path args after -- for Electron", () => {
  assert.equal(
    buildExplorerContextMenuCommand("C:\\Program Files\\Netcatty\\Netcatty.exe", "%1"),
    '"C:\\Program Files\\Netcatty\\Netcatty.exe" -- --open-terminal-path="%1."',
  );
  assert.equal(
    buildExplorerContextMenuCommand("C:\\Netcatty\\Netcatty.exe", "%V"),
    '"C:\\Netcatty\\Netcatty.exe" -- --open-terminal-path="%V."',
  );
});

test("isExplorerContextMenuRegistered checks HKCU and HKLM shell keys", () => {
  const queries = [];
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    queries.push(args.slice());
    if (args[1] === "HKCU\\Software\\Classes\\Directory\\shell\\Netcatty") {
      return { status: 0, stdout: "ok", stderr: "" };
    }
    return { status: 1, stdout: "", stderr: "not found" };
  };

  assert.equal(
    isExplorerContextMenuRegistered({ platform: "win32", spawnSyncImpl, logWarn: () => {} }),
    true,
  );
  assert.ok(queries.some((args) => args[0] === "query"));
});

test("removeExplorerContextMenu deletes both folder and background keys", () => {
  const deleted = [];
  const present = new Set([
    "HKCU\\Software\\Classes\\Directory\\shell\\Netcatty",
    "HKCU\\Software\\Classes\\Directory\\Background\\shell\\Netcatty",
    "HKLM\\Software\\Classes\\Directory\\shell\\Netcatty",
    "HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty",
  ]);
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] === "query") {
      return present.has(args[1])
        ? { status: 0, stdout: "ok", stderr: "" }
        : { status: 1, stdout: "", stderr: "missing" };
    }
    if (args[0] === "delete") {
      deleted.push(args[1]);
      present.delete(args[1]);
      return { status: 0, stdout: "", stderr: "" };
    }
    return { status: 1, stdout: "", stderr: "unexpected" };
  };

  const result = removeExplorerContextMenu({
    platform: "win32",
    spawnSyncImpl,
    logWarn: () => {},
  });
  assert.equal(result.success, true);
  assert.equal(result.enabled, false);
  assert.ok(deleted.some((key) => key.endsWith("Directory\\shell\\Netcatty")));
  assert.ok(deleted.some((key) => key.includes("Directory\\Background\\shell\\Netcatty")));
});

test("installExplorerContextMenu writes HKCU shell command entries", () => {
  const writes = [];
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] === "query") {
      return { status: 1, stdout: "", stderr: "missing" };
    }
    if (args[0] === "add") {
      writes.push(args.slice());
      return { status: 0, stdout: "", stderr: "" };
    }
    return { status: 1, stdout: "", stderr: "unexpected" };
  };

  const result = installExplorerContextMenu({
    executablePath: "C:\\Apps\\Netcatty.exe",
    platform: "win32",
    spawnSyncImpl,
    logWarn: () => {},
  });

  assert.equal(result.success, true);
  assert.ok(writes.some((args) => args.includes("MUIVerb") && args.includes("Open in Netcatty")));
  assert.ok(writes.some((args) =>
    args.some((part) => String(part).includes('--open-terminal-path="%1."'))
  ));
  assert.ok(writes.some((args) =>
    args.some((part) => String(part).includes('--open-terminal-path="%V."'))
  ));
});

test("resolveExplorerContextMenuEnabled prefers saved preference over registry", () => {
  const fsModule = {
    existsSync: () => true,
    readFileSync: () => JSON.stringify({ enabled: false }),
  };
  const app = { getPath: () => "C:\\Users\\test\\AppData\\Roaming\\Netcatty" };
  const spawnSyncImpl = () => ({ status: 0, stdout: "present", stderr: "" });

  const resolved = resolveExplorerContextMenuEnabled({
    app,
    platform: "win32",
    fsModule,
    spawnSyncImpl,
    logWarn: () => {},
  });
  assert.deepEqual(resolved, { enabled: false, supported: true });
});

test("updateExplorerContextMenuEnabledPreference rolls back apply on write failure", () => {
  const applied = [];
  const result = updateExplorerContextMenuEnabledPreference({
    currentEnabled: true,
    enabled: false,
    applyPreference: (next) => {
      applied.push(next);
      return { success: true, enabled: next };
    },
    writePreference: () => false,
  });
  assert.equal(result.success, false);
  assert.deepEqual(applied, [false, true]);
  assert.equal(result.enabled, true);
});

test("non-windows platforms report unsupported explorer context menu", () => {
  assert.equal(
    isExplorerContextMenuRegistered({ platform: "darwin", spawnSyncImpl: () => {
      throw new Error("should not run");
    } }),
    false,
  );
  const removed = removeExplorerContextMenu({ platform: "linux" });
  assert.equal(removed.supported, false);
  assert.equal(removed.enabled, false);
});
