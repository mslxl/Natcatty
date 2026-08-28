import { useCallback, useEffect, useState } from 'react';
import type { AIToolIntegrationMode } from '../../infrastructure/ai/types';
import { STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED } from '../../infrastructure/config/storageKeys';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';
import { AI_STATE_CHANGED_EVENT } from './aiStateEvents';
import { readExternalMcpStoredEnabled } from './useExternalMcpToggleState';

/** Right after an enable toggle the runtime may still be starting; poll briefly. */
const EXTERNAL_MCP_READY_RETRY_LIMIT = 3;
const EXTERNAL_MCP_READY_RETRY_DELAY_MS = 1200;

export type ToolAccessGuidanceState = {
  /** Path of the local Netcatty skill file (Skills + CLI mode). */
  skillPath: string | null;
  /** Human-readable CLI command prefix shown under the skill path. */
  commandPrefix: string;
  /** Launcher path of the External MCP host (null while not enabled/ready). */
  mcpLauncherPath: string | null;
  /** Discovery file path handed to the external MCP host. */
  mcpDiscoveryPath: string | null;
};

/**
 * Resolves Tool Access guidance data for the settings view.
 *
 * Owns the External MCP lifecycle synchronization (status fetch, persisted
 * enable-key subscriptions and start-up retry polling) so view components only
 * render the resolved guidance state. The status paths are only trusted while
 * External MCP is actually enabled: the host keeps reporting launcher and
 * discovery paths even when the runtime is disabled or after an idle shutdown.
 */
export function useToolAccessGuidanceState(mode: AIToolIntegrationMode): ToolAccessGuidanceState {
  const [skillPath, setSkillPath] = useState<string | null>(null);
  const [commandPrefix, setCommandPrefix] = useState('');
  const [mcpLauncherPath, setMcpLauncherPath] = useState<string | null>(null);
  const [mcpDiscoveryPath, setMcpDiscoveryPath] = useState<string | null>(null);

  const refreshMcpStatus = useCallback(async (): Promise<string | null | undefined> => {
    const status = await netcattyBridge.get()?.externalMcpGetStatus?.();
    if (!status?.ok) return undefined;
    const ready = Boolean(status.enabled);
    const launcherPath = ready ? status.launcherPath ?? null : null;
    const discoveryPath = ready ? status.discoveryPath ?? null : null;
    setMcpLauncherPath(launcherPath);
    setMcpDiscoveryPath(discoveryPath);
    return launcherPath && discoveryPath ? launcherPath : null;
  }, []);

  // Initial one-shot fetch for the active mode.
  useEffect(() => {
    let cancelled = false;
    if (mode === 'skills') {
      void netcattyBridge
        .get()
        ?.aiSkillsCliGetInvocation?.()
        .then((result) => {
          if (cancelled || !result?.ok) return;
          setSkillPath(result.skillPath ?? null);
          setCommandPrefix(result.commandPrefix || '');
        });
    } else {
      void refreshMcpStatus().catch(() => undefined);
    }
    return () => {
      cancelled = true;
    };
  }, [mode, refreshMcpStatus]);

  // Keep MCP guidance in sync with the shared persisted enable switch and
  // retry briefly while the runtime finishes starting after an enable toggle.
  useEffect(() => {
    if (mode === 'skills') return;
    let cancelled = false;
    let retryTimer: number | undefined;
    let retries = 0;

    const refetch = async () => {
      if (cancelled) return;
      const readyLauncherPath = await refreshMcpStatus().catch(() => undefined);
      if (cancelled) return;
      // Retry while the fetched runtime status disagrees with the persisted
      // switch: after an enable the runtime may still be starting, and the
      // disable event is emitted before the lifecycle IPC settles, so a stale
      // "enabled" status can survive the immediate refetch.
      const statusReady = readyLauncherPath != null;
      if (retries < EXTERNAL_MCP_READY_RETRY_LIMIT && readExternalMcpStoredEnabled() !== statusReady) {
        retries += 1;
        retryTimer = window.setTimeout(() => {
          void refetch();
        }, EXTERNAL_MCP_READY_RETRY_DELAY_MS);
      }
    };

    const refetchOnChange = () => {
      retries = 0;
      void refetch();
    };
    const handleAIStateChanged = (event: Event) => {
      const key = (event as CustomEvent<{ key?: string }>).detail?.key;
      if (key !== STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED) return;
      refetchOnChange();
    };
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED) return;
      refetchOnChange();
    };

    window.addEventListener(AI_STATE_CHANGED_EVENT, handleAIStateChanged);
    window.addEventListener('storage', handleStorage);
    return () => {
      cancelled = true;
      if (retryTimer) window.clearTimeout(retryTimer);
      window.removeEventListener(AI_STATE_CHANGED_EVENT, handleAIStateChanged);
      window.removeEventListener('storage', handleStorage);
    };
  }, [mode, refreshMcpStatus]);

  return { skillPath, commandPrefix, mcpLauncherPath, mcpDiscoveryPath };
}
