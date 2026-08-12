"use strict";

/**
 * Merge a previous scoped snapshot with a cross-scope fallback.
 * Always prefer a connected:true signal from either side so a stale
 * host_open connected:false snapshot cannot mask a fresher update.
 *
 * @param {Record<string, unknown> | null | undefined} previous
 * @param {Record<string, unknown> | null | undefined} fallback
 * @returns {Record<string, unknown> | null}
 */
function mergeRetentionMeta(previous, fallback) {
  if (!previous && !fallback) return null;
  if (!previous) return fallback && typeof fallback === "object" ? fallback : null;
  if (!fallback || typeof fallback !== "object") return previous;

  const previousConnected = previous.connected !== false;
  const fallbackConnected = fallback.connected !== false;
  return {
    ...previous,
    hostname: fallback.hostname || previous.hostname,
    label: fallback.label || previous.label,
    os: fallback.os || previous.os,
    username: fallback.username || previous.username,
    protocol: fallback.protocol || previous.protocol,
    shellType: fallback.shellType || previous.shellType,
    deviceType: fallback.deviceType || previous.deviceType,
    hostId: fallback.hostId || previous.hostId,
    hostChain: Array.isArray(fallback.hostChain) && fallback.hostChain.length > 0
      ? fallback.hostChain
      : previous.hostChain,
    activePortForwards: Array.isArray(fallback.activePortForwards)
      && fallback.activePortForwards.length > 0
      ? fallback.activePortForwards
      : previous.activePortForwards,
    connected: previousConnected || fallbackConnected,
  };
}

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

    const previous = previousById?.get?.(ownedId) || null;
    // Always consult fallback — a stale previous connected:false must not
    // block a fresher cross-scope snapshot (e.g. External MCP / other tab).
    const fallback = typeof findFallbackMeta === "function"
      ? findFallbackMeta(ownedId)
      : null;
    const meta = mergeRetentionMeta(previous, fallback);
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

module.exports = { retainOwnedSessions, mergeRetentionMeta };
