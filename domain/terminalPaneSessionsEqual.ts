import type { TerminalSession } from './models';

/**
 * Session fields that affect terminal pane React trees (xterm host, layout,
 * connection chrome). Presentation-only fields used by TopTabs (dynamicTitle,
 * codingCliProviderId) are intentionally ignored so title churn does not
 * invalidate TerminalLayer / TerminalPanesHost memoization.
 */
export type TerminalPaneSessionFields = Pick<
  TerminalSession,
  | 'id'
  | 'hostId'
  | 'workspaceId'
  | 'status'
  | 'protocol'
  | 'hostname'
  | 'username'
  | 'port'
  | 'moshEnabled'
  | 'etEnabled'
  | 'fontSize'
  | 'customName'
  | 'hostLabel'
  | 'hiddenFromTabs'
  | 'localShell'
  | 'localShellName'
>;

function paneFieldEqual(
  a: TerminalPaneSessionFields,
  b: TerminalPaneSessionFields,
): boolean {
  return a.id === b.id
    && a.hostId === b.hostId
    && a.workspaceId === b.workspaceId
    && a.status === b.status
    && a.protocol === b.protocol
    && a.hostname === b.hostname
    && a.username === b.username
    && (a.port ?? 22) === (b.port ?? 22)
    && Boolean(a.moshEnabled) === Boolean(b.moshEnabled)
    && Boolean(a.etEnabled) === Boolean(b.etEnabled)
    && a.fontSize === b.fontSize
    && a.customName === b.customName
    && a.hostLabel === b.hostLabel
    && Boolean(a.hiddenFromTabs) === Boolean(b.hiddenFromTabs)
    && a.localShell === b.localShell
    && a.localShellName === b.localShellName;
}

export function terminalPaneSessionsEqual(
  prev: ReadonlyArray<TerminalPaneSessionFields> | null | undefined,
  next: ReadonlyArray<TerminalPaneSessionFields> | null | undefined,
): boolean {
  if (prev === next) return true;
  if (!prev || !next) return false;
  if (prev.length !== next.length) return false;
  for (let i = 0; i < prev.length; i += 1) {
    const a = prev[i];
    const b = next[i];
    if (!a || !b) return false;
    if (a === b) continue;
    if (!paneFieldEqual(a, b)) return false;
  }
  return true;
}
