import type { Terminal as XTerm } from "@xterm/xterm";
import type { CompletionSuggestion } from "./completionEngine";
import type { SubDirPanel } from "./useTerminalAutocomplete";
import { getXTermCellDimensions } from "./xtermUtils";

export function resolveAutocompleteCwd(
  promptText: string,
  currentWord: string,
  fallbackCwd: string | undefined,
  os: "linux" | "windows" | "macos",
): string | undefined {
  if (os === "windows") return fallbackCwd;

  const normalizedWord = currentWord.trim().replace(/^['"]/, "");

  // Absolute or home-relative paths don't depend on cwd
  if (normalizedWord.startsWith("/") || normalizedWord.startsWith("~/")) {
    return fallbackCwd;
  }

  // For empty word (e.g. "cd ") and relative paths, try prompt-based cwd
  // extraction which reflects the current visible prompt — more up-to-date
  // than fallbackCwd when OSC 7 is not supported.
  const promptCwd = extractPosixCwdFromPrompt(promptText);
  return chooseAutocompleteCwd(promptCwd, fallbackCwd);
}

function chooseAutocompleteCwd(
  promptCwd: string | undefined,
  fallbackCwd: string | undefined,
): string | undefined {
  if (!promptCwd) return fallbackCwd;
  if (!fallbackCwd) return promptCwd;

  // Prompt cwd is extracted from the currently visible prompt, so it tracks
  // directory changes even when OSC 7 is not supported. Prefer it over
  // fallbackCwd (which may be stale from initial connection) whenever it
  // looks like a usable path.
  if (promptCwd.startsWith("/") || promptCwd === "~" || promptCwd.startsWith("~/")) {
    return promptCwd;
  }

  // Bare directory name (e.g. "xunlong") can't be used as a path — fallback
  return fallbackCwd;
}

function extractPosixCwdFromPrompt(promptText: string): string | undefined {
  const trimmed = promptText.trimEnd().replace(/[#$%>]\s*$/, "");
  if (!trimmed) return undefined;

  const patterns = [
    /:(\/[^\s\]]*|~(?:\/[^\s\]]*)?)$/,
    /\s(\/[^\s\]]*|~(?:\/[^\s\]]*)?)\]$/,
    /(^|[\s:])(\/[^\s\]]*|~(?:\/[^\s\]]*)?)$/,
  ];

  for (const pattern of patterns) {
    const match = trimmed.match(pattern);
    if (!match) continue;
    const candidate = match[match.length - 1];
    if (candidate === "/" || candidate.startsWith("/") || candidate === "~" || candidate.startsWith("~/")) {
      return candidate;
    }
  }

  const fallbackTokens = trimmed
    .split(/\s+/)
    .map((token) => token.replace(/^[([{:]+/, "").replace(/[\])}:]+$/, ""));

  for (let index = fallbackTokens.length - 1; index >= 0; index--) {
    const candidate = fallbackTokens[index];
    if (candidate === "/" || candidate.startsWith("/") || candidate === "~" || candidate.startsWith("~/")) {
      return candidate;
    }
  }

  return undefined;
}

export function areSuggestionsEqual(
  left: CompletionSuggestion[],
  right: CompletionSuggestion[],
): boolean {
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i++) {
    const a = left[i];
    const b = right[i];
    if (
      a.text !== b.text ||
      a.displayText !== b.displayText ||
      a.description !== b.description ||
      a.source !== b.source ||
      a.score !== b.score ||
      a.frequency !== b.frequency ||
      a.fileType !== b.fileType
    ) {
      return false;
    }
  }
  return true;
}

export function areSubDirPanelsEqual(left: SubDirPanel[], right: SubDirPanel[]): boolean {
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i++) {
    const a = left[i];
    const b = right[i];
    if (a.dirPath !== b.dirPath || a.selectedIndex !== b.selectedIndex) return false;
    if (a.entries.length !== b.entries.length) return false;
    for (let j = 0; j < a.entries.length; j++) {
      if (a.entries[j].name !== b.entries[j].name || a.entries[j].type !== b.entries[j].type) {
        return false;
      }
    }
  }
  return true;
}

export interface PopupPlacementInput {
  /** Anchor (current input line) top edge, in viewport coordinates. */
  anchorTop: number;
  /** Anchor (current input line) bottom edge, in viewport coordinates. */
  anchorBottom: number;
  /** Desired left edge (cursor column), in viewport coordinates. */
  anchorLeft: number;
  viewportWidth: number;
  viewportHeight: number;
  /** Natural height the popup wants if unconstrained (main list or detail). */
  desiredHeight: number;
  /**
   * Total horizontal extent of the popup including any cascading sub-directory
   * panels and the detail tooltip — used so the whole assembly is clamped
   * inside the viewport, not just the main list.
   */
  totalWidth: number;
  /** Hard cap on rendered height (matches the list's maxHeight prop). */
  maxHeight: number;
  /** Gap between the anchor line and the popup. */
  anchorGap: number;
  /** Minimum distance to keep from the viewport edges. */
  viewportPadding: number;
  /**
   * Direction hint from the cursor-cell based calculation. Only used to break
   * ties when neither side can fully fit the desired height.
   */
  expandUpwardHint: boolean;
}

