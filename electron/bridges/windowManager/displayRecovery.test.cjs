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

function createMockPowerMonitor() {
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
    emit(event) {
      for (const handler of listeners.get(event) || []) handler();
    },
    __listeners: listeners,
  };
}

function moveWindowManually(win, nextBounds) {
  for (const handler of win.__listeners.get("will-move") || []) {
    handler({}, { ...nextBounds });
  }
  win.bounds = { ...nextBounds };
  for (const handler of win.__listeners.get("move") || []) handler();
}

function resizeWindowManually(win, nextBounds) {
  for (const handler of win.__listeners.get("will-resize") || []) {
    handler({}, { ...nextBounds }, { edge: "bottom-right" });
  }
  win.bounds = { ...nextBounds };
  for (const handler of win.__listeners.get("resize") || []) handler();
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

test("pickDisplayRecoveryBounds matches a candidate by display identity when bounds changed", () => {
  // The display came back with different bounds (e.g. DPI/resolution or
  // topology change): the remembered geometry no longer intersects it, but
  // the candidate is still valid because it was remembered for this display.
  const restored = pickDisplayRecoveryBounds({
    addedDisplay: { id: 2, bounds: { x: 1920, y: 0, width: 1024, height: 768 } },
    currentBounds: { x: 100, y: 100, width: 1200, height: 800 },
    candidates: [{ bounds: { x: 2000, y: 100, width: 1200, height: 800 }, displayId: 2 }],
  });
  assert.deepEqual(restored, { x: 2000, y: 100, width: 1200, height: 800 });
});

test("pickDisplayRecoveryBounds does not match a candidate remembered for another display", () => {
  const restored = pickDisplayRecoveryBounds({
    addedDisplay: { id: 3, bounds: { x: 1920, y: 0, width: 1024, height: 768 } },
    currentBounds: { x: 100, y: 100, width: 1200, height: 800 },
    // Non-intersecting bounds remembered for a different display id: neither
    // the identity nor the geometry matches, so nothing is restored.
    candidates: [{ bounds: { x: 5000, y: 5000, width: 400, height: 300 }, displayId: 2 }],
  });
  assert.equal(restored, null);
});

test("pickDisplayRecoveryBounds does not match a tagged candidate by geometry alone", () => {
  const restored = pickDisplayRecoveryBounds({
    // Display 3 re-appears first with bounds overlapping the geometry that
    // was remembered for display 2: the identity mismatch must win over the
    // geometry match, or the window would be dragged onto the wrong display
    // before its real owner returns.
    addedDisplay: { id: 3, bounds: { x: 1920, y: 0, width: 2560, height: 1440 } },
    currentBounds: { x: 100, y: 100, width: 1200, height: 800 },
    candidates: [{ bounds: { x: 2000, y: 100, width: 1400, height: 900 }, displayId: 2 }],
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

test("attachDisplayRecovery restores the placement when the display returns with changed bounds", () => {
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });

  // The window is tracked on the secondary display (never moved, so only the
  // seeded placement exists).
  // Lock: the secondary display disappears.
  screen.emit("display-removed", {}, SECONDARY);

  // Unlock: the display comes back with different bounds (DPI/resolution or
  // topology change), and the window still sits on the secondary's old
  // coordinates, which no longer intersect the new ones. The remembered
  // placement must still be restored (clamped into the new bounds).
  const changedSecondary = { id: SECONDARY.id, bounds: { x: -1024, y: 0, width: 1024, height: 768 } };
  screen.emit("display-added", {}, changedSecondary);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: -1024, y: 0, width: 1024, height: 768 });
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

test("attachDisplayRecovery keeps the promoted snapshot through trailing teardown-burst events", () => {
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen });

  // User is working with the window on the secondary display: placement gets tracked.
  win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();

  // Teardown: Windows relocates the window to the primary BEFORE
  // "display-removed" fires (the secondary is still connected, so the move
  // is stashed as a pending teardown move).
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  // The removal promotes the pending move to the removal-time snapshot.
  screen.emit("display-removed", {}, SECONDARY);
  // A trailing "resize" of the same relocation burst fires after the
  // removal: it must not clear the promoted snapshot.
  win.bounds = { x: 100, y: 100, width: 1400, height: 898 };
  for (const handler of win.__listeners.get("resize") || []) handler();

  // Unlock: the display comes back and the window must be restored.
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });
});

