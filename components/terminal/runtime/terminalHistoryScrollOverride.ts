type WheelLike = Pick<
  WheelEvent,
  "altKey" | "ctrlKey" | "deltaMode" | "deltaY" | "metaKey" | "shiftKey"
>;

type KeyLike = Pick<
  KeyboardEvent,
  "altKey" | "ctrlKey" | "key" | "metaKey" | "shiftKey" | "type"
>;

type BufferLineLike = {
  translateToString(trimRight?: boolean): string;
};

type BufferLike = {
  baseY: number;
  length: number;
  type: "normal" | "alternate";
  viewportY: number;
  getLine(y: number): BufferLineLike | undefined;
};

const DOM_DELTA_LINE = 1;
const DOM_DELTA_PAGE = 2;
const DEFAULT_WHEEL_SCROLL_LINES = 3;
const PAGE_WHEEL_SCROLL_LINES = 24;

export const forcedHistoryScrollWheelListenerOptions = {
  passive: false,
  capture: true,
} as const satisfies AddEventListenerOptions;

const hasOnlyShiftModifier = (event: {
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
}): boolean => event.shiftKey && !event.altKey && !event.ctrlKey && !event.metaKey;

export const forcedHistoryScrollLinesForWheel = (event: WheelLike): number | null => {
  if (!hasOnlyShiftModifier(event) || event.deltaY === 0) return null;

  const direction = event.deltaY < 0 ? -1 : 1;
  if (event.deltaMode === DOM_DELTA_LINE) {
    return direction * Math.max(1, Math.round(Math.abs(event.deltaY)));
  }
  if (event.deltaMode === DOM_DELTA_PAGE) {
    return direction * PAGE_WHEEL_SCROLL_LINES;
  }
  return direction * DEFAULT_WHEEL_SCROLL_LINES;
};

export const forcedHistoryScrollPagesForKey = (event: KeyLike): number | null => {
  if (event.type !== "keydown" || !hasOnlyShiftModifier(event)) return null;

  if (event.key === "PageUp") return -1;
  if (event.key === "PageDown") return 1;
  return null;
};

export const forcedHistoryScrollPageToLines = (pageCount: number, rows: number): number =>
  pageCount * Math.max(1, rows - 1);

export const clampHistoryPreviewTop = (top: number, buffer: Pick<BufferLike, "baseY">): number => {
  const maxTop = Math.max(0, buffer.baseY);
  return Math.max(0, Math.min(maxTop, top));
};

export const nextHistoryPreviewTop = ({
  buffer,
  currentTop,
  lines,
}: {
  buffer: Pick<BufferLike, "baseY" | "viewportY">;
  currentTop: number | null;
  lines: number;
}): number => clampHistoryPreviewTop(
  clampHistoryPreviewTop(currentTop ?? buffer.viewportY ?? buffer.baseY, buffer) + lines,
  buffer,
);

export const getHistoryPreviewLines = ({
  buffer,
  rows,
  top,
}: {
  buffer: BufferLike;
  rows: number;
  top: number;
}): string[] => {
  const clampedTop = clampHistoryPreviewTop(top, buffer);
  const visibleRows = Math.max(1, rows);
  const lines: string[] = [];
  for (let row = 0; row < visibleRows; row += 1) {
    lines.push(buffer.getLine(clampedTop + row)?.translateToString(true) ?? "");
  }
  return lines;
};

export const HISTORY_PREVIEW_OVERLAY_ATTR = "data-terminal-history-preview";

const MODIFIER_ONLY_KEYS = new Set(["Shift", "Control", "Meta", "Alt"]);

export type HistoryPreviewSelectionLike = {
  rangeCount: number;
  isCollapsed?: boolean;
  anchorNode: { nodeType?: number } | null;
  focusNode: { nodeType?: number } | null;
  toString(): string;
};

export type HistoryPreviewNodeLike = {
  contains(node: { nodeType?: number } | null): boolean;
};

export const isHistoryPreviewPointerTarget = (
  target: EventTarget | null | undefined,
  overlay: EventTarget | null | undefined,
): boolean => {
  if (!target || !overlay) return false;
  if (target === overlay) return true;
  if (typeof (overlay as HistoryPreviewNodeLike).contains === "function") {
    return (overlay as HistoryPreviewNodeLike).contains(target as HistoryPreviewNodeLike);
  }
  return false;
};

export const shouldHideHistoryPreviewOnMouseDown = (
  target: EventTarget | null | undefined,
  overlay: EventTarget | null | undefined,
): boolean => Boolean(overlay) && !isHistoryPreviewPointerTarget(target, overlay);

export const isHistoryPreviewContextMenuTarget = (
  target: EventTarget | null | undefined,
): boolean => {
  if (!target || typeof target !== "object") return false;
  const element = target as { closest?: (selector: string) => Element | null };
  return Boolean(element.closest?.(`[${HISTORY_PREVIEW_OVERLAY_ATTR}]`));
};

export const shouldKeepHistoryPreviewOnKey = (
  event: KeyLike,
  options: {
    action?: string | null;
    hasPreviewSelection?: boolean;
    overlayVisible?: boolean;
  } = {},
): boolean => {
  if (forcedHistoryScrollPagesForKey(event) !== null) return true;
  if (MODIFIER_ONLY_KEYS.has(event.key)) return true;
  if (options.action === "copy" || options.action === "selectAll") return true;
  if (
    options.overlayVisible
    && event.key.toLowerCase() === "a"
    && (event.metaKey || event.ctrlKey)
    && !event.altKey
  ) {
    return true;
  }
  return Boolean(
    options.hasPreviewSelection
    && (event.metaKey || event.ctrlKey)
    && !event.altKey
    && event.key.toLowerCase() === "c",
  );
};

export const getHistoryPreviewSelectionText = (
  overlay: HistoryPreviewNodeLike | null | undefined,
  selection: HistoryPreviewSelectionLike | null | undefined,
): string => {
  if (!overlay || !selection || selection.rangeCount === 0 || selection.isCollapsed) {
    return "";
  }
  const { anchorNode, focusNode } = selection;
  if (!anchorNode || !focusNode) return "";
  if (!overlay.contains(anchorNode) || !overlay.contains(focusNode)) return "";
  return selection.toString();
};

export const findHistoryPreviewOverlay = (
  root: ParentNode | Element | null | undefined,
): HTMLElement | null => {
  if (!root || !("querySelector" in root)) return null;
  return root.querySelector<HTMLElement>(`[${HISTORY_PREVIEW_OVERLAY_ATTR}]`);
};

export const getHistoryPreviewSelectionFromRoot = (
  root: ParentNode | Element | null | undefined,
  selection?: HistoryPreviewSelectionLike | null,
): string => {
  const overlay = findHistoryPreviewOverlay(root);
  const activeSelection = selection ?? overlay?.ownerDocument.getSelection() ?? null;
  return getHistoryPreviewSelectionText(overlay, activeSelection);
};

export const selectHistoryPreviewAll = (overlay: HTMLElement | null | undefined): boolean => {
  if (!overlay) return false;
  const selection = overlay.ownerDocument.getSelection();
  if (!selection) return false;
  const range = overlay.ownerDocument.createRange();
  range.selectNodeContents(overlay);
  selection.removeAllRanges();
  selection.addRange(range);
  return !selection.isCollapsed;
};
