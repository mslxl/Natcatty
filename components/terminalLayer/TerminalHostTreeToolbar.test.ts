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

const { TERMINAL_HOST_TREE_MIN_WIDTH } = await import('../../application/state/terminalHostTreeStore.ts');
const {
  shouldCompactTerminalHostTreeToolbar,
  shouldShowTerminalHostTreeExpandCollapseControls,
} = await import('./TerminalHostTreeToolbar.tsx');

test('shouldCompactTerminalHostTreeToolbar activates near the sidebar minimum width', () => {
  assert.equal(shouldCompactTerminalHostTreeToolbar(0), false);
  assert.equal(shouldCompactTerminalHostTreeToolbar(TERMINAL_HOST_TREE_MIN_WIDTH + 24), true);
  assert.equal(shouldCompactTerminalHostTreeToolbar(TERMINAL_HOST_TREE_MIN_WIDTH + 25), false);
});

test('shouldShowTerminalHostTreeExpandCollapseControls hides controls in compact mode', () => {
  assert.equal(
    shouldShowTerminalHostTreeExpandCollapseControls(3, false, false, true),
    false,
  );
  assert.equal(
    shouldShowTerminalHostTreeExpandCollapseControls(3, false, false, false),
    true,
  );
  assert.equal(
    shouldShowTerminalHostTreeExpandCollapseControls(0, false, false, false),
    false,
  );
  assert.equal(
    shouldShowTerminalHostTreeExpandCollapseControls(3, true, false, false),
    false,
  );
});

test('host tree toolbar keeps the close button outside the clipped action row', () => {
  const source = readFileSync(new URL('./TerminalHostTreeToolbar.tsx', import.meta.url), 'utf8');

  assert.match(source, /data-section="terminal-host-tree-toolbar-actions"/);
  assert.match(source, /data-section="terminal-host-tree-toolbar-close"/);
  assert.match(source, /data-section="terminal-host-tree-toolbar-actions-fade"/);
  assert.match(source, /linear-gradient\(to right, transparent, var\(--terminal-host-tree-bg/);
  assert.doesNotMatch(source, /flex-1 min-w-0" \/>/);
});

test('host tree sidebar passes compact toolbar state from display width', () => {
  const source = readFileSync(new URL('./TerminalHostTreeSidebar.tsx', import.meta.url), 'utf8');

  assert.match(source, /compactActions=\{compactToolbarActions\}/);
  assert.match(source, /showExpandCollapseControls=\{showExpandCollapseControls\}/);
  assert.match(source, /shouldCompactTerminalHostTreeToolbar\(displayWidth\)/);
});
