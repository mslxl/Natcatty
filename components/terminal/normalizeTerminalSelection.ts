/**
 * Normalize an xterm selection into clipboard-ready logical text.
 *
 * xterm's getSelection() already joins soft-wrapped rows (isWrapped) and trims
 * *empty* buffer cells, but TUI apps often pad rows with real space characters.
 * Those written spaces survive copy and corrupt pasted paragraphs/code blocks.
 *
 * This helper rebuilds the selection from buffer coordinates so we can:
 * - strip written trailing padding on each physical row
 * - join only rows marked soft-wrapped by xterm
 * - keep genuine hard line breaks
 * - preserve rectangular (column) selections
 * - convert non-breaking spaces like xterm's selectionText path
 */

export type SelectionBufferLine = {
  isWrapped?: boolean;
  length: number;
  /**
   * xterm semantics: trimRight only drops empty cells (getTrimmedLength),
   * not written ASCII spaces used as display padding.
   */
  translateToString(trimRight?: boolean, startColumn?: number, endColumn?: number): string;
};

export type SelectionBuffer = {
  getLine(y: number): SelectionBufferLine | undefined;
};

export type SelectionPosition = {
  start: { x: number; y: number };
  end: { x: number; y: number };
};

export type SelectionTerminal = {
  getSelection?: () => string;
  getSelectionPosition?: () => SelectionPosition | undefined | null;
  buffer: {
    active: SelectionBuffer;
  };
  /** Present on real xterm Terminal instances; used only to detect column select. */
  _core?: {
    _selectionService?: {
      /** xterm SelectionMode: NORMAL=0, WORD=1, LINE=2, COLUMN=3 */
      _activeSelectionMode?: number;
    };
  };
};

/** Matches xterm SelectionMode.COLUMN */
const SELECTION_MODE_COLUMN = 3;
const ALL_NON_BREAKING_SPACE_REGEX = /\u00a0/g;

/**
 * Return clipboard-ready text for the current terminal selection.
 * Falls back to term.getSelection() when position/buffer APIs are unavailable.
 */
export function getNormalizedTerminalSelection(term: SelectionTerminal): string {
  const range = term.getSelectionPosition?.() ?? null;
  if (!range) {
    return normalizeClipboardText(term.getSelection?.() ?? "");
  }

  const { start, end } = normalizeSelectionRange(range);
  if (end.y < start.y) {
    return "";
  }

  if (isColumnSelectionMode(term)) {
    return buildColumnSelection(term.buffer.active, start, end);
  }

  return buildLinearSelection(term.buffer.active, start, end);
}

function isColumnSelectionMode(term: SelectionTerminal): boolean {
  return term._core?._selectionService?._activeSelectionMode === SELECTION_MODE_COLUMN;
}

function buildColumnSelection(
  buffer: SelectionBuffer,
  start: { x: number; y: number },
  end: { x: number; y: number },
): string {
  if (start.x === end.x) {
    return "";
  }

  const startCol = Math.min(start.x, end.x);
  const endCol = Math.max(start.x, end.x);
  const rows: string[] = [];

  for (let y = start.y; y <= end.y; y += 1) {
    const line = buffer.getLine(y);
    if (!line) {
      rows.push("");
      continue;
    }
    rows.push(trimWrittenPadding(line.translateToString(true, startCol, endCol)));
  }

  return normalizeClipboardText(rows.join("\n"));
}

function buildLinearSelection(
  buffer: SelectionBuffer,
  start: { x: number; y: number },
  end: { x: number; y: number },
): string {
  const logicalLines: string[] = [];
  let current = "";

  for (let y = start.y; y <= end.y; y += 1) {
    const line = buffer.getLine(y);
    if (!line) {
      if (current.length > 0 || logicalLines.length > 0) {
        logicalLines.push(trimWrittenPadding(current));
        current = "";
      }
      continue;
    }

    const startCol = y === start.y ? start.x : 0;
    // Match xterm: on multi-row selections the first row runs to the line end
    // (undefined endCol → line length), not only to the selection's end.x.
    const endCol = y === end.y ? end.x : undefined;
    // Keep written trailing spaces on the raw row so soft-wrap joins can tell
    // word-boundary wraps ("hello " + "world") apart from mid-word wraps.
    const rowText =
      endCol === undefined
        ? line.translateToString(true, startCol)
        : line.translateToString(true, startCol, endCol);

    if (y === start.y) {
      current = rowText;
      continue;
    }

    if (line.isWrapped) {
      current = joinSoftWrappedRows(current, rowText);
      continue;
    }

    logicalLines.push(trimWrittenPadding(current));
    current = rowText;
  }

  logicalLines.push(trimWrittenPadding(current));
  return normalizeClipboardText(logicalLines.join("\n"));
}

/**
 * Join two physical rows that xterm marked as a soft wrap.
 *
 * - Mid-word wrap (no trailing whitespace on previous row): concatenate tightly.
 * - Word-boundary wrap or TUI padding (trailing whitespace): collapse boundary
 *   whitespace to a single separator space, except between CJK characters where
 *   a space must not be invented.
 */
export function joinSoftWrappedRows(previousRaw: string, nextRaw: string): string {
  const left = trimWrittenPadding(previousRaw);
  const hadTrailingWhitespace = left.length < previousRaw.length;

  if (!hadTrailingWhitespace) {
    return left + nextRaw;
  }

  if (!nextRaw) {
    return left;
  }

  if (/^\s/u.test(nextRaw)) {
    // Next row already carries leading whitespace; don't double-insert.
    return left + nextRaw;
  }

  if (left.length === 0) {
    return nextRaw;
  }

  if (endsWithCjk(left) && startsWithCjk(nextRaw)) {
    return left + nextRaw;
  }

  return `${left} ${nextRaw}`;
}

function endsWithCjk(text: string): boolean {
  if (!text) return false;
  return isCjkCodePoint(text.codePointAt(text.length - 1) ?? 0);
}

function startsWithCjk(text: string): boolean {
  if (!text) return false;
  return isCjkCodePoint(text.codePointAt(0) ?? 0);
}

function isCjkCodePoint(cp: number): boolean {
  return (
    (cp >= 0x3040 && cp <= 0x30ff) || // Hiragana / Katakana
    (cp >= 0x3400 && cp <= 0x9fff) || // CJK Unified Ideographs
    (cp >= 0xf900 && cp <= 0xfaff) || // CJK Compatibility Ideographs
    (cp >= 0xff66 && cp <= 0xff9d) // Half-width Katakana
  );
}

/**
 * Strip written display-padding spaces that survive xterm's empty-cell trim.
 * Only trailing whitespace is removed so intentional leading/internal spaces stay.
 */
export function trimWrittenPadding(text: string): string {
  return text.replace(/[ \t\f\v]+$/u, "");
}

function normalizeClipboardText(text: string): string {
  return text.replace(ALL_NON_BREAKING_SPACE_REGEX, " ");
}

function normalizeSelectionRange(range: SelectionPosition): SelectionPosition {
  const { start, end } = range;
  if (start.y < end.y || (start.y === end.y && start.x <= end.x)) {
    return {
      start: { x: Math.max(0, start.x), y: start.y },
      end: { x: Math.max(0, end.x), y: end.y },
    };
  }
  return {
    start: { x: Math.max(0, end.x), y: end.y },
    end: { x: Math.max(0, start.x), y: start.y },
  };
}
