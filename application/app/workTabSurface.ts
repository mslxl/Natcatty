import {
  fromEditorTabId,
  isEditorTabId,
} from '../state/activeTabStore';
import type { EditorTab } from '../state/editorTabStore';
import type { TerminalSession, Workspace } from '../../types';

export function isRootPageTabId(activeTabId: string): boolean {
  return activeTabId === 'vault' || activeTabId === 'sftp';
}

export function buildOrderedWorkTabIds(
  tabOrder: readonly string[],
  allTabIds: readonly string[],
): string[] {
  const allTabIdSet = new Set(allTabIds);
  const orderedIds = tabOrder.filter((id) => allTabIdSet.has(id));
  const orderedIdSet = new Set(orderedIds);
  const newIds = allTabIds.filter((id) => !orderedIdSet.has(id));
  return [...orderedIds, ...newIds];
}

export function isHostTreeWorkTabSurface({
  enabled,
  activeTabId,
  logViewIds = new Set(),
  orderedTabs,
  sessionIds,
  workspaceIds,
}: {
  enabled: boolean;
  activeTabId: string;
  logViewIds?: ReadonlySet<string>;
  orderedTabs: readonly string[];
  sessionIds: ReadonlySet<string>;
  workspaceIds: ReadonlySet<string>;
}): boolean {
  if (!enabled) return false;
  if (isRootPageTabId(activeTabId)) return false;
  return orderedTabs.includes(activeTabId)
    || isEditorTabId(activeTabId)
    || logViewIds.has(activeTabId)
    || sessionIds.has(activeTabId)
    || workspaceIds.has(activeTabId);
}

export function isTerminalContentTabSurface({
  activeTabId,
  sessionIds,
  workspaceIds,
}: {
  activeTabId: string;
  sessionIds: ReadonlySet<string>;
  workspaceIds: ReadonlySet<string>;
}): boolean {
  return sessionIds.has(activeTabId) || workspaceIds.has(activeTabId);
}

export function resolveWorkTabActiveHostId({
  activeTabId,
  editorTabs,
  sessions,
  workspaces,
}: {
  activeTabId: string;
  editorTabs: readonly EditorTab[];
  sessions: readonly TerminalSession[];
  workspaces: readonly Workspace[];
}): string | null {
  if (isEditorTabId(activeTabId)) {
    const editorId = fromEditorTabId(activeTabId);
    return editorTabs.find((tab) => tab.id === editorId)?.hostId ?? null;
  }

  const activeSession = sessions.find((session) => session.id === activeTabId);
  if (activeSession) return activeSession.hostId ?? null;

  const activeWorkspace = workspaces.find((workspace) => workspace.id === activeTabId);
  if (!activeWorkspace) return null;

  const focusedSessionId = activeWorkspace.focusedSessionId;
  if (focusedSessionId) {
    return sessions.find((session) => session.id === focusedSessionId)?.hostId ?? null;
  }

  return null;
}
