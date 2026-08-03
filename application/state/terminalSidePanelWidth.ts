export const TERMINAL_SIDE_PANEL_MIN_WIDTH = 280;
export const TERMINAL_SIDE_PANEL_MAX_WIDTH = 1200;
export const TERMINAL_SIDE_PANEL_MIN_TERMINAL_WIDTH = 320;
export const TERMINAL_SIDE_PANEL_TOOL_BUTTON_WIDTH = 28;
export const TERMINAL_SIDE_PANEL_TOOLBAR_RESERVED_WIDTH = 200;
export const TERMINAL_SIDE_PANEL_VIEWPORT_MAX_WIDTH = `max(${TERMINAL_SIDE_PANEL_MIN_WIDTH}px, min(${TERMINAL_SIDE_PANEL_MAX_WIDTH}px, calc(100vw - ${TERMINAL_SIDE_PANEL_MIN_TERMINAL_WIDTH}px)))`;

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

export function getTerminalSidePanelMaxShownTools(panelWidth: number): number {
  return Math.max(
    1,
    Math.floor(
      (panelWidth - TERMINAL_SIDE_PANEL_TOOLBAR_RESERVED_WIDTH)
      / TERMINAL_SIDE_PANEL_TOOL_BUTTON_WIDTH,
    ),
  );
}