test("attachDisplayRecovery keeps the promoted snapshot through a delayed lock-screen teardown burst", () => {
  const realNow = Date.now;
  let now = 1_500_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerMonitor = createMockPowerMonitor();

    attachDisplayRecovery({ win, screen, powerMonitor });

    // During Win+L teardown, Windows relocates the window before Electron
    // reports the display removal. The removal promotes the secondary bounds.
    powerMonitor.emit("lock-screen");
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-removed", {}, SECONDARY);

    // The session remains locked while a delayed resize from the same OS
    // relocation arrives well after the ordinary teardown grace window.
    now += 60_000;
    win.bounds = { x: 100, y: 100, width: 1200, height: 800 };
    for (const handler of win.__listeners.get("resize") || []) handler();

    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], secondaryBounds);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the removal snapshot through a delayed lock-screen relocation burst", () => {
  const realNow = Date.now;
  let now = 1_750_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerMonitor = createMockPowerMonitor();

    attachDisplayRecovery({ win, screen, powerMonitor });

    // In the opposite valid ordering, Electron reports the display removal
    // while the window still has its secondary placement, then Windows moves
    // it onto the primary display.
    powerMonitor.emit("lock-screen");
    screen.emit("display-removed", {}, SECONDARY);
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // A delayed resize is still part of the OS relocation because the session
    // remains locked, even though the normal grace window has elapsed.
    now += 60_000;
    win.bounds = { x: 100, y: 100, width: 1200, height: 800 };
    for (const handler of win.__listeners.get("resize") || []) handler();

    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], secondaryBounds);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery lets an immediate post-unlock user edit invalidate recovery", () => {
  const realNow = Date.now;
  let now = 1_900_000;
  Date.now = () => now;
  try {
    const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
    const screen = createMockScreen();
    const powerMonitor = createMockPowerMonitor();

    attachDisplayRecovery({ win, screen, powerMonitor });

    powerMonitor.emit("lock-screen");
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-removed", {}, SECONDARY);

    // Once the session unlocks, the user can deliberately re-place the
    // relocated window. That edit must win even when it happens immediately
    // after unlock and before the monitor returns.
    powerMonitor.emit("unlock-screen");
    moveWindowManually(win, { x: 300, y: 200, width: 1200, height: 800 });
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 0);
    assert.deepEqual(win.bounds, { x: 300, y: 200, width: 1200, height: 800 });
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery does not promote an immediate post-unlock user move", () => {
  const realNow = Date.now;
  let now = 1_950_000;
  Date.now = () => now;
  try {
    const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
    const screen = createMockScreen();
    const powerMonitor = createMockPowerMonitor();

    attachDisplayRecovery({ win, screen, powerMonitor });

    // The interruption ends before the display is removed. An immediate move
    // onto the primary is therefore a real user edit, not an OS relocation
    // racing ahead of display-removed, even though it falls inside the usual
    // teardown grace window.
    powerMonitor.emit("lock-screen");
    powerMonitor.emit("unlock-screen");
    moveWindowManually(win, { x: 300, y: 200, width: 1200, height: 800 });
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 0);
    assert.deepEqual(win.bounds, { x: 300, y: 200, width: 1200, height: 800 });
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery lets an immediate post-unlock manual resize cancel recovery", () => {
  const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
  const win = createMockWindow({ ...secondaryBounds });
  const screen = createMockScreen();
  const powerMonitor = createMockPowerMonitor();

  attachDisplayRecovery({ win, screen, powerMonitor });

  powerMonitor.emit("lock-screen");
  screen.emit("display-removed", {}, SECONDARY);
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  powerMonitor.emit("unlock-screen");

  resizeWindowManually(win, { x: 100, y: 100, width: 1200, height: 800 });
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
  assert.deepEqual(win.bounds, { x: 100, y: 100, width: 1200, height: 800 });
});

