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
const MAXIMIZED_MONITOR_TRANSFER_INPUT_GRACE_MS = 2000;

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

function normalizeDisplayId(displayId) {
  return typeof displayId === "number" && Number.isFinite(displayId) && displayId >= 0
    ? displayId
    : null;
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
      // Electron reserves negative IDs for invalid/unknown and unified
      // displays. Neither is a durable, unique identity, so fall back to
      // geometry when either side has one of those sentinel values.
      displayId: normalizeDisplayId(candidate.displayId),
    };
  }
  return null;
}

function recoveryPlacementMatchesDisplay(placement, display) {
  const normalized = normalizeRecoveryCandidate(placement);
  if (!normalized || !display || !isFiniteBounds(display.bounds)) return false;
  const displayId = normalizeDisplayId(display.id);
  if (normalized.displayId !== null && displayId !== null) {
    return normalized.displayId === displayId;
  }
  return boundsIntersectDisplay(normalized.bounds, display.bounds);
}

/**
 * Decide whether the window should be moved back onto a (re-)added display.
 * Returns the remembered bounds to restore, or null when the window is already
 * on that display or no remembered placement matches it. Candidates are
 * evaluated in order, so more specific snapshots should come first. A
 * candidate is accepted when it was remembered for that very display id — the
 * display may have come back with different bounds (DPI/resolution/topology
 * change), in which case the old geometry is clamped into the new bounds by
 * the caller — or, when either side lacks a stable display id, when its bounds
 * intersect the re-added display. Candidates with stable ids that belong to
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
  const addedDisplayId = normalizeDisplayId(addedDisplay.id);
  for (const candidate of candidates || []) {
    const normalized = normalizeRecoveryCandidate(candidate);
    if (!normalized) continue;
    if (normalized.displayId !== null && addedDisplayId !== null) {
      // A tagged candidate belongs to one specific display: only accept it
      // for that display and never fall back to geometry matching.
      if (normalized.displayId === addedDisplayId) return normalized.bounds;
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
 * Usable placement rectangle for a display: prefer the work area (which
 * excludes reserved desktop space such as a top taskbar or dock) so a
 * restored window never ends up partially beneath that UI — with a top
 * taskbar that could hide a frameless title bar and its window controls.
 * Falls back to the full display bounds when no work area is reported.
 */
