import assert from 'node:assert/strict';
import test from 'node:test';

import {
  getTerminalPaneFocusTransition,
  TERMINAL_PANE_FOCUS_TRANSITION_MS,
} from './terminalPaneFocusTransition.ts';

test('focused terminal panes animate between split and full size within the issue limit', () => {
  assert.equal(TERMINAL_PANE_FOCUS_TRANSITION_MS, 160);
  assert.equal(
    getTerminalPaneFocusTransition({
      inActiveWorkspace: true,
      isFocusTarget: true,
      isFocusTransitionActive: true,
      isResizing: false,
    }),
    'left 160ms ease, top 160ms ease, width 160ms ease, height 160ms ease',
  );
});

test('ordinary split resizing never inherits the focus transition', () => {
  assert.equal(getTerminalPaneFocusTransition({
    inActiveWorkspace: true,
    isFocusTarget: true,
    isFocusTransitionActive: false,
    isResizing: false,
  }), undefined);
  assert.equal(getTerminalPaneFocusTransition({
    inActiveWorkspace: true,
    isFocusTarget: true,
    isFocusTransitionActive: true,
    isResizing: true,
  }), undefined);
});
