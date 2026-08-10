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

export type AIPanelViewHandoffLike = {
  mode: 'draft' | 'session';
  sessionId?: string;
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
 * workspace-scoped chats and any handed-off chat whose original terminal pane
 * is gone, plus a session panel view so terminal scopes do not fall back to a
 * blank draft.
 */
export function handoffDissolvedWorkspaceAIScope<T extends AISessionScopeHandoffLike>(input: {
  activeSessionIdMap: AIActiveSessionIdMap;
  sessions: readonly T[];
  workspaceId: string;
  terminalIds: readonly string[];
  preferredTerminalId?: string | null;
  panelViewByScope?: Readonly<Record<string, AIPanelViewHandoffLike | undefined>>;
}): {
  activeSessionIdMap: Record<string, string | null>;
  sessions: T[];
  panelViewByScope: Record<string, AIPanelViewHandoffLike>;
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
  const survivorTerminalIds = new Set(input.terminalIds.filter(Boolean));
  const previousPanelViewByScope = (
    input.panelViewByScope as Record<string, AIPanelViewHandoffLike> | undefined
  ) ?? {};

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
    if (!preferredTerminalId) return session;

    const isWorkspaceScoped = (
      session.scope.type === 'workspace'
      && session.scope.targetId === input.workspaceId
    );
    const isOrphanedActiveChat = (
      hasWorkspaceActive
      && session.id === workspaceActive
      && session.scope.type === 'terminal'
      && Boolean(session.scope.targetId)
      && !survivorTerminalIds.has(session.scope.targetId as string)
    );
    if (!isWorkspaceScoped && !isOrphanedActiveChat) return session;
    if (session.scope.type === 'terminal' && session.scope.targetId === preferredTerminalId) {
      return session;
    }

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

  let panelViewsChanged = false;
  const nextPanelViewByScope: Record<string, AIPanelViewHandoffLike> = {
    ...previousPanelViewByScope,
  };
  if (preferredTerminalId && hasWorkspaceActive) {
    const terminalKey = buildAIScopeKey('terminal', preferredTerminalId);
    const workspacePanelView = previousPanelViewByScope[workspaceKey];
    const terminalPanelView = previousPanelViewByScope[terminalKey];
    const nextView: AIPanelViewHandoffLike = (
      workspacePanelView?.mode === 'session'
      && workspacePanelView.sessionId === workspaceActive
    )
      ? workspacePanelView
      : { mode: 'session', sessionId: workspaceActive };

    if (
      terminalPanelView?.mode !== nextView.mode
      || (nextView.mode === 'session' && terminalPanelView.sessionId !== nextView.sessionId)
    ) {
      nextPanelViewByScope[terminalKey] = nextView;
      panelViewsChanged = true;
    }
  }

  if (!mapChanged && !sessionsChanged && !panelViewsChanged) {
    return {
      activeSessionIdMap: input.activeSessionIdMap as Record<string, string | null>,
      sessions: input.sessions as T[],
      panelViewByScope: previousPanelViewByScope,
      changed: false,
    };
  }

  return {
    activeSessionIdMap: nextMap,
    sessions: sessionsChanged ? nextSessions : input.sessions as T[],
    panelViewByScope: nextPanelViewByScope,
    changed: true,
  };
}
