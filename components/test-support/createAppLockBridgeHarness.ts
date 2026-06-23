import type { RuntimeAppLockState } from "../../application/state/useAppLockRuntime";

type UnlockResult =
  | { ok: true }
  | { ok: false; error: "empty" | "incorrect" };

type HarnessOptions = {
  runtimeState: RuntimeAppLockState;
  unlockPassword?: string;
};

function cloneRuntimeState(input: RuntimeAppLockState): RuntimeAppLockState {
  return {
    ...input,
  };
}

export function createAppLockBridgeHarness(options: HarnessOptions) {
  let runtimeState = cloneRuntimeState(options.runtimeState);
  let nextVersion = runtimeState.version + 1;
  const unlockPassword = options.unlockPassword ?? "secret";
  const runtimeListeners = new Set<(state: RuntimeAppLockState) => void>();
  const reopenListeners = new Set<() => void>();
  const rendererReadyCalls: number[] = [];
  const unlockAttempts: string[] = [];
  const activityReports: number[] = [];
  let runtimeFetchCount = 0;

  const emitRuntimeState = () => {
    const snapshot = cloneRuntimeState(runtimeState);
    for (const listener of runtimeListeners) {
      listener(snapshot);
    }
  };

  const setRuntimeState = (nextState: Partial<RuntimeAppLockState>, { notify = true } = {}) => {
    runtimeState = {
      ...runtimeState,
      ...nextState,
      version: nextVersion++,
    };
    if (notify) emitRuntimeState();
  };

  const bridge: NetcattyBridge = {
    getAppLockRuntimeState: async () => {
      runtimeFetchCount += 1;
      return cloneRuntimeState(runtimeState);
    },
    onAppLockRuntimeStateChanged: (listener) => {
      runtimeListeners.add(listener);
      return () => runtimeListeners.delete(listener);
    },
    requestAppLockUnlock: async (password) => {
      unlockAttempts.push(password);
      if (!password) return { ok: false, error: "empty" } satisfies UnlockResult;
      if (password !== unlockPassword) return { ok: false, error: "incorrect" } satisfies UnlockResult;
      setRuntimeState({
        initialized: true,
        locked: false,
        reason: null,
        lastUnlockedAt: Date.now(),
        lastActivityAt: Date.now(),
      });
      return { ok: true } satisfies UnlockResult;
    },
    setAppLockRuntimeLocked: async (reason) => {
      setRuntimeState({
        initialized: true,
        locked: true,
        reason,
        lastLockedAt: Date.now(),
      });
      return cloneRuntimeState(runtimeState);
    },
    reportAppLockActivity: async () => {
      activityReports.push(Date.now());
      setRuntimeState({
        lastActivityAt: Date.now(),
      }, { notify: false });
      return cloneRuntimeState(runtimeState);
    },
    onAppLockReopen: (listener) => {
      reopenListeners.add(listener);
      return () => reopenListeners.delete(listener);
    },
    rendererReady: () => {
      rendererReadyCalls.push(Date.now());
    },
  };

  return {
    bridge,
    getRuntimeState() {
      return cloneRuntimeState(runtimeState);
    },
    getRuntimeFetchCount() {
      return runtimeFetchCount;
    },
    setRuntimeState,
    emitReopen() {
      for (const listener of reopenListeners) {
        listener();
      }
    },
    getUnlockAttempts() {
      return [...unlockAttempts];
    },
    getRendererReadyCallCount() {
      return rendererReadyCalls.length;
    },
    getActivityReportCount() {
      return activityReports.length;
    },
  };
}
