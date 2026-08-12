"use strict";

/**
 * Keep host_open-owned sessions in a chat scope when a full metadata replace
 * would otherwise drop them (e.g. AIChatSidePanel pushing only the current
 * terminal tab after a mid-turn host_open).
 *
 * Empty incoming lists are treated as authoritative clears and are not retained.
 *
 * @param {{
 *   incomingSessions: Array<Record<string, unknown>>,
 *   ownedSessionIds: string[],
 *   previousById?: Map<string, Record<string, unknown>> | null,
 *   findFallbackMeta?: ((sessionId: string) => Record<string, unknown> | null | undefined) | null,
 * }} args
 * @returns {Array<Record<string, unknown>>}
 */
function retainOwnedSessions({
  incomingSessions,
  ownedSessionIds,
  previousById = null,
  findFallbackMeta = null,
}) {
  if (!Array.isArray(incomingSessions) || incomingSessions.length === 0) {
    return incomingSessions;
  }
  if (!Array.isArray(ownedSessionIds) || ownedSessionIds.length === 0) {
    return incomingSessions;
  }

  const byId = new Map();
  for (const entry of incomingSessions) {
    if (!entry || typeof entry !== "object" || !entry.sessionId) continue;
    byId.set(String(entry.sessionId), entry);
  }

  for (const ownedIdRaw of ownedSessionIds) {
    const ownedId = typeof ownedIdRaw === "string" ? ownedIdRaw.trim() : "";
    if (!ownedId || byId.has(ownedId)) continue;

    const previous = previousById?.get?.(ownedId);
    const fallback = previous ? null : (typeof findFallbackMeta === "function"
      ? findFallbackMeta(ownedId)
      : null);
    const meta = previous || fallback;
    if (!meta || typeof meta !== "object") continue;

    byId.set(ownedId, {
      sessionId: ownedId,
      hostname: meta.hostname || "",
      label: meta.label || "",
      os: meta.os || "",
      username: meta.username || "",
      protocol: meta.protocol || "",
      shellType: meta.shellType || "",
      deviceType: meta.deviceType || "",
      connected: meta.connected !== false,
      hostId: meta.hostId || "",
      hostChain: Array.isArray(meta.hostChain) ? meta.hostChain : [],
      activePortForwards: Array.isArray(meta.activePortForwards) ? meta.activePortForwards : [],
    });
  }

  return Array.from(byId.values());
}

module.exports = { retainOwnedSessions };
