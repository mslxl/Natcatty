const test = require("node:test");
const assert = require("node:assert/strict");

const {
  SUPPRESSION_VALUE,
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
    // Value queries (e.g. ProgrammaticAccessOnly) are not present on a real install.
    if (args.includes("/v")) {
      return { status: 1, stdout: "", stderr: "value not found" };
    }
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

test("isExplorerContextMenuRegistered is false when user suppressed HKLM menu", () => {
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] !== "query") return { status: 1, stdout: "", stderr: "unexpected" };
    const key = args[1];
    if (args.includes("/v")) {
      if (
        args.includes(SUPPRESSION_VALUE)
        && (
          key === "HKCU\\Software\\Classes\\Directory\\shell\\Netcatty"
          || key === "HKCU\\Software\\Classes\\Directory\\Background\\shell\\Netcatty"
        )
      ) {
        return { status: 0, stdout: "ok", stderr: "" };
      }
      return { status: 1, stdout: "", stderr: "value missing" };
    }
    if (
      key === "HKCU\\Software\\Classes\\Directory\\shell\\Netcatty"
      || key === "HKLM\\Software\\Classes\\Directory\\shell\\Netcatty"
    ) {
      return { status: 0, stdout: "ok", stderr: "" };
    }
    return { status: 1, stdout: "", stderr: "missing" };
  };

  assert.equal(
    isExplorerContextMenuRegistered({ platform: "win32", spawnSyncImpl, logWarn: () => {} }),
    false,
  );
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
      if (args.includes("/v")) {
        return { status: 1, stdout: "", stderr: "no value" };
      }
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

test("removeExplorerContextMenu suppresses leftover HKLM when delete is denied", () => {
  const present = new Set([
    "HKLM\\Software\\Classes\\Directory\\shell\\Netcatty",
    "HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty",
  ]);
  const suppressed = new Set();
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] === "query") {
      if (args.includes("/v")) {
        if (args.includes(SUPPRESSION_VALUE) && suppressed.has(args[1])) {
          return { status: 0, stdout: "ok", stderr: "" };
        }
        return { status: 1, stdout: "", stderr: "no value" };
      }
      return present.has(args[1]) || suppressed.has(args[1])
        ? { status: 0, stdout: "ok", stderr: "" }
        : { status: 1, stdout: "", stderr: "missing" };
    }
    if (args[0] === "delete") {
      if (String(args[1]).startsWith("HKLM\\")) {
        return { status: 1, stdout: "", stderr: "access denied" };
      }
      present.delete(args[1]);
      suppressed.delete(args[1]);
      return { status: 0, stdout: "", stderr: "" };
    }
    if (args[0] === "add") {
      const key = args[1];
      const valueIdx = args.indexOf("/v");
      if (valueIdx >= 0 && args[valueIdx + 1] === SUPPRESSION_VALUE) {
        suppressed.add(key);
        present.add(key);
        return { status: 0, stdout: "", stderr: "" };
      }
      return { status: 1, stdout: "", stderr: "unexpected add" };
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
  assert.ok(suppressed.has("HKCU\\Software\\Classes\\Directory\\shell\\Netcatty"));
  assert.ok(suppressed.has("HKCU\\Software\\Classes\\Directory\\Background\\shell\\Netcatty"));
  // Machine keys remain, but the user-facing menu is suppressed.
  assert.ok(present.has("HKLM\\Software\\Classes\\Directory\\shell\\Netcatty"));
});

test("installExplorerContextMenu writes HKCU shell command entries", () => {
  const writes = [];
  const present = new Set();
  const values = new Map();
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] === "query") {
      if (args.includes("/v") || args.includes("/ve")) {
        const key = args[1];
        const valueName = args.includes("/ve") ? "" : args[args.indexOf("/v") + 1];
        const mapKey = `${key}::${valueName}`;
        if (values.has(mapKey)) {
          return {
            status: 0,
            stdout: `    ${(valueName || "(Default)").padEnd(16)}REG_SZ    ${values.get(mapKey)}\n`,
            stderr: "",
          };
        }
        return { status: 1, stdout: "", stderr: "no value" };
      }
      return present.has(args[1])
        ? { status: 0, stdout: "ok", stderr: "" }
        : { status: 1, stdout: "", stderr: "missing" };
    }
    if (args[0] === "add") {
      writes.push(args.slice());
      present.add(args[1]);
      const valueName = args.includes("/ve") ? "" : args[args.indexOf("/v") + 1];
      const dataIdx = args.indexOf("/d");
      if (dataIdx >= 0) values.set(`${args[1]}::${valueName}`, args[dataIdx + 1]);
      return { status: 0, stdout: "", stderr: "" };
    }
    if (args[0] === "delete") {
      present.delete(args[1]);
      for (const key of [...values.keys()]) {
        if (key.startsWith(`${args[1]}::`)) values.delete(key);
      }
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
  assert.equal(result.enabled, true);
  assert.ok(writes.some((args) => args.includes("MUIVerb") && args.includes("Open in Netcatty")));
  assert.ok(writes.some((args) =>
    args.some((part) => String(part).includes('--open-terminal-path="%1."'))
  ));
  assert.ok(writes.some((args) =>
    args.some((part) => String(part).includes('--open-terminal-path="%V."'))
  ));
  // Per-user only when no machine registration exists.
  assert.ok(writes.every((args) => String(args[1]).startsWith("HKCU\\")));
});

