/**
 * Vault host/group click activation.
 *
 * - `connect` (default): single click immediately connects / opens
 * - `select`: first click focuses; click the focused item again to activate
 */
export type HostClickBehavior = 'connect' | 'select';

export const DEFAULT_HOST_CLICK_BEHAVIOR: HostClickBehavior = 'connect';

export function isHostClickBehavior(value: unknown): value is HostClickBehavior {
  return value === 'connect' || value === 'select';
}

export function resolveHostActivateAction(input: {
  behavior: HostClickBehavior;
  isMultiSelectMode: boolean;
  focusedHostId: string | null | undefined;
  hostId: string;
}): 'connect' | 'select' | 'toggle-multi' {
  if (input.isMultiSelectMode) return 'toggle-multi';
  if (input.behavior === 'connect') return 'connect';
  if (input.focusedHostId === input.hostId) return 'connect';
  return 'select';
}

export function resolveGroupActivateAction(input: {
  behavior: HostClickBehavior;
  focusedGroupPath: string | null | undefined;
  groupPath: string;
}): 'open' | 'select' {
  if (input.behavior === 'connect') return 'open';
  if (input.focusedGroupPath === input.groupPath) return 'open';
  return 'select';
}

/**
 * Visual focus styles for vault host/group cards.
 * Must read clearly vs hover: solid accent border + light primary wash.
 */
export function hostCardFocusClassName(
  viewMode: 'grid' | 'list' | 'tree',
  isFocused: boolean,
): string {
  if (!isFocused) return '';
  // Full accent edge (not /opacity border) so selection is obvious at a glance
  if (viewMode === 'grid') {
    return 'border-2 border-primary bg-primary/10 ring-2 ring-primary/40 shadow-[0_0_0_1px_hsl(var(--primary))]';
  }
  // List/tree rows: accent outline + wash (distinct from hover bg-secondary)
  return 'border border-primary bg-primary/10 ring-1 ring-primary/50';
}
