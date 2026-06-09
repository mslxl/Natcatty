import assert from 'node:assert/strict';
import test from 'node:test';

import { getTerminalSidePanelShellWidth } from './TerminalLayerSidePanelSection.tsx';

test('AI side panel shell can be force-hidden for layout isolation', () => {
  assert.equal(getTerminalSidePanelShellWidth({
    activeSidePanelTab: 'ai',
    forceHideAiShell: true,
    isSidePanelOpenForCurrentTab: true,
    resizePreviewWidth: null,
    sidePanelWidth: 420,
  }), 0);
});

test('non-AI side panels keep their open width', () => {
  assert.equal(getTerminalSidePanelShellWidth({
    activeSidePanelTab: 'sftp',
    forceHideAiShell: true,
    isSidePanelOpenForCurrentTab: true,
    resizePreviewWidth: null,
    sidePanelWidth: 420,
  }), 420);
});

test('resize preview width is still honored for visible side panels', () => {
  assert.equal(getTerminalSidePanelShellWidth({
    activeSidePanelTab: 'theme',
    forceHideAiShell: true,
    isSidePanelOpenForCurrentTab: true,
    resizePreviewWidth: 512,
    sidePanelWidth: 420,
  }), 512);
});

test('closed side panel shell has no width', () => {
  assert.equal(getTerminalSidePanelShellWidth({
    activeSidePanelTab: null,
    forceHideAiShell: true,
    isSidePanelOpenForCurrentTab: false,
    resizePreviewWidth: null,
    sidePanelWidth: 420,
  }), 0);
});
