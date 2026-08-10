import { buildAIScopeKey } from './aiSessionsForScope';
import { resolveInheritedAIActiveSessionId } from './aiWorkspaceScopeInherit';

export type AIActiveSessionIdMap = Readonly<Record<string, string | null | undefined>>;

export type AISessionScopeHandoffLike = {
  id: string;
  scope: {
    type: string;
    targetId?: string;
    hostIds?: string[];
  };
  updatedAt?: number;
};

/**
 * Seed a brand-new workspace scope from member terminal maps (focused first)
 * so the first paint does not wait on a visible-panel write-back.
 */
export function seedWorkspaceAIActiveSessionFromMembers(input: {
  activeSessionIdMap: AIActiveSessionIdMap;
  workspaceId: string;
  memberTerminalIds: readonly string[];
  preferredTerminalId?: string | null;
}): Record<string, string | null> | null {
  const workspaceKey = buildAIScopeKey('workspace', input.workspaceId);
  const existing = input.activeSessionIdMap[workspaceKey];
  if (typeof existing === 'string' && existing.length > 0) {
    return null;
  }

  const inherited = resolveInheritedAIActiveSessionId({
    scopeType: 'workspace',
    scopeTargetId: input.workspaceId,
    activeSessionIdMap: input.activeSessionIdMap,
    memberTerminalIds: input.memberTerminalIds,
    preferredTerminalId: input.preferredTerminalId,
  });
  if (!inherited) return null;

  return {
    ...(input.activeSessionIdMap as Record<string, string | null>),
    [workspaceKey]: inherited,
  };
}

/**
 * When a workspace tab dissolves, copy its active chat onto the preferred
 * surviving terminal before the workspace scope key is pruned. Also remint
 * workspace-scoped chat records onto that terminal so history matching and
 * cleanup keep them visible.
 */
export function handoffDissolvedWorkspaceAIScope<T extends AISessionScopeHandoffLike>(input: {
  activeSessionIdMap: AIActiveSessionIdMap;
  sessions: readonly T[];
  workspaceId: string;
  terminalIds: readonly string[];
  preferredTerminalId?: string | null;
}): {
  activeSessionIdMap: Record<string, string | null>;
  sessions: T[];
  changed: boolean;
} {
  const preferredTerminalId = (
    input.preferredTerminalId
    && input.terminalIds.includes(input.preferredTerminalId)
      ? input.preferredTerminalId
      : input.terminalIds.find(Boolean)
  ) ?? null;

  const workspaceKey = buildAIScopeKey('workspace', input.workspaceId);
  const workspaceActive = input.activeSessionIdMap[workspaceKey];
  const hasWorkspaceActive = typeof workspaceActive === 'string' && workspaceActive.length > 0;

  let nextMap: Record<string, string | null> = {
    ...(input.activeSessionIdMap as Record<string, string | null>),
  };
  let mapChanged = false;

  if (preferredTerminalId && hasWorkspaceActive) {
    const terminalKey = buildAIScopeKey('terminal', preferredTerminalId);
    if (nextMap[terminalKey] !== workspaceActive) {
      nextMap = { ...nextMap, [terminalKey]: workspaceActive };
      mapChanged = true;
    }
  }

  let sessionsChanged = false;
  const nextSessions = input.sessions.map((session) => {
    if (session.scope.type !== 'workspace' || session.scope.targetId !== input.workspaceId) {
      return session;
    }
    if (!preferredTerminalId) return session;
    sessionsChanged = true;
    return {
      ...session,
      scope: {
        ...session.scope,
        type: 'terminal',
        targetId: preferredTerminalId,
      },
      updatedAt: Date.now(),
    };
  });

  if (!mapChanged && !sessionsChanged) {
    return {
      activeSessionIdMap: input.activeSessionIdMap as Record<string, string | null>,
      sessions: input.sessions as T[],
      changed: false,
    };
  }

  return {
    activeSessionIdMap: nextMap,
    sessions: sessionsChanged ? nextSessions : input.sessions as T[],
    changed: true,
  };
}
