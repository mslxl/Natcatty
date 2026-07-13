/**
 * Normalize an xterm selection into clipboard-ready logical text.
 *
 * xterm's getSelection() already joins soft-wrapped rows (isWrapped) and trims
 * *empty* buffer cells, but TUI apps often pad rows with real space characters.
 * Those written spaces survive copy and corrupt pasted paragraphs/code blocks.
 *
 * This helper rebuilds the selection from buffer coordinates so we can:
 * - strip written trailing padding on completed physical rows
 * - join only rows marked soft-wrapped by xterm
 * - keep genuine hard line breaks
 * - preserve rectangular (column) selections and partial end-column spaces
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
/** Characters that usually mean the next soft-wrapped row continues the same token. */
const TOKEN_CONTINUATION_END = new Set("\\/-_.:@?#&=+%".split(""));
const TOKEN_CONTINUATION_START = new Set("\\/-_.".split(""));

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
    // Keep right-edge spaces: the user selected those columns on purpose.
    rows.push(line.translateToString(true, startCol, endCol));
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
        logicalLines.push(trimCompletedRowPadding(current));
        current = "";
      }
      continue;
    }

    const startCol = y === start.y ? start.x : 0;
    // Match xterm: on multi-row selections the first row runs to the line end
    // (undefined endCol → line length), not only to the selection's end.x.
    const endCol = y === end.y ? end.x : undefined;
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

    logicalLines.push(trimCompletedRowPadding(current));
    current = rowText;
  }

  // Last logical segment: only strip padding when the selection ends at/after
  // the buffer line's content end (full-row copy). Partial end columns keep
  // explicitly selected trailing spaces.
  const lastLine = buffer.getLine(end.y);
  const selectionEndsAtRowEnd =
    !lastLine || end.x >= lastLine.length || end.x >= measureContentEnd(lastLine);
  logicalLines.push(
    selectionEndsAtRowEnd ? trimCompletedRowPadding(current) : current,
  );
  return normalizeClipboardText(logicalLines.join("\n"));
}

/**
 * Join two physical rows that xterm marked as a soft wrap.
 *
 * Trailing whitespace on the previous row may be a real word separator (one
 * space) or TUI display padding (often many spaces). Padding alone is not
 * treated as proof of a word boundary — token-like continuations (URLs, paths,
 * CJK) stay tight; only clear prose-style boundaries get a single space.
 */
export function joinSoftWrappedRows(previousRaw: string, nextRaw: string): string {
  const trailingWhitespace = countTrailingHorizontalWhitespace(previousRaw);
  const left = trailingWhitespace > 0 ? previousRaw.slice(0, -trailingWhitespace) : previousRaw;

  if (!nextRaw) {
    return left;
  }

  if (trailingWhitespace === 0) {
    return left + nextRaw;
  }

  if (/^\s/u.test(nextRaw)) {
    return left + nextRaw;
  }

  if (!left) {
    return nextRaw;
  }

  // A single trailing space is the common soft-wrap-at-word-boundary case.
  if (trailingWhitespace === 1) {
    return `${left} ${nextRaw}`;
  }

  // Multi-space runs are usually TUI padding. Do not invent a separator when
  // the next row clearly continues the same token (URL/path/CJK/punctuation).
  if (isTokenContinuation(left, nextRaw)) {
    return left + nextRaw;
  }

  // Prose-style padding wrap: collapse padding to one space.
  return `${left} ${nextRaw}`;
}

function isTokenContinuation(left: string, next: string): boolean {
  const leftEnd = lastCodePointChar(left);
  const nextStart = firstCodePointChar(next);
  if (!leftEnd || !nextStart) return false;

  if (isCjkCodePoint(leftEnd.codePointAt(0) ?? 0)) {
    // CJK runs and CJK punctuation should not gain Latin-style spaces.
    return isCjkCodePoint(nextStart.codePointAt(0) ?? 0) || isCjkPunctuation(nextStart);
  }

  if (TOKEN_CONTINUATION_END.has(leftEnd)) {
    return true;
  }

  if (TOKEN_CONTINUATION_START.has(nextStart) && isAsciiWordChar(leftEnd)) {
    return true;
  }

  return false;
}

function isAsciiWordChar(char: string): boolean {
  return /^[A-Za-z0-9]$/u.test(char);
}

function isCjkPunctuation(char: string): boolean {
  const cp = char.codePointAt(0) ?? 0;
  // Common fullwidth / CJK punctuation used after ideographs.
  return (
    (cp >= 0x3000 && cp <= 0x303f) ||
    (cp >= 0xff01 && cp <= 0xff60) ||
    char === "，" ||
    char === "。" ||
    char === "、" ||
    char === "：" ||
    char === "；" ||
    char === "！" ||
    char === "？"
  );
}

function firstCodePointChar(text: string): string {
  if (!text) return "";
  const cp = text.codePointAt(0);
  if (cp === undefined) return "";
  return String.fromCodePoint(cp);
}

function lastCodePointChar(text: string): string {
  if (!text) return "";
  const chars = Array.from(text);
  return chars[chars.length - 1] ?? "";
}

function isCjkCodePoint(cp: number): boolean {
  return (
    (cp >= 0x3040 && cp <= 0x30ff) || // Hiragana / Katakana
    (cp >= 0x3400 && cp <= 0x9fff) || // CJK Unified Ideographs
    (cp >= 0xf900 && cp <= 0xfaff) || // CJK Compatibility Ideographs
    (cp >= 0xff66 && cp <= 0xff9d) // Half-width Katakana
  );
}

function countTrailingHorizontalWhitespace(text: string): number {
  let count = 0;
  for (let i = text.length - 1; i >= 0; i -= 1) {
    const ch = text[i];
    if (ch === " " || ch === "\t" || ch === "\f" || ch === "\v") {
      count += 1;
      continue;
    }
    break;
  }
  return count;
}

/**
 * Strip written display-padding spaces that survive xterm's empty-cell trim.
 * Only trailing horizontal whitespace is removed.
 */
export function trimWrittenPadding(text: string): string {
  return text.replace(/[ \t\f\v]+$/u, "");
}

/** Alias used when a completed physical/hard row may still carry TUI padding. */
function trimCompletedRowPadding(text: string): string {
  return trimWrittenPadding(text);
}

function measureContentEnd(line: SelectionBufferLine): number {
  // Empty-cell trim length of the full line — selection ending here is "full row".
  return line.translateToString(true).length;
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
