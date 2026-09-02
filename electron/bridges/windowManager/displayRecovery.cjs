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

// A move to the primary display while the remembered secondary display is
// still connected is ambiguous: it can be a deliberate user move, or the OS
// relocating the window during teardown before Electron emits
// "display-removed". If the remembered display is removed within this grace
// window, treat the move as a teardown relocation and keep the snapshot.
const DEFAULT_TEARDOWN_GRACE_MS = 2000;

// There is no portable clock that keeps running unchanged across system
// suspension: Date.now() (and every clock Node exposes) advances while the
// machine is asleep. The "suspend" event of Electron's powerMonitor is the
// only reliable signal that the wall clock jumped since a timestamp was
// taken. It is loaded lazily so the module also works outside Electron
// (e.g. in the unit tests).
let powerMonitor = null;
try {
  const electron = require("electron");
  if (electron && typeof electron === "object") {
    powerMonitor = electron.powerMonitor || null;
  }
} catch {
  // Not running inside Electron; suspension tracking stays unavailable and
  // the wall-clock grace window is used as-is.
}

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
 * Area of the part of `bounds` that lies on a display with `displayBounds`
 * (0 when they do not overlap).
 */
function displayIntersectionArea(bounds, displayBounds) {
  if (!boundsIntersectDisplay(bounds, displayBounds)) return 0;
  const width =
    Math.min(bounds.x + bounds.width, displayBounds.x + displayBounds.width) -
    Math.max(bounds.x, displayBounds.x);
  const height =
    Math.min(bounds.y + bounds.height, displayBounds.y + displayBounds.height) -
    Math.max(bounds.y, displayBounds.y);
  return width * height;
}

/**
 * Normalize a recovery candidate: either plain bounds or an object carrying
 * the bounds plus the id of the display they were remembered for.
 */
function normalizeRecoveryCandidate(candidate) {
  if (!candidate) return null;
  if (isFiniteBounds(candidate)) return { bounds: candidate, displayId: null };
  if (isFiniteBounds(candidate.bounds)) {
    return {
      bounds: candidate.bounds,
      displayId:
        candidate.displayId === undefined || candidate.displayId === null
          ? null
          : candidate.displayId,
    };
  }
  return null;
}

/**
 * Decide whether the window should be moved back onto a (re-)added display.
 * Returns the remembered bounds to restore, or null when the window is already
 * on that display or no remembered placement matches it. Candidates are
 * evaluated in order, so more specific snapshots should come first. A
 * candidate is accepted when it was remembered for that very display id — the
 * display may have come back with different bounds (DPI/resolution/topology
 * change), in which case the old geometry is clamped into the new bounds by
 * the caller — or, for untagged (legacy) candidates without a display id,
 * when its bounds intersect the re-added display. Candidates tagged for
 * another display never match by geometry: during multi-display churn an
 * unrelated display can re-appear first with overlapping bounds, and
 * restoring the owner's geometry onto it would move the window to the wrong
 * display and clobber the placement remembered for its real owner.
 */
function pickDisplayRecoveryBounds({ addedDisplay, currentBounds, candidates }) {
  if (!addedDisplay || !isFiniteBounds(addedDisplay.bounds)) return null;
  if (!isFiniteBounds(currentBounds)) return null;
  // The window is already (at least partially) on this display: nothing to do.
  if (boundsIntersectDisplay(currentBounds, addedDisplay.bounds)) return null;
  for (const candidate of candidates || []) {
    const normalized = normalizeRecoveryCandidate(candidate);
    if (!normalized) continue;
    if (normalized.displayId !== null) {
      // A tagged candidate belongs to one specific display: only accept it
      // for that display and never fall back to geometry matching.
      if (normalized.displayId === addedDisplay.id) return normalized.bounds;
      continue;
    }
    if (boundsIntersectDisplay(normalized.bounds, addedDisplay.bounds)) {
      return normalized.bounds;
    }
  }
  return null;
}

