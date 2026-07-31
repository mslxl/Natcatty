/**
 * Pure helpers for the terminal cursor-line highlight (WindTerm-style).
 * The highlight is rendered as a translucent DOM overlay so existing ANSI
 * cell backgrounds remain visible underneath it.
 */

export type CursorLineHighlightColors = {
  background: string;
  foreground: string;
  selection: string;
};

const HEX_RGB_RE = /^#([0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/i;

type Rgb = { r: number; g: number; b: number };

const parseHexRgb = (value: string): Rgb | null => {
  const match = value.trim().match(HEX_RGB_RE);
  if (!match) return null;
  let hex = match[1];
  if (hex.length === 3) {
    hex = hex.split('').map((ch) => ch + ch).join('');
  } else if (hex.length === 8) {
    hex = hex.slice(0, 6);
  }
  const r = Number.parseInt(hex.slice(0, 2), 16);
  const g = Number.parseInt(hex.slice(2, 4), 16);
  const b = Number.parseInt(hex.slice(4, 6), 16);
  if (![r, g, b].every((channel) => Number.isFinite(channel))) return null;
  return { r, g, b };
};

/** Opacity of the selection/foreground overlay above the cursor row. */
export const CURSOR_LINE_HIGHLIGHT_OPACITY = 0.18;

/**
 * Resolve a translucent overlay color for the cursor line decoration.
 * Prefers selection over foreground so the highlight stays theme-aligned.
 */
export const resolveCursorLineHighlightOverlay = (
  colors: CursorLineHighlightColors,
): string => {
  const background = parseHexRgb(colors.background) ?? { r: 13, g: 17, b: 23 };
  const backgroundLuminance =
    background.r * 0.299 + background.g * 0.587 + background.b * 0.114;
  const overlay =
    parseHexRgb(colors.selection) ??
    parseHexRgb(colors.foreground) ??
    (backgroundLuminance >= 128
      ? { r: 0, g: 0, b: 0 }
      : { r: 255, g: 255, b: 255 });
  return `rgba(${overlay.r}, ${overlay.g}, ${overlay.b}, ${CURSOR_LINE_HIGHLIGHT_OPACITY})`;
};