test("attachDisplayRecovery keeps a removal snapshot through a queued post-unlock OS move", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  const screen = createMockScreen();
  const powerMonitor = createMockPowerMonitor();

  attachDisplayRecovery({ win, screen, powerMonitor });

  powerMonitor.emit("lock-screen");
  screen.emit("display-removed", {}, SECONDARY);
  powerMonitor.emit("unlock-screen");

  // This regular move has no matching will-move because Windows queued it as
  // part of the display teardown rather than the user moving the window.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], {
    x: 2000,
    y: 100,
    width: 1400,
    height: 900,
  });
});

test("attachDisplayRecovery promotes a queued post-unlock OS move when removal follows", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  const screen = createMockScreen();
  const powerMonitor = createMockPowerMonitor();

  attachDisplayRecovery({ win, screen, powerMonitor });

  powerMonitor.emit("lock-screen");
  powerMonitor.emit("unlock-screen");

  // Windows can deliver the queued relocation before Electron reports that
  // the display disappeared. It is still a system move, not manual intent.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], {
    x: 2000,
    y: 100,
    width: 1400,
    height: 900,
  });
});

test("attachDisplayRecovery drops the promoted snapshot for user edits after the grace window", () => {
  const realNow = Date.now;
  let now = 2_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();

    attachDisplayRecovery({ win, screen });

    // Teardown relocation before "display-removed": the pending move is
    // promoted to the removal-time snapshot.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-removed", {}, SECONDARY);

    // Well after the grace window, the user deliberately re-places the
    // window on the primary: the stale snapshot must be dropped.
    now += 10_000;
    win.bounds = { x: 300, y: 300, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 0);
  } finally {
    Date.now = realNow;
  }
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

test("attachDisplayRecovery drops the stale recovery when the user edits the relocated window later", () => {
  const realNow = Date.now;
  let now = 2_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();

    attachDisplayRecovery({ win, screen });

    // The window lives on the secondary display, then teardown removes the
    // display from the list and relocates the window to the primary.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-removed", {}, SECONDARY);
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // While the monitor stays disconnected, the user deliberately moves the
    // relocated window well past the teardown grace window: the stale
    // secondary placement must not be restored when the display re-appears.
    now += 60_000;
    win.bounds = { x: 150, y: 150, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    now += 60_000;
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 0);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the snapshot when the teardown relocation also resizes", () => {
  const realNow = Date.now;
  let now = 3_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();

    attachDisplayRecovery({ win, screen });

    // The window lives on the secondary display, then teardown removes the
    // display from the list and relocates the window to the primary.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    screen.emit("display-removed", {}, SECONDARY);
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // The same relocation burst also emits a "resize" (well within the
    // teardown grace window): it must not be mistaken for a user edit and
    // must not invalidate the pre-teardown placement.
    win.bounds = { x: 100, y: 100, width: 1200, height: 800 };
    for (const handler of win.__listeners.get("resize") || []) handler();

    // Unlock: the display comes back and the window must be restored.
    now += 60_000;
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });
  } finally {
    Date.now = realNow;
  }
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

