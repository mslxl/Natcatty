import {
  DEFAULT_FONT_SIZE,
  MAX_FONT_SIZE,
  MIN_FONT_SIZE,
} from '../../infrastructure/config/fonts';

export type TerminalFontSizeMutationSource = 'local' | 'incoming';

export type TerminalFontSizeRecord = {
  fontSize: number;
  version: number;
};

export function clampTerminalFontSizeValue(fontSize: unknown): number {
  const value = typeof fontSize === 'number' ? fontSize : Number(fontSize);
  if (!Number.isFinite(value)) return DEFAULT_FONT_SIZE;
  return Math.max(MIN_FONT_SIZE, Math.min(MAX_FONT_SIZE, Math.round(value)));
}

/**
 * Parse persisted / IPC terminal-font-size payloads.
 * Accepts legacy plain numbers ("16") and versioned records.
 */
export function parseTerminalFontSizeRecord(raw: unknown): TerminalFontSizeRecord {
  if (typeof raw === 'number' || typeof raw === 'string') {
    const trimmed = typeof raw === 'string' ? raw.trim() : raw;
    if (typeof trimmed === 'string' && trimmed.startsWith('{')) {
      try {
        return parseTerminalFontSizeRecord(JSON.parse(trimmed));
      } catch {
        // fall through to Number()
      }
    }
    const asNumber = Number(trimmed);
    if (Number.isFinite(asNumber)) {
      return { fontSize: clampTerminalFontSizeValue(asNumber), version: 0 };
    }
  }

  if (raw && typeof raw === 'object') {
    const record = raw as { fontSize?: unknown; version?: unknown };
    const fontSize = clampTerminalFontSizeValue(record.fontSize);
    const version = Number(record.version);
    return {
      fontSize,
      version: Number.isFinite(version) && version > 0 ? Math.floor(version) : 0,
    };
  }

  return { fontSize: DEFAULT_FONT_SIZE, version: 0 };
}

export function serializeTerminalFontSizeRecord(record: TerminalFontSizeRecord): string {
  return JSON.stringify({
    fontSize: clampTerminalFontSizeValue(record.fontSize),
    version: Math.max(0, Math.floor(record.version) || 0),
  });
}

/**
 * Incoming peer updates must not clobber a newer local revision.
 * Equal versions are treated as already-applied (no state thrash).
 */
export function shouldApplyTerminalFontSizeRecord(
  current: TerminalFontSizeRecord,
  incoming: TerminalFontSizeRecord,
): boolean {
  if (incoming.version > current.version) return true;
  if (incoming.version < current.version) return false;
  // Same version: only apply when the size itself differs and both are
  // legacy/unversioned (version 0), so first-load plain strings still sync.
  if (incoming.version === 0 && current.version === 0) {
    return incoming.fontSize !== current.fontSize;
  }
  return false;
}

/**
 * Decide whether a terminal-font-size state change should be rebroadcast to
 * peer windows. Incoming IPC/storage updates must not notify again —
 * otherwise +/- clicks in the Settings window ping-pong with the main window
 * and terminals oscillate between the last two sizes (see #2689, same class
 * as window-opacity #2018).
 */
export function shouldBroadcastTerminalFontSizeChange(
  mutationSource: TerminalFontSizeMutationSource,
  persistMounted: boolean,
): { shouldBroadcast: boolean; nextSource: TerminalFontSizeMutationSource } {
  if (mutationSource === 'incoming') {
    return { shouldBroadcast: false, nextSource: 'local' };
  }
  return { shouldBroadcast: persistMounted, nextSource: 'local' };
}
