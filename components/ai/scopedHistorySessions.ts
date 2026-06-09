import type { AISession } from '../../infrastructure/ai/types';
import { getSessionScopeMatchRank } from './sessionScopeMatch';

type HistoryCacheKey = string;
const historyCache = new WeakMap<AISession[], Map<HistoryCacheKey, AISession[]>>();

function buildHistoryCacheKey(
  scopeType: 'terminal' | 'workspace',
  scopeTargetId: string | undefined,
  scopeHostIds: string[] | undefined,
  activeTerminalSessionIds: Set<string>,
): HistoryCacheKey {
  const hostKey = scopeHostIds?.join(',') ?? '';
  const terminalKey = [...activeTerminalSessionIds].sort().join(',');
  return `${scopeType}:${scopeTargetId ?? ''}:${hostKey}:${terminalKey}`;
}

export function getScopedHistorySessions(
  sessions: AISession[],
  scopeType: 'terminal' | 'workspace',
  scopeTargetId: string | undefined,
  scopeHostIds: string[] | undefined,
  activeTerminalSessionIds: Set<string>,
): AISession[] {
  let scopeCache = historyCache.get(sessions);
  if (!scopeCache) {
    scopeCache = new Map();
    historyCache.set(sessions, scopeCache);
  }

  const cacheKey = buildHistoryCacheKey(
    scopeType,
    scopeTargetId,
    scopeHostIds,
    activeTerminalSessionIds,
  );
  const cached = scopeCache.get(cacheKey);
  if (cached) {
    return cached;
  }

  const result = sessions
    .map((session) => ({
      session,
      matchRank: getSessionScopeMatchRank(
        session,
        scopeType,
        scopeTargetId,
        scopeHostIds,
        activeTerminalSessionIds,
      ),
    }))
    .filter(({ matchRank }) => matchRank > 0)
    .sort((a, b) => b.matchRank - a.matchRank || b.session.updatedAt - a.session.updatedAt)
    .map(({ session }) => session);

  scopeCache.set(cacheKey, result);
  return result;
}
