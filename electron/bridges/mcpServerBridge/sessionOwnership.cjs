"use strict";

function createSessionOwnershipRegistry() {
  const ownedByScope = new Map();

  function register(chatSessionId, sessionId) {
    if (!chatSessionId || !sessionId) return false;
    const owned = ownedByScope.get(chatSessionId) || new Set();
    owned.add(sessionId);
    ownedByScope.set(chatSessionId, owned);
    return true;
  }

  function validate(chatSessionId, sessionId) {
    if (!chatSessionId) return { ok: false, error: "chatSessionId is required." };
    if (!ownedByScope.get(chatSessionId)?.has(sessionId)) {
      return {
        ok: false,
        error: `Session "${sessionId}" was not opened by the current AI scope.`,
      };
    }
    return { ok: true };
  }

  function forgetSession(sessionId) {
    for (const [scopeId, owned] of ownedByScope) {
      owned.delete(sessionId);
      if (owned.size === 0) ownedByScope.delete(scopeId);
    }
  }

  function clearScope(chatSessionId) {
    ownedByScope.delete(chatSessionId);
  }

  return { register, validate, forgetSession, clearScope };
}

module.exports = { createSessionOwnershipRegistry };
