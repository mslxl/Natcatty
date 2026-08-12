export const FOCUS_SIDEBAR_HOST_DRAG_TYPE = 'host-id';
export const FOCUS_SIDEBAR_SESSION_DRAG_TYPE = 'workspace-focus-session-id';

export type FocusSidebarDragKind = 'session-reorder' | 'host-append' | null;

export function dataTransferHasType(
  types: ArrayLike<string> | readonly string[],
  type: string,
): boolean {
  return Array.from(types as ArrayLike<string>).includes(type);
}

/**
 * Decide how the focus-mode workspace sidebar should treat an in-progress drag.
 * Session reorder wins when this sidebar started the drag, so a host mime that
 * happens to be present cannot steal reorder gestures.
 */
export function resolveFocusSidebarDragKind(input: {
  types: ArrayLike<string> | readonly string[];
  activeSessionDragId?: string | null;
}): FocusSidebarDragKind {
  if (
    input.activeSessionDragId
    || dataTransferHasType(input.types, FOCUS_SIDEBAR_SESSION_DRAG_TYPE)
  ) {
    return 'session-reorder';
  }
  if (dataTransferHasType(input.types, FOCUS_SIDEBAR_HOST_DRAG_TYPE)) {
    return 'host-append';
  }
  return null;
}

export function readHostIdFromDataTransfer(
  getData: (type: string) => string,
): string | null {
  const hostId = getData(FOCUS_SIDEBAR_HOST_DRAG_TYPE)?.trim();
  return hostId || null;
}
