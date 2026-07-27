"use strict";

const SDK_SESSION_ID_PREFIX = "netcatty-sdk-session:";

function normalizeCursorAuthMode(authMode) {
  return authMode === "cli-login" ? "cli-login" : authMode === "api-key" ? "api-key" : undefined;
}

function normalizeCursorCliMode(cliMode) {
  return cliMode === "ask" ? "ask" : cliMode === "agent" ? "agent" : undefined;
}

function encodeSdkSessionIdentity(sessionId, sdkBackend, binPath, runtime = "sdk", authMode, cliMode) {
  if (!sessionId || !sdkBackend) return sessionId;
  const payload = {
    v: 1,
    id: sessionId,
    backend: sdkBackend,
    binPath: binPath || "",
    runtime: runtime === "app-server" ? "app-server" : "sdk",
  };
  const normalizedAuthMode = normalizeCursorAuthMode(authMode);
  if (normalizedAuthMode) payload.authMode = normalizedAuthMode;
  const normalizedCliMode = normalizeCursorCliMode(cliMode);
  if (normalizedCliMode) payload.cliMode = normalizedCliMode;
  return `${SDK_SESSION_ID_PREFIX}${encodeURIComponent(JSON.stringify(payload))}`;
}

function parseSdkSessionIdentity(value) {
  const raw = String(value || "").trim();
  if (!raw.startsWith(SDK_SESSION_ID_PREFIX)) return null;
  try {
    const parsed = JSON.parse(decodeURIComponent(raw.slice(SDK_SESSION_ID_PREFIX.length)));
    if (!parsed || parsed.v !== 1 || !parsed.id || !parsed.backend) return null;
    const authMode = normalizeCursorAuthMode(parsed.authMode);
    const cliMode = normalizeCursorCliMode(parsed.cliMode);
    return {
      ...parsed,
      runtime: parsed.runtime === "app-server" ? "app-server" : "sdk",
      ...(authMode ? { authMode } : {}),
      ...(cliMode ? { cliMode } : {}),
    };
  } catch {
    return null;
  }
}

module.exports = {
  SDK_SESSION_ID_PREFIX,
  encodeSdkSessionIdentity,
  normalizeCursorAuthMode,
  normalizeCursorCliMode,
  parseSdkSessionIdentity,
};