function boundsEqual(a, b) {
  return Boolean(
    a &&
    b &&
    a.x === b.x &&
    a.y === b.y &&
    a.width === b.width &&
    a.height === b.height
  );
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
function attachDisplayRecovery({
  win,
  screen,
  teardownGraceMs = DEFAULT_TEARDOWN_GRACE_MS,
  // Injectable for tests; defaults to Electron's powerMonitor (null outside
  // Electron).
  powerMonitor: injectedPowerMonitor = null,
}) {
  if (!win || !screen || typeof screen.on !== "function") {
    return () => {};
  }

  let boundsAtDisplayRemoval = null;
  // Id of the display `boundsAtDisplayRemoval` was captured for, so the
  // snapshot can still be matched when that display re-appears with changed
  // bounds (DPI/resolution/topology change).
  let boundsAtDisplayRemovalDisplayId = null;
  let rememberedSecondaryBounds = null;
  let rememberedDisplayId = null;
  let pendingTeardownMove = null;
  // Time at which `boundsAtDisplayRemoval` was (re)captured or promoted from
  // `pendingTeardownMove`. Trailing teardown-burst events (e.g. a paired
  // "resize" after "display-removed" promoted the snapshot) arrive within the
  // grace window and must not clear the snapshot; later edits past the grace
  // window are deliberate user placement and invalidate it.
  let teardownSnapshotAt = null;
  // Time of the first primary-display edit observed while the remembered
  // display is already gone (i.e. the OS teardown relocation). Later edits
  // past the grace window are deliberate user placement, not part of the
  // relocation, and must invalidate the pending recovery candidate.
  let teardownRelocationAt = null;
  // Recovery computed while the window was maximized/full-screen (when
  // setBounds would be wrong or would clobber the maximized state) is held
  // here and applied once the window returns to its normal state. `bounds`
  // is the restore target, `displayId` the display it belongs to, and
  // `fromBounds` the window's placement at deferral time — if the window has
  // been re-placed since, the user wins and the recovery is dropped.
  let pendingRecovery = null;
  // Wall-clock time of the last powerMonitor "suspend" or "lock-screen"
  // event. Used to tell a grace-window expiry caused by actual elapsed time
  // from one caused by the clock advancing while the machine was asleep or
  // the session locked (Win+L tears displays down without necessarily
  // emitting "suspend"; "lock-screen" is the only signal for that path —
  // see onDisplayRemoved).
  let sessionInterruptedAt = null;
  const activePowerMonitor = injectedPowerMonitor || powerMonitor;
  let attached = true;

  const onSessionInterrupted = () => {
    sessionInterruptedAt = Date.now();
  };

  const isTrackable = () => {
    try {
      return !win.isDestroyed();
    } catch {
      return false;
    }
  };

  const isMaximizedOrFullScreen = () => {
    try {
      return Boolean(win.isMaximized?.() || win.isFullScreen?.());
    } catch {
      return false;
    }
  };

  const copyBounds = () => {
    try {
      // While maximized/full-screen, getBounds() reports the inflated
      // maximized geometry; getNormalBounds() reports the placement the window
      // will return to, which is what recovery must remember.
      const maximized = isMaximizedOrFullScreen();
      const bounds = maximized
        ? typeof win.getNormalBounds === "function"
          ? win.getNormalBounds()
          : null
        : win.getBounds();
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
      if (!primary || !display) return;
      if (display.id === primary.id) {
        // The window is on the primary display now. If the remembered
        // secondary display is still connected, this is either a deliberate
        // user move or an OS teardown relocation that raced ahead of the
        // "display-removed" event (the display list has not changed yet).
        // The two are indistinguishable here, so drop the stale snapshot but
        // stash it briefly: if the remembered display is removed within the
        // grace window, the pre-relocation placement is still recoverable.
        // When the remembered display is already gone from the display list,
        // teardown has happened: keep the snapshot for recovery.
        if (rememberedDisplayId !== null) {
          const connected = screen.getAllDisplays?.() || [];
          if (!connected.some((candidate) => candidate.id === rememberedDisplayId)) {
            // Teardown relocation: preserve the pre-teardown placement so the
            // later "display-added" event can restore it. But only the OS's
            // initial relocation (and the events it emits in the same burst,
            // e.g. a paired "resize") may do so: a move/resize that arrives
            // past the grace window is a deliberate user edit while the
            // monitor stays disconnected, and it supersedes the stale
            // recovery candidate.
            const now = Date.now();
            if (
              teardownRelocationAt !== null &&
              now - teardownRelocationAt >= teardownGraceMs
            ) {
              teardownRelocationAt = null;
              rememberedSecondaryBounds = null;
              rememberedDisplayId = null;
              pendingTeardownMove = null;
              boundsAtDisplayRemoval = null;
              boundsAtDisplayRemovalDisplayId = null;
              teardownSnapshotAt = null;
              return;
            }
            if (teardownRelocationAt === null) teardownRelocationAt = now;
            return;
          }
          pendingTeardownMove = {
            bounds: rememberedSecondaryBounds,
            displayId: rememberedDisplayId,
            at: Date.now(),
          };
        }
        rememberedSecondaryBounds = null;
        rememberedDisplayId = null;
        teardownRelocationAt = null;
        // A snapshot promoted from `pendingTeardownMove` must survive the rest
        // of the teardown burst: the OS relocation can emit trailing events
        // (e.g. a paired "resize") after "display-removed" already promoted
        // the snapshot, and `rememberedDisplayId` is cleared by then, so this
        // is the only protection left. Events within the grace window of the
        // promotion are part of the burst and keep the snapshot; later edits
        // past the grace window are deliberate user placement.
        if (
          boundsAtDisplayRemoval !== null &&
          teardownSnapshotAt !== null &&
          Date.now() - teardownSnapshotAt < teardownGraceMs
        ) {
          return;
        }
        // Any pending removal-time snapshot was captured for a recovery this
        // edit now supersedes: the user deliberately (re)placed the window on
        // the primary display, so a later "display-added" must not drag it
        // back to the old geometry.
        boundsAtDisplayRemoval = null;
        boundsAtDisplayRemovalDisplayId = null;
        teardownSnapshotAt = null;
        return;
      }
      // A fresh placement on a non-primary display supersedes any removal-time
      // snapshot: the snapshot belongs to a display that was removed earlier,
      // and restoring it when that display re-appears would move the window
      // away from this newer placement (e.g. the user relocated the window
      // from a removed display onto another still-connected one). If the
      // snapshot's display is still connected, its "display-added" event has
      // already fired, so dropping the snapshot here can never lose a pending
      // recovery.
      boundsAtDisplayRemoval = null;
      boundsAtDisplayRemovalDisplayId = null;
      teardownSnapshotAt = null;
      rememberedSecondaryBounds = bounds;
      rememberedDisplayId = display.id;
      pendingTeardownMove = null;
      teardownRelocationAt = null;
    } catch {
      // Screen queries can fail during display teardown; ignore.
    }
  };

  // Electron invokes "display-removed" listeners as (event, oldDisplay).
  const onDisplayRemoved = (_event, oldDisplay) => {
    if (!attached) return;
    if (pendingTeardownMove && oldDisplay && oldDisplay.id === pendingTeardownMove.displayId) {
      // Date.now() advances while the machine is asleep: when the OS
      // relocates the window right before suspension, the matching
      // "display-removed" event can be delivered hours later on the wall
      // clock. A suspension after the pending move was recorded means the
      // elapsed grace window proves nothing about a deliberate user move, so
      // the pending snapshot must not be expired for that reason alone.
      const suspendedAfterPendingMove =
        sessionInterruptedAt !== null && sessionInterruptedAt >= pendingTeardownMove.at;
      if (
        suspendedAfterPendingMove ||
        Date.now() - pendingTeardownMove.at < teardownGraceMs
      ) {
        // The OS relocated the window to the primary before this removal event
        // fired: restore the pre-relocation placement on the removed display.
        boundsAtDisplayRemoval = pendingTeardownMove.bounds;
        boundsAtDisplayRemovalDisplayId = pendingTeardownMove.displayId;
        // Trailing events of the same relocation burst (e.g. a paired
        // "resize" after this removal) must not clear the promoted snapshot;
        // stamp the promotion so rememberWindowPlacement can tell burst
        // events from later deliberate user edits.
        teardownSnapshotAt = Date.now();
      }
      pendingTeardownMove = null;
      return;
    }
    // Snapshot the bounds at removal time: if the OS has not relocated the
    // window yet this is the most accurate placement to restore. Only do this
    // when the window actually lives on the removed display: during a lock/
    // sleep teardown multiple secondary displays can be removed in sequence,
    // and snapshotting the window's (possibly already relocated, primary-
    // display) bounds for an unrelated removal would clobber the valid
    // snapshot captured for the display the window was really on.
    const currentBounds = copyBounds();
    if (!oldDisplay || !boundsIntersectDisplay(currentBounds, oldDisplay.bounds)) return;
    // Only treat the window as belonging to the removed display when that
    // display holds the largest share of it — the same "most closely
    // intersecting" rule screen.getDisplayMatching applies. The removed
    // display is already gone from the display list when this event fires, so
    // getDisplayMatching alone cannot identify it; compare against every
    // still-connected display instead. A window that lives primarily on
    // another display and merely overlaps the removed one must not be
    // snapshotted: recovery would later drag it onto the re-added display
    // against the user's placement.
    const removedArea = displayIntersectionArea(currentBounds, oldDisplay.bounds);
    if (removedArea <= 0) return;
    try {
      // An exact tie with a connected display is ambiguous (Electron's own
      // tie-breaking could have assigned the window to either display), so
      // treat equal overlap as "not owned by the removed display" too and
      // skip the snapshot rather than risk dragging the window onto the
      // re-added display against its actual placement.
      for (const display of screen.getAllDisplays?.() || []) {
        if (displayIntersectionArea(currentBounds, display.bounds) >= removedArea) return;
      }
    } catch {
      // Screen queries can fail during display teardown; skip the snapshot.
      return;
    }
    boundsAtDisplayRemoval = currentBounds;
    boundsAtDisplayRemovalDisplayId = oldDisplay.id;
    teardownSnapshotAt = Date.now();
  };

  // Apply a recovery that was deferred while the window was maximized or
  // full-screen (see onDisplayAdded). Invoked when the window leaves that
  // state; also guards against the target display vanishing again.
  const applyPendingRecovery = () => {
    if (!attached || !pendingRecovery) return;
    try {
      if (isMaximizedOrFullScreen()) return;
      const { bounds, displayId } = pendingRecovery;
      if (!isFiniteBounds(bounds)) {
        pendingRecovery = null;
        return;
      }
      let targetBounds = bounds;
      if (displayId !== null && displayId !== undefined) {
        const display = (screen.getAllDisplays?.() || []).find(
          (candidate) => candidate.id === displayId
        );
        // The display disappeared again before the window left the
        // maximized/full-screen state: keep the recovery queued so the next
        // "display-added" event can still apply it (see onDisplayAdded). It
        // is the only remaining recovery candidate here — the removal-time
        // snapshot was already consumed when it was deferred.
        if (!display) return;
        targetBounds = clampBoundsToDisplay(bounds, display.bounds) || bounds;
      }
      pendingRecovery = null;
      win.setBounds(targetBounds);
    } catch {
      // Never let display churn break the window.
    }
  };

  // Electron invokes "display-added" listeners as (event, newDisplay).
  const onDisplayAdded = (_event, display) => {
    if (!attached) return;
    try {
      const currentBounds = copyBounds();
      // A recovery deferred while the window was maximized/full-screen may
      // still be queued because its target display vanished again before the
      // window left that state (applyPendingRecovery keeps it queued in that
      // case). When the target display re-appears, apply the deferred
      // recovery instead of letting it sit forever — unless the user has
      // re-placed the window in the meantime, in which case the user wins.
      if (
        pendingRecovery &&
        display &&
        isFiniteBounds(display.bounds) &&
        pendingRecovery.displayId === display.id
      ) {
        if (boundsIntersectDisplay(currentBounds, display.bounds)) {
          // The window already sits on the re-added display: nothing to do.
          pendingRecovery = null;
          return;
        }
        if (
          !isFiniteBounds(currentBounds) ||
          !isFiniteBounds(pendingRecovery.fromBounds) ||
          !boundsEqual(currentBounds, pendingRecovery.fromBounds)
        ) {
          // The user deliberately re-placed the window while the target
          // display was absent: drop the stale deferred recovery and fall
          // through to the candidate-based recovery below.
          pendingRecovery = null;
        } else {
          const requeued = clampBoundsToDisplay(pendingRecovery.bounds, display.bounds);
          pendingRecovery = null;
          if (requeued) {
            if (isMaximizedOrFullScreen()) {
              // Still maximized/full-screen: defer once more.
              pendingRecovery = {
                bounds: requeued,
                displayId: display.id,
                fromBounds: currentBounds,
              };
              return;
            }
            win.setBounds(requeued);
          }
          return;
        }
      }
      const restored = pickDisplayRecoveryBounds({
        addedDisplay: display,
        currentBounds,
        // The removal-time snapshot is the most accurate; the continuously
        // tracked placement covers the case where the OS relocated the window
        // before the removal event fired. Each candidate carries the id of
        // the display it was remembered for so a returning display with
        // changed bounds (DPI/resolution/topology) is still matched and the
        // old geometry gets clamped into the new bounds below.
        candidates: [
          boundsAtDisplayRemoval && {
            bounds: boundsAtDisplayRemoval,
            displayId: boundsAtDisplayRemovalDisplayId,
          },
          rememberedSecondaryBounds && {
            bounds: rememberedSecondaryBounds,
            displayId: rememberedDisplayId,
          },
        ],
      });
      // Recovery is already unnecessary when the returning display contains
      // the window: invalidate the removal-time snapshot captured for it so a
      // later teardown + re-add cannot restore stale placement over the
      // user's current position (e.g. after they moved to the primary during
      // the teardown grace window).
      if (
        display &&
        isFiniteBounds(display.bounds) &&
        isFiniteBounds(currentBounds) &&
        boundsIntersectDisplay(currentBounds, display.bounds) &&
        boundsAtDisplayRemoval !== null &&
        boundsAtDisplayRemovalDisplayId === display.id
      ) {
        boundsAtDisplayRemoval = null;
        boundsAtDisplayRemovalDisplayId = null;
        teardownSnapshotAt = null;
      }
      if (!restored) return;
      // The removal-time snapshot is only valid for the recovery it was taken
      // for. Once consumed (or once the window is back on the re-added
      // display), drop it so a later re-add can't restore the window against
      // the user's most recent placement.
      if (restored === boundsAtDisplayRemoval) {
        boundsAtDisplayRemoval = null;
        boundsAtDisplayRemovalDisplayId = null;
        teardownSnapshotAt = null;
      }
      const clamped = clampBoundsToDisplay(restored, display?.bounds);
      if (!clamped) return;
      if (isMaximizedOrFullScreen()) {
        // setBounds would clobber the maximized/full-screen state (or be
        // ignored): defer until the window returns to its normal state.
        // `fromBounds` records the placement at deferral time so a later
        // re-apply can tell an untouched window from a user-replaced one.
        pendingRecovery = {
          bounds: clamped,
          displayId: display?.id ?? null,
          fromBounds: currentBounds,
        };
        return;
      }
      win.setBounds(clamped);
    } catch {
      // Never let display churn break the window.
    }
  };

  try {
    activePowerMonitor?.on?.("suspend", onSessionInterrupted);
    activePowerMonitor?.on?.("lock-screen", onSessionInterrupted);
  } catch {
    // Suspension/lock tracking is best-effort; the grace window still applies.
  }

  try {
    screen.on("display-removed", onDisplayRemoved);
    screen.on("display-added", onDisplayAdded);
    win.on?.("move", rememberWindowPlacement);
    win.on?.("resize", rememberWindowPlacement);
    win.on?.("unmaximize", applyPendingRecovery);
    win.on?.("leave-full-screen", applyPendingRecovery);
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
      activePowerMonitor?.removeListener?.("suspend", onSessionInterrupted);
      activePowerMonitor?.removeListener?.("lock-screen", onSessionInterrupted);
    } catch {}
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
    try {
      win.removeListener?.("unmaximize", applyPendingRecovery);
    } catch {}
    try {
      win.removeListener?.("leave-full-screen", applyPendingRecovery);
    } catch {}
    boundsAtDisplayRemoval = null;
    boundsAtDisplayRemovalDisplayId = null;
    rememberedSecondaryBounds = null;
    rememberedDisplayId = null;
    pendingTeardownMove = null;
    pendingRecovery = null;
    teardownRelocationAt = null;
    teardownSnapshotAt = null;
  };
}

module.exports = {
  attachDisplayRecovery,
  boundsIntersectDisplay,
  clampBoundsToDisplay,
  isFiniteBounds,
  pickDisplayRecoveryBounds,
};
