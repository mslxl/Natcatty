export const CHAT_INPUT_MIN_HEIGHT = 112;
export const CHAT_INPUT_MAX_HEIGHT = 420;
export const CHAT_INPUT_DEFAULT_HEIGHT = 128;
export const CHAT_INPUT_PANEL_RESERVE = 112;

export function resolveChatInputMaxHeight(panelHeight: number): number {
  return Math.max(
    CHAT_INPUT_MIN_HEIGHT,
    Math.min(CHAT_INPUT_MAX_HEIGHT, panelHeight - CHAT_INPUT_PANEL_RESERVE),
  );
}

export function resolveChatInputResizeHeight(
  startHeight: number,
  startPointerY: number,
  pointerY: number,
  maxHeight: number,
): number {
  return Math.min(
    maxHeight,
    Math.max(CHAT_INPUT_MIN_HEIGHT, startHeight + startPointerY - pointerY),
  );
}
