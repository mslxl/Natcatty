import assert from 'node:assert/strict';
import test from 'node:test';

import {
  getTerminalPaneFocusTransition,
  TERMINAL_PANE_FOCUS_TRANSITION_MS,
} from './terminalPaneFocusTransition.ts';

test('focused terminal panes animate between split and full size within the issue limit', () => {
  assert.equal(TERMINAL_PANE_FOCUS_TRANSITION_MS, 160);
  assert.equal(
    getTerminalPaneFocusTransition(true, true),
    'left 160ms ease, top 160ms ease, width 160ms ease, height 160ms ease',
  );
});

test('terminal panes outside the focused workspace do not animate their layout', () => {
  assert.equal(getTerminalPaneFocusTransition(false, true), undefined);
  assert.equal(getTerminalPaneFocusTransition(true, false), undefined);
});
