"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function isFile(filePath, deps) {
  try {
    return deps.existsSync(filePath) && deps.statSync(filePath).isFile();
  } catch {
    return false;
  }
}

function findFirstFile(candidates, deps) {
  for (const candidate of candidates) {
    if (isFile(candidate, deps)) return candidate;
  }
  return null;
}

function compareVersionDirectoryNames(left, right) {
  return String(right).localeCompare(String(left), "en", {
    numeric: true,
    sensitivity: "base",
  });
}

function resolveCodexDesktopCli(homeDir, deps) {
  const appRoots = [
    "/Applications/ChatGPT.app",
    "/Applications/Codex.app",
    path.join(homeDir, "Applications", "ChatGPT.app"),
    path.join(homeDir, "Applications", "Codex.app"),
  ];
  return findFirstFile(
    appRoots.map((appRoot) => path.join(appRoot, "Contents", "Resources", "codex")),
    deps,
  );
}

function resolveClaudeDesktopCli(homeDir, deps) {
  const versionsRoot = path.join(
    homeDir,
    "Library",
    "Application Support",
    "Claude",
    "claude-code",
  );

  let versionDirectories;
  try {
    versionDirectories = deps.readdirSync(versionsRoot, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort(compareVersionDirectoryNames);
  } catch {
    return null;
  }

  return findFirstFile(
    versionDirectories.map((version) => path.join(
      versionsRoot,
      version,
      "claude.app",
      "Contents",
      "MacOS",
      "claude",
    )),
    deps,
  );
}

function resolveDesktopManagedCli(name, options = {}) {
  const platform = options.platform || process.platform;
  if (platform !== "darwin") return null;

  const deps = {
    existsSync: options.existsSync || fs.existsSync,
    statSync: options.statSync || fs.statSync,
    readdirSync: options.readdirSync || fs.readdirSync,
  };
  const homeDir = options.homeDir || os.homedir();

  if (name === "codex") return resolveCodexDesktopCli(homeDir, deps);
  if (name === "claude") return resolveClaudeDesktopCli(homeDir, deps);
  return null;
}

module.exports = {
  compareVersionDirectoryNames,
  resolveDesktopManagedCli,
};
