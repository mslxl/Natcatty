/**
 * Exact-scope AI session helpers for multi-panel memo isolation.
 *
 * History still needs the full sessions list for fuzzy host-match ranking
 * (`getScopedHistorySessions`). Stream thrash is blocked by comparing only
 * exact-scope session object refs in panel areEqual — not by pre-filtering
 * the history universe away.
 */

export type AISessionScopeLike = {
  type: string;
  targetId?: string;
};

export type AISessionLike = {
  id: string;
  scope: AISessionScopeLike;
};

export function buildAIScopeKey(scopeType: string, scopeTargetId?: string): string {
  return `${scopeType}:${scopeTargetId ?? ''}`;
}

export function sessionMatchesAIScope(
  session: AISessionLike,
  scopeType: string,
  scopeTargetId?: string,
): boolean {
  return session.scope.type === scopeType
    && (session.scope.targetId ?? '') === (scopeTargetId ?? '');
}

export function filterAISessionsForScope<T extends AISessionLike>(
  sessions: readonly T[],
  scopeType: string,
  scopeTargetId?: string,
): T[] {
  return sessions.filter((session) => sessionMatchesAIScope(session, scopeType, scopeTargetId));
}

/**
 * True when exact-scope session object identities match (order-insensitive by id).
 * Sibling stream updates replace only their own session objects, so other panels
 * see the same exact-scope refs and can skip re-render.
 */
export function exactScopeAISessionsEqual<T extends AISessionLike>(
  prev: readonly T[] | null | undefined,
  next: readonly T[] | null | undefined,
  scopeType: string,
  scopeTargetId?: string,
): boolean {
  if (prev === next) return true;
  if (!prev || !next) return false;
  const prevExact = filterAISessionsForScope(prev, scopeType, scopeTargetId);
  const nextExact = filterAISessionsForScope(next, scopeType, scopeTargetId);
  if (prevExact.length !== nextExact.length) return false;
  if (prevExact.length === 0) return true;
  const prevById = new Map(prevExact.map((session) => [session.id, session]));
  for (const session of nextExact) {
    if (prevById.get(session.id) !== session) return false;
  }
  return true;
}

/**
 * Keep previous filtered array identity when every matching session ref is the same.
 */
export function retainStableAISessionsForScope<T extends AISessionLike>(
  previous: readonly T[] | null | undefined,
  next: readonly T[],
): readonly T[] {
  if (
    previous
    && previous.length === next.length
    && previous.every((session, index) => session === next[index])
  ) {
    return previous;
  }
  return next;
}
