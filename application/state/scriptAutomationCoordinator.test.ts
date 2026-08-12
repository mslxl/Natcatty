import assert from 'node:assert/strict';
import test from 'node:test';
import {
  runConnectScriptsSequential,
  selectScriptOverlayRun,
  setScriptRuns,
  waitForScriptRun,
} from './scriptAutomationCoordinator.ts';
import type { ScriptRun } from '@/types/global/netcatty-bridge-script.d.ts';
import type { Snippet } from '@/domain/models';
import { netcattyBridge } from '@/infrastructure/services/netcattyBridge.ts';

test('waitForScriptRun resolves when run is already completed on subscribe', async () => {
  const runId = 'run-already-done';
  setScriptRuns([{
    runId,
    scriptId: 's1',
    sessionId: 'sess1',
    status: 'completed',
    startedAt: Date.now() - 1000,
    endedAt: Date.now(),
    logs: [],
  }]);

  const run = await waitForScriptRun(runId, { timeoutMs: 5000 });
  assert.equal(run.runId, runId);
  assert.equal(run.status, 'completed');
});

test('waitForScriptRun rejects when run already failed on subscribe', async () => {
  const runId = 'run-already-failed';
  setScriptRuns([{
    runId,
    sessionId: 'sess1',
    status: 'failed',
    startedAt: Date.now() - 1000,
    endedAt: Date.now(),
    error: 'boom',
    logs: [],
  }]);

  await assert.rejects(
    () => waitForScriptRun(runId, { timeoutMs: 5000 }),
    /boom/,
  );
});

test('selectScriptOverlayRun does not resurface older completed runs after dismissal', () => {
  const dismissedRunIds = new Set<string>();
  const completed = (runId: string, endedAt: number): ScriptRun => ({
    runId,
    sessionId: 'sess1',
    status: 'completed',
    startedAt: endedAt - 100,
    endedAt,
    logs: [],
  });
  const olderRun = completed('older-run', 1_000);
  const latestRun = completed('latest-run', 2_000);

  assert.equal(
    selectScriptOverlayRun([olderRun, latestRun], 'sess1', dismissedRunIds)?.runId,
    latestRun.runId,
  );
  assert.ok(dismissedRunIds.has(olderRun.runId));

  dismissedRunIds.add(latestRun.runId);
  assert.equal(selectScriptOverlayRun([olderRun, latestRun], 'sess1', dismissedRunIds), undefined);
});

test('runConnectScriptsSequential stops the backend run when aborted mid-flight', async () => {
  const originalGet = netcattyBridge.get;
  const runId = 'connect-run-abort';
  const sessionId = 'sess-connect-abort';
  let resolveScriptRun: ((value: { runId: string; runIds: string[] }) => void) | undefined;
  const scriptStopCalls: string[] = [];
  const storage = new Map<string, string>();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => { storage.set(key, String(value)); },
      removeItem: (key: string) => { storage.delete(key); },
      clear: () => { storage.clear(); },
    },
  });

  setScriptRuns([{
    runId,
    scriptId: 'connect-script',
    sessionId,
    status: 'running',
    startedAt: Date.now(),
    logs: [],
  }]);

  netcattyBridge.get = () => ({
    scriptRun: () => new Promise((resolve) => {
      resolveScriptRun = resolve;
    }),
    scriptStop: async (id: string) => {
      scriptStopCalls.push(id);
      setScriptRuns([{
        runId: id,
        scriptId: 'connect-script',
        sessionId,
        status: 'failed',
        startedAt: Date.now() - 10,
        endedAt: Date.now(),
        error: 'Stopped by user',
        logs: [],
      }]);
      resolveScriptRun?.({ runId: id, runIds: [id] });
      return { ok: true };
    },
  }) as ReturnType<typeof netcattyBridge.get>;

  const controller = new AbortController();
  const snippet: Snippet = {
    id: 'connect-script',
    label: 'Connect',
    command: 'nct.session.sleep(60)',
    kind: 'script',
  };

  try {
    const running = runConnectScriptsSequential({
      scripts: [snippet],
      sessionId,
      signal: controller.signal,
    });
    // Allow scriptRun to start and register the abort listener path.
    await Promise.resolve();
    controller.abort();
    await assert.rejects(() => running, /Aborted/);
    assert.deepEqual(scriptStopCalls, [runId]);
  } finally {
    netcattyBridge.get = originalGet;
    setScriptRuns([]);
    Reflect.deleteProperty(globalThis, 'localStorage');
  }
});
