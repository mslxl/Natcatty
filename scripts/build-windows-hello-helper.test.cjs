const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");

const {
  buildWindowsHelloHelper,
  normalizeWindowsHelperArch,
} = require("./build-windows-hello-helper.cjs");

test("normalizeWindowsHelperArch accepts only packaged Windows architectures", () => {
  assert.equal(normalizeWindowsHelperArch("x64"), "x64");
  assert.equal(normalizeWindowsHelperArch("arm64"), "arm64");
  assert.equal(normalizeWindowsHelperArch("ia32"), null);
  assert.equal(normalizeWindowsHelperArch(""), null);
});

test("buildWindowsHelloHelper writes target architecture helper into an arch-specific directory", () => {
  const calls = [];
  const result = buildWindowsHelloHelper({
    projectDir: "/repo",
    platform: "win32",
    arch: "arm64",
    env: {},
    run: (...args) => calls.push(args),
    mkdir: () => {},
    logger: { warn() {} },
  });

  assert.equal(result.skipped, false);
  assert.equal(
    result.outputPath,
    path.join("/repo", "electron", "bridges", "windowsHelloHelper", "build", "arm64", "NetcattyWindowsHello.exe"),
  );
  assert.match(
    calls[0][1].join(" "),
    /\/Fe:.*windowsHelloHelper.*build.*arm64.*NetcattyWindowsHello\.exe/,
  );
});

test("buildWindowsHelloHelper rejects unsupported target architectures on Windows", () => {
  const result = buildWindowsHelloHelper({
    projectDir: "/repo",
    platform: "win32",
    arch: "ia32",
    mkdir: () => {
      throw new Error("should not create output dir");
    },
    run: () => {
      throw new Error("should not run compiler");
    },
    logger: { warn() {} },
  });

  assert.deepEqual(result, { skipped: true, reason: "unsupported-arch" });
});
