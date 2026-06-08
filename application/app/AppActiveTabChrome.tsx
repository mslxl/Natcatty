import { useEffect, useMemo } from 'react';

import {
  fromEditorTabId,
  isEditorTabId,
  useActiveTabId,
} from '../state/activeTabStore';
import { setImmersiveActive } from '../state/immersiveStore';
import { useImmersiveMode } from '../state/useImmersiveMode';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';
import {
  applyCustomAccentToTerminalTheme,
  resolveHostTerminalThemeId,
} from '../../domain/terminalAppearance';
import { collectSessionIds } from '../../domain/workspace';
import type {
  Host,
  TerminalSession,
  TerminalTheme,
  Workspace,
} from '../../types';
import type { LogView } from '../state/logViewState';
import type { EditorTab } from '../state/editorTabStore';

interface AppActiveTabChromeProps {
  showSftpTab: boolean;
  setActiveTabId: (id: string) => void;
  hostById: Map<string, Host>;
  sessionById: Map<string, TerminalSession>;
  workspaceById: Map<string, Workspace>;
  themeById: Map<string, TerminalTheme>;
  currentTerminalTheme: TerminalTheme;
  followAppTerminalTheme: boolean;
  accentMode: 'theme' | 'custom';
  customAccent: string;
  reapplyCurrentTheme: () => void;
  editorTabs: readonly EditorTab[];
  logViews: readonly LogView[];
  t: (key: string) => string;
}

/**
 * Owns the `activeTabId` subscription and the purely side-effectful "chrome"
 * work derived from it: immersive-mode theming, window title, and the
 * SFTP-tab guard. Extracted out of <App> so that switching top tabs only
 * re-renders this null-rendering component (and the self-subscribing leaves)
 * instead of forcing the entire App tree (which holds all vault/session/
 * settings state and rebuilds the giant AppView ctx) to re-render.
 *
 * Renders nothing; publishes "immersive active" to immersiveStore so AppView
 * and TopTabs can read it without re-rendering App.
 */
export function AppActiveTabChrome({
  showSftpTab,
  setActiveTabId,
  hostById,
  sessionById,
  workspaceById,
  themeById,
  currentTerminalTheme,
  followAppTerminalTheme,
  accentMode,
  customAccent,
  reapplyCurrentTheme,
  editorTabs,
  logViews,
  t,
}: AppActiveTabChromeProps) {
  const activeTabId = useActiveTabId();

  useEffect(() => {
    if (!showSftpTab && activeTabId === 'sftp') {
      setActiveTabId('vault');
    }
  }, [showSftpTab, activeTabId, setActiveTabId]);

  const activeTerminalTheme = useMemo<TerminalTheme | null>(() => {
    if (activeTabId === 'vault' || activeTabId === 'sftp') return null;

    const resolveTheme = (s: TerminalSession): TerminalTheme => {
      let baseTheme: TerminalTheme;
      if (followAppTerminalTheme) {
        baseTheme = currentTerminalTheme;
      } else {
        const host = hostById.get(s.hostId) ?? null;
        const themeId = resolveHostTerminalThemeId(host, currentTerminalTheme.id);
        baseTheme = themeById.get(themeId) || currentTerminalTheme;
      }
      return applyCustomAccentToTerminalTheme(baseTheme, accentMode, customAccent);
    };

    const workspace = workspaceById.get(activeTabId);
    if (workspace) {
      if (workspace.viewMode === 'focus') {
        const wsSessionIds = collectSessionIds(workspace.root);
        const focused = (workspace.focusedSessionId
          ? sessionById.get(workspace.focusedSessionId)
          : null)
          ?? wsSessionIds.map((id) => sessionById.get(id)).find(Boolean);
        return focused ? resolveTheme(focused) : null;
      }
      const sessionIds = collectSessionIds(workspace.root);
      const wsSessions = sessionIds
        .map((id) => sessionById.get(id))
        .filter(Boolean) as TerminalSession[];
      if (wsSessions.length === 0) return null;
      const firstTheme = resolveTheme(wsSessions[0]);
      const allSame = wsSessions.every((s) => resolveTheme(s).id === firstTheme.id);
      return allSame ? firstTheme : null;
    }

    const session = sessionById.get(activeTabId);
    if (!session) return null;
    return resolveTheme(session);
  }, [accentMode, activeTabId, currentTerminalTheme, customAccent, followAppTerminalTheme, hostById, sessionById, themeById, workspaceById]);

  useImmersiveMode({
    activeTabId,
    activeTerminalTheme,
    restoreOriginalTheme: reapplyCurrentTheme,
  });

  useEffect(() => {
    setImmersiveActive(activeTerminalTheme !== null);
  }, [activeTerminalTheme]);

  const editorTabFileNameCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const tab of editorTabs) counts.set(tab.fileName, (counts.get(tab.fileName) ?? 0) + 1);
    return counts;
  }, [editorTabs]);

  const activeWindowTitle = useMemo(() => {
    if (activeTabId === 'vault') return 'Vaults';
    if (activeTabId === 'sftp') return 'SFTP';
    if (isEditorTabId(activeTabId)) {
      const editorTab = editorTabs.find((tab) => tab.id === fromEditorTabId(activeTabId));
      if (!editorTab) return 'Editor';
      const suffix = (editorTabFileNameCounts.get(editorTab.fileName) ?? 0) > 1
        ? ` · ${editorTab.remotePath.split('/').slice(-2, -1)[0] || '/'}`
        : '';
      return `${editorTab.fileName}${suffix}`;
    }
    const workspace = workspaceById.get(activeTabId);
    if (workspace) return workspace.title;
    const session = sessionById.get(activeTabId);
    if (session) return session.hostLabel;
    const logView = logViews.find((item) => item.id === activeTabId);
    if (logView) {
      const isLocal = logView.log.protocol === 'local' || logView.log.hostname === 'localhost';
      return `${t('tabs.logPrefix')} ${isLocal ? t('tabs.logLocal') : logView.log.hostname}`;
    }
    return 'Netcatty';
  }, [activeTabId, editorTabFileNameCounts, editorTabs, logViews, sessionById, t, workspaceById]);

  useEffect(() => {
    void netcattyBridge.get()?.setWindowTitle?.(activeWindowTitle);
  }, [activeWindowTitle]);

  return null;
}
