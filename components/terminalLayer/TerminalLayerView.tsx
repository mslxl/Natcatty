/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { memo } from 'react';

import { TerminalLayerFocusSidebarSection } from './TerminalLayerFocusSidebarSection';
import { TerminalLayerHostTreeSection } from './TerminalLayerHostTreeSection';
import { TerminalLayerSidePanelSection } from './TerminalLayerSidePanelSection';
import { TerminalLayerWorkspaceSection } from './TerminalLayerWorkspaceSection';
import { terminalLayerViewCtxEqual } from './terminalLayerViewMemo';

type TerminalLayerViewContext = Record<string, any>;

function TerminalLayerViewInner({ ctx }: { ctx: TerminalLayerViewContext }) {
  const {
    activeWorkspace,
    composeBarThemeColors,
    focusedSessionId,
    handleComposeSend,
    isBroadcastEnabled,
    isComposeBarOpen,
    isTerminalLayerVisible,
    refocusTerminalSession,
    setIsComposeBarOpen,
    TerminalComposeBar,
    workspaceOuterRef,
  } = ctx;

  return (
    <div
      ref={workspaceOuterRef}
      className="absolute inset-0 bg-background flex flex-col"
      data-section="terminal-workspace"
      style={{
        visibility: isTerminalLayerVisible ? 'visible' : 'hidden',
        pointerEvents: isTerminalLayerVisible ? 'auto' : 'none',
        zIndex: isTerminalLayerVisible ? 10 : 0,
      }}
    >
      <div className="flex-1 flex min-h-0 relative">
        <TerminalLayerHostTreeSection ctx={ctx} />
        <TerminalLayerSidePanelSection ctx={ctx} />
        <TerminalLayerFocusSidebarSection ctx={ctx} />
        <TerminalLayerWorkspaceSection ctx={ctx} />
      </div>

      {activeWorkspace && isComposeBarOpen && (
        <TerminalComposeBar
          onSend={handleComposeSend}
          onClose={() => {
            setIsComposeBarOpen(false);
            refocusTerminalSession(focusedSessionId);
          }}
          isBroadcastEnabled={isBroadcastEnabled?.(activeWorkspace.id)}
          themeColors={composeBarThemeColors}
        />
      )}
    </div>
  );
}

export const TerminalLayerView = memo(
  TerminalLayerViewInner,
  (prev, next) => terminalLayerViewCtxEqual(prev.ctx, next.ctx),
);
TerminalLayerView.displayName = 'TerminalLayerView';
