import type { TerminalSession, Workspace } from "../../domain/models";
import {
  buildSessionRestorePayload,
  sanitizeSessionRestorePayload,
  type SessionRestorePayload,
} from "../../domain/sessionRestore";

export type InitialRestoredSessionState = {
  sessions: TerminalSession[];
  workspaces: Workspace[];
  tabOrder: string[];
  activeTabId: string;
};

export function createInitialRestoredSessionState({
  restoreEnabled,
  payload,
}: {
  restoreEnabled: boolean;
  payload: SessionRestorePayload | null;
}): InitialRestoredSessionState {
  if (!restoreEnabled || !payload) {
    return {
      sessions: [],
      workspaces: [],
      tabOrder: [],
      activeTabId: "vault",
    };
  }

  const sanitized = sanitizeSessionRestorePayload(payload);
  return {
    sessions: sanitized.sessions,
    workspaces: sanitized.workspaces,
    tabOrder: sanitized.tabOrder,
    activeTabId: sanitized.activeTabId,
  };
}

export function shouldPersistSessionRestoreState(
  sessions: readonly TerminalSession[],
  workspaces: readonly Workspace[],
  tabOrder: readonly string[],
): boolean {
  return sessions.length > 0 || workspaces.length > 0 || tabOrder.length > 0;
}

export function buildPersistableSessionRestorePayload({
  sessions,
  workspaces,
  tabOrder,
  activeTabId,
  now,
}: {
  sessions: TerminalSession[];
  workspaces: Workspace[];
  tabOrder: string[];
  activeTabId: string;
  now?: number;
}): SessionRestorePayload | null {
  if (!shouldPersistSessionRestoreState(sessions, workspaces, tabOrder)) return null;
  return buildSessionRestorePayload({
    sessions,
    workspaces,
    tabOrder,
    activeTabId,
    now,
  });
}

export function buildAndWriteSessionRestorePayload({
  restoreEnabled = true,
  clearOnEmpty = false,
  sessions,
  workspaces,
  tabOrder,
  activeTabId,
  now,
  storage,
}: {
  restoreEnabled?: boolean;
  clearOnEmpty?: boolean;
  sessions: TerminalSession[];
  workspaces: Workspace[];
  tabOrder: string[];
  activeTabId: string;
  now?: number;
  storage: {
    write(payload: SessionRestorePayload): boolean;
    clear(): void;
  };
}): boolean {
  if (!restoreEnabled) {
    storage.clear();
    return false;
  }
  const payload = buildPersistableSessionRestorePayload({
    sessions,
    workspaces,
    tabOrder,
    activeTabId,
    now,
  });
  if (!payload) {
    if (clearOnEmpty) {
      storage.clear();
    }
    return false;
  }
  return storage.write(payload);
}

/**
 * Patch only activeTabId on an already-persisted restore payload.
 * Used on top-tab switches so we avoid rebuilding/serializing every session
 * just because the active tab id changed.
 *
 * Returns:
 * - 'patched' when storage was written with a new activeTabId
 * - 'unchanged' when the cached/stored activeTabId already matches
 * - 'missing' when there is no base payload to patch (caller should full-write)
 */
export function patchSessionRestoreActiveTabId({
  activeTabId,
  now = Date.now(),
  cachedPayload,
  storage,
}: {
  activeTabId: string;
  now?: number;
  /** In-memory last full payload; preferred over storage.read() for tab clicks. */
  cachedPayload: SessionRestorePayload | null;
  storage: {
    read?(): SessionRestorePayload | null;
    write(payload: SessionRestorePayload): boolean;
  };
}): { status: 'patched' | 'unchanged' | 'missing'; payload: SessionRestorePayload | null } {
  const base = cachedPayload
    ?? (typeof storage.read === 'function' ? storage.read() : null);
  if (!base) {
    return { status: 'missing', payload: null };
  }
  if (base.activeTabId === activeTabId) {
    return { status: 'unchanged', payload: base };
  }
  const next: SessionRestorePayload = {
    ...base,
    activeTabId,
    savedAt: now,
  };
  storage.write(next);
  return { status: 'patched', payload: next };
}

export function mergeSessionRestoreCwd(
  payload: SessionRestorePayload,
  sessionId: string,
  cwd: string | null,
): SessionRestorePayload {
  return sanitizeSessionRestorePayload({
    ...payload,
    sessions: payload.sessions.map((session) => {
      if (session.id !== sessionId) return session;
      const { lastCwd: _lastCwd, ...rest } = session;
      return cwd ? { ...rest, lastCwd: cwd } : rest;
    }),
  });
}

export function updateRestoredSessionStatusState<T extends Pick<TerminalSession, "id" | "status" | "restoreState">>(
  sessions: readonly T[],
  sessionId: string,
  status: TerminalSession["status"],
): T[] {
  let changed = false;
  const next = sessions.map((session) => {
    if (session.id !== sessionId) return session;
    const shouldClearRestoreState = status === "connecting" || status === "connected";
    if (session.status === status && (!shouldClearRestoreState || session.restoreState === undefined)) {
      return session;
    }
    changed = true;
    if (!shouldClearRestoreState) return { ...session, status };
    const { restoreState: _restoreState, ...rest } = session;
    return { ...rest, status } as T;
  });
  return changed ? next : sessions as T[];
}
