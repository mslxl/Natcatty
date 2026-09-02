"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  attachDisplayRecovery,
  boundsIntersectDisplay,
  clampBoundsToDisplay,
  pickDisplayRecoveryBounds,
} = require("./displayRecovery.cjs");

const PRIMARY = { id: 1, bounds: { x: 0, y: 0, width: 1920, height: 1080 } };
const SECONDARY = { id: 2, bounds: { x: 1920, y: 0, width: 2560, height: 1440 } };

function createMockWindow(initialBounds) {
  const listeners = new Map();
  const win = {
    bounds: { ...initialBounds },
    destroyed: false,
    maximized: false,
    fullScreen: false,
    setBoundsCalls: [],
    isDestroyed() {
      return win.destroyed;
    },
    isMaximized() {
      return win.maximized;
    },
    isFullScreen() {
      return win.fullScreen;
    },
    getBounds() {
      return { ...win.bounds };
    },
    getNormalBounds() {
      return { ...(win.normalBounds || win.bounds) };
    },
    unmaximize() {
      win.maximized = false;
      for (const handler of listeners.get("unmaximize") || []) handler();
    },
    setBounds(next) {
      win.setBoundsCalls.push({ ...next });
      win.bounds = { ...next };
      for (const handler of listeners.get("move") || []) handler();
    },
    on(event, handler) {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event).push(handler);
    },
    removeListener(event, handler) {
      const list = listeners.get(event) || [];
      const index = list.indexOf(handler);
      if (index >= 0) list.splice(index, 1);
    },
    __listeners: listeners,
  };
  return win;
}

function createMockScreen({ primary = PRIMARY, displays = [PRIMARY, SECONDARY] } = {}) {
  const listeners = new Map();
  const connected = [...displays];
  const mock = {
    on(event, handler) {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event).push(handler);
    },
    removeListener(event, handler) {
      const list = listeners.get(event) || [];
      const index = list.indexOf(handler);
      if (index >= 0) list.splice(index, 1);
    },
    emit(event, ...args) {
      // Mirror Electron: the display list changes when these events fire.
      const display = args[1] || args[0];
      if (event === "display-removed" && display) {
        const index = connected.findIndex((candidate) => candidate.id === display.id);
        if (index >= 0) connected.splice(index, 1);
      }
      if (event === "display-added" && display) {
        if (!connected.some((candidate) => candidate.id === display.id)) connected.push(display);
      }
      // Mirror Electron: screen event listeners receive (event, display).
      for (const handler of listeners.get(event) || []) handler(...args);
    },
    getPrimaryDisplay() {
      return primary;
    },
    getAllDisplays() {
      return [...connected];
    },
    getDisplayMatching(bounds) {
      let best = null;
      let bestArea = 0;
      for (const display of connected) {
        const overlap = boundsIntersectDisplay(bounds, display.bounds)
          ? Math.min(bounds.x + bounds.width, display.bounds.x + display.bounds.width) -
            Math.max(bounds.x, display.bounds.x)
          : 0;
        if (overlap > bestArea) {
          bestArea = overlap;
          best = display;
        }
      }
      return best || connected[0];
    },
    __listeners: listeners,
  };
  return mock;
}

test("boundsIntersectDisplay detects overlap and rejects invalid input", () => {
  assert.equal(boundsIntersectDisplay({ x: 2000, y: 100, width: 800, height: 600 }, SECONDARY.bounds), true);
  assert.equal(boundsIntersectDisplay({ x: 0, y: 0, width: 800, height: 600 }, SECONDARY.bounds), false);
  assert.equal(boundsIntersectDisplay(null, SECONDARY.bounds), false);
  assert.equal(boundsIntersectDisplay({ x: 10, y: 10, width: 0, height: 100 }, PRIMARY.bounds), false);
});

