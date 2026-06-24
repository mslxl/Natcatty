"use strict";

const { SerializeAddon } = require("@xterm/addon-serialize");
const { Terminal: HeadlessTerminal } = require("@xterm/headless");

const DEFAULT_COLS = 80;
const DEFAULT_ROWS = 24;
const DEFAULT_SCROLLBACK = 3000;

/** @type {Map<string, { headless: import("@xterm/headless").Terminal, serializer: SerializeAddon, cols: number, rows: number }>} */
const mirrors = new Map();

function isAlternateScreenActive(headless) {
  return headless.buffer.active.type === "alternate";
}

function createMirror(cols, rows) {
  const headless = new HeadlessTerminal({
    cols,
    rows,
    scrollback: DEFAULT_SCROLLBACK,
    allowProposedApi: true,
  });
  const serializer = new SerializeAddon();
  headless.loadAddon(serializer);
  return { headless, serializer, cols, rows };
}

function ensureMirror(sessionId, cols = DEFAULT_COLS, rows = DEFAULT_ROWS) {
  let mirror = mirrors.get(sessionId);
  if (!mirror) {
    mirror = createMirror(cols, rows);
    mirrors.set(sessionId, mirror);
    return mirror;
  }
  if (mirror.cols !== cols || mirror.rows !== rows) {
    try {
      mirror.headless.resize(cols, rows);
    } catch {
      // Ignore resize failures on disposed mirrors.
    }
    mirror.cols = cols;
    mirror.rows = rows;
  }
  return mirror;
}

function writeMirror(sessionId, data, cols, rows) {
  if (!data) return;
  const mirror = ensureMirror(sessionId, cols, rows);
  mirror.headless.write(data);
}

function resizeMirror(sessionId, cols, rows) {
  if (!mirrors.has(sessionId)) return;
  const mirror = ensureMirror(sessionId, cols, rows);
  try {
    mirror.headless.resize(cols, rows);
  } catch {
    mirrors.delete(sessionId);
  }
}

function serializeMirror(sessionId) {
  const mirror = mirrors.get(sessionId);
  if (!mirror) {
    return { snapshot: "", alternateScreen: false };
  }
  const alternateScreen = isAlternateScreenActive(mirror.headless);
  try {
    const snapshot = mirror.serializer.serialize({
      excludeAltBuffer: !alternateScreen,
      excludeModes: !alternateScreen,
      ...(alternateScreen
        ? { range: { start: 0, end: Math.max(0, mirror.rows - 1) } }
        : {}),
    });
    return { snapshot, alternateScreen };
  } catch {
    return { snapshot: "", alternateScreen };
  }
}

function disposeMirror(sessionId) {
  const mirror = mirrors.get(sessionId);
  if (!mirror) return;
  try {
    mirror.headless.dispose();
  } catch {
    // Ignore dispose failures.
  }
  mirrors.delete(sessionId);
}

module.exports = {
  writeMirror,
  resizeMirror,
  serializeMirror,
  disposeMirror,
};
