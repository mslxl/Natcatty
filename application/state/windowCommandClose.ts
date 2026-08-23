export type WindowCommandCloseIntent =
  | { kind: 'forwardShortcut' }
  | { kind: 'closeDialog' }
  | { kind: 'closeTab' }
  | { kind: 'closeLogView'; tabId: string }
  | { kind: 'closeWindow' };

interface ResolveWindowCommandCloseIntentInput {
  activeTabId: string | null;
  editorTabIds: string[];
  sessionIds: string[];
  workspaceIds: string[];
  logViewIds: string[];
  pluginViewTabIds?: string[];
  closeTabShortcutEnabled?: boolean;
  hasOpenDialog?: boolean;
}

export function resolveWindowCommandCloseIntent({
  activeTabId,
  editorTabIds,
  sessionIds,
  workspaceIds,
  logViewIds,
  pluginViewTabIds = [],
  closeTabShortcutEnabled = true,
  hasOpenDialog = false,
}: ResolveWindowCommandCloseIntentInput): WindowCommandCloseIntent {
  if (!closeTabShortcutEnabled) {
    return { kind: 'forwardShortcut' };
  }

  if (hasOpenDialog) {
    return { kind: 'closeDialog' };
  }

  if (!activeTabId) {
    return { kind: 'closeWindow' };
  }

  if (editorTabIds.includes(activeTabId) || pluginViewTabIds.includes(activeTabId)) {
    return { kind: 'closeTab' };
  }

  if (sessionIds.includes(activeTabId) || workspaceIds.includes(activeTabId)) {
    return { kind: 'closeTab' };
  }

  if (logViewIds.includes(activeTabId)) {
    return { kind: 'closeLogView', tabId: activeTabId };
  }

  if (activeTabId === 'vault' || activeTabId === 'sftp') {
    return { kind: 'closeWindow' };
  }

  return { kind: 'closeWindow' };
}
