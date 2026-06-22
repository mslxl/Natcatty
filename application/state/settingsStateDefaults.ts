import type { HotkeyScheme, SessionLogFormat, TerminalSettings } from '../../domain/models';
import { STORAGE_KEY_TERM_FONT_FAMILY } from '../../infrastructure/config/storageKeys';
import { isDeprecatedPrimaryFontId } from '../../infrastructure/config/fonts';
import { DARK_UI_THEMES, LIGHT_UI_THEMES, type UiThemeTokens } from '../../infrastructure/config/uiThemes';
import { UI_FONTS } from '../../infrastructure/config/uiFonts';
import { uiFontStore } from './uiFontStore';
import { localStorageAdapter } from '../../infrastructure/persistence/localStorageAdapter';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';

export const DEFAULT_THEME: 'light' | 'dark' | 'system' = 'dark';
export const DEFAULT_WINDOW_OPACITY = 1;
export function clampWindowOpacity(opacity: unknown): number {
  const value = Number(opacity);
  if (!Number.isFinite(value)) return DEFAULT_WINDOW_OPACITY;
  return Math.min(1, Math.max(0.5, value));
}

/** Resolve the current OS color scheme preference. */
export const getSystemPreference = (): 'light' | 'dark' =>
  typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches
    ? 'dark'
    : 'light';
export const DEFAULT_LIGHT_UI_THEME = 'snow';
export const DEFAULT_DARK_UI_THEME = 'midnight';
export const DEFAULT_ACCENT_MODE: 'theme' | 'custom' = 'theme';
export const DEFAULT_CUSTOM_ACCENT = '221.2 83.2% 53.3%';
export const DEFAULT_TERMINAL_THEME = 'netcatty-dark';
export const DEFAULT_FONT_FAMILY = 'menlo';

/**
 * Migrate any terminal font id arriving from storage / IPC / sync to a
 * safe value. If `raw` is a deprecated proportional id (pingfang-sc,
 * microsoft-yahei, comic-sans-ms), persist the rewrite back to
 * localStorage so subsequent ingest paths and cloud-sync uploads stop
 * carrying it. Used by every place that reads STORAGE_KEY_TERM_FONT_FAMILY
 * — initial useState init, rehydrateAllFromStorage, IPC notifySettings
 * change listener, and cross-window storage event listener — so a
 * single point of truth keeps deprecated ids from re-entering state.
 *
 * Returns null when there's nothing to apply (raw is empty); callers
 * fall back to DEFAULT_FONT_FAMILY in that case.
 */
export function migrateIncomingTerminalFontId(raw: string | null | undefined): string | null {
  if (!raw) return null;
  if (isDeprecatedPrimaryFontId(raw)) {
    localStorageAdapter.writeString(STORAGE_KEY_TERM_FONT_FAMILY, DEFAULT_FONT_FAMILY);
    return DEFAULT_FONT_FAMILY;
  }
  return raw;
}
// Auto-detect default hotkey scheme based on platform
export const DEFAULT_HOTKEY_SCHEME: HotkeyScheme =
  typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/i.test(navigator.platform)
    ? 'mac'
    : 'pc';
export const DEFAULT_SFTP_DOUBLE_CLICK_BEHAVIOR: 'open' | 'transfer' = 'open';
export const DEFAULT_SFTP_AUTO_SYNC = false;
export const DEFAULT_SFTP_SHOW_HIDDEN_FILES = false;
export const DEFAULT_SFTP_USE_COMPRESSED_UPLOAD = true;
export const DEFAULT_SFTP_AUTO_OPEN_SIDEBAR = false;
export const DEFAULT_SFTP_FOLLOW_TERMINAL_CWD = false;
export const DEFAULT_SFTP_DEFAULT_VIEW_MODE: 'list' | 'tree' = 'list';
export const DEFAULT_SHOW_RECENT_HOSTS = true;
export const DEFAULT_SHOW_ONLY_UNGROUPED_HOSTS_IN_ROOT = false;
export const DEFAULT_SHOW_SFTP_TAB = true;
export const DEFAULT_SHOW_HOST_TREE_SIDEBAR = true;
export const DEFAULT_SHELL_ONLY_TAB_NUMBER_SHORTCUTS = false;
export const DEFAULT_DISABLE_TERMINAL_FONT_ZOOM = false;
export { DEFAULT_RESTORE_PREVIOUS_SESSION } from './sessionRestoreSettings';

