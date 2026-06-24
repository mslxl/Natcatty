const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

function findCompiler(env = process.env) {
  if (env.CXX) return env.CXX;
  if (env.CL) return "cl.exe";
  return "cl.exe";
}

function normalizeWindowsHelperArch(arch) {
  if (arch === "x64" || arch === "arm64") return arch;
  return null;
}

function buildWindowsHelloHelper({
  projectDir = process.cwd(),
  platform = process.platform,
  arch = process.env.npm_config_arch || process.arch,
  env = process.env,
  run = execFileSync,
  mkdir = fs.mkdirSync,
  logger = console,
} = {}) {
  if (platform !== "win32") return { skipped: true, reason: "non-windows" };
  const targetArch = normalizeWindowsHelperArch(arch);
  if (!targetArch) return { skipped: true, reason: "unsupported-arch" };

  const sourcePath = path.join(projectDir, "electron", "bridges", "windowsHelloHelper", "NetcattyWindowsHello.cpp");
  const outputDir = path.join(projectDir, "electron", "bridges", "windowsHelloHelper", "build", targetArch);
  const outputPath = path.join(outputDir, "NetcattyWindowsHello.exe");
  mkdir(outputDir, { recursive: true });

  const compiler = findCompiler(env);
  try {
    run(compiler, [
      "/nologo",
      "/EHsc",
      "/std:c++17",
      sourcePath,
      "/Fe:" + outputPath,
      "runtimeobject.lib",
      "windowsapp.lib",
    ], {
      cwd: projectDir,
      stdio: "inherit",
      env,
    });
  } catch (err) {
    logger.warn?.(`[windowsHelloHelper] Failed to build Windows Hello helper: ${err?.message || err}`);
    return { skipped: true, reason: "compiler-unavailable" };
  }

  return { skipped: false, outputPath };
}

if (require.main === module) {
  const result = buildWindowsHelloHelper();
  if (result.skipped) {
    console.log(`[windowsHelloHelper] skipped: ${result.reason}`);
  } else {
    console.log(`[windowsHelloHelper] built: ${result.outputPath}`);
  }
}

module.exports = {
  buildWindowsHelloHelper,
  findCompiler,
  normalizeWindowsHelperArch,
};
