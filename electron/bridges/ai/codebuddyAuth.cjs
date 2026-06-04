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
 * settings.json is the canonical file-based config. A return of 'none'
 * enriches the error message when the CLI actually fails (soft-detection only).
 */

const { readFileSync } = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function getCodebuddyConfigDir() {
  return path.join(os.homedir(), ".codebuddy");
}

/**
 * Default file reader: tries to read a UTF-8 file, returns null on any error.
 * @param {string} filePath
 * @returns {string | null}
 */
function defaultReadFile(filePath) {
  try {
    return readFileSync(filePath, "utf-8");
  } catch {
    return null;
  }
}

/**
 * @param {Record<string, unknown> | null | undefined} env
 * @param {(filePath: string) => string | null} readFile
 * @returns {'auth-token'|'settings-file'|'none'}
 */
function detectCodebuddyAuthPresence(env, readFile = defaultReadFile) {
  const authToken = typeof env?.CODEBUDDY_AUTH_TOKEN === "string" ? env.CODEBUDDY_AUTH_TOKEN.trim() : "";
  if (authToken) return "auth-token";

  const content = readFile(path.join(getCodebuddyConfigDir(), "settings.json"));
  if (content !== null) {
    // File exists — but does it actually contain auth credentials?
    // Empty files, bad JSON, or settings without auth fields must NOT
    // count as "configured", otherwise the user gets a generic agent
    // error instead of the targeted CODEBUDDY_AUTH_HELP_MESSAGE.
    try {
      const parsed = JSON.parse(content);
      if (parsed && typeof parsed === "object") {
        if (typeof parsed.authToken === "string" && parsed.authToken.trim()) {
          return "settings-file";
        }
        if (typeof parsed.apiKeyHelper === "string" && parsed.apiKeyHelper.trim()) {
          return "settings-file";
        }
      }
    } catch {
      // Malformed JSON — treat as no auth
    }
  }

  return "none";
}

module.exports = { detectCodebuddyAuthPresence, getCodebuddyConfigDir };