test("pickDisplayRecoveryBounds restores a remembered placement on the re-added display", () => {
  const restored = pickDisplayRecoveryBounds({
    addedDisplay: SECONDARY,
    currentBounds: { x: 100, y: 100, width: 1200, height: 800 },
    candidates: [{ x: 2000, y: 100, width: 1200, height: 800 }],
  });
  assert.deepEqual(restored, { x: 2000, y: 100, width: 1200, height: 800 });
});

test("pickDisplayRecoveryBounds does nothing when the window is already on the display", () => {
  const restored = pickDisplayRecoveryBounds({
    addedDisplay: SECONDARY,
    currentBounds: { x: 2000, y: 100, width: 1200, height: 800 },
    candidates: [{ x: 2100, y: 100, width: 1200, height: 800 }],
  });
  assert.equal(restored, null);
});

test("pickDisplayRecoveryBounds ignores candidates on other displays", () => {
  const restored = pickDisplayRecoveryBounds({
    addedDisplay: SECONDARY,
    currentBounds: { x: 100, y: 100, width: 1200, height: 800 },
    candidates: [{ x: 50, y: 50, width: 1200, height: 800 }],
  });
  assert.equal(restored, null);
});

test("clampBoundsToDisplay keeps the restored window fully visible", () => {
  const clamped = clampBoundsToDisplay(
    { x: 3000, y: -200, width: 3000, height: 2000 },
    SECONDARY.bounds
  );
  assert.deepEqual(clamped, { x: 1920, y: 0, width: 2560, height: 1440 });
});

test("attachDisplayRecovery moves the window back after lock/unlock display churn", () => {
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });

  // User is working with the window on the secondary display: placement gets tracked.
  win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Lock: the secondary display disappears.
  screen.emit("display-removed", {}, SECONDARY);
  // Windows relocates the window to the primary display.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Unlock: the display comes back.
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });
});

test("attachDisplayRecovery clears the remembered placement when the user moves to the primary", () => {
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen();

  // A deliberate user move happens well before any display churn, so the
  // teardown grace window is disabled for this scenario.
  attachDisplayRecovery({ win, screen, teardownGraceMs: 0 });

  // The user deliberately moves the window to the primary display while the
  // secondary display is still connected.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Later the secondary display is torn down and re-added (lock cycle or
  // unplug/replug): the stale secondary placement must not be restored.
  screen.emit("display-removed", {}, SECONDARY);
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery preserves the fallback when the OS relocates the window before display-removed", () => {
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });

  // User is working with the window on the secondary display: placement gets tracked.
  win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Teardown: Windows relocates the window to the primary display BEFORE
  // Electron emits "display-removed" (the secondary is still connected when
  // the relocation fires, so the move looks like a user move).
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);

  // Unlock: the display comes back and the window must be restored.
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });
});

test("attachDisplayRecovery keeps the snapshot when unrelated secondary displays are removed later", () => {
  const TERTIARY = { id: 3, bounds: { x: -1920, y: 0, width: 1920, height: 1080 } };
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen({ displays: [PRIMARY, SECONDARY, TERTIARY] });

  attachDisplayRecovery({ win, screen });

  // User is working with the window on the secondary display: placement gets tracked.
  win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Teardown: Windows relocates the window to the primary BEFORE the
  // "display-removed" event for the window's own display fires.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  // A second secondary display is removed during the same teardown: this
  // unrelated removal must not overwrite the snapshot for SECONDARY.
  screen.emit("display-removed", {}, TERTIARY);

  // Unlock: the window's display comes back and the window must be restored.
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });
});

