"use strict";

const { describe, it } = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");

const {
  resolveDesktopManagedCli,
} = require("./desktopCliResolver.cjs");

function createFileDeps(files, directories = {}) {
  const fileSet = new Set(files);
  return {
    existsSync: (filePath) => fileSet.has(filePath),
    statSync: (filePath) => ({ isFile: () => fileSet.has(filePath) }),
    readdirSync: (directoryPath) => {
      if (!(directoryPath in directories)) throw new Error("ENOENT");
      return directories[directoryPath].map((name) => ({
        name,
        isDirectory: () => true,
      }));
    },
  };
}

describe("macOS desktop-managed CLI resolution", () => {
  it("finds the Codex CLI bundled with ChatGPT Desktop", () => {
    const codexPath = "/Applications/ChatGPT.app/Contents/Resources/codex";
    assert.equal(resolveDesktopManagedCli("codex", {
      platform: "darwin",
      homeDir: "/Users/test",
      ...createFileDeps([codexPath]),
    }), codexPath);
  });

  it("finds a user-installed Codex Desktop bundle", () => {
    const codexPath = "/Users/test/Applications/Codex.app/Contents/Resources/codex";
    assert.equal(resolveDesktopManagedCli("codex", {
      platform: "darwin",
      homeDir: "/Users/test",
      ...createFileDeps([codexPath]),
    }), codexPath);
  });

  it("uses the newest valid Claude Code managed by Claude Desktop", () => {
    const root = path.join(
      "/Users/test",
      "Library",
      "Application Support",
      "Claude",
      "claude-code",
    );
    const newestPath = path.join(root, "2.10.0", "claude.app", "Contents", "MacOS", "claude");
    const olderPath = path.join(root, "2.9.9", "claude.app", "Contents", "MacOS", "claude");
    assert.equal(resolveDesktopManagedCli("claude", {
      platform: "darwin",
      homeDir: "/Users/test",
      ...createFileDeps([newestPath, olderPath], {
        [root]: ["2.9.9", "2.10.0"],
      }),
    }), newestPath);
  });

  it("falls back to the newest installed Claude version that has an executable", () => {
    const root = path.join(
      "/Users/test",
      "Library",
      "Application Support",
      "Claude",
      "claude-code",
    );
    const validPath = path.join(root, "2.9.9", "claude.app", "Contents", "MacOS", "claude");
    assert.equal(resolveDesktopManagedCli("claude", {
      platform: "darwin",
      homeDir: "/Users/test",
      ...createFileDeps([validPath], {
        [root]: ["2.10.0", "2.9.9"],
      }),
    }), validPath);
  });

  it("does not probe desktop locations on other platforms", () => {
    assert.equal(resolveDesktopManagedCli("codex", {
      platform: "linux",
      homeDir: "/home/test",
      ...createFileDeps(["/Applications/ChatGPT.app/Contents/Resources/codex"]),
    }), null);
  });
});
