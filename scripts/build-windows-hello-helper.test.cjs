const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");

const {
  buildWindowsHelloHelper,
  getExpectedPeMachine,
  normalizeWindowsHelperArch,
  readPeMachine,
} = require("./build-windows-hello-helper.cjs");

test("normalizeWindowsHelperArch accepts only packaged Windows architectures", () => {
  assert.equal(normalizeWindowsHelperArch("x64"), "x64");
  assert.equal(normalizeWindowsHelperArch("arm64"), "arm64");
  assert.equal(normalizeWindowsHelperArch(1), "x64");
  assert.equal(normalizeWindowsHelperArch(3), "arm64");
  assert.equal(normalizeWindowsHelperArch("ia32"), null);
  assert.equal(normalizeWindowsHelperArch(""), null);
});

test("getExpectedPeMachine maps target helper architectures", () => {
  assert.equal(getExpectedPeMachine("x64"), 0x8664);
  assert.equal(getExpectedPeMachine("arm64"), 0xaa64);
  assert.equal(getExpectedPeMachine("ia32"), null);
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
    readMachine: () => 0xaa64,
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
  assert.deepEqual(calls[0][1].slice(-4), ["/link", "/MACHINE:ARM64", "runtimeobject.lib", "windowsapp.lib"]);
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

test("buildWindowsHelloHelper rejects a built helper with the wrong PE machine", () => {
  const result = buildWindowsHelloHelper({
    projectDir: "/repo",
    platform: "win32",
    arch: "arm64",
    mkdir: () => {},
    run: () => {},
    readMachine: () => 0x8664,
    logger: { warn() {} },
  });

  assert.deepEqual(result, { skipped: true, reason: "wrong-arch" });
});

test("readPeMachine reads the PE COFF machine value", () => {
  const buffer = Buffer.alloc(0x90);
  buffer.write("MZ", 0, "ascii");
  buffer.writeUInt32LE(0x80, 0x3c);
  buffer.write("PE\0\0", 0x80, "ascii");
  buffer.writeUInt16LE(0xaa64, 0x84);

  assert.equal(readPeMachine(buffer), 0xaa64);
  assert.equal(readPeMachine(Buffer.from("not-pe")), null);
});
