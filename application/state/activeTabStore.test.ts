import assert from 'node:assert/strict';
import test from 'node:test';

import { fromEditorTabId, isEditorTabId, toEditorTabId } from './activeTabStore';

test('editor tab helpers round trip ids', () => {
  assert.equal(toEditorTabId('file-1'), 'editor:file-1');
  assert.equal(fromEditorTabId('editor:file-1'), 'file-1');
});

test('editor tab helper detects editor top-tab ids', () => {
  assert.equal(isEditorTabId('editor:file-1'), true);
  assert.equal(isEditorTabId('session-1'), false);
});
