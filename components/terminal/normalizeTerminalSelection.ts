/**
 * Normalize an xterm selection into logical text for the clipboard.
 *
 * xterm's getSelection() joins every physical buffer row with a hard newline,
 * so soft-wrapped rows (isWrapped) become real line breaks and display-padding
 * spaces on each row can survive as trailing whitespace. This helper rebuilds
 * the selection from buffer coordinates: trim row padding, join soft wraps,
 * and preserve genuine hard line breaks.
 */

export type SelectionBufferLine = {
  isWrapped?: boolean;
  length: number;
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
};

/**
 * Return clipboard-ready text for the current terminal selection.
 * Falls back to term.getSelection() when position/buffer APIs are unavailable.
 */
export function getNormalizedTerminalSelection(term: SelectionTerminal): string {
  const range = term.getSelectionPosition?.() ?? null;
  if (!range) {
    return term.getSelection?.() ?? "";
  }

  const { start, end } = normalizeSelectionRange(range);
  if (end.y < start.y) {
    return "";
  }

  const buffer = term.buffer.active;
  const logicalLines: string[] = [];
  let current = "";

  for (let y = start.y; y <= end.y; y += 1) {
    const line = buffer.getLine(y);
    if (!line) {
      if (current.length > 0 || logicalLines.length > 0) {
        logicalLines.push(current);
        current = "";
      }
      continue;
    }

    const startCol = y === start.y ? start.x : 0;
    const endCol = y === end.y ? end.x : Math.max(line.length, startCol);
    // Always trim display-padding trailing spaces within the selected columns.
    // Soft-wrapped continuation rows (isWrapped) are concatenated without a
    // newline so visual wraps do not become hard breaks in the clipboard.
    const rowText = line.translateToString(true, startCol, endCol);

    if (y === start.y) {
      current = rowText;
      continue;
    }

    if (line.isWrapped) {
      current += rowText;
      continue;
    }

    logicalLines.push(current);
    current = rowText;
  }

  logicalLines.push(current);
  return logicalLines.join("\n");
}

function normalizeSelectionRange(range: SelectionPosition): SelectionPosition {
  const { start, end } = range;
  if (start.y < end.y || (start.y === end.y && start.x <= end.x)) {
    return {
      start: { x: Math.max(0, start.x), y: start.y },
      end: { x: Math.max(0, end.x), y: end.y },
    };
  }
  // Defensive: some selection APIs can return an inverted range.
  return {
    start: { x: Math.max(0, end.x), y: end.y },
    end: { x: Math.max(0, start.x), y: start.y },
  };
}