// Editor defaults
export const DEFAULT_EDITOR_WORD_WRAP = false;

// Session Logs defaults
export const DEFAULT_SESSION_LOGS_ENABLED = false;
export const DEFAULT_SESSION_LOGS_FORMAT: SessionLogFormat = 'txt';
export const DEFAULT_SESSION_LOGS_TIMESTAMPS_ENABLED = false;
export const DEFAULT_SSH_DEBUG_LOGS_ENABLED = false;
export const DEFAULT_SSH_DEEP_LINK_ENABLED = true;

export const readStoredString = (key: string): string | null => {
  const raw = localStorageAdapter.readString(key);
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed) return null;

  try {
    const parsed = JSON.parse(trimmed);
    return typeof parsed === 'string' ? parsed : trimmed;
  } catch {
    return trimmed;
  }
};

export const isValidTheme = (value: unknown): value is 'light' | 'dark' | 'system' => value === 'light' || value === 'dark' || value === 'system';

export const isValidHslToken = (value: string): boolean => {
  // Expect: "<h> <s>% <l>%", e.g. "221.2 83.2% 53.3%"
  return /^\s*\d+(\.\d+)?\s+\d+(\.\d+)?%\s+\d+(\.\d+)?%\s*$/.test(value);
};

type ParsedHslToken = {
  hue: number;
  saturation: number;
  lightness: number;
};

const BLACK_HSL = '0 0% 0%';
const WHITE_HSL = '0 0% 100%';

const parseHslToken = (value: string): ParsedHslToken | null => {
  const match = /^\s*(\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)%\s+(\d+(?:\.\d+)?)%\s*$/.exec(value);
  if (!match) return null;
  const hue = Number(match[1]);
  const saturation = Number(match[2]);
  const lightness = Number(match[3]);
  if (![hue, saturation, lightness].every(Number.isFinite)) return null;
  return {
    hue: ((hue % 360) + 360) % 360,
    saturation: Math.min(100, Math.max(0, saturation)) / 100,
    lightness: Math.min(100, Math.max(0, lightness)) / 100,
  };
};

const hslToRgb = ({ hue, saturation, lightness }: ParsedHslToken): [number, number, number] => {
  if (saturation === 0) return [lightness, lightness, lightness];

  const chroma = (1 - Math.abs(2 * lightness - 1)) * saturation;
  const huePrime = hue / 60;
  const x = chroma * (1 - Math.abs((huePrime % 2) - 1));
  const [red, green, blue] =
    huePrime < 1 ? [chroma, x, 0] :
    huePrime < 2 ? [x, chroma, 0] :
    huePrime < 3 ? [0, chroma, x] :
    huePrime < 4 ? [0, x, chroma] :
    huePrime < 5 ? [x, 0, chroma] :
    [chroma, 0, x];
  const match = lightness - chroma / 2;
  return [red + match, green + match, blue + match];
};

const toLinearSrgb = (channel: number): number => (
  channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
);

export const getHslTokenRelativeLuminance = (value: string): number | null => {
  const parsed = parseHslToken(value);
  if (!parsed) return null;
  const [red, green, blue] = hslToRgb(parsed).map(toLinearSrgb);
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
};

