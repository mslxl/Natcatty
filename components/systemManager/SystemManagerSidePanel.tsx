import { Activity, Box, LayoutList, TerminalSquare } from 'lucide-react';
import React, { memo, useMemo, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { useSystemManagerBackend } from '../../application/state/useSystemManagerBackend';
import type { TerminalSettings } from '../../domain/models';
import type { Host } from '../../domain/models/connection';
import type { SystemManagerSubTab } from '../../domain/systemManager/types';
import { buildSystemManagerTabs } from '../../domain/systemManager/systemTarget';
import type { Snippet, TerminalSession } from '../../types';
import { cn } from '../../lib/utils';
import { DockerManagerTab } from './DockerManagerTab';
import { ProcessManagerTab } from './ProcessManagerTab';
import { TmuxManagerTab } from './TmuxManagerTab';
import { WorkspaceSidebarHostHeader } from '../terminalLayer/WorkspaceSidebarHostHeader';
import { SystemPanelEmpty, SystemPanelShell } from './SystemPanelUi';
import { useSessionCapabilities } from './hooks/useSystemManager';

interface SystemManagerSidePanelProps {
  session: TerminalSession | null;
  sessionHost: Host | null;
  showWorkspaceHostHeader?: boolean;
  isVisible: boolean;
  terminalSettings: TerminalSettings;
  snippets: Snippet[];
}

export const SystemManagerSidePanel = memo(function SystemManagerSidePanel({
  session,
  sessionHost,
  showWorkspaceHostHeader = false,
  isVisible,
  terminalSettings,
  snippets,
}: SystemManagerSidePanelProps) {
  const { t } = useI18n();
  const backend = useSystemManagerBackend();
  const sessionId = session?.id ?? null;
  const isConnected = session?.status === 'connected';

  const { capabilities, probing } = useSessionCapabilities(sessionId, isConnected, backend, isVisible);

  const availableTabs = useMemo(
    () => buildSystemManagerTabs(sessionHost, capabilities, session),
    [capabilities, session, sessionHost],
  );

  const [activeTab, setActiveTab] = useState<SystemManagerSubTab>('processes');
  const resolvedTab = availableTabs.includes(activeTab) ? activeTab : 'processes';

  const workspaceHostHeader = showWorkspaceHostHeader && sessionHost ? (
    <WorkspaceSidebarHostHeader
      host={sessionHost}
      section="terminal-system-host-header"
    />
  ) : null;

  if (!sessionId || !session) {
    return (
      <SystemPanelShell section="system-manager-panel">
        {workspaceHostHeader}
        <SystemPanelEmpty icon={Activity} message={t('systemManager.noSession')} />
      </SystemPanelShell>
    );
  }

  if (!isConnected) {
    return (
      <SystemPanelShell section="system-manager-panel">
        {workspaceHostHeader}
        <SystemPanelEmpty icon={Activity} message={t('systemManager.notConnected')} />
      </SystemPanelShell>
    );
  }

  const tabDefs: { id: SystemManagerSubTab; icon: typeof LayoutList; label: string }[] = [
    { id: 'processes', icon: LayoutList, label: t('systemManager.tabs.processes') },
    { id: 'tmux', icon: TerminalSquare, label: t('systemManager.tabs.tmux') },
    { id: 'docker', icon: Box, label: t('systemManager.tabs.docker') },
  ];

  const tmuxReady = capabilities?.hasTmux === true;
  const dockerReady = capabilities?.hasDocker === true;
  const tmuxUnavailable = !probing && capabilities !== undefined && !tmuxReady;
  const dockerUnavailable = !probing && capabilities !== undefined && !dockerReady;

  return (
    <SystemPanelShell section="system-manager-panel">
      {workspaceHostHeader}
      <div className="shrink-0 flex items-center gap-0.5 px-2 py-1 border-b border-border/50">
        {tabDefs.filter((tab) => availableTabs.includes(tab.id)).map(({ id, icon: Icon, label }) => (
          <button
            key={id}
            type="button"
            className={cn(
              'flex items-center gap-1.5 px-2 py-1 rounded text-[11px] transition-colors',
              resolvedTab === id
                ? 'bg-muted text-foreground font-medium'
                : 'text-muted-foreground hover:text-foreground',
            )}
            onClick={() => setActiveTab(id)}
          >
            <Icon size={12} />
            {label}
          </button>
        ))}
      </div>

      <div className="flex-1 min-h-0 flex flex-col">
        {resolvedTab === 'processes' && (
          <ProcessManagerTab
            sessionId={sessionId}
            isVisible={isVisible}
            backend={backend}
            refreshIntervalSec={terminalSettings.systemManagerProcessRefreshInterval}
          />
        )}
        {resolvedTab === 'tmux' && (
          tmuxUnavailable ? (
            <SystemPanelEmpty icon={TerminalSquare} message={t('systemManager.tmux.unavailable')} />
          ) : (
            <TmuxManagerTab
              sessionId={sessionId}
              parentSession={session}
              isVisible={isVisible && tmuxReady}
              backend={backend}
              refreshIntervalSec={terminalSettings.systemManagerTmuxRefreshInterval}
              snippets={snippets}
            />
          )
        )}
        {resolvedTab === 'docker' && (
          dockerUnavailable ? (
            <SystemPanelEmpty icon={Box} message={t('systemManager.docker.unavailable')} />
          ) : (
            <DockerManagerTab
              sessionId={sessionId}
              parentSession={session}
              isVisible={isVisible && dockerReady}
              backend={backend}
              listRefreshIntervalSec={terminalSettings.systemManagerDockerListRefreshInterval}
              statsRefreshIntervalSec={terminalSettings.systemManagerDockerStatsRefreshInterval}
            />
          )
        )}
      </div>
    </SystemPanelShell>
  );
});
