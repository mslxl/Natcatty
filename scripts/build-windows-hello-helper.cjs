const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

function findCompiler(env = process.env) {
  if (env.CXX) return env.CXX;
  if (env.CL) return "cl.exe";
  return "cl.exe";
}

function buildWindowsHelloHelper({
  projectDir = process.cwd(),
  platform = process.platform,
  env = process.env,
  run = execFileSync,
  logger = console,
} = {}) {
  if (platform !== "win32") return { skipped: true, reason: "non-windows" };

  const sourcePath = path.join(projectDir, "electron", "bridges", "windowsHelloHelper", "NetcattyWindowsHello.cpp");
  const outputDir = path.join(projectDir, "electron", "bridges", "windowsHelloHelper", "build");
  const outputPath = path.join(outputDir, "NetcattyWindowsHello.exe");
  fs.mkdirSync(outputDir, { recursive: true });

  const compiler = findCompiler(env);
  try {
    run(compiler, [
      "/nologo",
      "/EHsc",
      "/std:c++17",
      sourcePath,
      "/Fe:" + outputPath,
      "runtimeobject.lib",
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
};
