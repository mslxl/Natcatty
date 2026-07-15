"use strict";

function createSessionService(ctx = {}) {
  const { invokeSessionAgent, validateClose, beforeClose, afterClose, onClosed } = ctx;

  return {
    close: async (params = {}) => {
      if (!params.sessionId || typeof params.sessionId !== "string") {
        return { ok: false, error: "sessionId is required." };
      }
      if (typeof validateClose === "function") {
        const validation = validateClose(params);
        if (validation && validation.ok === false) return validation;
      }
      if (typeof invokeSessionAgent !== "function") {
        return { ok: false, error: "Session close bridge is unavailable." };
      }

      try {
        await beforeClose?.(params);
        const result = await invokeSessionAgent("session.close", { sessionId: params.sessionId });
        if (result?.ok !== false) await onClosed?.(params.sessionId);
        return result;
      } finally {
        await afterClose?.(params);
      }
    },
  };
}

module.exports = { createSessionService };
