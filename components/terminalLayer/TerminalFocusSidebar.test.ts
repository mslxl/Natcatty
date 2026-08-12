import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./TerminalFocusSidebar.tsx', import.meta.url), 'utf8');

test('focus sidebar row memo refreshes when dynamic title mode changes', () => {
  assert.match(source, /prev\.dynamicTabTitleMode === next\.dynamicTabTitleMode/);
});

test('focus sidebar accepts host-id drops for append-to-workspace', () => {
  assert.match(source, /onAppendHostToWorkspace/);
  assert.match(source, /resolveFocusSidebarDragKind/);
  assert.match(source, /readHostIdFromDataTransfer/);
  assert.match(source, /dropEffect = 'copy'/);
  assert.match(source, /data-host-drop-active/);
});
