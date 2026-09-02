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
  return {
    on(event, handler) {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event).push(handler);
    },
    removeListener(event, handler) {
      const list = listeners.get(event) || [];
      const index = list.indexOf(handler);
      if (index >= 0) list.splice(index, 1);
    },
    emit(event, payload) {
      for (const handler of listeners.get(event) || []) handler(payload);
    },
    getPrimaryDisplay() {
      return primary;
    },
    getDisplayMatching(bounds) {
      let best = null;
      let bestArea = 0;
      for (const display of displays) {
        const overlap = boundsIntersectDisplay(bounds, display.bounds)
          ? Math.min(bounds.x + bounds.width, display.bounds.x + display.bounds.width) -
            Math.max(bounds.x, display.bounds.x)
          : 0;
        if (overlap > bestArea) {
          bestArea = overlap;
          best = display;
        }
      }
      return best || displays[0];
    },
    __listeners: listeners,
  };
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
  screen.emit("display-removed", SECONDARY);
  // Windows relocates the window to the primary display.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Unlock: the display comes back.
  screen.emit("display-added", SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });
});

test("attachDisplayRecovery leaves the window alone while maximized", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  win.maximized = true;
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", SECONDARY);
  // The window stays maximized while the display is missing and returns.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery does nothing when the window never left the primary display", () => {
  const win = createMockWindow({ x: 100, y: 100, width: 1200, height: 800 });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", SECONDARY);
  screen.emit("display-added", SECONDARY);

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

  // Events after detach must not move the window.
  screen.emit("display-removed", SECONDARY);
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  screen.emit("display-added", SECONDARY);
  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery tolerates a missing screen module", () => {
  const win = createMockWindow({ x: 100, y: 100, width: 1200, height: 800 });
  const detach = attachDisplayRecovery({ win, screen: null });
  assert.equal(typeof detach, "function");
  detach();
});
