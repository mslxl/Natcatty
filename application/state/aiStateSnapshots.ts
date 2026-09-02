import { localStorageAdapter } from '../../infrastructure/persistence/localStorageAdapter';
import {
  STORAGE_KEY_AI_ACTIVE_SESSION_MAP,
  STORAGE_KEY_AI_SESSIONS,
} from '../../infrastructure/config/storageKeys';
import type {
  AIDraft,
  AIPanelView,
  AISession,
  AIPermissionMode,
  AIToolIntegrationMode,
} from '../../infrastructure/ai/types';
import type { ProviderContinuationOptions } from '../../infrastructure/ai/providerContinuation';
import {
  bumpDraftMutationVersionState,
  bumpDraftUploadGenerationState,
  getDraftUploadGenerationState,
} from './aiDraftState';
import {
  pruneInactiveScopedSessions,
  pruneInactiveScopedTransientState,
} from './aiScopeCleanup';
import { emitAIStateChanged } from './aiStateEvents';
import { getAgentRuntime } from '../../infrastructure/ai/harness/globalAgentRuntime';

/** Typed accessor for the Electron IPC bridge exposed on `window.netcatty`. */
export interface AIBridge {
  aiSdkAgentCleanup?: (chatSessionId: string) => Promise<{ ok: boolean }>;
  deleteChatToolOutputsTemp?: (chatSessionId: string) => Promise<{ deletedCount: number }>;
  deleteTerminalToolOutputsEverywhereTemp?: (terminalSessionId: string) => Promise<{ deletedCount: number }>;
  aiMcpSetPermissionMode?: (mode: AIPermissionMode) => Promise<unknown> | unknown;
  aiMcpSetToolIntegrationMode?: (mode: AIToolIntegrationMode) => Promise<unknown> | unknown;
  aiMcpSetCommandBlocklist?: (blocklist: string[]) => Promise<unknown> | unknown;
  aiMcpSetCommandTimeout?: (timeout: number) => Promise<unknown> | unknown;
  aiMcpSetMaxIterations?: (maxIterations: number) => Promise<unknown> | unknown;
}

export function getAIBridge() {
  return (window as unknown as { netcatty?: AIBridge }).netcatty;
}


export const AI_STATE_CHANGED_DRAFTS_BY_SCOPE = 'netcatty:ai-drafts-by-scope';
export const AI_STATE_CHANGED_PANEL_VIEW_BY_SCOPE = 'netcatty:ai-panel-view-by-scope';

export type DraftsByScope = Partial<Record<string, AIDraft>>;
export type PanelViewByScope = Partial<Record<string, AIPanelView>>;

export function cleanupSdkAgentSessions(sessionIds: string[]) {
  const bridge = getAIBridge();
  if (sessionIds.length === 0) return;
  for (const sessionId of sessionIds) {
    void bridge?.aiSdkAgentCleanup?.(sessionId).catch(() => {});
  }
}

export function cleanupDeletedAIChatSessions(sessionIds: string[]) {
  const bridge = getAIBridge();
  if (sessionIds.length === 0) return;
  for (const sessionId of sessionIds) {
    getAgentRuntime().clearChatSession(sessionId);
    void bridge?.aiSdkAgentCleanup?.(sessionId).catch(() => {});
    void bridge?.deleteChatToolOutputsTemp?.(sessionId).catch(() => {});
  }
}

export function cleanupClosedTerminalSessions(terminalSessionIds: string[]) {
  const bridge = getAIBridge();
  for (const terminalSessionId of new Set(terminalSessionIds)) {
    getAgentRuntime().clearTerminalSession(terminalSessionId);
    void bridge?.deleteTerminalToolOutputsEverywhereTemp?.(terminalSessionId).catch(() => {});
  }
}

function isScopeKeyActive(scopeKey: string, activeTargetIds: Set<string>) {
  const separatorIndex = scopeKey.indexOf(':');
  if (separatorIndex === -1) return true;

  const targetId = scopeKey.slice(separatorIndex + 1);
  if (!targetId) return true;

  return activeTargetIds.has(targetId);
}

