import assert from 'node:assert/strict';
import test from 'node:test';

import {
  FOCUS_SIDEBAR_HOST_DRAG_TYPE,
  FOCUS_SIDEBAR_SESSION_DRAG_TYPE,
  readHostIdFromDataTransfer,
  resolveFocusSidebarDragKind,
} from './focusSidebarHostDrop.ts';

test('resolveFocusSidebarDragKind prefers in-flight session reorder', () => {
  assert.equal(
    resolveFocusSidebarDragKind({
      types: [FOCUS_SIDEBAR_HOST_DRAG_TYPE],
      activeSessionDragId: 'session-1',
    }),
    'session-reorder',
  );
});

test('resolveFocusSidebarDragKind detects session mime even without local state', () => {
  assert.equal(
    resolveFocusSidebarDragKind({
      types: [FOCUS_SIDEBAR_SESSION_DRAG_TYPE],
      activeSessionDragId: null,
    }),
    'session-reorder',
  );
});

test('resolveFocusSidebarDragKind accepts vault or host-tree host drags', () => {
  assert.equal(
    resolveFocusSidebarDragKind({
      types: [FOCUS_SIDEBAR_HOST_DRAG_TYPE],
      activeSessionDragId: null,
    }),
    'host-append',
  );
});

test('resolveFocusSidebarDragKind ignores unrelated drag payloads', () => {
  assert.equal(
    resolveFocusSidebarDragKind({
      types: ['text/plain', 'tab-reorder-id'],
      activeSessionDragId: null,
    }),
    null,
  );
});

test('readHostIdFromDataTransfer returns trimmed host id', () => {
  assert.equal(
    readHostIdFromDataTransfer((type) => (type === FOCUS_SIDEBAR_HOST_DRAG_TYPE ? '  host-42  ' : '')),
    'host-42',
  );
});

test('readHostIdFromDataTransfer returns null when host id is missing', () => {
  assert.equal(readHostIdFromDataTransfer(() => ''), null);
  assert.equal(readHostIdFromDataTransfer(() => '   '), null);
});
