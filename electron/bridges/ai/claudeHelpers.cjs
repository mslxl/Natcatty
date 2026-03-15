/**
 * Claude Agent SDK helper functions and state.
 *
 * Manages Claude config directory setup, environment building,
 * session IDs, active streams, and the cached SDK query function.
 */
"use strict";

const path = require("node:path");

// ── Module-level state ──

// Keys to strip from env before passing to Claude SDK (prevent interference)
const CLAUDE_STRIPPED_ENV_KEYS = [
  "OPENAI_API_KEY",
  "CLAUDE_CODE_USE_BEDROCK",
  "CLAUDE_CODE_USE_VERTEX",
];

// Claude config dir base path for session isolation
let claudeConfigDirBase = null;

// Claude session IDs (chatSessionId -> SDK sessionId for resume)
const claudeSessionIds = new Map();

// Claude Agent SDK active streams
const claudeActiveStreams = new Map();

// Claude SDK query function cache (avoid re-importing on every message)
let cachedClaudeQuery = null;

// ── Config directory ──

function getClaudeConfigDirBase() {
  if (claudeConfigDirBase) return claudeConfigDirBase;
  const home = process.env.HOME || require("node:os").homedir();
  claudeConfigDirBase = path.join(home, ".netcatty", "claude-sessions");
  return claudeConfigDirBase;
}

// ── Environment building ──

function buildClaudeEnv(shellEnv) {
  const env = { ...shellEnv };

  // Overlay process.env but preserve shell PATH (Electron's PATH is minimal)
  const shellPath = env.PATH;
  for (const [key, value] of Object.entries(process.env)) {
    if (value !== undefined) {
      env[key] = value;
    }
  }
  if (shellPath) {
    env.PATH = shellPath;
  }

  // Strip sensitive keys (prevent interference from unrelated providers)
  for (const key of CLAUDE_STRIPPED_ENV_KEYS) {
    delete env[key];
  }

  // Ensure critical vars
  const os = require("node:os");
  if (!env.HOME) env.HOME = os.homedir();
  if (!env.USER) env.USER = os.userInfo().username;
  if (!env.TERM) env.TERM = "xterm-256color";
  if (!env.SHELL) env.SHELL = process.env.SHELL || "/bin/zsh";

  // Mark as SDK entry
  env.CLAUDE_CODE_ENTRYPOINT = "sdk-ts";

  return env;
}

// ── Cached query accessor ──

function getCachedClaudeQuery() {
  return cachedClaudeQuery;
}

function setCachedClaudeQuery(queryFn) {
  cachedClaudeQuery = queryFn;
}

// ── Cleanup helper ──

function clearClaudeState() {
  claudeActiveStreams.clear();
  claudeSessionIds.clear();
  cachedClaudeQuery = null;
}

module.exports = {
  CLAUDE_STRIPPED_ENV_KEYS,
  claudeSessionIds,
  claudeActiveStreams,
  getClaudeConfigDirBase,
  buildClaudeEnv,
  getCachedClaudeQuery,
  setCachedClaudeQuery,
  clearClaudeState,
};