export function cleanupOrphanedAISessions(activeTargetIds: Set<string>) {
  const currentSessions = latestAISessionsSnapshot
    ?? localStorageAdapter.read<AISession[]>(STORAGE_KEY_AI_SESSIONS)
    ?? [];

  // Sessions shown by a still-live scope must be protected from cleanup
  // even when their own `scope.targetId` points at a closed terminal —
  // history can be resumed into a different terminal and we must not
  // delete it outright while it's actively being used.
  const preCleanupActiveSessionMap = latestAIActiveSessionMapSnapshot
    ?? localStorageAdapter.read<Record<string, string | null>>(STORAGE_KEY_AI_ACTIVE_SESSION_MAP)
    ?? {};
  const activeSessionIds = new Set<string>();
  for (const [scopeKey, sessionId] of Object.entries(preCleanupActiveSessionMap)) {
    if (!sessionId) continue;
    if (!isScopeKeyActive(scopeKey, activeTargetIds)) continue;
    activeSessionIds.add(sessionId);
  }

  const nextSessionCleanup = pruneInactiveScopedSessions(
    currentSessions,
    activeTargetIds,
    activeSessionIds,
  );

  if (nextSessionCleanup.orphanedSessionIds.length > 0) {
    cleanupSdkAgentSessions(nextSessionCleanup.orphanedSessionIds);
  }

  if (nextSessionCleanup.sessions !== currentSessions) {
    setLatestAISessionsSnapshot(nextSessionCleanup.sessions);
    writeSessionsForStorage(nextSessionCleanup.sessions);
    emitAIStateChanged(STORAGE_KEY_AI_SESSIONS);
  }

  const activeSessionIdMap = preCleanupActiveSessionMap;
  let activeSessionMapChanged = false;
  const nextActiveSessionIdMap = { ...activeSessionIdMap };

  for (const scopeKey of Object.keys(activeSessionIdMap)) {
    if (isScopeKeyActive(scopeKey, activeTargetIds)) continue;
    delete nextActiveSessionIdMap[scopeKey];
    activeSessionMapChanged = true;
  }

  if (activeSessionMapChanged) {
    setLatestAIActiveSessionMapSnapshot(nextActiveSessionIdMap);
    localStorageAdapter.write(STORAGE_KEY_AI_ACTIVE_SESSION_MAP, nextActiveSessionIdMap);
    emitAIStateChanged(STORAGE_KEY_AI_ACTIVE_SESSION_MAP);
  }

  const currentActiveSessionIdMap = activeSessionMapChanged
    ? nextActiveSessionIdMap
    : activeSessionIdMap;
  const currentDraftsByScope = latestAIDraftsByScopeSnapshot ?? {};
  const currentPanelViewByScope = latestAIPanelViewByScopeSnapshot ?? {};
  const prunedScopedTransientState = pruneInactiveScopedTransientState(
    currentActiveSessionIdMap,
    currentDraftsByScope,
    currentPanelViewByScope,
    activeTargetIds,
  );

  if (prunedScopedTransientState.activeSessionIdMap !== currentActiveSessionIdMap) {
    setLatestAIActiveSessionMapSnapshot(prunedScopedTransientState.activeSessionIdMap);
    localStorageAdapter.write(
      STORAGE_KEY_AI_ACTIVE_SESSION_MAP,
      prunedScopedTransientState.activeSessionIdMap,
    );
    emitAIStateChanged(STORAGE_KEY_AI_ACTIVE_SESSION_MAP);
  }

  if (prunedScopedTransientState.draftsByScope !== currentDraftsByScope) {
    for (const scopeKey of Object.keys(currentDraftsByScope)) {
      if (scopeKey in prunedScopedTransientState.draftsByScope) continue;
      bumpDraftMutationVersion(scopeKey);
      bumpDraftUploadGeneration(scopeKey);
    }
    setLatestAIDraftsByScopeSnapshot(prunedScopedTransientState.draftsByScope);
    emitAIStateChanged(AI_STATE_CHANGED_DRAFTS_BY_SCOPE);
  }

  if (prunedScopedTransientState.panelViewByScope !== currentPanelViewByScope) {
    for (const scopeKey of Object.keys(currentPanelViewByScope)) {
      if (scopeKey in prunedScopedTransientState.panelViewByScope) continue;
      bumpDraftMutationVersion(scopeKey);
    }
    setLatestAIPanelViewByScopeSnapshot(prunedScopedTransientState.panelViewByScope);
    emitAIStateChanged(AI_STATE_CHANGED_PANEL_VIEW_BY_SCOPE);
  }
}


