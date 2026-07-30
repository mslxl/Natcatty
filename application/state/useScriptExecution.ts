import { useCallback, useEffect, useSyncExternalStore } from 'react';
import type { ScriptRunParams } from '@/types/global/netcatty-bridge-script.d.ts';
import { netcattyBridge } from '@/infrastructure/services/netcattyBridge.ts';
import {
  getScriptRunsSnapshot,
  publishScriptRunsSnapshot,
  subscribeScriptRuns,
} from './scriptRunsStore.ts';

let scriptRunsBridgeBound = false;

function ensureScriptRunsBridgeBound(): void {
  if (scriptRunsBridgeBound) return;
  if (typeof window === 'undefined') return;
  const bridge = netcattyBridge.get();
  if (!bridge?.scriptGetRuns) return;
  scriptRunsBridgeBound = true;
  bridge.scriptGetRuns()
    .then((runs) => {
      publishScriptRunsSnapshot(runs);
    })
    .catch(() => {});
  bridge.onScriptRunsUpdated?.(({ runs: nextRuns }) => {
    publishScriptRunsSnapshot(nextRuns);
  });
}

/**
 * Script run state is externalized so TerminalLayer can avoid re-rendering on
 * every automation log tick. Call sites that need the list should subscribe
 * via this hook (or scriptRunsStore directly).
 */
export function useScriptExecution() {
  useEffect(() => {
    ensureScriptRunsBridgeBound();
  }, []);

  const runs = useSyncExternalStore(
    subscribeScriptRuns,
    getScriptRunsSnapshot,
    getScriptRunsSnapshot,
  );

  const runScript = useCallback(async (params: ScriptRunParams) => {
    const bridge = netcattyBridge.get();
    if (!bridge?.scriptRun) {
      throw new Error('Script bridge unavailable');
    }
    return bridge.scriptRun(params);
  }, []);

  const stopRun = useCallback(async (runId: string) => {
    await netcattyBridge.get()?.scriptStop?.(runId);
  }, []);

  const pauseRun = useCallback(async (runId: string) => {
    await netcattyBridge.get()?.scriptPause?.(runId);
  }, []);

  const resumeRun = useCallback(async (runId: string) => {
    await netcattyBridge.get()?.scriptResume?.(runId);
  }, []);

  const getRunsForSession = useCallback((sessionId: string) => {
    return getScriptRunsSnapshot().filter((run) => run.sessionId === sessionId);
  }, []);

  return {
    runs,
    runScript,
    stopRun,
    pauseRun,
    resumeRun,
    getRunsForSession,
  };
}