test("attachDisplayRecovery keeps the pending teardown snapshot across a suspension delay", () => {
  const realNow = Date.now;
  let now = 2_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
      __listeners: powerListeners,
    };

    const detach = attachDisplayRecovery({ win, screen, powerMonitor });

    // The window lives on the secondary display, then the OS relocates it to
    // the primary before "display-removed" fires (within the grace window).
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // The machine suspends right after the relocation. On wake the wall
    // clock has advanced far past the teardown grace window — but the jump
    // happened while the machine was asleep, so the pending snapshot must
    // not be expired because of it.
    powerMonitor.emit("suspend");
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);

    // Unlock: the display returns and the pre-relocation placement must
    // still be restored.
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    // The tracked placement was last updated by the pre-teardown move that
    // still happened on the secondary display (2100,120), not by the OS
    // relocation to the primary.
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });

    // Detach must also unsubscribe the suspend listener.
    detach();
    assert.equal((powerListeners.get("suspend") || []).length, 0);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the pending teardown snapshot across a lock-screen delay (Win+L without suspend)", () => {
  const realNow = Date.now;
  let now = 3_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
    };

    const detach = attachDisplayRecovery({ win, screen, powerMonitor });

    // The window lives on the secondary display, then the OS relocates it to
    // the primary before "display-removed" fires (within the grace window).
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // Locking the session (Win+L) tears the display down without necessarily
    // emitting "suspend". The lock must count as a session interruption just
    // like a suspension: on unlock the wall clock is far past the grace
    // window, but the pending snapshot must not be expired because of it.
    powerMonitor.emit("lock-screen");
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });

    detach();
    assert.equal((powerListeners.get("lock-screen") || []).length, 0);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the pending teardown snapshot when the relocation follows the lock-screen event", () => {
  const realNow = Date.now;
  let now = 3_200_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
    };

    const detach = attachDisplayRecovery({ win, screen, powerMonitor });

    // The window lives on the secondary display.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // Windows may lock the session BEFORE relocating the window: the
    // lock-screen event fires first and the OS teardown relocation to the
    // primary lands afterwards. The pending move is then stamped as part of
    // the interruption even though its timestamp postdates the lock.
    powerMonitor.emit("lock-screen");
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // The "display-removed" event is delivered long after the relocation
    // (e.g. the machine went to sleep right after the lock), so the wall
    // clock is far past the teardown grace window. The snapshot must still
    // be promoted: the move belonged to the interruption, not to the user.
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });

    detach();
    assert.equal((powerListeners.get("lock-screen") || []).length, 0);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the pending teardown snapshot when a suspend follows the lock in the same interruption", () => {
  const realNow = Date.now;
  let now = 3_400_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
      __listeners: powerListeners,
    };

    const detach = attachDisplayRecovery({ win, screen, powerMonitor });

    // The window lives on the secondary display, then the OS relocates it to
    // the primary right after the lock (Win+L).
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    powerMonitor.emit("lock-screen");
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // More than the grace window after the relocation (but within the same
    // lock/sleep cycle) the machine actually suspends. The repeated
    // interruption signal must not clear the pending move recorded for this
    // cycle — the session was locked, so the user cannot have moved the
    // window in between.
    now += 60 * 60_000;
    powerMonitor.emit("suspend");

    // "display-removed" is delivered after the suspend, sees only the
    // relocated primary bounds, and must still promote the pending snapshot.
    now += 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });

    // After a resume the interruption is over: a later lock starts a new one
    // and must again expire pending moves that went stale while the session
    // was running.
    powerMonitor.emit("resume");
    assert.equal((powerListeners.get("suspend") || []).length, 1);

    detach();
    assert.equal((powerListeners.get("suspend") || []).length, 0);
    assert.equal((powerListeners.get("lock-screen") || []).length, 0);
    assert.equal((powerListeners.get("resume") || []).length, 0);
    assert.equal((powerListeners.get("unlock-screen") || []).length, 0);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the pending teardown snapshot when resume fires while the session is still locked", () => {
  const realNow = Date.now;
  let now = 3_600_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
      __listeners: powerListeners,
    };

    attachDisplayRecovery({ win, screen, powerMonitor });

    // Win+L locks the session and the machine then suspends. On wake the
    // screen is still locked: "resume" fires while "unlock-screen" has not.
    powerMonitor.emit("lock-screen");
    powerMonitor.emit("suspend");
    powerMonitor.emit("resume");

    // The OS teardown relocation lands after the resume, while the session
    // is still locked — the user cannot have moved the window.
    now += 60_000;
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // The matching "display-removed" arrives outside the grace window. The
    // interruption must still count as active (the session is locked), so
    // the pre-relocation placement is promoted and restored on re-add.
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], secondaryBounds);
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery drops a pending teardown move that predates the interruption", () => {
  const realNow = Date.now;
  let now = 3_500_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
    };

    attachDisplayRecovery({ win, screen, powerMonitor });

    // The user deliberately moves the window from the secondary display to
    // the primary, well before any session interruption: this supersedes the
    // remembered secondary placement.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // Much later the user locks the session (Win+L). The pending move is
    // stale by now — it was a deliberate user edit, not the OS's pre-teardown
    // relocation — so it must not be promoted by the "display-removed" event
    // that fires during the lock.
    now += 60_000;
    powerMonitor.emit("lock-screen");
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    // The user's primary-display placement must survive the teardown and
    // re-addition of the secondary display.
    assert.equal(win.setBoundsCalls.length, 0);
    assert.deepEqual(win.bounds, { x: 100, y: 100, width: 1400, height: 900 });
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery keeps the pending snapshot for a relocation that lands long after the lock", () => {
  const realNow = Date.now;
  let now = 3_600_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
    };

    const detach = attachDisplayRecovery({ win, screen, powerMonitor });

    // The window lives on the secondary display.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // Win+L locks the session; the OS teardown relocation to the primary
    // lands long after the lock event (the teardown itself takes longer
    // than the grace window) while the session is still locked.
    powerMonitor.emit("lock-screen");
    now += 60_000;
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();

    // The matching "display-removed" is delivered much later still (the
    // machine went to sleep right after the lock): every elapsed-time check
    // fails, but the move was recorded while the interruption was active —
    // the session was locked, so the user cannot have moved the window —
    // and the pending snapshot must still be promoted.
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 1);
    assert.deepEqual(win.setBoundsCalls[0], { x: 2100, y: 120, width: 1400, height: 900 });

    detach();
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery drops the pending teardown move when the interruption ends without a removal", () => {
  const realNow = Date.now;
  let now = 3_700_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();
    const powerListeners = new Map();
    const powerMonitor = {
      on(event, handler) {
        if (!powerListeners.has(event)) powerListeners.set(event, []);
        powerListeners.get(event).push(handler);
      },
      removeListener(event, handler) {
        const list = powerListeners.get(event) || [];
        const index = list.indexOf(handler);
        if (index >= 0) list.splice(index, 1);
      },
      emit(event) {
        for (const handler of powerListeners.get(event) || []) handler();
      },
    };

    attachDisplayRecovery({ win, screen, powerMonitor });

    // The user deliberately moves the window from the secondary display to
    // the primary, then locks the session within the grace window — the
    // pending move is indistinguishable from a teardown relocation at this
    // point, so it is kept.
    win.bounds = { x: 2100, y: 120, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    powerMonitor.emit("lock-screen");

    // The display never disappears: the interruption ends without the
    // pending move ever being claimed by a "display-removed" event.
    now += 60_000;
    powerMonitor.emit("unlock-screen");

    // Much later the secondary display is unplugged ordinarily. The stale
    // pending move must not be promoted by the "interruption started after
    // the move" ordering check, and the re-added display must not drag the
    // window back to the superseded placement.
    now += 60 * 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 0);
    assert.deepEqual(win.bounds, { x: 100, y: 100, width: 1400, height: 900 });
  } finally {
    Date.now = realNow;
  }
});