/** Maximum number of sessions to keep in localStorage. */
const MAX_STORED_SESSIONS = 50;
/** Maximum number of messages per session when persisting to localStorage. */
const MAX_SESSION_MESSAGES = 200;
/**
 * Byte budget for the serialized sessions JSON. The localStorage quota is
 * ~5-10 MB across all keys, and Responses reasoning ciphertext can add tens
 * of KB per turn, so keep the sessions blob well under the quota with
 * headroom for the rest of the app's storage keys.
 */
const MAX_SESSIONS_JSON_BYTES = 2 * 1024 * 1024;
/** Retry budgets used when the primary budget still fails to persist. */
const RETRY_SESSIONS_JSON_BYTES = [1024 * 1024, 512 * 1024] as const;

/**
 * Remove `reasoningEncryptedContent` ciphertext from a message's persisted
 * continuation. The ciphertext exists so stateless Responses turns can replay
 * prior reasoning items; it is also by far the largest per-message payload.
 * When storage pressure forces it, dropping the ciphertext keeps the visible
 * conversation intact at the cost of reasoning replay for affected messages.
 */
function stripReasoningEncryptedContent(
  options: ProviderContinuationOptions,
): ProviderContinuationOptions | undefined {
  const hasCiphertext = Object.values(options).some(
    providerOptions => typeof providerOptions?.reasoningEncryptedContent === 'string',
  );
  if (!hasCiphertext) return options;
  const stripped: ProviderContinuationOptions = {};
  for (const [provider, providerOptions] of Object.entries(options)) {
    const rest = { ...providerOptions };
    delete rest.reasoningEncryptedContent;
    if (Object.keys(rest).length) stripped[provider] = rest;
  }
  return Object.keys(stripped).length ? stripped : undefined;
}

function stripEncryptedReasoningFromSessions(sessions: AISession[]): AISession[] {
  return sessions.map(session => {
    let changed = false;
    const messages = session.messages.map(message => {
      const continuation = message.providerContinuation;
      if (!continuation?.reasoningParts) return message;
      let partsChanged = false;
      const parts = continuation.reasoningParts.map(part => {
        if (!part.providerOptions) return part;
        const providerOptions = stripReasoningEncryptedContent(part.providerOptions);
        if (providerOptions === part.providerOptions) return part;
        partsChanged = true;
        return providerOptions ? { text: part.text, providerOptions } : { text: part.text };
      });
      if (!partsChanged) return message;
      changed = true;
      return {
        ...message,
        providerContinuation: {
          ...continuation,
          reasoningParts: parts,
        },
      };
    });
    return changed ? { ...session, messages } : session;
  });
}

/**
 * Prune sessions before writing to localStorage to prevent hitting the
 * ~5-10 MB storage quota. Only affects what is persisted — the in-memory
 * state retains all messages until the session is reloaded.
 *
 * - Keeps only the MAX_STORED_SESSIONS most-recently-updated sessions.
 * - Trims each session's messages to the last MAX_SESSION_MESSAGES.
 */
export function pruneSessionsForStorage(sessions: AISession[]): AISession[] {
  // Sort by updatedAt descending so we keep the newest
  const sorted = [...sessions].sort((a, b) => b.updatedAt - a.updatedAt);
  const limited = sorted.slice(0, MAX_STORED_SESSIONS);
  return limited.map(s => {
    if (s.messages.length > MAX_SESSION_MESSAGES) {
      return { ...s, messages: s.messages.slice(-MAX_SESSION_MESSAGES) };
    }
    return s;
  });
}

/**
 * Serialize sessions for localStorage under a byte budget, escalating pruning
 * as needed. Returns the JSON to persist plus the (possibly) further-pruned
 * sessions that JSON represents.
 */