test("attachDisplayRecovery clears the removal-time snapshot once it is consumed", () => {
  const realNow = Date.now;
  let now = 1_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();

    attachDisplayRecovery({ win, screen });

    // The window lives on the secondary display, then teardown relocates it to
    // the primary before "display-removed" fires (within the grace window).
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-removed", {}, SECONDARY);

    // Recovery: the display returns and the window is restored.
    screen.emit("display-added", {}, SECONDARY);
    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });

    // The user later deliberately moves the window back to the primary
    // display, well past the teardown grace window, and the display is torn
    // down and re-added again: the already-consumed snapshot must not restore
    // the old placement against the user's latest move.
    now += 60_000;
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    now += 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery drops the removal snapshot after the user moves to another secondary display", () => {
  const TERTIARY = { id: 3, bounds: { x: -1920, y: 0, width: 1920, height: 1080 } };
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen({ displays: [PRIMARY, SECONDARY, TERTIARY] });

  attachDisplayRecovery({ win, screen, teardownGraceMs: 0 });

  // User is working with the window on the secondary display: placement gets tracked.
  win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // The secondary display disappears and the OS relocates the window to the
  // primary display, capturing a removal-time snapshot for SECONDARY.
  screen.emit("display-removed", {}, SECONDARY);
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // The user then deliberately moves the relocated window onto the
  // still-connected third display: the stale SECONDARY snapshot must be
  // dropped so a later re-add of SECONDARY cannot yank the window back.
  win.bounds = { x: -1800, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // The removed display comes back: the user's placement on TERTIARY stands.
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery defers recovery while maximized and applies it on unmaximize", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  win.maximized = true;
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  // The window stays maximized while the display is missing and returns:
  // recovery must not be lost, only deferred.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);

  // Leaving the maximized state applies the deferred recovery.
  win.unmaximize();

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2000, y: 100, width: 1400, height: 900 });
});

test("attachDisplayRecovery does not claim a window split evenly across the removed display", () => {
  // Exactly half of the window sits on the primary display and half on the
  // removed secondary one (the 1920 boundary splits it down the middle): the
  // ownership tie must be treated as ambiguous, so no snapshot is taken and
  // re-adding the display does not move the window.
  const win = createMockWindow({ x: 1720, y: 100, width: 400, height: 300 });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();

  screen.emit("display-removed", {}, SECONDARY);
  win.bounds = { x: 100, y: 100, width: 400, height: 300 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery does nothing when the window never left the primary display", () => {
  const win = createMockWindow({ x: 100, y: 100, width: 1200, height: 800 });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery does not claim a primary-display window that merely overlaps the removed display", () => {
  // The window lives primarily on the primary display but its right edge
  // overlaps the secondary display.
  const overlappingBounds = { x: 1700, y: 100, width: 400, height: 300 };
  const win = createMockWindow({ ...overlappingBounds });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();

  // The secondary display is torn down and the OS moves the window fully onto
  // the primary display: the removal-time snapshot must not claim the window
  // for the secondary display, since it primarily lived on the primary.
  screen.emit("display-removed", {}, SECONDARY);
  win.bounds = { x: 100, y: 100, width: 400, height: 300 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // When the secondary display returns, the user's primary-display placement
  // must stand: no recovery move.
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("detach removes all listeners and stops recovery", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  const screen = createMockScreen();

  const detach = attachDisplayRecovery({ win, screen });
  detach();

  assert.equal((screen.__listeners.get("display-removed") || []).length, 0);
  assert.equal((screen.__listeners.get("display-added") || []).length, 0);
  assert.equal((win.__listeners.get("move") || []).length, 0);
  assert.equal((win.__listeners.get("resize") || []).length, 0);
  assert.equal((win.__listeners.get("unmaximize") || []).length, 0);
  assert.equal((win.__listeners.get("leave-full-screen") || []).length, 0);

  // Events after detach must not move the window.
  screen.emit("display-removed", {}, SECONDARY);
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  screen.emit("display-added", {}, SECONDARY);
  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery tolerates a missing screen module", () => {
  const win = createMockWindow({ x: 100, y: 100, width: 1200, height: 800 });
  const detach = attachDisplayRecovery({ win, screen: null });
  assert.equal(typeof detach, "function");
  detach();
});
