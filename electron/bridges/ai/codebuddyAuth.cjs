"use strict";

/**
 * CodeBuddy auth/config detection helpers (main process).
 *
 * codebuddy --acp authenticates from:
 *   1. CODEBUDDY_AUTH_TOKEN (env) — highest precedence
 *   2. ~/.codebuddy/settings.json  (file-based auth: authToken, apiKeyHelper, etc.)
 *
 * NOTE: CodeBuddy CLI does NOT use CODEBUDDY_API_KEY. That variable is
 * irrelevant to the CLI's auth flow and must not be checked here.
 *
 * Unlike Claude (where Keychain may hold creds invisibly), CodeBuddy's
 * settings.json is the canonical file-based config, so we CAN hard-block
 * when no auth source is found — 'none' means the CLI will definitely fail.
 */

const { existsSync } = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function getCodebuddyConfigDir() {
  return path.join(os.homedir(), ".codebuddy");
}

/**
 * @returns {'auth-token'|'settings-file'|'none'}
 */
function detectCodebuddyAuthPresence(env, fileExists = existsSync) {
  const authToken = typeof env?.CODEBUDDY_AUTH_TOKEN === "string" ? env.CODEBUDDY_AUTH_TOKEN.trim() : "";
  if (authToken) return "auth-token";

  if (fileExists(path.join(getCodebuddyConfigDir(), "settings.json"))) return "settings-file";

  return "none";
}

module.exports = { detectCodebuddyAuthPresence, getCodebuddyConfigDir };