test("installExplorerContextMenu does not duplicate HKLM verbs into HKCU", () => {
  const writes = [];
  const present = new Set([
    "HKLM\\Software\\Classes\\Directory\\shell\\Netcatty",
    "HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty",
  ]);
  const values = new Map();
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] === "query") {
      if (args.includes("/v") || args.includes("/ve")) {
        const key = args[1];
        const valueName = args.includes("/ve") ? "" : args[args.indexOf("/v") + 1];
        const mapKey = `${key}::${valueName}`;
        if (values.has(mapKey)) {
          return {
            status: 0,
            stdout: `    ${(valueName || "(Default)").padEnd(16)}REG_SZ    ${values.get(mapKey)}\n`,
            stderr: "",
          };
        }
        return { status: 1, stdout: "", stderr: "no value" };
      }
      return present.has(args[1])
        ? { status: 0, stdout: "ok", stderr: "" }
        : { status: 1, stdout: "", stderr: "missing" };
    }
    if (args[0] === "add") {
      writes.push(args.slice());
      present.add(args[1]);
      const valueName = args.includes("/ve") ? "" : args[args.indexOf("/v") + 1];
      const dataIdx = args.indexOf("/d");
      if (dataIdx >= 0) values.set(`${args[1]}::${valueName}`, args[dataIdx + 1]);
      return { status: 0, stdout: "", stderr: "" };
    }
    if (args[0] === "delete") {
      present.delete(args[1]);
      for (const key of [...values.keys()]) {
        if (key.startsWith(`${args[1]}::`)) values.delete(key);
      }
      return { status: 0, stdout: "", stderr: "" };
    }
    return { status: 1, stdout: "", stderr: "unexpected" };
  };

  const result = installExplorerContextMenu({
    executablePath: "C:\\Program Files\\Netcatty\\Netcatty.exe",
    platform: "win32",
    spawnSyncImpl,
    logWarn: () => {},
  });

  assert.equal(result.success, true);
  assert.equal(result.enabled, true);
  assert.ok(writes.length > 0);
  assert.ok(writes.every((args) => String(args[1]).startsWith("HKLM\\")));
  assert.ok(!writes.some((args) => String(args[1]).startsWith("HKCU\\")));
});

test("installExplorerContextMenu skips reg writes when shell verbs are already current", () => {
  const exe = "C:\\Program Files\\Netcatty\\Netcatty.exe";
  const folderCmd = buildExplorerContextMenuCommand(exe, "%1");
  const backgroundCmd = buildExplorerContextMenuCommand(exe, "%V");
  const present = new Set([
    "HKLM\\Software\\Classes\\Directory\\shell\\Netcatty",
    "HKLM\\Software\\Classes\\Directory\\shell\\Netcatty\\command",
    "HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty",
    "HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty\\command",
  ]);
  const values = new Map([
    ["HKLM\\Software\\Classes\\Directory\\shell\\Netcatty::MUIVerb", "Open in Netcatty"],
    ["HKLM\\Software\\Classes\\Directory\\shell\\Netcatty::Icon", `${exe},0`],
    ["HKLM\\Software\\Classes\\Directory\\shell\\Netcatty\\command::", folderCmd],
    ["HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty::MUIVerb", "Open in Netcatty"],
    ["HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty::Icon", `${exe},0`],
    ["HKLM\\Software\\Classes\\Directory\\Background\\shell\\Netcatty\\command::", backgroundCmd],
  ]);
  const writes = [];
  const spawnSyncImpl = (cmd, args) => {
    assert.equal(cmd, "reg.exe");
    if (args[0] === "query") {
      if (args.includes("/v") || args.includes("/ve")) {
        const key = args[1];
        const valueName = args.includes("/ve") ? "" : args[args.indexOf("/v") + 1];
        const mapKey = `${key}::${valueName}`;
        if (values.has(mapKey)) {
          return {
            status: 0,
            stdout: `    ${(valueName || "(Default)").padEnd(16)}REG_SZ    ${values.get(mapKey)}\n`,
            stderr: "",
          };
        }
        return { status: 1, stdout: "", stderr: "no value" };
      }
      return present.has(args[1])
        ? { status: 0, stdout: "ok", stderr: "" }
        : { status: 1, stdout: "", stderr: "missing" };
    }
    if (args[0] === "add") {
      writes.push(args.slice());
      return { status: 0, stdout: "", stderr: "" };
    }
    if (args[0] === "delete") {
      return { status: 0, stdout: "", stderr: "" };
    }
    return { status: 1, stdout: "", stderr: "unexpected" };
  };

  const result = installExplorerContextMenu({
    executablePath: exe,
    platform: "win32",
    spawnSyncImpl,
    logWarn: () => {},
  });

  assert.equal(result.success, true);
  assert.equal(result.enabled, true);
  assert.equal(writes.length, 0);
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
