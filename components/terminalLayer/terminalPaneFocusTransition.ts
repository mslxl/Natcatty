export const TERMINAL_PANE_FOCUS_TRANSITION_MS = 160;

export function getTerminalPaneFocusTransition({
  inActiveWorkspace,
  isFocusTarget,
  isFocusTransitionActive,
  isResizing,
}: {
  inActiveWorkspace: boolean;
  isFocusTarget: boolean;
  isFocusTransitionActive: boolean;
  isResizing: boolean;
}): string | undefined {
  if (!inActiveWorkspace || !isFocusTarget || !isFocusTransitionActive || isResizing) {
    return undefined;
  }
  const timing = `${TERMINAL_PANE_FOCUS_TRANSITION_MS}ms ease`;
  return `left ${timing}, top ${timing}, width ${timing}, height ${timing}`;
}
