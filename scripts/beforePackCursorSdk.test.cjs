const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const {
  CURSOR_PLATFORM_PACKAGES,
  beforePackCursorSdk,
  ensureCursorSdkPlatformPackages,
} = require("./beforePackCursorSdk.cjs");

function writeJson(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

test("ensureCursorSdkPlatformPackages installs both macOS Cursor runtime packages", (t) => {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-cursor-pack-"));
  t.after(() => fs.rmSync(tempDir, { recursive: true, force: true }));
  writeJson(path.join(tempDir, "node_modules", "@cursor", "sdk", "package.json"), { version: "1.0.18" });
  writeJson(path.join(tempDir, "node_modules", "@cursor", "sdk-darwin-arm64", "package.json"), { version: "1.0.18" });
  const calls = [];

  const installed = ensureCursorSdkPlatformPackages({
    projectDir: tempDir,
    platform: "darwin",
    run: (...args) => calls.push(args),
    logger: { log() {}, warn() {} },
  });

  assert.deepEqual(installed, ["@cursor/sdk-darwin-x64"]);
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], process.platform === "win32" ? "npm.cmd" : "npm");
  assert.deepEqual(calls[0][1], [
    "install",
    "--no-save",
    "--force",
    "--ignore-scripts",
    "@cursor/sdk-darwin-x64@1.0.18",
  ]);
  assert.equal(calls[0][2].cwd, tempDir);
});

test("ensureCursorSdkPlatformPackages is a no-op when target packages exist", (t) => {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-cursor-pack-"));
  t.after(() => fs.rmSync(tempDir, { recursive: true, force: true }));
  writeJson(path.join(tempDir, "node_modules", "@cursor", "sdk", "package.json"), { version: "1.0.18" });
  for (const packageName of CURSOR_PLATFORM_PACKAGES.linux) {
    writeJson(path.join(tempDir, "node_modules", ...packageName.split("/"), "package.json"), { version: "1.0.18" });
  }
  const calls = [];

  const installed = ensureCursorSdkPlatformPackages({
    projectDir: tempDir,
    platform: "linux",
    run: (...args) => calls.push(args),
    logger: { log() {}, warn() {} },
  });

  assert.deepEqual(installed, []);
  assert.deepEqual(calls, []);
});

test("beforePackCursorSdk builds Windows Hello helper only for Windows packages", () => {
  const calls = [];

  beforePackCursorSdk({
    appDir: process.cwd(),
    electronPlatformName: "win32",
    arch: "arm64",
    ensureCursorSdkPlatformPackages: () => [],
    buildWindowsHelloHelper: (projectDir) => calls.push(projectDir),
  });

  assert.deepEqual(calls, [{ projectDir: process.cwd(), platform: "win32", arch: "arm64" }]);

  beforePackCursorSdk({
    appDir: process.cwd(),
    electronPlatformName: "darwin",
    ensureCursorSdkPlatformPackages: () => [],
    buildWindowsHelloHelper: (projectDir) => calls.push(projectDir),
  });

  assert.deepEqual(calls, [{ projectDir: process.cwd(), platform: "win32", arch: "arm64" }]);
});

test("beforePackCursorSdk falls back to npm_config_arch for Windows Hello helper arch", () => {
  const calls = [];
  const originalArch = process.env.npm_config_arch;
  process.env.npm_config_arch = "x64";
  try {
    beforePackCursorSdk({
      appDir: process.cwd(),
      electronPlatformName: "win32",
      ensureCursorSdkPlatformPackages: () => [],
      buildWindowsHelloHelper: (projectDir) => calls.push(projectDir),
    });
  } finally {
    if (originalArch === undefined) {
      delete process.env.npm_config_arch;
    } else {
      process.env.npm_config_arch = originalArch;
    }
  }

  assert.deepEqual(calls, [{ projectDir: process.cwd(), platform: "win32", arch: "x64" }]);
});

test("beforePackCursorSdk fails Windows packaging when Windows Hello helper build is skipped", () => {
  assert.throws(
    () => beforePackCursorSdk({
      appDir: process.cwd(),
      electronPlatformName: "win32",
      ensureCursorSdkPlatformPackages: () => [],
      buildWindowsHelloHelper: () => ({ skipped: true, reason: "compiler-unavailable" }),
    }),
    /Windows Hello helper was not built: compiler-unavailable/,
  );
});
