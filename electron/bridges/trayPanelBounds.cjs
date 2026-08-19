/**
 * Tray panel size and placement.
 *
 * The panel is a fixed 360x520 overlay. Windows right-click often reports
 * stale or zero `tray.getBounds()` (y=0 while the icon is on a bottom
 * taskbar). Electron already passes live bounds on click / right-click;
 * those plus the cursor are the anchors we trust.
 */

const TRAY_PANEL_WIDTH = 360;
const TRAY_PANEL_HEIGHT = 520;
const TRAY_PANEL_GAP = 6;
const ANCHOR_CURSOR_SLOP_PX = 96;

function isFiniteNumber(value) {
  return typeof value === "number" && Number.isFinite(value);
}

function isValidRect(rect) {
  return Boolean(
    rect
    && isFiniteNumber(rect.x)
    && isFiniteNumber(rect.y)
    && isFiniteNumber(rect.width)
    && isFiniteNumber(rect.height)
    && rect.width > 0
    && rect.height > 0,
  );
}

function isValidPoint(point) {
  return Boolean(point && isFiniteNumber(point.x) && isFiniteNumber(point.y));
}

function rectNearPoint(rect, point, slop = ANCHOR_CURSOR_SLOP_PX) {
  if (!isValidRect(rect) || !isValidPoint(point)) return false;
  return point.x >= rect.x - slop
    && point.x <= rect.x + rect.width + slop
    && point.y >= rect.y - slop
    && point.y <= rect.y + rect.height + slop;
}

/**
 * Pick an anchor rectangle for the tray icon.
 * Prefer event bounds when they sit near the cursor; otherwise use the cursor
 * (Windows y=0 / zero-size getBounds). Last resort: work-area trailing edge.
 */
function resolveTrayAnchor(trayBounds, cursorPoint, workArea) {
  if (isValidRect(trayBounds) && (!isValidPoint(cursorPoint) || rectNearPoint(trayBounds, cursorPoint))) {
    return {
      x: trayBounds.x,
      y: trayBounds.y,
      width: trayBounds.width,
      height: trayBounds.height,
    };
  }

  if (isValidPoint(cursorPoint)) {
    return { x: cursorPoint.x, y: cursorPoint.y, width: 1, height: 1 };
  }

  const area = isValidRect(workArea) ? workArea : { x: 0, y: 0, width: 0, height: 0 };
  return {
    x: area.x + Math.max(area.width, 0),
    y: area.y + Math.max(area.height, 0),
    width: 1,
    height: 1,
  };
}

function clamp(value, min, max) {
  if (max < min) return min;
  return Math.min(Math.max(value, min), max);
}

/**
 * Place the designed panel next to the tray: below when there is room
 * (macOS menu bar), otherwise above (Windows / Linux bottom taskbar).
 */
function placeTrayPanel({
  anchor,
  workArea,
  width = TRAY_PANEL_WIDTH,
  height = TRAY_PANEL_HEIGHT,
  gap = TRAY_PANEL_GAP,
} = {}) {
  const area = isValidRect(workArea)
    ? workArea
    : { x: 0, y: 0, width: Math.max(width, 1), height: Math.max(height, 1) };
  const panelWidth = Math.min(Math.max(1, width), area.width || width);
  const panelHeight = Math.min(Math.max(1, height), area.height || height);
  const resolvedAnchor = isValidRect(anchor)
    ? anchor
    : { x: area.x, y: area.y, width: 1, height: 1 };

  const minX = area.x;
  const maxX = area.x + area.width - panelWidth;
  const x = Math.round(clamp(
    resolvedAnchor.x + resolvedAnchor.width / 2 - panelWidth / 2,
    minX,
    maxX,
  ));

  const minY = area.y;
  const maxY = area.y + area.height - panelHeight;
  const below = resolvedAnchor.y + resolvedAnchor.height + gap;
  const above = resolvedAnchor.y - panelHeight - gap;

  let y;
  if (below + panelHeight <= area.y + area.height) {
    y = below;
  } else if (above >= area.y) {
    y = above;
  } else {
    y = clamp(below, minY, maxY);
  }

  return {
    x,
    y: Math.round(clamp(y, minY, maxY)),
    width: panelWidth,
    height: panelHeight,
  };
}

module.exports = {
  TRAY_PANEL_WIDTH,
  TRAY_PANEL_HEIGHT,
  TRAY_PANEL_GAP,
  isValidRect,
  resolveTrayAnchor,
  placeTrayPanel,
};