export const getContrastRatio = (foregroundLuminance: number, backgroundLuminance: number): number => {
  const lighter = Math.max(foregroundLuminance, backgroundLuminance);
  const darker = Math.min(foregroundLuminance, backgroundLuminance);
  return (lighter + 0.05) / (darker + 0.05);
};

export const resolveReadableForegroundForHsl = (
  backgroundHsl: string,
  fallback: string = WHITE_HSL,
): string => {
  const backgroundLuminance = getHslTokenRelativeLuminance(backgroundHsl);
  if (backgroundLuminance == null) return fallback;

  const blackContrast = getContrastRatio(0, backgroundLuminance);
  const whiteContrast = getContrastRatio(1, backgroundLuminance);
  return whiteContrast >= blackContrast ? WHITE_HSL : BLACK_HSL;
};

export const resolveThemeAccentForeground = (
  tokens: UiThemeTokens,
  accentMode: 'theme' | 'custom',
  accentOverride: string,
): string => {
  const accentToken = accentMode === 'custom' ? accentOverride : tokens.accent;
  return resolveReadableForegroundForHsl(accentToken, tokens.primaryForeground);
};

export const isValidUiThemeId = (theme: 'light' | 'dark', value: string): boolean => {
  const list = theme === 'dark' ? DARK_UI_THEMES : LIGHT_UI_THEMES;
  return list.some((preset) => preset.id === value);
};

export const isValidUiFontId = (value: string): boolean => {
  // Local fonts are always considered valid
  if (value.startsWith('local-')) return true;
  // Check bundled fonts first, then check dynamically loaded fonts
  return UI_FONTS.some((font) => font.id === value) ||
    uiFontStore.getAvailableFonts().some((font) => font.id === value);
};

export const serializeTerminalSettings = (settings: TerminalSettings): string =>
  JSON.stringify(settings);

export const areTerminalSettingsEqual = (a: TerminalSettings, b: TerminalSettings): boolean =>
  serializeTerminalSettings(a) === serializeTerminalSettings(b);

export const createCustomKeyBindingsSyncOrigin = (): string => {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }

  return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
};

export const applyThemeTokens = (
  themeSource: 'light' | 'dark' | 'system',
  resolvedTheme: 'light' | 'dark',
  tokens: UiThemeTokens,
  accentMode: 'theme' | 'custom',
  accentOverride: string,
) => {
  const root = window.document.documentElement;
  root.classList.remove('light', 'dark');
  root.classList.add(resolvedTheme);
  root.style.setProperty('--background', tokens.background);
  root.style.setProperty('--foreground', tokens.foreground);
  root.style.setProperty('--card', tokens.card);
  root.style.setProperty('--card-foreground', tokens.cardForeground);
  root.style.setProperty('--popover', tokens.popover);
  root.style.setProperty('--popover-foreground', tokens.popoverForeground);
  const accentToken = accentMode === 'custom' ? accentOverride : tokens.accent;
  const computedAccentForeground = resolveThemeAccentForeground(tokens, accentMode, accentOverride);

  root.style.setProperty('--primary', accentToken);
  root.style.setProperty('--primary-foreground', computedAccentForeground);
  root.style.setProperty('--secondary', tokens.secondary);
  root.style.setProperty('--secondary-foreground', tokens.secondaryForeground);
  root.style.setProperty('--muted', tokens.muted);
  root.style.setProperty('--muted-foreground', tokens.mutedForeground);
  root.style.setProperty('--accent', accentToken);
  root.style.setProperty('--accent-foreground', computedAccentForeground);
  root.style.setProperty('--destructive', tokens.destructive);
  root.style.setProperty('--destructive-foreground', tokens.destructiveForeground);
  root.style.setProperty('--border', tokens.border);
  root.style.setProperty('--input', tokens.input);
  root.style.setProperty('--ring', accentToken);

  // Sync with native window title bar (Electron)
  netcattyBridge.get()?.setTheme?.(themeSource);
  netcattyBridge.get()?.setBackgroundColor?.(tokens.background);
};
