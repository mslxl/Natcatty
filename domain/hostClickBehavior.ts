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

/** Visual focus styles for vault host cards (grid/list). */
export function hostCardFocusClassName(
  viewMode: 'grid' | 'list' | 'tree',
  isFocused: boolean,
): string {
  if (!isFocused) return '';
  // Grid: accent edge ring (issue #2116)
  if (viewMode === 'grid') {
    return 'ring-2 ring-primary border-primary/70';
  }
  // List/other: match hover glass fill
  return 'bg-secondary/60';
}
