import type { TerminalSession } from '../types';
import type { CodingCliProviderId } from './codingCliProviders';
import { normalizeCodingCliTitle } from './codingCliTitleParse';
import type { DynamicTabTitleMode } from './models/terminal';

/** Static connection label: user rename or host label. */
export const getSessionConnectionLabel = (session: Pick<TerminalSession, 'customName' | 'hostLabel'>): string => {
  return session.customName || session.hostLabel || '';
};

/**
 * Default title given to a freshly created split workspace. Used as a sentinel:
 * while the workspace still carries this title (i.e. the user has not renamed
 * it), the tab derives a label from its sessions instead of showing "Workspace".
 */
export const DEFAULT_WORKSPACE_TITLE = 'Workspace';

type WorkspaceTabLabelSession = Pick<
  TerminalSession,
  'id' | 'customName' | 'hostLabel' | 'dynamicTitle' | 'codingCliProviderId'
>;

/**
 * Resolve the label shown on a split-workspace tab. When the user has renamed
 * the workspace, that name wins. Otherwise derive it from the focused session's
 * host (e.g. "Localhost" for a local shell, the host config name for SSH) so the
 * tab reads as a concrete host instead of the generic "Workspace". If the
 * workspace holds sessions for more than one distinct host, the focused host is
 * shown with a "+N" suffix for the others.
 */
export const resolveWorkspaceTabLabel = (
  workspace: { title?: string; focusedSessionId?: string | null },
  sessions: readonly WorkspaceTabLabelSession[],
  dynamicTabTitleMode: DynamicTabTitleMode = 'agent',
): string => {
  const title = workspace.title?.trim();
  if (title && title !== DEFAULT_WORKSPACE_TITLE) {
    return title;
  }
  if (sessions.length === 0) {
    return title || DEFAULT_WORKSPACE_TITLE;
  }
  const focused = sessions.find((s) => s.id === workspace.focusedSessionId) ?? sessions[0];
  const primary = resolveSessionTabTitle(focused, dynamicTabTitleMode);
  const distinct = new Set(
    sessions.map((s) => resolveSessionTabTitle(s, dynamicTabTitleMode)).filter(Boolean),
  );
  if (distinct.size > 1) {
    return `${primary} +${distinct.size - 1}`;
  }
  return primary || title || DEFAULT_WORKSPACE_TITLE;
};

export const shouldUpdateCodingCliTabIcon = (
  dynamicTabTitleMode: DynamicTabTitleMode = 'agent',
): boolean => dynamicTabTitleMode !== 'off';

export const resolveCodingCliProviderIconUpdate = ({
  dynamicTabTitleMode,
  currentProviderId,
  nextProviderId,
}: {
  dynamicTabTitleMode: DynamicTabTitleMode;
  currentProviderId?: CodingCliProviderId;
  nextProviderId: CodingCliProviderId | null;
}): CodingCliProviderId | null | undefined => {
  if (!shouldUpdateCodingCliTabIcon(dynamicTabTitleMode)) return undefined;
  if ((currentProviderId ?? null) === nextProviderId) return undefined;
  return nextProviderId;
};

/**
 * Resolve the label shown on session tabs and pane headers.
 * Uses the shell-reported title according to the global dynamic title mode.
 */
export const resolveSessionTabTitle = (
  session: Pick<TerminalSession, 'customName' | 'hostLabel' | 'dynamicTitle' | 'codingCliProviderId'>,
  dynamicTabTitleMode: DynamicTabTitleMode = 'agent',
): string => {
  const connectionLabel = getSessionConnectionLabel(session);
  if (dynamicTabTitleMode === 'off') {
    return connectionLabel;
  }
  if (session.customName) {
    return session.customName;
  }
  if (dynamicTabTitleMode === 'agent' && !session.codingCliProviderId) {
    return connectionLabel;
  }
  const dynamicTitle = session.dynamicTitle?.trim();
  if (!dynamicTitle) {
    return connectionLabel;
  }
  return normalizeCodingCliTitle(dynamicTitle) || dynamicTitle;
};
