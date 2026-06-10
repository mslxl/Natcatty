import { Pause, Pencil, Play, Trash2, Zap } from 'lucide-react';
import React, { memo, useCallback, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import type { useSystemManagerBackend } from '../../application/state/useSystemManagerBackend';
import type { DockerContainerAction, DockerContainerInfo, DockerStatInfo } from '../../domain/systemManager/types';
import { getContainerFlags } from '../../domain/systemManager/containerState';
import { DockerInspectView } from './DockerInspectView';
import { ResourceBar } from './ResourceBar';
import {
  SystemPanelActionChip,
  SystemPanelDetailStrip,
} from './SystemPanelUi';
import { SystemPanelPromptDialog } from './SystemPanelPromptDialog';
import { usePolling } from './hooks/useSystemManager';

type Backend = ReturnType<typeof useSystemManagerBackend>;

interface DockerContainerDetailProps {
  container: DockerContainerInfo;
  sessionId: string;
  backend: Backend;
  statsRefreshIntervalSec: number;
  inspect: Record<string, unknown> | null;
  pendingAction: DockerContainerAction | null;
  onCloseInspect: () => void;
  onRunAction: (containerId: string, action: DockerContainerAction, newName?: string) => Promise<void>;
}

export const DockerContainerDetail = memo(function DockerContainerDetail({
  container,
  sessionId,
  backend,
  statsRefreshIntervalSec,
  inspect,
  pendingAction,
  onCloseInspect,
  onRunAction,
}: DockerContainerDetailProps) {
  const { t } = useI18n();
  const shortId = container.id.slice(0, 12);
  const { isRunning, isPaused } = getContainerFlags(container);

  const statsFetcher = useCallback(async () => {
    const result = await backend.getDockerStats({ sessionId, ids: [container.id] });
    if (!result.success || !result.stats) {
      throw new Error(result.error || t('systemManager.errors.loadDockerStats'));
    }
    return result.stats;
  }, [backend, container.id, sessionId, t]);

  const statsIntervalMs = Math.max(2, statsRefreshIntervalSec) * 1000;
  // docker stats still reports paused containers, so keep polling them.
  const { data: stats } = usePolling<DockerStatInfo[]>(statsFetcher, statsIntervalMs, isRunning || isPaused);

  const stat = stats?.find((s) => s.id === container.id || s.id.startsWith(shortId)) ?? stats?.[0];

  const [renameOpen, setRenameOpen] = useState(false);
  const actionBusy = pendingAction !== null;

  return (
    <>
      <SystemPanelDetailStrip>
        {container.ports && (
          <div className="text-[10px] text-muted-foreground mb-2 truncate">{container.ports}</div>
        )}
        {stat && (
          <div className="space-y-1 mb-2">
            <ResourceBar label="CPU" value={stat.cpuPercent} />
            <ResourceBar label="MEM" value={stat.memPercent} />
            <div className="text-[10px] text-muted-foreground">{stat.netIO} · {stat.memUsage}</div>
          </div>
        )}
        <div className="flex flex-wrap items-center gap-0.5">
          <SystemPanelActionChip title={t('systemManager.docker.renamePrompt')} disabled={actionBusy} onClick={() => setRenameOpen(true)}>
            <Pencil size={11} /> {t('common.rename')}
          </SystemPanelActionChip>
          {isRunning && (
            <SystemPanelActionChip title={t('systemManager.docker.pause')} disabled={actionBusy} onClick={() => void onRunAction(shortId, 'pause')}>
              <Pause size={11} /> {t('systemManager.docker.pause')}
            </SystemPanelActionChip>
          )}
          {isPaused && (
            <SystemPanelActionChip title={t('systemManager.docker.unpause')} disabled={actionBusy} onClick={() => void onRunAction(shortId, 'unpause')}>
              <Play size={11} /> {t('systemManager.docker.unpause')}
            </SystemPanelActionChip>
          )}
          {(isRunning || isPaused) && (
            <SystemPanelActionChip title={t('systemManager.docker.kill')} disabled={actionBusy} onClick={() => void onRunAction(shortId, 'kill')} destructive>
              <Zap size={11} /> {t('systemManager.docker.kill')}
            </SystemPanelActionChip>
          )}
          <SystemPanelActionChip title={t('systemManager.docker.confirmRemove')} disabled={actionBusy} onClick={() => void onRunAction(shortId, 'rm')} destructive>
            <Trash2 size={11} />
          </SystemPanelActionChip>
        </div>
      </SystemPanelDetailStrip>
      {inspect && (
        <DockerInspectView
          kind="container"
          data={inspect}
          onClose={onCloseInspect}
        />
      )}

      <SystemPanelPromptDialog
        open={renameOpen}
        title={t('common.rename')}
        fields={[{
          id: 'name',
          label: t('systemManager.docker.renamePrompt'),
          initialValue: container.name || shortId,
        }]}
        confirmLabel={t('common.rename')}
        onOpenChange={setRenameOpen}
        onSubmit={(values) => {
          setRenameOpen(false);
          if (values.name !== container.name) {
            void onRunAction(shortId, 'rename', values.name);
          }
        }}
      />
    </>
  );
});