function displayPlacementRect(display) {
  if (!display) return null;
  if (isFiniteBounds(display.workArea)) return display.workArea;
  return isFiniteBounds(display.bounds) ? display.bounds : null;
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
  // The recovery flow depends on Windows' manual-only will-move/will-resize
  // events to distinguish user intent from OS display relocation.
  platform = process.platform,
  // Injectable for tests; defaults to Electron's powerMonitor (null outside
  // Electron).
  powerMonitor: injectedPowerMonitor = null,
}) {
  if (platform !== "win32" || !win || !screen || typeof screen.on !== "function") {
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
  // A display can re-appear before Windows delivers the queued relocation
  // that moves the window to the primary display. Keep the matching recovery
  // target until that late system move arrives, a manual placement cancels it,
  // or a new interruption starts. `fromBounds` records the normal placement
  // at return time so a maximized Win+Shift+Arrow move can be recognized even
  // though Electron does not emit will-move for it. The queued relocation is
  // not guaranteed to arrive within the short teardown grace window.
  let recentlyReturnedRecovery = null;
  // Win+Shift+Left/Right moves a maximized window between monitors without
  // Electron's manual-only will-move event. before-input-event is the missing
  // user-intent signal that distinguishes that shortcut from an otherwise
  // identical queued OS relocation after unlock.
  let lastMaximizedMonitorTransferInputAt = null;
  // A display-added event is authoritative even while Electron reports the
  // returning display with a transient negative id. Keep enough of that
  // event to correlate the display's later stable-id metrics event without
  // letting an unrelated primary-display metrics event claim the snapshot.
  let transientReturnedDisplay = null;
  // Wall-clock time of the last powerMonitor "suspend" or "lock-screen"
  // event. Used to tell a grace-window expiry caused by actual elapsed time
  // from one caused by the clock advancing while the machine was asleep or
  // the session locked (Win+L tears displays down without necessarily
  // emitting "suspend"; "lock-screen" is the only signal for that path —
  // see onDisplayRemoved).
  let sessionInterruptedAt = null;
  // Lock and suspend lifetimes are tracked independently: a single cycle can
  // emit "lock-screen" then "suspend" (Win+L followed by sleep), and the end
  // signals can arrive out of order — "resume" may fire while the session is
  // still locked. The interruption therefore stays active until every started
  // signal has been matched by its own end signal, not merely until the first
  // end signal of either kind arrives.
  const activeInterruptionSignals = new Set();
  // True while any started interruption signal ("lock-screen" or "suspend")
  // has not yet been matched by its end signal ("unlock-screen" or "resume").
  let sessionInterruptionActive = false;
  // Wall-clock time at which the last interruption ended. Teardown events
  // queued while the event loop was frozen (the machine was asleep) can be
  // delivered shortly after the end signal, so a removal arriving within the
  // grace window of the end still belongs to the finished interruption.
  let sessionInterruptionEndedAt = null;
  const activePowerMonitor = injectedPowerMonitor || powerMonitor;
  let attached = true;

  const isSessionInterruptionDraining = () =>
    !sessionInterruptionActive &&
    (sessionInterruptionEndedAt !== null &&
      Date.now() - sessionInterruptionEndedAt < teardownGraceMs);

  const isSessionInterruptionActiveOrDraining = () =>
    sessionInterruptionActive || isSessionInterruptionDraining();

  const clearRecoveryCandidates = () => {
    rememberedSecondaryBounds = null;
    rememberedDisplayId = null;
    pendingTeardownMove = null;
    boundsAtDisplayRemoval = null;
    boundsAtDisplayRemovalDisplayId = null;
    teardownRelocationAt = null;
    teardownSnapshotAt = null;
    pendingRecovery = null;
    recentlyReturnedRecovery = null;
    transientReturnedDisplay = null;
    lastMaximizedMonitorTransferInputAt = null;
  };

  // Electron emits these events only for a manual move/resize, before the
  // ordinary move/resize event. That distinction matters just after unlock:
  // Windows can deliver a queued OS relocation in the same period, and a
  // regular move event alone cannot tell the two apart.
  const onManualPlacement = (_event, nextBounds) => {
    if (!attached || !isTrackable() || sessionInterruptionActive) return;
    if (isFiniteBounds(nextBounds)) {
      try {
        const destinationDisplay = screen.getDisplayMatching?.(nextBounds);
        const primaryDisplay = screen.getPrimaryDisplay?.();
        const recentRecoveryCanStillReceiveQueuedEvents = Boolean(
          recentlyReturnedRecovery &&
            (recentlyReturnedRecovery.burstExpiresAt === null ||
              Date.now() <= recentlyReturnedRecovery.burstExpiresAt)
        );
        if (
          destinationDisplay &&
          primaryDisplay &&
          !recoveryPlacementMatchesDisplay(destinationDisplay, primaryDisplay) &&
          (recentRecoveryCanStillReceiveQueuedEvents ||
            transientReturnedDisplay)
        ) {
          // A display has already returned, but its queued Windows relocation
          // may still arrive. Protect whichever non-primary placement the user
          // chose now — either a refined target position or another secondary.
          // If this is the still-transient target, retain its association so a
          // later stable id can be transferred to the updated placement.
          const destinationDisplayId = normalizeDisplayId(destinationDisplay.id);
          const retainedTransientAssociation =
            transientReturnedDisplay &&
            !transientReturnedDisplay.unrelatedDisplayIds.has(
              destinationDisplayId
            ) &&
            boundsIntersectDisplay(
              transientReturnedDisplay.displayBounds,
              destinationDisplay.bounds
            )
              ? transientReturnedDisplay
              : null;
          const burstExpiresAt = recentRecoveryCanStillReceiveQueuedEvents
            ? recentlyReturnedRecovery.burstExpiresAt
            : null;
          clearRecoveryCandidates();
          transientReturnedDisplay = retainedTransientAssociation;
          recentlyReturnedRecovery = {
            bounds: { ...nextBounds },
            displayId: destinationDisplayId,
            fromBounds: { ...nextBounds },
            burstExpiresAt,
          };
          return;
        }
      } catch {
        // Fall through to clearing recovery state.
      }
    }
    clearRecoveryCandidates();
  };

  const onBeforeInputEvent = (_event, input) => {
    if (!attached || !isTrackable() || !isMaximizedOrFullScreen()) return;
    if (
      input?.type === "keyDown" &&
      input.meta === true &&
      input.shift === true &&
      (input.key === "ArrowLeft" || input.key === "ArrowRight")
    ) {
      lastMaximizedMonitorTransferInputAt = Date.now();
    }
  };

  const onSessionInterrupted = (signal) => {
    sessionInterruptedAt = Date.now();
    lastMaximizedMonitorTransferInputAt = null;
    // Repeated start signals within the same interruption (e.g. a "suspend"
    // following a "lock-screen" more than the grace window later) must not
    // clear a pending move recorded for that very cycle: the session is
    // locked/asleep, so the user cannot have moved the window in between.
    // Only the start of a NEW interruption may expire a pending move that
    // went stale while the session was running.
    const wasActive = sessionInterruptionActive;
    activeInterruptionSignals.add(signal);
    sessionInterruptionActive = true;
    if (wasActive) return;
    sessionInterruptionEndedAt = null;
    recentlyReturnedRecovery = null;
    transientReturnedDisplay = null;
    // Only a pending move that belongs to the current interruption may be
    // promoted by a later "display-removed" event: the OS relocation that
    // races ahead of display teardown happens immediately around the
    // suspend/lock event (before or after it), so it is still fresh here.
    // A move recorded long before this interruption is a deliberate user
    // placement (e.g. the user moved the window to the primary display and
    // locked the session much later); drop it so the removal during the
    // interruption cannot resurrect the superseded placement.
    if (
      pendingTeardownMove &&
      Date.now() - pendingTeardownMove.at >= teardownGraceMs
    ) {
      pendingTeardownMove = null;
    }
  };

  // `signal` is the start signal ("suspend" or "lock-screen") whose end
  // signal ("resume" / "unlock-screen") just fired.
  const onSessionResumed = (signal) => {
    // Only when the last outstanding start signal has been matched does the
    // interruption end: a "resume" while the session is still locked (or an
    // "unlock-screen" while the machine is still asleep) must not mark it
    // inactive, or a teardown relocation delivered between the two end
    // signals would be misread as an ordinary user move.
    activeInterruptionSignals.delete(signal);
    if (activeInterruptionSignals.size > 0) return;
    sessionInterruptionActive = false;
    // The interruption is over: remember when it ended so the promotion
    // logic below can tell a removal event still in flight from the finished
    // interruption (delivered within the grace window of the end signal,
    // e.g. queued while the machine was asleep) from a much later ordinary
    // unplug. Without this, a pending move recorded just before a lock that
    // never tore the display down would keep satisfying the
    // "interruption started after the move" ordering check forever, and a
    // later unplug would resurrect the superseded placement.
    sessionInterruptionEndedAt = Date.now();
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

  const isPrimaryDisplay = (display) => {
    if (!display || !isFiniteBounds(display.bounds)) return false;
    try {
      const primary = screen.getPrimaryDisplay?.();
      if (!primary || !isFiniteBounds(primary.bounds)) return false;
      const displayId = normalizeDisplayId(display.id);
      const primaryId = normalizeDisplayId(primary.id);
      if (displayId !== null && primaryId !== null) {
        return displayId === primaryId;
      }
      return boundsEqual(display.bounds, primary.bounds);
    } catch {
      return false;
    }
  };

  const connectedSecondaryDisplays = () => {
    try {
      return (screen.getAllDisplays?.() || []).filter(
        (display) => !isPrimaryDisplay(display)
      );
    } catch {
      return [];
    }
  };

  const rememberTransientReturnedDisplay = (display, recoveryCandidate) => {
    const connectedSecondaries = connectedSecondaryDisplays();
    transientReturnedDisplay = {
      displayBounds: { ...display.bounds },
      recoveryBounds: { ...recoveryCandidate.bounds },
      // Stable secondary displays that were already connected when this
      // unknown-id display was added are known to be unrelated. Their later
      // metrics/removal events must neither claim nor clear this association.
      unrelatedDisplayIds: new Set(
        connectedSecondaries
          // The event payload may still carry -1 after getAllDisplays() has
          // learned the id. Exclude the only secondary, or the one with the
          // same bounds, because it is the just-added display itself.
          .filter(
            (candidate) =>
              connectedSecondaries.length > 1 &&
              !boundsEqual(candidate.bounds, display.bounds)
          )
          .map((candidate) => normalizeDisplayId(candidate.id))
          .filter((displayId) => displayId !== null)
      ),
    };
  };

  const matchesTransientReturnedDisplay = (
    display,
    { allowDisconnectedDisplay = false } = {}
  ) => {
    const association = transientReturnedDisplay;
    const stableDisplayId = normalizeDisplayId(display?.id);
    if (
      !association ||
      stableDisplayId === null ||
      !isFiniteBounds(display?.bounds) ||
      isPrimaryDisplay(display)
    ) {
      return false;
    }

    if (association.unrelatedDisplayIds.has(stableDisplayId)) {
      return false;
    }

    if (boundsIntersectDisplay(association.displayBounds, display.bounds)) {
      if (allowDisconnectedDisplay) return true;
      // A second unknown display can stabilize while moving across the
      // target's old coordinates. Geometry is authoritative only when exactly
      // one still-connected, not-known-unrelated secondary overlaps the
      // transient target. Multiple overlaps are indistinguishable, so keep
      // waiting rather than assigning the recovery to the wrong display.
      const overlappingMatches = connectedSecondaryDisplays().filter(
        (candidate) => {
          const candidateId = normalizeDisplayId(candidate.id);
          return (
            !association.unrelatedDisplayIds.has(candidateId) &&
            boundsIntersectDisplay(
              association.displayBounds,
              candidate.bounds
            )
          );
        }
      );
      return overlappingMatches.length === 1;
    }

    // Some Windows topology changes update the id and move the display in the
    // same event. With no overlapping geometry, correlation is still
    // unambiguous when this is the only connected secondary that was not
    // already known to be unrelated when the association was established.
    const possibleMatches = connectedSecondaryDisplays().filter((candidate) => {
      const candidateId = normalizeDisplayId(candidate.id);
      return (
        candidateId === null ||
        !association.unrelatedDisplayIds.has(candidateId)
      );
    });
    return (
      possibleMatches.length === 1 &&
      normalizeDisplayId(possibleMatches[0].id) === stableDisplayId
    );
  };

  const promoteTransientReturnedDisplay = (display, options) => {
    if (!matchesTransientReturnedDisplay(display, options)) return false;
    const association = transientReturnedDisplay;
    const stableDisplayId = normalizeDisplayId(display.id);
    const belongsToAssociation = (bounds) =>
      isFiniteBounds(bounds) &&
      boundsIntersectDisplay(bounds, association.recoveryBounds);

    if (
      boundsAtDisplayRemovalDisplayId === null &&
      belongsToAssociation(boundsAtDisplayRemoval)
    ) {
      boundsAtDisplayRemovalDisplayId = stableDisplayId;
    }
    if (
      rememberedDisplayId === null &&
      belongsToAssociation(rememberedSecondaryBounds)
    ) {
      rememberedDisplayId = stableDisplayId;
    }
    if (
      pendingTeardownMove &&
      normalizeDisplayId(pendingTeardownMove.displayId) === null &&
      belongsToAssociation(pendingTeardownMove.bounds)
    ) {
      pendingTeardownMove.displayId = stableDisplayId;
    }
    if (
      pendingRecovery &&
      normalizeDisplayId(pendingRecovery.displayId) === null &&
      belongsToAssociation(pendingRecovery.bounds)
    ) {
      pendingRecovery.displayId = stableDisplayId;
    }
    if (
      recentlyReturnedRecovery &&
      normalizeDisplayId(recentlyReturnedRecovery.displayId) === null &&
      belongsToAssociation(recentlyReturnedRecovery.bounds)
    ) {
      recentlyReturnedRecovery.displayId = stableDisplayId;
    }

    transientReturnedDisplay = null;
    return true;
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
      const windowOnPrimary = recoveryPlacementMatchesDisplay(
        { bounds: display.bounds, displayId: display.id },
        primary
      );

      // Maximized Win+Shift+Arrow transfers do not emit will-move. Clear any
      // target that the shortcut superseded before deciding whether the new
      // monitor is primary or secondary; otherwise moving from the returned
      // display onto another secondary can leave the old target armed.
      const maximizedTransferSourceBounds =
        recentlyReturnedRecovery?.fromBounds ||
        pendingRecovery?.fromBounds ||
        boundsAtDisplayRemoval ||
        pendingTeardownMove?.bounds ||
        rememberedSecondaryBounds;
      if (
        isFiniteBounds(maximizedTransferSourceBounds) &&
        isMaximizedOrFullScreen() &&
        !boundsEqual(bounds, maximizedTransferSourceBounds) &&
        lastMaximizedMonitorTransferInputAt !== null &&
        Date.now() - lastMaximizedMonitorTransferInputAt <=
          MAXIMIZED_MONITOR_TRANSFER_INPUT_GRACE_MS
      ) {
        clearRecoveryCandidates();
        if (windowOnPrimary) return;
      }

      if (recentlyReturnedRecovery) {
        let returnedRecovery = recentlyReturnedRecovery;
        if (
          returnedRecovery.burstExpiresAt !== null &&
          Date.now() > returnedRecovery.burstExpiresAt
        ) {
          // Once one queued relocation event has been recovered, keep the
          // record only for a bounded burst. User moves clear it earlier via
          // will-move/will-resize (or the maximized shortcut signal above).
          recentlyReturnedRecovery = null;
          returnedRecovery = null;
        }
        const connected = screen.getAllDisplays?.() || [];
        const returnedDisplay = returnedRecovery
          ? connected.find((candidate) =>
              recoveryPlacementMatchesDisplay(returnedRecovery, candidate)
            )
          : null;
        const windowOnReturnedDisplay =
          returnedDisplay &&
          recoveryPlacementMatchesDisplay(
            { bounds: display.bounds, displayId: display.id },
            returnedDisplay
          );
        if (returnedDisplay && !windowOnReturnedDisplay) {
          const restored = returnedDisplay
            ? clampBoundsToDisplay(
                returnedRecovery.bounds,
                displayPlacementRect(returnedDisplay)
              )
            : null;
          if (restored) {
            // A single Windows relocation may emit multiple move and resize
            // events. Retain the target for the bounded burst; a real manual
            // placement has its own intent signal and clears it immediately.
            recentlyReturnedRecovery =
              teardownGraceMs > 0
                ? {
                    ...returnedRecovery,
                    fromBounds: { ...restored },
                    burstExpiresAt:
                      returnedRecovery.burstExpiresAt ??
                      Date.now() + teardownGraceMs,
                  }
                : null;
            if (isMaximizedOrFullScreen()) {
              pendingRecovery = {
                bounds: restored,
                displayId:
                  normalizeDisplayId(returnedDisplay.id) ??
                  normalizeDisplayId(returnedRecovery.displayId),
                fromBounds: bounds,
              };
            } else {
              win.setBounds(restored);
            }
            return;
          }
        }
      }

      if (windowOnPrimary) {
        // The window is on the primary display now. If the remembered
        // secondary display is still connected, this is either a deliberate
        // user move or an OS teardown relocation that raced ahead of the
        // "display-removed" event (the display list has not changed yet).
        // The two are indistinguishable here, so drop the stale snapshot but
        // stash it briefly: if the remembered display is removed within the
        // grace window, the pre-relocation placement is still recoverable.
        // When the remembered display is already gone from the display list,
        // teardown has happened: keep the snapshot for recovery.
        if (rememberedSecondaryBounds !== null) {
          const connected = screen.getAllDisplays?.() || [];
          if (
            !connected.some((candidate) =>
              recoveryPlacementMatchesDisplay(
                {
                  bounds: rememberedSecondaryBounds,
                  displayId: rememberedDisplayId,
                },
                candidate
              )
            )
          ) {
            // Teardown relocation: preserve the pre-teardown placement so the
            // later "display-added" event can restore it. But only the OS's
            // initial relocation (and the events it emits in the same burst,
            // e.g. a paired "resize") may do so: a move/resize that arrives
            // past the grace window is a deliberate user edit while the
            // monitor stays disconnected, and it supersedes the stale
            // recovery candidate. While a lock/sleep interruption remains
            // active, however, the user cannot deliberately edit placement,
            // so retain the snapshot for the full interruption.
            const now = Date.now();
            if (
              teardownRelocationAt !== null &&
              !sessionInterruptionActive &&
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
            // Windows may relocate the window either just before or just
            // after the lock-screen/suspend event. When the relocation lands
            // after the interruption, the timestamp ordering check in
            // onDisplayRemoved cannot tell that this move belongs to the
            // interruption, so record it here: a primary-display move
            // observed while the session is locked or the machine is asleep
            // is the OS's teardown relocation — the user cannot have moved
            // the window. The live `sessionInterruptionActive` flag is used
            // rather than the elapsed time since `sessionInterruptedAt`:
            // the relocation can land long after the lock/sleep event (the
            // teardown itself can take longer than the grace window) while
            // the session is still interrupted.
            duringSessionInterruption: sessionInterruptionActive === true,
          };
        }
        rememberedSecondaryBounds = null;
        rememberedDisplayId = null;
        teardownRelocationAt = null;
        // A snapshot promoted from `pendingTeardownMove` must survive the rest
        // of the teardown burst: the OS relocation can emit trailing events
        // (e.g. a paired "resize") after "display-removed" already promoted
        // the snapshot, and `rememberedDisplayId` is cleared by then, so this
        // is the only protection left. Keep it for the full active lock/sleep
        // interruption, when the user cannot deliberately place the window;
        // otherwise the grace window separates the teardown burst from a later
        // user edit.
        if (
          boundsAtDisplayRemoval !== null &&
          teardownSnapshotAt !== null &&
          (sessionInterruptionActive ||
            Date.now() - teardownSnapshotAt < teardownGraceMs)
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
      const rememberedPlacementBelongsElsewhere =
        rememberedSecondaryBounds !== null &&
        !recoveryPlacementMatchesDisplay(
          {
            bounds: rememberedSecondaryBounds,
            displayId: rememberedDisplayId,
          },
          display
        );
      if (
        boundsAtDisplayRemoval !== null ||
        pendingTeardownMove !== null ||
        pendingRecovery !== null ||
        recentlyReturnedRecovery !== null ||
        rememberedPlacementBelongsElsewhere
      ) {
        // Windows can deliver a queued relocation onto another connected
        // secondary long after unlock. A real manual placement clears recovery
        // state in onManualPlacement (or the maximized shortcut gate above), so
        // preserve the original target until one of those intent signals wins.
        return;
      }
      boundsAtDisplayRemoval = null;
      boundsAtDisplayRemovalDisplayId = null;
      teardownSnapshotAt = null;
      rememberedSecondaryBounds = bounds;
      rememberedDisplayId = normalizeDisplayId(display.id);
      pendingTeardownMove = null;
      teardownRelocationAt = null;
    } catch {
      // Screen queries can fail during display teardown; ignore.
    }
  };

  // Electron invokes "display-removed" listeners as (event, oldDisplay).
  const onDisplayRemoved = (_event, oldDisplay) => {
    if (!attached) return;
    if (
      transientReturnedDisplay &&
      oldDisplay &&
      !isPrimaryDisplay(oldDisplay)
    ) {
      const association = transientReturnedDisplay;
      const removedDisplayId = normalizeDisplayId(oldDisplay.id);
      if (association.unrelatedDisplayIds.has(removedDisplayId)) {
        // This display was already present when the transient target returned,
        // or arrived later as separate topology. Its removal must not fall
        // through to the geometry-only recovery matcher below: Windows may
        // temporarily move it over the target's old coordinates during churn.
        return;
      }
      if (
        !isFiniteBounds(oldDisplay.bounds) ||
        !boundsIntersectDisplay(association.displayBounds, oldDisplay.bounds)
      ) {
        // This removal is neither a known pre-existing display nor a
        // geometry match for the transient target. Its identity is
        // ambiguous, so fail closed instead of allowing a later display to
        // inherit the target's recovery record.
        clearRecoveryCandidates();
        return;
      }
      // The transient target itself disappeared again. End only the live
      // association; the regular removal logic below must first get a chance
      // to promote a pending teardown move or capture the current placement.
      // Clearing every candidate here would lose the last trusted bounds
      // when Windows relocates the window before this event.
      if (removedDisplayId !== null) {
        // Electron can reveal the durable id for the first time in this
        // removal payload. Transfer it to every pending candidate before the
        // live association disappears so a changed-geometry return can still
        // be matched by identity.
        promoteTransientReturnedDisplay(oldDisplay, {
          allowDisconnectedDisplay: true,
        });
      } else {
        transientReturnedDisplay = null;
      }
      recentlyReturnedRecovery = null;
    }
    if (
      pendingTeardownMove &&
      recoveryPlacementMatchesDisplay(pendingTeardownMove, oldDisplay)
    ) {
      // Date.now() advances while the machine is asleep: when the OS
      // relocates the window right before suspension, the matching
      // "display-removed" event can be delivered hours later on the wall
      // clock. A suspension after the pending move was recorded means the
      // elapsed grace window proves nothing about a deliberate user move, so
      // the pending snapshot must not be expired for that reason alone.
      const suspendedAfterPendingMove =
        sessionInterruptedAt !== null &&
        sessionInterruptedAt >= pendingTeardownMove.at &&
        // The interruption that started after the pending move must still be
        // the live one this removal event belongs to: either it has not ended
        // yet, or it ended so recently that teardown events queued while the
        // event loop was frozen may still be in flight. Without this gate, a
        // pending move recorded just before a lock whose interruption ended
        // without the display ever disappearing (a deliberate user move, not
        // a teardown relocation) would keep satisfying the ordering check
        // forever, and a much later ordinary unplug would promote the stale
        // placement and undo the user's move on the next re-add.
        isSessionInterruptionActiveOrDraining();
      if (
        suspendedAfterPendingMove ||
        // The OS relocation can also land after the lock-screen/suspend
        // event (Windows is free to emit them in either order), in which
        // case the ordering check above is false even though the pending
        // move belongs to this very interruption. Such moves are stamped at
        // creation time and must not be expired by the grace window either.
        // The stamp must not outlive its interruption, though: if the locked
        // session never tears the display down, the tagged move would
        // otherwise be promoted by an ordinary unplug long after unlock and
        // restore the obsolete pre-lock placement on re-add. Gate the stamp
        // by the same live-interruption / ended-grace check as above.
        (pendingTeardownMove.duringSessionInterruption === true &&
          isSessionInterruptionActiveOrDraining()) ||
        Date.now() - pendingTeardownMove.at < teardownGraceMs
      ) {
        // The OS relocated the window to the primary before this removal event
        // fired: restore the pre-relocation placement on the removed display.
        boundsAtDisplayRemoval = pendingTeardownMove.bounds;
        boundsAtDisplayRemovalDisplayId =
          normalizeDisplayId(pendingTeardownMove.displayId) ??
          normalizeDisplayId(oldDisplay?.id);
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
    boundsAtDisplayRemovalDisplayId = normalizeDisplayId(oldDisplay.id);
    teardownSnapshotAt = Date.now();
  };

  // Apply a recovery that was deferred while the window was maximized or
  // full-screen (see onDisplayAdded). Invoked when the window leaves that
  // state; also guards against the target display vanishing again.
  const applyPendingRecovery = () => {
    if (!attached || !pendingRecovery) return;
    try {
      if (isMaximizedOrFullScreen()) return;
      const { bounds, displayId, fromBounds } = pendingRecovery;
      if (!isFiniteBounds(bounds)) {
        pendingRecovery = null;
        return;
      }
      // Revalidate the deferred recovery against the window's current normal
      // placement. While still maximized, a user can move the window to
      // another monitor (e.g. Win+Shift+Arrow), which updates
      // getNormalBounds() without clearing the pending recovery; applying it
      // unconditionally here would overwrite the user's newer monitor and
      // geometry. `fromBounds` records the placement at deferral time, so any
      // change since means the user wins and the recovery is dropped. Normal
      // bounds are read directly because fromBounds was captured through
      // getNormalBounds() while the window was maximized (mid-unmaximize
      // getBounds() may still report the inflated maximized geometry).
      const currentNormalBounds = (() => {
        try {
          if (typeof win.getNormalBounds === "function") {
            const normal = win.getNormalBounds();
            if (isFiniteBounds(normal)) return { ...normal };
          }
        } catch {
          // Fall through to the regular bounds below.
        }
        return copyBounds();
      })();
      if (
        isFiniteBounds(currentNormalBounds) &&
        isFiniteBounds(fromBounds) &&
        !boundsEqual(currentNormalBounds, fromBounds)
      ) {
        // The user deliberately re-placed the window while the recovery was
        // deferred: drop the stale recovery.
        pendingRecovery = null;
        return;
      }
      const display = (screen.getAllDisplays?.() || []).find((candidate) =>
        recoveryPlacementMatchesDisplay({ bounds, displayId }, candidate)
      );
      // The display disappeared again before the window left the
      // maximized/full-screen state: keep the recovery queued so the next
      // "display-added" event can still apply it (see onDisplayAdded). It
      // is the only remaining recovery candidate here — the removal-time
      // snapshot was already consumed when it was deferred.
      if (!display) return;
      const targetBounds =
        clampBoundsToDisplay(bounds, displayPlacementRect(display)) || bounds;
      pendingRecovery = null;
      win.setBounds(targetBounds);
    } catch {
      // Never let display churn break the window.
    }
  };

  // Electron invokes "display-added" listeners as (event, newDisplay).
  // `requireStableIdentity` is used for display-metrics-changed: unlike an
  // add event, a metrics event does not prove that an unknown-id recovery
  // candidate belongs to the display that emitted it. Geometry fallback there
  // could consume a secondary snapshot on an unrelated primary-display event.
  const onDisplayAdded = (_event, display, requireStableIdentity = false) => {
    if (!attached) return;
    try {
      if (requireStableIdentity) {
        promoteTransientReturnedDisplay(display);
      } else if (transientReturnedDisplay) {
        const separatelyAddedDisplayId = normalizeDisplayId(display?.id);
        if (separatelyAddedDisplayId === null) {
          // Keep waiting. When either id stabilizes, the overlap check above
          // will accept it only if exactly one possible target remains.
          return;
        }
        // A second display-added event is new topology, not identity
        // stabilization for the earlier unknown display. Mark stable
        // newcomers as unrelated before any geometry-based promotion.
        transientReturnedDisplay.unrelatedDisplayIds.add(
          separatelyAddedDisplayId
        );
        return;
      }
      const currentBounds = copyBounds();
      const recoveryCandidates = [
        boundsAtDisplayRemoval && {
          bounds: boundsAtDisplayRemoval,
          displayId: boundsAtDisplayRemovalDisplayId,
        },
        rememberedSecondaryBounds && {
          bounds: rememberedSecondaryBounds,
          displayId: rememberedDisplayId,
        },
      ].filter(Boolean);
      const candidateMatchesDisplay = (candidate) => {
        const normalized = normalizeRecoveryCandidate(candidate);
        if (!normalized) return false;
        if (!requireStableIdentity) {
          return recoveryPlacementMatchesDisplay(normalized, display);
        }
        const displayId = normalizeDisplayId(display?.id);
        return (
          normalized.displayId !== null &&
          displayId !== null &&
          normalized.displayId === displayId
        );
      };
      const eligibleRecoveryCandidates = requireStableIdentity
        ? recoveryCandidates.filter(candidateMatchesDisplay)
        : recoveryCandidates;
      const normalizedCandidates = [pendingRecovery, ...recoveryCandidates]
        .filter(Boolean)
        .map(normalizeRecoveryCandidate);
      let matchingCandidate = normalizedCandidates.find(candidateMatchesDisplay);
      if (
        !matchingCandidate &&
        !requireStableIdentity &&
        normalizeDisplayId(display?.id) === null &&
        isFiniteBounds(display?.bounds) &&
        !isPrimaryDisplay(display)
      ) {
        const untaggedCandidates = normalizedCandidates.filter(
          (candidate) => candidate?.displayId === null
        );
        const otherUnknownSecondaries = connectedSecondaryDisplays().filter(
          (candidate) =>
            normalizeDisplayId(candidate.id) === null &&
            !boundsEqual(candidate.bounds, display.bounds)
        );
        // Multiple snapshots for the same placement are common (continuous
        // tracking plus removal-time capture). Treat them as one target only
        // when their geometries overlap. Stable displays already connected
        // beside the newly added unknown-id display are distinguishable and
        // do not make the add ambiguous; another unknown secondary does.
        const firstUntagged = untaggedCandidates[0] || null;
        if (
          otherUnknownSecondaries.length === 0 &&
          firstUntagged &&
          untaggedCandidates.every((candidate) =>
            boundsIntersectDisplay(candidate.bounds, firstUntagged.bounds)
          )
        ) {
          matchingCandidate = firstUntagged;
        }
      }
      if (
        !requireStableIdentity &&
        matchingCandidate &&
        normalizeDisplayId(display?.id) === null &&
        isFiniteBounds(display?.bounds)
      ) {
        rememberTransientReturnedDisplay(display, matchingCandidate);
      }
      if (
        matchingCandidate &&
        boundsIntersectDisplay(currentBounds, display?.bounds)
      ) {
        recentlyReturnedRecovery = {
          bounds: { ...matchingCandidate.bounds },
          displayId:
            normalizeDisplayId(display?.id) ?? matchingCandidate.displayId,
          fromBounds: isFiniteBounds(currentBounds) ? { ...currentBounds } : null,
          burstExpiresAt: null,
        };
      }
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
        candidateMatchesDisplay(pendingRecovery)
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
          const requeuedDisplayId =
            normalizeDisplayId(display.id) ??
            normalizeDisplayId(pendingRecovery.displayId);
          const requeued = clampBoundsToDisplay(
            pendingRecovery.bounds,
            displayPlacementRect(display)
          );
          pendingRecovery = null;
          if (requeued) {
            if (isMaximizedOrFullScreen()) {
              // Still maximized/full-screen: defer once more.
              pendingRecovery = {
                bounds: requeued,
                displayId: requeuedDisplayId,
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
        candidates: eligibleRecoveryCandidates,
      });
      const restoredDisplayId = normalizeDisplayId(
        eligibleRecoveryCandidates.find((candidate) => candidate.bounds === restored)
          ?.displayId
      );
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
        candidateMatchesDisplay(
          {
            bounds: boundsAtDisplayRemoval,
            displayId: boundsAtDisplayRemovalDisplayId,
          }
        )
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
      const clamped = clampBoundsToDisplay(restored, displayPlacementRect(display));
      if (!clamped) return;
      if (isMaximizedOrFullScreen()) {
        // setBounds would clobber the maximized/full-screen state (or be
        // ignored): defer until the window returns to its normal state.
        // `fromBounds` records the placement at deferral time so a later
        // re-apply can tell an untouched window from a user-replaced one.
        pendingRecovery = {
          bounds: clamped,
          displayId: normalizeDisplayId(display?.id) ?? restoredDisplayId,
          fromBounds: currentBounds,
        };
        return;
      }
      win.setBounds(clamped);
    } catch {
      // Never let display churn break the window.
    }
  };

  const onDisplayMetricsChanged = (event, display) =>
    onDisplayAdded(event, display, true);

  const onSuspend = () => onSessionInterrupted("suspend");
  const onLockScreen = () => onSessionInterrupted("lock-screen");
  const onResume = () => onSessionResumed("suspend");
  const onUnlockScreen = () => onSessionResumed("lock-screen");

  try {
    activePowerMonitor?.on?.("suspend", onSuspend);
    activePowerMonitor?.on?.("lock-screen", onLockScreen);
    activePowerMonitor?.on?.("resume", onResume);
    activePowerMonitor?.on?.("unlock-screen", onUnlockScreen);
  } catch {
    // Suspension/lock tracking is best-effort; the grace window still applies.
  }

  try {
    screen.on("display-removed", onDisplayRemoved);
    screen.on("display-added", onDisplayAdded);
    screen.on("display-metrics-changed", onDisplayMetricsChanged);
    win.on?.("will-move", onManualPlacement);
    win.on?.("will-resize", onManualPlacement);
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

  try {
    win.webContents?.on?.("before-input-event", onBeforeInputEvent);
  } catch {
    // Keyboard intent tracking is best-effort; display recovery still works.
  }

  return function detach() {
    attached = false;
    try {
      activePowerMonitor?.removeListener?.("suspend", onSuspend);
      activePowerMonitor?.removeListener?.("lock-screen", onLockScreen);
      activePowerMonitor?.removeListener?.("resume", onResume);
      activePowerMonitor?.removeListener?.("unlock-screen", onUnlockScreen);
    } catch {}
    try {
      screen.removeListener?.("display-removed", onDisplayRemoved);
    } catch {}
    try {
      screen.removeListener?.("display-added", onDisplayAdded);
    } catch {}
    try {
      screen.removeListener?.("display-metrics-changed", onDisplayMetricsChanged);
    } catch {}
    try {
      win.removeListener?.("will-move", onManualPlacement);
    } catch {}
    try {
      win.removeListener?.("will-resize", onManualPlacement);
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
    try {
      win.webContents?.removeListener?.(
        "before-input-event",
        onBeforeInputEvent
      );
    } catch {}
    clearRecoveryCandidates();
    activeInterruptionSignals.clear();
    sessionInterruptionActive = false;
    sessionInterruptionEndedAt = null;
  };
}

module.exports = {
  attachDisplayRecovery,
  boundsIntersectDisplay,
  clampBoundsToDisplay,
  isFiniteBounds,
  pickDisplayRecoveryBounds,
};