test("attachDisplayRecovery drops the removal snapshot when the returning display already holds the window", () => {
  const realNow = Date.now;
  let now = 4_000_000;
  Date.now = () => now;
  try {
    const secondaryBounds = { x: 2000, y: 100, width: 1400, height: 900 };
    const win = createMockWindow({ ...secondaryBounds });
    const screen = createMockScreen();

    attachDisplayRecovery({ win, screen });

    // The window lives on the secondary display, then the display disappears
    // (a removal-time snapshot is captured) and re-appears before the window
    // coordinates stop intersecting it: recovery is a no-op, but the stale
    // removal snapshot must still be invalidated.
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);
    assert.equal(win.setBoundsCalls.length, 0);

    // The user then deliberately moves the window to the primary display
    // (still inside the snapshot's teardown grace window, so the grace
    // protection alone cannot clear it), and that display is later torn down
    // and re-added again: the stale snapshot must not restore the old
    // placement over the user's newer choice.
    win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
    for (const handler of win.__listeners.get("move") || []) handler();
    now += 60_000;
    screen.emit("display-removed", {}, SECONDARY);
    screen.emit("display-added", {}, SECONDARY);

    assert.equal(win.setBoundsCalls.length, 0);
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

test("attachDisplayRecovery keeps a deferred recovery queued when its display vanishes again", () => {
  // The deferred recovery is the only remaining candidate: the removal-time
  // snapshot was consumed when the recovery was deferred. If the display
  // vanishes again before the window leaves the maximized state and the user
  // unmaximizes while it is still gone, the next re-add must still restore.
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  win.maximized = true;
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen, teardownGraceMs: 0 });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  // The OS relocates the maximized window to the primary display; the
  // re-added display defers the recovery because the window stays maximized.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);
  assert.equal(win.setBoundsCalls.length, 0);

  // The display disappears once more while the window is still maximized.
  screen.emit("display-removed", {}, SECONDARY);
  // A later edit while the display stays disconnected supersedes the
  // tracked placement (past the grace window), leaving the deferred
  // recovery as the only remaining candidate.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  // The user unmaximizes while the display is absent: the deferred recovery
  // must survive instead of being discarded.
  win.unmaximize();
  assert.equal(win.setBoundsCalls.length, 0);

  // When the display returns, the deferred recovery is finally applied.
  screen.emit("display-added", {}, SECONDARY);
  assert.equal(win.setBoundsCalls.length, 1);
  assert.deepEqual(win.setBoundsCalls[0], { x: 2000, y: 100, width: 1400, height: 900 });
});

