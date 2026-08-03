export const TERMINAL_SIDE_PANEL_MIN_WIDTH = 280;
export const TERMINAL_SIDE_PANEL_MAX_WIDTH = 1200;
export const TERMINAL_SIDE_PANEL_MIN_TERMINAL_WIDTH = 320;

export function getTerminalSidePanelMaxWidth(viewportWidth: number): number {
  const availableWidth = viewportWidth - TERMINAL_SIDE_PANEL_MIN_TERMINAL_WIDTH;
  return Math.max(
    TERMINAL_SIDE_PANEL_MIN_WIDTH,
    Math.min(TERMINAL_SIDE_PANEL_MAX_WIDTH, availableWidth),
  );
}

export function clampTerminalSidePanelWidth(width: number, viewportWidth: number): number {
  return Math.max(
    TERMINAL_SIDE_PANEL_MIN_WIDTH,
    Math.min(getTerminalSidePanelMaxWidth(viewportWidth), width),
  );
}
