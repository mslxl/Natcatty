/**
 * Pure helpers for the terminal cursor-line highlight (WindTerm-style).
 * xterm decorations only accept opaque `#RRGGBB` backgrounds, so we blend
 * theme colors into a single solid hex.
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

const toHexChannel = (value: number): string =>
  Math.round(Math.max(0, Math.min(255, value)))
    .toString(16)
    .padStart(2, '0');

const mixRgb = (base: Rgb, overlay: Rgb, amount: number): Rgb => {
  const t = Math.max(0, Math.min(1, amount));
  return {
    r: base.r + (overlay.r - base.r) * t,
    g: base.g + (overlay.g - base.g) * t,
    b: base.b + (overlay.b - base.b) * t,
  };
};

const rgbToHex = ({ r, g, b }: Rgb): string =>
  `#${toHexChannel(r)}${toHexChannel(g)}${toHexChannel(b)}`;

/** Blend amount of selection/foreground into the terminal background. */
export const CURSOR_LINE_HIGHLIGHT_BLEND = 0.18;

/**
 * Resolve an opaque background color for the cursor line decoration.
 * Prefers selection over foreground so the highlight stays theme-aligned.
 */
export const resolveCursorLineHighlightBackground = (
  colors: CursorLineHighlightColors,
): string => {
  const background = parseHexRgb(colors.background) ?? { r: 13, g: 17, b: 23 };
  const overlay =
    parseHexRgb(colors.selection) ??
    parseHexRgb(colors.foreground) ??
    { r: 201, g: 209, b: 217 };
  return rgbToHex(mixRgb(background, overlay, CURSOR_LINE_HIGHLIGHT_BLEND));
};
