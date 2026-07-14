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
 * Focus styles for vault host/group cards.
 * Unfocused: no extra border/bg (caller keeps hover only).
 * Focused: accent border only — no fill, no ring wash.
 */
export function hostCardFocusClassName(
  viewMode: 'grid' | 'list' | 'tree',
  isFocused: boolean,
): string {
  if (!isFocused) return '';
  // Grid soft-card already draws a border; force accent color (beat .soft-card).
  if (viewMode === 'grid') {
    return '!border-primary';
  }
  // List/tree: no chrome until selected — add accent border only.
  return 'border border-primary';
}
