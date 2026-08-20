export const TERMINAL_PANE_FOCUS_TRANSITION_MS = 160;

export function getTerminalPaneFocusTransition(
  inActiveWorkspace: boolean,
  isFocusTarget: boolean,
): string | undefined {
  if (!inActiveWorkspace || !isFocusTarget) return undefined;
  const timing = `${TERMINAL_PANE_FOCUS_TRANSITION_MS}ms ease`;
  return `left ${timing}, top ${timing}, width ${timing}, height ${timing}`;
}
