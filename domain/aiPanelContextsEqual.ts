import type { AITerminalSessionInfo } from '../components/terminalLayer/buildAITerminalSessionInfo';

/** Minimal AI panel context shape used for structural equality. */
export type AIPanelContextLike = {
  scopeType: 'terminal' | 'workspace';
  scopeTargetId?: string;
  scopeHostIds: string[];
  scopeLabel: string;
  terminalSessions: AITerminalSessionInfo[];
};

function hostChainEqual(
  a: AITerminalSessionInfo['hostChain'] | undefined,
  b: AITerminalSessionInfo['hostChain'] | undefined,
): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (
      a[i].hostId !== b[i].hostId
      || a[i].label !== b[i].label
      || a[i].hostname !== b[i].hostname
    ) {
      return false;
    }
  }
  return true;
}

function portForwardsEqual(
  a: AITerminalSessionInfo['activePortForwards'] | undefined,
  b: AITerminalSessionInfo['activePortForwards'] | undefined,
): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (
      a[i].ruleId !== b[i].ruleId
      || a[i].label !== b[i].label
      || a[i].type !== b[i].type
      || a[i].localPort !== b[i].localPort
      || a[i].status !== b[i].status
    ) {
      return false;
    }
  }
  return true;
}

function aiTerminalSessionInfoEqual(
  a: AITerminalSessionInfo,
  b: AITerminalSessionInfo,
): boolean {
  return a.sessionId === b.sessionId
    && a.hostId === b.hostId
    && a.hostname === b.hostname
    && a.label === b.label
    && a.os === b.os
    && a.username === b.username
    && a.protocol === b.protocol
    && a.shellType === b.shellType
    && a.deviceType === b.deviceType
    && a.connected === b.connected
    && hostChainEqual(a.hostChain, b.hostChain)
    && portForwardsEqual(a.activePortForwards, b.activePortForwards);
}

function scopeHostIdsEqual(a: string[], b: string[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

function aiPanelContextEqual(a: AIPanelContextLike, b: AIPanelContextLike): boolean {
  if (a === b) return true;
  if (a.scopeType !== b.scopeType) return false;
  if (a.scopeTargetId !== b.scopeTargetId) return false;
  if (a.scopeLabel !== b.scopeLabel) return false;
  if (!scopeHostIdsEqual(a.scopeHostIds, b.scopeHostIds)) return false;
  if (a.terminalSessions.length !== b.terminalSessions.length) return false;
  for (let i = 0; i < a.terminalSessions.length; i += 1) {
    if (!aiTerminalSessionInfoEqual(a.terminalSessions[i], b.terminalSessions[i])) {
      return false;
    }
  }
  return true;
}

/**
 * Structural equal for AI side-panel context maps. Ignores Map identity and
 * presentation-only terminal noise by comparing AI-relevant session/host fields.
 */
export function aiPanelContextsEqual(
  prev: Map<string, AIPanelContextLike> | null | undefined,
  next: Map<string, AIPanelContextLike> | null | undefined,
): boolean {
  if (prev === next) return true;
  if (!prev || !next) return false;
  if (prev.size !== next.size) return false;
  for (const [tabId, context] of prev) {
    const other = next.get(tabId);
    if (!other || !aiPanelContextEqual(context, other)) return false;
  }
  return true;
}

/**
 * Return `next` unless it is structurally equal to `previous`, in which case
 * keep the previous Map identity for React memo consumers.
 */
export function retainStableAiPanelContexts<T extends AIPanelContextLike>(
  previous: Map<string, T> | null | undefined,
  next: Map<string, T>,
): Map<string, T> {
  if (previous && aiPanelContextsEqual(previous, next)) {
    return previous;
  }
  return next;
}