export interface PopupPlacement {
  /** Whether the popup renders above the anchor line (flipped up). */
  renderUpward: boolean;
  /** Final top edge, in viewport coordinates (already clamped). */
  top: number;
  /** Final left edge, in viewport coordinates (already clamped). */
  left: number;
  /** Height budget for the rendered content (drives scrolling). */
  maxHeight: number;
}

/**
 * Decide where to place the autocomplete popup so it never spills past the
 * viewport edges. Pure and deterministic so the boundary math is unit-tested
 * independently of React/DOM.
 *
 * Vertical: prefer downward, but flip upward when the space below the input
 * line can't fit the desired height and the space above is a better fit. The
 * height is then clamped to whatever the chosen side actually offers so the
 * list scrolls instead of overflowing.
 *
 * Horizontal: clamp the left edge using the popup's *total* width (main list +
 * cascading sub-dir panels + detail tooltip), not just the main list, so wide
 * assemblies near the right edge slide left instead of overflowing. When the
 * assembly is wider than the viewport it pins to the left padding so the
 * primary list stays visible.
 */
export function computeAutocompletePopupPlacement(
  input: PopupPlacementInput,
): PopupPlacement {
  const {
    anchorTop,
    anchorBottom,
    anchorLeft,
    viewportWidth,
    viewportHeight,
    desiredHeight,
    totalWidth,
    maxHeight,
    anchorGap,
    viewportPadding,
    expandUpwardHint,
  } = input;

  const cappedDesiredHeight = Math.min(maxHeight, Math.max(0, desiredHeight));
  const spaceAbove = Math.max(0, anchorTop - viewportPadding - anchorGap);
  const spaceBelow = Math.max(0, viewportHeight - anchorBottom - viewportPadding - anchorGap);
  const canFullyRenderAbove = spaceAbove >= cappedDesiredHeight;
  const canFullyRenderBelow = spaceBelow >= cappedDesiredHeight;
  const renderUpward = canFullyRenderBelow
    ? false
    : canFullyRenderAbove
      ? true
      : expandUpwardHint
        ? spaceAbove >= Math.min(spaceBelow, 80)
        : spaceAbove > spaceBelow;

  const availableVerticalSpace = renderUpward ? spaceAbove : spaceBelow;
  const effectiveMaxHeight = Math.max(0, Math.min(maxHeight, availableVerticalSpace));
  const contentHeightForPlacement = Math.min(effectiveMaxHeight, cappedDesiredHeight);
  const top = renderUpward
    ? Math.max(viewportPadding, anchorTop - anchorGap - contentHeightForPlacement)
    : Math.min(
        anchorBottom + anchorGap,
        viewportHeight - viewportPadding - contentHeightForPlacement,
      );

  // Right edge that keeps the whole assembly inside the viewport. When the
  // assembly is wider than the available room this goes below viewportPadding,
  // so the final clamp pins the popup to the left padding (primary list wins).
  const maxLeft = viewportWidth - viewportPadding - Math.max(0, totalWidth);
  const left = Math.max(viewportPadding, Math.min(anchorLeft, maxLeft));

  return { renderUpward, top, left, maxHeight: effectiveMaxHeight };
}

/**
 * Calculate popup position based on terminal cursor.
 */
export function calculatePopupPosition(
  term: XTerm,
  itemCount: number,
): {
  position: { x: number; y: number };
  cursorLineTop: number;
  cursorLineBottom: number;
  expandUpward: boolean;
} {
  const termElement = term.element;
  if (!termElement) {
    return {
      position: { x: 0, y: 0 },
      cursorLineTop: 0,
      cursorLineBottom: 0,
      expandUpward: false,
    };
  }

  const dims = getXTermCellDimensions(term);
  const buffer = term.buffer.active;
  const cursorX = buffer.cursorX;
  const cursorY = buffer.cursorY;
  const cursorLineTop = cursorY * dims.height;
  const cursorLineBottom = (cursorY + 1) * dims.height;

  const estimatedPopupHeight = itemCount * 28 + 8;
  const totalRows = term.rows;
  const spaceBelow = (totalRows - cursorY - 1) * dims.height;
  const expandUpward = spaceBelow < estimatedPopupHeight && cursorY > 2;

  if (expandUpward) {
    return {
      position: { x: cursorX * dims.width, y: cursorY * dims.height },
      cursorLineTop,
      cursorLineBottom,
      expandUpward: true,
    };
  }

  return {
    position: { x: cursorX * dims.width, y: (cursorY + 1) * dims.height + 4 },
    cursorLineTop,
    cursorLineBottom,
    expandUpward: false,
  };
}