test("attachDisplayRecovery drops a deferred recovery when the user re-placed the window", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  win.maximized = true;
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen, teardownGraceMs: 0 });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);
  screen.emit("display-removed", {}, SECONDARY);
  win.unmaximize();

  // While the display is absent the user deliberately moves the window
  // somewhere else on the primary display: the stale deferred recovery must
  // not drag it back when the display re-appears.
  win.bounds = { x: 40, y: 60, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery drops a deferred recovery when the maximized window is moved to another monitor", () => {
  const win = createMockWindow({ x: 2000, y: 100, width: 1400, height: 900 });
  win.maximized = true;
  const screen = createMockScreen();

  attachDisplayRecovery({ win, screen, teardownGraceMs: 0 });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, SECONDARY);
  // The OS relocates the maximized window to the primary display; the
  // re-added display defers the recovery because the window stays maximized.
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, SECONDARY);
  assert.equal(win.setBoundsCalls.length, 0);

  // While still maximized, the user moves the window to another monitor
  // (e.g. Win+Shift+Arrow): getNormalBounds() changes but the deferred
  // recovery stays queued. Unmaximizing must not overwrite the newer
  // placement with the stale deferred recovery.
  win.normalBounds = { x: 40, y: 60, width: 1400, height: 900 };
  win.unmaximize();

  assert.equal(win.setBoundsCalls.length, 0);
});

test("attachDisplayRecovery clamps restored windows to the display work area", () => {
  // The re-added secondary display reserves a 40px top taskbar: restoring
  // with the full display bounds would place the window partly beneath it.
  const DOCKED = {
    id: 2,
    bounds: { x: 1920, y: 0, width: 2560, height: 1440 },
    workArea: { x: 1920, y: 40, width: 2560, height: 1400 },
  };
  const win = createMockWindow({ x: 2000, y: 20, width: 1400, height: 900 });
  const screen = createMockScreen({ displays: [PRIMARY, DOCKED] });

  attachDisplayRecovery({ win, screen });
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-removed", {}, DOCKED);
  win.bounds = { x: 100, y: 100, width: 1400, height: 900 };
  for (const handler of win.__listeners.get("move") || []) handler();
  screen.emit("display-added", {}, DOCKED);

  assert.equal(win.setBoundsCalls.length, 1);
  // The y coordinate is pulled down to the top of the work area instead of
  // the display bounds.
  assert.deepEqual(win.setBoundsCalls[0], { x: 2000, y: 40, width: 1400, height: 900 });
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
  assert.equal((win.__listeners.get("will-move") || []).length, 0);
  assert.equal((win.__listeners.get("will-resize") || []).length, 0);
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
