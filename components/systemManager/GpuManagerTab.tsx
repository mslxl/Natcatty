import { CircuitBoard, Cpu, Thermometer, Zap } from 'lucide-react';
import React, { memo, useCallback, useEffect, useMemo, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import type { useSystemManagerBackend } from '../../application/state/useSystemManagerBackend';
import type {
  AcceleratorDeviceInfo,
  AcceleratorProcessInfo,
  AcceleratorSnapshot,
} from '../../domain/systemManager/types';
import { ResourceBar } from './ResourceBar';
import {
  SystemPanelEmpty,
  SystemPanelError,
  SystemPanelInlineError,
  SystemPanelList,
  SystemPanelLoading,
  SystemPanelRefreshButton,
  SystemPanelRow,
  SystemPanelShell,
  SystemPanelStatusBadge,
  SystemPanelToolbar,
} from './SystemPanelUi';
import { usePolling, useStableTranslate } from '../../application/state/useSystemManager';

type Backend = ReturnType<typeof useSystemManagerBackend>;

interface GpuManagerTabProps {
  sessionId: string;
  isVisible: boolean;
  backend: Backend;
  refreshIntervalSec: number;
}

function formatMb(mb: number | null | undefined): string {
  if (!Number.isFinite(mb)) return '--';
  const value = Number(mb);
  if (value >= 1024) return `${(value / 1024).toFixed(1)} GB`;
  return `${Math.round(value)} MB`;
}

function memoryPercent(device: AcceleratorDeviceInfo): number | null {
  if (!Number.isFinite(device.memoryUsedMb) || !Number.isFinite(device.memoryTotalMb)) return null;
  if (!device.memoryTotalMb || device.memoryTotalMb <= 0) return null;
  return Math.max(0, Math.min(100, (Number(device.memoryUsedMb) / Number(device.memoryTotalMb)) * 100));
}

function vendorLabel(
  vendor: AcceleratorDeviceInfo['vendor'],
  t: ReturnType<typeof useI18n>['t'],
): string {
  return vendor === 'nvidia'
    ? t('systemManager.gpu.vendor.nvidia')
    : t('systemManager.gpu.vendor.ascend');
}

const DeviceCard = memo(function DeviceCard({
  device,
}: {
  device: AcceleratorDeviceInfo;
}) {
  const { t } = useI18n();
  const memPct = memoryPercent(device);
  const util = Number.isFinite(device.utilizationPercent) ? Number(device.utilizationPercent) : null;

  return (
    <div className="border-b border-border/30 px-3 py-2.5 space-y-2">
      <div className="flex items-start justify-between gap-2 min-w-0">
        <div className="min-w-0">
          <div className="flex items-center gap-1.5 min-w-0">
            <span className="text-xs font-medium truncate">
              [{device.index}] {device.name}
            </span>
            <SystemPanelStatusBadge tone="muted">
              {vendorLabel(device.vendor, t)}
            </SystemPanelStatusBadge>
            {device.health ? (
              <SystemPanelStatusBadge tone={/ok|healthy|good/i.test(device.health) ? 'success' : 'warning'}>
                {device.health}
              </SystemPanelStatusBadge>
            ) : null}
          </div>
          {device.driverVersion ? (
            <div className="text-[10px] text-muted-foreground mt-0.5">
              {t('systemManager.gpu.driver', { version: device.driverVersion })}
            </div>
          ) : null}
        </div>
        <div className="flex items-center gap-2 shrink-0 text-[10px] text-muted-foreground tabular-nums">
          {Number.isFinite(device.temperatureC) ? (
            <span className="inline-flex items-center gap-0.5" title={t('systemManager.gpu.temperature')}>
              <Thermometer size={10} />
              {Math.round(Number(device.temperatureC))}°C
            </span>
          ) : null}
          {Number.isFinite(device.powerDrawW) ? (
            <span className="inline-flex items-center gap-0.5" title={t('systemManager.gpu.power')}>
              <Zap size={10} />
              {Number(device.powerDrawW).toFixed(0)}
              {Number.isFinite(device.powerLimitW) ? `/${Math.round(Number(device.powerLimitW))}` : ''}
              W
            </span>
          ) : null}
        </div>
      </div>
      <ResourceBar
        label={t('systemManager.gpu.util')}
        value={util}
      />
      <div className="flex items-center gap-2 min-w-0">
        <ResourceBar
          label={device.vendor === 'ascend' ? t('systemManager.gpu.hbm') : t('systemManager.gpu.memory')}
          value={memPct}
          className="flex-1"
        />
        <span className="text-[10px] tabular-nums text-muted-foreground shrink-0">
          {formatMb(device.memoryUsedMb)} / {formatMb(device.memoryTotalMb)}
        </span>
      </div>
      {Number.isFinite(device.fanPercent) ? (
        <div className="text-[10px] text-muted-foreground">
          {t('systemManager.gpu.fan', { value: Math.round(Number(device.fanPercent)) })}
        </div>
      ) : null}
    </div>
  );
});

const ProcessRow = memo(function ProcessRow({
  process,
}: {
  process: AcceleratorProcessInfo;
}) {
  const { t } = useI18n();
  return (
    <SystemPanelRow
      title={process.processName || '—'}
      subtitle={`${vendorLabel(process.vendor, t)} #${process.gpuIndex}`}
      trailing={(
        <div className="flex items-center gap-2 text-[10px] text-muted-foreground tabular-nums">
          <span>PID {process.pid}</span>
          <span>{formatMb(process.memoryUsedMb)}</span>
        </div>
      )}
    />
  );
});

export const GpuManagerTab = memo(function GpuManagerTab({
  sessionId,
  isVisible,
  backend,
  refreshIntervalSec,
}: GpuManagerTabProps) {
  const { t } = useI18n();
  const stableT = useStableTranslate();
  const intervalMs = Math.max(2, refreshIntervalSec) * 1000;
  const [gpuListPending, setGpuListPending] = useState(false);

  useEffect(() => {
    setGpuListPending(false);
  }, [sessionId]);

  const fetcher = useCallback(async (): Promise<AcceleratorSnapshot | null> => {
    const result = await backend.listAccelerators(sessionId);
    if (result.pending) {
      setGpuListPending(true);
      return null;
    }
    setGpuListPending(false);
    if (!result.success) {
      throw new Error(result.error || stableT('systemManager.errors.loadGpu'));
    }
    return {
      devices: result.devices || [],
      processes: result.processes || [],
      nvidiaDriverVersion: result.nvidiaDriverVersion ?? null,
      probedAt: result.probedAt || Date.now(),
    };
  }, [backend, sessionId, stableT]);

  const { data, error, loading, refresh } = usePolling(
    fetcher,
    intervalMs,
    isVisible,
    undefined,
    { resetKey: sessionId },
  );

  const devices = useMemo(() => data?.devices ?? [], [data?.devices]);
  const processes = useMemo(() => data?.processes ?? [], [data?.processes]);
  const isRefreshActive = loading || gpuListPending;

  const meta = useMemo(() => {
    const nvidiaCount = devices.filter((d) => d.vendor === 'nvidia').length;
    const ascendCount = devices.filter((d) => d.vendor === 'ascend').length;
    return t('systemManager.gpu.meta', {
      devices: devices.length,
      processes: processes.length,
      nvidia: nvidiaCount,
      ascend: ascendCount,
    });
  }, [devices, processes, t]);

  if (!isVisible && !data) {
    return null;
  }

  if (!data) {
    if (error && !isRefreshActive) {
      return (
        <SystemPanelShell section="system-manager-gpu">
          <SystemPanelError
            message={error}
            retryLabel={t('history.action.refresh')}
            onRetry={() => void refresh()}
          />
        </SystemPanelShell>
      );
    }
    return (
      <SystemPanelShell section="system-manager-gpu">
        <SystemPanelLoading message={t('systemManager.gpu.loading')} />
      </SystemPanelShell>
    );
  }

  if (!devices.length && !processes.length) {
    return (
      <SystemPanelShell section="system-manager-gpu">
        <SystemPanelToolbar trailing={(
          <SystemPanelRefreshButton
            title={t('history.action.refresh')}
            loading={isRefreshActive}
            onClick={() => void refresh()}
          />
        )}>
          <span className="text-[11px] text-muted-foreground truncate">{meta}</span>
        </SystemPanelToolbar>
        <SystemPanelEmpty icon={CircuitBoard} message={t('systemManager.gpu.empty')} />
      </SystemPanelShell>
    );
  }

  return (
    <SystemPanelShell section="system-manager-gpu">
      <SystemPanelToolbar trailing={(
        <SystemPanelRefreshButton
          title={t('history.action.refresh')}
          loading={isRefreshActive}
          onClick={() => void refresh()}
        />
      )}>
        <span className="text-[11px] text-muted-foreground truncate">{meta}</span>
      </SystemPanelToolbar>
      {error && !isRefreshActive ? (
        <SystemPanelInlineError
          message={error}
          retryLabel={t('history.action.refresh')}
          onRetry={() => void refresh()}
        />
      ) : null}
      <SystemPanelList>
        <div className="px-3 py-1.5 text-[10px] uppercase tracking-wide text-muted-foreground flex items-center gap-1">
          <Cpu size={10} />
          {t('systemManager.gpu.devices')}
        </div>
        {devices.length === 0 ? (
          <div className="px-3 py-2 text-[11px] text-muted-foreground">
            {t('systemManager.gpu.empty')}
          </div>
        ) : (
          devices.map((device) => (
            <DeviceCard key={`${device.vendor}-${device.index}-${device.uuid || device.name}`} device={device} />
          ))
        )}
        <div className="px-3 pt-3 pb-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
          {t('systemManager.gpu.processes')}
        </div>
        {processes.length === 0 ? (
          <div className="px-3 py-2 text-[11px] text-muted-foreground">
            {t('systemManager.gpu.noProcesses')}
          </div>
        ) : (
          processes.map((process) => (
            <ProcessRow
              key={`${process.vendor}-${process.gpuIndex}-${process.pid}-${process.processName}`}
              process={process}
            />
          ))
        )}
      </SystemPanelList>
    </SystemPanelShell>
  );
});
