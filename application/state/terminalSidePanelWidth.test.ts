import assert from 'node:assert/strict';
import test from 'node:test';

import {
  clampTerminalSidePanelWidth,
  getTerminalSidePanelMaxWidth,
  TERMINAL_SIDE_PANEL_MAX_WIDTH,
  TERMINAL_SIDE_PANEL_MIN_WIDTH,
} from './terminalSidePanelWidth.ts';

test('terminal side panel can expand to the wider maximum', () => {
  assert.equal(getTerminalSidePanelMaxWidth(2000), TERMINAL_SIDE_PANEL_MAX_WIDTH);
  assert.equal(clampTerminalSidePanelWidth(1400, 2000), TERMINAL_SIDE_PANEL_MAX_WIDTH);
});

test('terminal side panel keeps usable terminal space in smaller windows', () => {
  assert.equal(getTerminalSidePanelMaxWidth(1000), 680);
  assert.equal(clampTerminalSidePanelWidth(900, 1000), 680);
  assert.equal(clampTerminalSidePanelWidth(100, 1000), TERMINAL_SIDE_PANEL_MIN_WIDTH);
});