export function serializeSessionsForStorage(
  sessions: AISession[],
  budgetBytes: number = MAX_SESSIONS_JSON_BYTES,
): { json: string; sessions: AISession[] } {
  let pruned = pruneSessionsForStorage(sessions);
  let json = JSON.stringify(pruned);
  // Escalation 1: drop the oldest sessions (already sorted by updatedAt desc)
  // until the payload fits. The active session is the newest, so its replay
  // context survives as long as possible.
  while (json.length > budgetBytes && pruned.length > 1) {
    pruned = pruned.slice(0, -1);
    json = JSON.stringify(pruned);
  }
  // Escalation 2: strip encrypted reasoning ciphertext, the largest payloads,
  // before letting a single oversized session fall through to memory-only.
  if (json.length > budgetBytes) {
    pruned = stripEncryptedReasoningFromSessions(pruned);
    json = JSON.stringify(pruned);
  }
  return { json, sessions: pruned };
}

/**
 * Persist sessions to localStorage with byte-budgeted pruning and retries.
 * Returns true when the write succeeded; a false result means the payload
 * could not be persisted even after escalation (it stays memory-only).
 */
export function writeSessionsForStorage(sessions: AISession[]): boolean {
  const { json } = serializeSessionsForStorage(sessions);
  if (localStorageAdapter.writeString(STORAGE_KEY_AI_SESSIONS, json)) return true;
  // Other keys may be consuming the shared quota: retry with progressively
  // tighter budgets so at least the most recent sessions survive a restart.
  for (const retryBudget of RETRY_SESSIONS_JSON_BYTES) {
    const retry = serializeSessionsForStorage(sessions, retryBudget);
    if (retry.json.length >= json.length) continue;
    if (localStorageAdapter.writeString(STORAGE_KEY_AI_SESSIONS, retry.json)) return true;
  }
  console.warn(
    '[AIState] Failed to persist AI sessions within the storage quota; recent chat history may not survive a restart.',
  );
  return false;
}

export let latestAISessionsSnapshot: AISession[] | null = null;
export let latestAIActiveSessionMapSnapshot: Record<string, string | null> | null = null;
export let latestAIDraftsByScopeSnapshot: DraftsByScope | null = null;
export let latestAIPanelViewByScopeSnapshot: PanelViewByScope | null = null;
let latestAIDraftMutationVersionByScopeSnapshot: Record<string, number> = {};
let latestAIDraftUploadGenerationByScopeSnapshot: Record<string, number> = {};

export function setLatestAISessionsSnapshot(sessions: AISession[]) {
  latestAISessionsSnapshot = sessions;
}

export function setLatestAIActiveSessionMapSnapshot(activeSessionIdMap: Record<string, string | null>) {
  latestAIActiveSessionMapSnapshot = activeSessionIdMap;
}

export function prewarmAIStateStorageSnapshots() {
  try {
    if (latestAISessionsSnapshot === null) {
      latestAISessionsSnapshot =
        localStorageAdapter.read<AISession[]>(STORAGE_KEY_AI_SESSIONS) ?? [];
    }
    if (latestAIActiveSessionMapSnapshot === null) {
      latestAIActiveSessionMapSnapshot =
        localStorageAdapter.read<Record<string, string | null>>(STORAGE_KEY_AI_ACTIVE_SESSION_MAP) ?? {};
    }
  } catch (error) {
    console.warn('[AIState] Failed to prewarm AI state storage snapshots:', error);
  }
}

export function setLatestAIDraftsByScopeSnapshot(draftsByScope: DraftsByScope) {
  latestAIDraftsByScopeSnapshot = draftsByScope;
}

export function setLatestAIPanelViewByScopeSnapshot(panelViewByScope: PanelViewByScope) {
  latestAIPanelViewByScopeSnapshot = panelViewByScope;
}

export function bumpDraftMutationVersion(scopeKey: string) {
  latestAIDraftMutationVersionByScopeSnapshot = bumpDraftMutationVersionState(
    latestAIDraftMutationVersionByScopeSnapshot,
    scopeKey,
  );
}

export function getDraftUploadGeneration(scopeKey: string) {
  return getDraftUploadGenerationState(
    latestAIDraftUploadGenerationByScopeSnapshot,
    scopeKey,
  );
}

export function bumpDraftUploadGeneration(scopeKey: string) {
  latestAIDraftUploadGenerationByScopeSnapshot = bumpDraftUploadGenerationState(
    latestAIDraftUploadGenerationByScopeSnapshot,
    scopeKey,
  );
}
