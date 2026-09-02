"use strict";

/**
 * Multi-monitor display recovery for content windows (#3244).
 *
 * On Windows, locking the session or letting the machine sleep can temporarily
 * tear down secondary displays (physical or virtual). When that happens the OS
 * relocates windows that lived on the missing display onto the primary
 * display, and the relocation is indistinguishable from a user move (it fires
 * regular "move" events, so it also pollutes the persisted window state).
 * When the display comes back after unlock, nothing puts the window back.
 *
 * This module keeps a light-weight memory of where the window was placed on a
 * non-primary display and, when that display returns, moves the window back if
 * it is currently somewhere else. It never fights the user: if the window
 * already intersects the re-added display, nothing happens.
 */

function isFiniteBounds(bounds) {
  return Boolean(
    bounds &&
    Number.isFinite(bounds.x) &&
    Number.isFinite(bounds.y) &&
    Number.isFinite(bounds.width) &&
    Number.isFinite(bounds.height) &&
    bounds.width > 0 &&
    bounds.height > 0
  );
}

function boundsIntersectDisplay(bounds, displayBounds) {
  if (!isFiniteBounds(bounds) || !isFiniteBounds(displayBounds)) return false;
  return (
    bounds.x < displayBounds.x + displayBounds.width &&
    bounds.x + bounds.width > displayBounds.x &&
    bounds.y < displayBounds.y + displayBounds.height &&
    bounds.y + bounds.height > displayBounds.y
  );
}

/**
 * Decide whether the window should be moved back onto a (re-)added display.
 * Returns the remembered bounds to restore, or null when the window is already
 * on that display or no remembered placement intersects it. Candidates are
 * evaluated in order, so more specific snapshots should come first.
 */
function pickDisplayRecoveryBounds({ addedDisplay, currentBounds, candidates }) {
  if (!addedDisplay || !isFiniteBounds(addedDisplay.bounds)) return null;
  if (!isFiniteBounds(currentBounds)) return null;
  // The window is already (at least partially) on this display: nothing to do.
  if (boundsIntersectDisplay(currentBounds, addedDisplay.bounds)) return null;
  for (const candidate of candidates || []) {
    if (boundsIntersectDisplay(candidate, addedDisplay.bounds)) {
      return candidate;
    }
  }
  return null;
}

/**
 * Clamp remembered bounds so the restored window stays fully visible on the
 * target display (size is capped at the display size, position is pulled
 * inside the display bounds).
 */
function clampBoundsToDisplay(bounds, displayBounds) {
  if (!isFiniteBounds(bounds) || !isFiniteBounds(displayBounds)) return null;
  const width = Math.min(bounds.width, displayBounds.width);
  const height = Math.min(bounds.height, displayBounds.height);
  const x = Math.min(
    Math.max(bounds.x, displayBounds.x),
    displayBounds.x + displayBounds.width - width
  );
  const y = Math.min(
    Math.max(bounds.y, displayBounds.y),
    displayBounds.y + displayBounds.height - height
  );
  return { x, y, width, height };
}

/**
 * Watch display add/remove events for one window and restore its placement on
 * a non-primary display when that display re-appears. Returns a detach()
 * function that removes every listener this function registered.
 */
function attachDisplayRecovery({ win, screen }) {
  if (!win || !screen || typeof screen.on !== "function") {
    return () => {};
  }

  let boundsAtDisplayRemoval = null;
  let rememberedSecondaryBounds = null;
  let attached = true;

  const isTrackable = () => {
    try {
      return !win.isDestroyed() && !win.isMaximized() && !win.isFullScreen();
    } catch {
      return false;
    }
  };

  const copyBounds = () => {
    try {
      const bounds = win.getBounds();
      return isFiniteBounds(bounds) ? { ...bounds } : null;
    } catch {
      return null;
    }
  };

  // Remember the window's placement while it lives on a non-primary display.
  const rememberWindowPlacement = () => {
    if (!attached || !isTrackable()) return;
    try {
      const bounds = copyBounds();
      if (!bounds) return;
      const primary = screen.getPrimaryDisplay?.();
      const display = screen.getDisplayMatching?.(bounds);
      if (!primary || !display || display.id === primary.id) return;
      rememberedSecondaryBounds = bounds;
    } catch {
      // Screen queries can fail during display teardown; ignore.
    }
  };

  const onDisplayRemoved = () => {
    if (!attached) return;
    // Snapshot the bounds at removal time: if the OS has not relocated the
    // window yet this is the most accurate placement to restore.
    boundsAtDisplayRemoval = copyBounds();
  };

  // Electron invokes "display-added" listeners as (event, newDisplay).
  const onDisplayAdded = (_event, display) => {
    if (!attached || !isTrackable()) return;
    try {
      const currentBounds = copyBounds();
      const restored = pickDisplayRecoveryBounds({
        addedDisplay: display,
        currentBounds,
        // The removal-time snapshot is the most accurate; the continuously
        // tracked placement covers the case where the OS relocated the window
        // before the removal event fired.
        candidates: [boundsAtDisplayRemoval, rememberedSecondaryBounds],
      });
      if (!restored) return;
      const clamped = clampBoundsToDisplay(restored, display?.bounds);
      if (clamped) {
        win.setBounds(clamped);
      }
    } catch {
      // Never let display churn break the window.
    }
  };

  try {
    screen.on("display-removed", onDisplayRemoved);
    screen.on("display-added", onDisplayAdded);
    win.on?.("move", rememberWindowPlacement);
    win.on?.("resize", rememberWindowPlacement);
    // Seed the tracked placement from the window's initial bounds so windows
    // that started on a secondary display and were never moved/resized are
    // still recoverable when that display is torn down and re-added.
    rememberWindowPlacement();
  } catch {
    return () => {};
  }

  return function detach() {
    attached = false;
    try {
      screen.removeListener?.("display-removed", onDisplayRemoved);
    } catch {}
    try {
      screen.removeListener?.("display-added", onDisplayAdded);
    } catch {}
    try {
      win.removeListener?.("move", rememberWindowPlacement);
    } catch {}
    try {
      win.removeListener?.("resize", rememberWindowPlacement);
    } catch {}
    boundsAtDisplayRemoval = null;
    rememberedSecondaryBounds = null;
  };
}

module.exports = {
  attachDisplayRecovery,
  boundsIntersectDisplay,
  clampBoundsToDisplay,
  isFiniteBounds,
  pickDisplayRecoveryBounds,
};
