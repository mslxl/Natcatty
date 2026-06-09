import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const storage = new Map<string, string>();
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  },
});

const {
  getTerminalHostTreeInitialLayoutWidth,
  getTerminalHostTreeLayoutTargetWidth,
  getTerminalHostTreeMeasuredLayoutWidth,
  getTerminalHostTreeSidebarPanelStyle,
  getTerminalHostTreeSidebarShellStyle,
  isTerminalHostTreeSidebarVisible,
} = await import('./TerminalHostTreeSidebar.tsx');
const { TERMINAL_HOST_TREE_WIDTH_TRANSITION } = await import('../../application/state/terminalHostTreeAnimation.ts');

test('host tree sidebar is visually hidden when disabled even if it remains open', () => {
  assert.equal(isTerminalHostTreeSidebarVisible(true, false), false);
});

test('host tree sidebar visibility still follows open state when enabled', () => {
  assert.equal(isTerminalHostTreeSidebarVisible(true, true), true);
  assert.equal(isTerminalHostTreeSidebarVisible(false, true), false);
});

test('host tree sidebar stays collapsed behind root pages', () => {
  assert.equal(isTerminalHostTreeSidebarVisible(true, true, false), false);
});

test('host tree layout target follows visible surface state', () => {
  assert.equal(getTerminalHostTreeLayoutTargetWidth(true, 240), 240);
  assert.equal(getTerminalHostTreeLayoutTargetWidth(false, 240), 0);
});

test('host tree layout starts collapsed so first mount can animate open', () => {
  assert.equal(getTerminalHostTreeInitialLayoutWidth(), 0);
});

test('host tree layout sync can sample the current shell width before targeting', () => {
  assert.equal(getTerminalHostTreeMeasuredLayoutWidth({
    getBoundingClientRect: () => ({ width: 84 }),
  } as unknown as HTMLElement, 240), 84);
  assert.equal(getTerminalHostTreeMeasuredLayoutWidth({
    getBoundingClientRect: () => ({ width: -12 }),
  } as unknown as HTMLElement, 240), 0);
  assert.equal(getTerminalHostTreeMeasuredLayoutWidth(null, 240), 240);
});

test('host tree layout width follows the animated shell via ResizeObserver', () => {
  const source = readFileSync(new URL('./TerminalHostTreeSidebar.tsx', import.meta.url), 'utf8');

  assert.match(source, /new ResizeObserver/);
  assert.match(source, /syncLayoutWidthFromShell/);
  assert.doesNotMatch(source, /performance\.now\(\)/);
});

test('host tree collapses instantly when hidden behind root pages', () => {
  const source = readFileSync(new URL('./TerminalHostTreeSidebar.tsx', import.meta.url), 'utf8');

  assert.match(source, /isResizing \|\| !surfaceVisible/);
  assert.match(source, /if \(!surfaceVisible\) \{\s*setShellWidth\(0\);\s*terminalHostTreeStore\.setLayoutWidth\(0\);/);
});

test('host tree sidebar memo tracks surface visibility changes', () => {
  const source = readFileSync(new URL('./TerminalHostTreeSidebar.tsx', import.meta.url), 'utf8');

  assert.match(source, /prev\.surfaceVisible === next\.surfaceVisible/);
});

test('host tree sidebar clips the panel instead of fading it out while closing', () => {
  const theme = {
    termBg: '#000000',
    termFg: '#ffffff',
    mutedFg: '#999999',
    separator: '#333333',
    rowHoverBg: '#111111',
    rowActiveBg: '#222222',
    rowDropBg: '#444444',
    folderFg: '#cccccc',
  };

  assert.deepEqual(getTerminalHostTreeSidebarShellStyle(false, 0, TERMINAL_HOST_TREE_WIDTH_TRANSITION), {
    width: 0,
    transition: TERMINAL_HOST_TREE_WIDTH_TRANSITION,
    pointerEvents: 'none',
  });
  assert.equal(getTerminalHostTreeSidebarPanelStyle({
    isVisible: false,
    displayWidth: 240,
    panelTransition: 'border-color 220ms ease-out',
    theme,
  }).width, 240);
  assert.equal(getTerminalHostTreeSidebarPanelStyle({
    isVisible: false,
    displayWidth: 240,
    panelTransition: 'border-color 220ms ease-out',
    theme,
  }).opacity, 1);
});
