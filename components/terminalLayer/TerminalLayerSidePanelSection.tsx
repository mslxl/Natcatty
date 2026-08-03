/* eslint-disable @typescript-eslint/no-explicit-any */
import { Activity, FolderTree, History, MessageSquare, NotebookText, Palette, PanelLeft, PanelRight, Play, SplitSquareHorizontal, SplitSquareVertical, X } from 'lucide-react';
import {
  buildSidePanelChromeThemeFromTerminalTheme,
  buildTerminalSidePanelCssVars,
} from '../../infrastructure/theme/terminalAppearanceTokens';
import { injectTerminalLayerChromeSurfaceVars } from '../../infrastructure/theme/terminalAppearanceVars';
import React, { memo, useCallback, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';

import { useActiveTabId } from '../../application/state/activeTabStore';
import {
  getSidePanelLiveSnapshot,
  subscribeSidePanelLiveSnapshot,
} from '../../application/state/sidePanelLiveStore';
import {
  reorderTerminalSidePanelTab,
  TERMINAL_SIDE_PANEL_TAB_DEFAULT_ORDER,
  TERMINAL_SIDE_PANEL_TAB_IDS,
  type TerminalSidePanelTabId,
  useTerminalSidePanelTabOrder,
} from '../../application/state/terminalSidePanelTabs';
import { terminalLayoutSuppressStore } from '../../application/state/terminalLayoutSuppressStore';
import { AI_PANEL_FORCE_HIDE_SHELL } from '../ai/aiPanelDiagnostics';

import {
  ToolbarCustomizeContextMenu,
  ToolbarOverflowMenu,
} from '../ui/toolbar-item-layout';
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip';
import { Popover, PopoverClose, PopoverContent, PopoverTrigger } from '../ui/popover';
import type { SidePanelTab } from './TerminalLayerSupport';
import {
  MAX_SIDE_PANEL_PANES,
  collectSidePanelPanes,
  type SidePanelLayout,
  type SidePanelLayoutNode,
  type SidePanelSplitDirection,
  type SidePanelSplitNode,
} from '../../domain/sidePanelLayout';
import { terminalLayerSidePanelStableCtxEqual } from './terminalLayerViewMemo';
import { SidePanelMountedContent } from './terminalLayerSidePanelSlots';

const MemoizedSidePanelMountedContent = memo(
  SidePanelMountedContent,
  (prev, next) => (
    prev.paneHosts === next.paneHosts
    && prev.parkingHost === next.parkingHost
    && terminalLayerSidePanelStableCtxEqual(prev.ctx, next.ctx)
  ),
);
MemoizedSidePanelMountedContent.displayName = 'MemoizedSidePanelMountedContent';

type SidePanelContext = Record<string, any>;
const SIDE_PANEL_TAB_DRAG_MIME = 'application/x-netcatty-sidepanel-tab';

type SidePanelTabItem = {
  id: SidePanelTab;
  label: string;
  icon: React.ReactNode;
  onClick: () => void;
};

function SidePanelPaneHost({
  node,
  focused,
  paneCount,
  label,
  onClose,
  onFocus,
  onHostChange,
  separator,
  mutedColor,
}: {
  node: Extract<SidePanelLayoutNode, { type: 'pane' }>;
  focused: boolean;
  paneCount: number;
  label: string;
  onClose: (paneId: string) => void;
  onFocus: (paneId: string) => void;
  onHostChange: (tool: SidePanelTab, host: HTMLElement | null) => void;
  separator: string;
  mutedColor: string;
}) {
  const hostRef = useRef<HTMLDivElement>(null);

  // Registration happens after commit and only when the actual host changes.
  // This avoids state writes from ref callbacks and provides a parking window
  // for portals while a pane tree is being replaced.
  useLayoutEffect(() => {
    const host = hostRef.current;
    onHostChange(node.tool, host);
    return () => onHostChange(node.tool, null);
  }, [node.tool, onHostChange]);

  return (
    <div
      className="h-full w-full min-h-0 min-w-0 overflow-hidden flex flex-col relative"
      data-section="terminal-side-panel-pane"
      data-pane-id={node.id}
      data-pane-tool={node.tool}
      data-focused={focused ? 'true' : 'false'}
      onMouseDown={() => onFocus(node.id)}
    >
      {paneCount > 1 && (
        <div
          className="h-7 px-2 flex items-center gap-2 shrink-0 select-none"
          style={{
            borderBottom: `1px solid ${separator}`,
            boxShadow: focused ? `inset 2px 0 0 ${mutedColor}` : undefined,
          }}
        >
          <span className="text-[11px] font-medium truncate flex-1">{label}</span>
          <button
            type="button"
            className="h-5 w-5 grid place-items-center rounded-sm opacity-70 hover:opacity-100 hover:bg-white/10"
            aria-label={label}
            onMouseDown={(event) => event.stopPropagation()}
            onClick={(event) => {
              event.stopPropagation();
              onClose(node.id);
            }}
          >
            <X size={12} />
          </button>
        </div>
      )}
      <div
        ref={hostRef}
        className="relative flex-1 min-h-0 min-w-0 overflow-hidden [contain:strict]"
        data-section="terminal-side-panel-pane-content"
      />
    </div>
  );
}

function SidePanelSplitView({
  node,
  children,
  onResize,
  separator,
}: {
  node: SidePanelSplitNode;
  children: React.ReactNode[];
  onResize: (splitId: string, sizes: number[]) => void;
  separator: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const total = node.sizes.reduce((sum, size) => sum + size, 0) || 1;
  const normalizedSizes = node.children.map((_, index) => (node.sizes[index] ?? 1) / total);

  const startResize = useCallback((event: React.MouseEvent, index: number) => {
    event.preventDefault();
    event.stopPropagation();
    const container = containerRef.current;
    if (!container) return;
    const rect = container.getBoundingClientRect();
    const axisLength = node.direction === 'vertical' ? rect.width : rect.height;
    if (axisLength <= 0) return;

    terminalLayoutSuppressStore.begin();
    const startClient = node.direction === 'vertical' ? event.clientX : event.clientY;
    const startSizes = [...normalizedSizes];
    const pairSize = startSizes[index] + startSizes[index + 1];
    const minimum = Math.min(pairSize / 2, Math.max(0.04, 80 / axisLength));
    let frame: number | null = null;
    let pendingClient = startClient;

    const commit = () => {
      frame = null;
      const delta = (pendingClient - startClient) / axisLength;
      const first = Math.max(minimum, Math.min(pairSize - minimum, startSizes[index] + delta));
      const next = [...startSizes];
      next[index] = first;
      next[index + 1] = pairSize - first;
      onResize(node.id, next);
    };
    const onMouseMove = (moveEvent: MouseEvent) => {
      pendingClient = node.direction === 'vertical' ? moveEvent.clientX : moveEvent.clientY;
      if (frame === null) frame = requestAnimationFrame(commit);
    };
    const onMouseUp = () => {
      if (frame !== null) {
        cancelAnimationFrame(frame);
        commit();
      }
      terminalLayoutSuppressStore.end();
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
    };
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
  }, [node.direction, node.id, normalizedSizes, onResize]);

  return (
    <div
      ref={containerRef}
      className={node.direction === 'vertical'
        ? 'h-full w-full min-h-0 min-w-0 flex flex-row overflow-hidden'
        : 'h-full w-full min-h-0 min-w-0 flex flex-col overflow-hidden'}
      data-section="terminal-side-panel-split"
      data-split-id={node.id}
      data-split-direction={node.direction}
    >
      {children.map((child, index) => (
        <React.Fragment key={node.children[index].id}>
          <div
            className="min-h-0 min-w-0 overflow-hidden relative"
            style={{ flexBasis: 0, flexGrow: normalizedSizes[index] }}
          >
            {child}
          </div>
          {index < children.length - 1 && (
            <div
              className={node.direction === 'vertical'
                ? 'group relative w-1 shrink-0 cursor-ew-resize z-20'
                : 'group relative h-1 shrink-0 cursor-ns-resize z-20'}
              data-section="terminal-side-panel-split-resizer"
              onMouseDown={(event) => startResize(event, index)}
            >
              <div
                className={node.direction === 'vertical'
                  ? 'absolute inset-y-0 left-1/2 w-px -translate-x-1/2 group-hover:w-0.5'
                  : 'absolute inset-x-0 top-1/2 h-px -translate-y-1/2 group-hover:h-0.5'}
                style={{ backgroundColor: separator }}
              />
            </div>
          )}
        </React.Fragment>
      ))}
    </div>
  );
}

function SidePanelLayoutTree({
  node,
  layout,
  paneCount,
  labels,
  onClose,
  onFocus,
  onHostChange,
  onResize,
  separator,
  accent,
}: {
  node: SidePanelLayoutNode;
  layout: SidePanelLayout;
  paneCount: number;
  labels: ReadonlyMap<SidePanelTab, string>;
  onClose: (paneId: string) => void;
  onFocus: (paneId: string) => void;
  onHostChange: (tool: SidePanelTab, host: HTMLElement | null) => void;
  onResize: (splitId: string, sizes: number[]) => void;
  separator: string;
  accent: string;
}): React.ReactNode {
  if (node.type === 'pane') {
    return (
      <SidePanelPaneHost
        node={node}
        focused={layout.focusedPaneId === node.id}
        paneCount={paneCount}
        label={labels.get(node.tool) ?? node.tool}
        onClose={onClose}
        onFocus={onFocus}
        onHostChange={onHostChange}
        separator={separator}
        mutedColor={accent}
      />
    );
  }

  return (
    <SidePanelSplitView node={node} onResize={onResize} separator={separator}>
      {node.children.map((child) => (
        <SidePanelLayoutTree
          key={child.id}
          node={child}
          layout={layout}
          paneCount={paneCount}
          labels={labels}
          onClose={onClose}
          onFocus={onFocus}
          onHostChange={onHostChange}
          onResize={onResize}
          separator={separator}
          accent={accent}
        />
      ))}
    </SidePanelSplitView>
  );
}

function SidePanelSplitMenu({
  direction,
  items,
  occupiedTools,
  disabled,
  onSelect,
  t,
  buttonColor,
}: {
  direction: SidePanelSplitDirection;
  items: SidePanelTabItem[];
  occupiedTools: ReadonlySet<SidePanelTab>;
  disabled: boolean;
  onSelect: (tool: SidePanelTab, direction: SidePanelSplitDirection) => void;
  t: (key: string) => string;
  buttonColor: string;
}) {
  const available = items.filter((item) => !occupiedTools.has(item.id));
  const label = direction === 'horizontal'
    ? t('terminal.layer.splitHorizontal')
    : t('terminal.layer.splitVertical');

  return (
    <Popover>
      <Tooltip>
        <TooltipTrigger asChild>
          <PopoverTrigger asChild>
            <button
              type="button"
              disabled={disabled || available.length === 0}
              className="h-7 w-7 rounded-md p-0 grid place-items-center disabled:opacity-35"
              style={{ color: buttonColor }}
              aria-label={label}
            >
              {direction === 'horizontal'
                ? <SplitSquareHorizontal size={15} />
                : <SplitSquareVertical size={15} />}
            </button>
          </PopoverTrigger>
        </TooltipTrigger>
        <TooltipContent side="bottom">{label}</TooltipContent>
      </Tooltip>
      <PopoverContent align="end" side="bottom" className="w-52 p-1">
        <div className="px-2 py-1.5 text-xs text-muted-foreground">
          {t('terminal.layer.openInNewSplit')}
        </div>
        {available.map((item) => (
          <PopoverClose asChild key={item.id}>
            <button
              type="button"
              className="w-full flex items-center gap-2 px-2 py-1.5 text-xs rounded-sm hover:bg-secondary text-left"
              onClick={() => onSelect(item.id, direction)}
            >
              <span className="shrink-0">{item.icon}</span>
              <span className="truncate">{item.label}</span>
            </button>
          </PopoverClose>
        ))}
      </PopoverContent>
    </Popover>
  );
}

export function getTerminalSidePanelShellWidth({
  activeSidePanelTab,
  forceHideAiShell,
  isSidePanelOpenForCurrentTab,
  resizePreviewWidth,
  sidePanelWidth,
}: {
  activeSidePanelTab: SidePanelTab | null;
  forceHideAiShell: boolean;
  isSidePanelOpenForCurrentTab: boolean;
  resizePreviewWidth: number | null;
  sidePanelWidth: number;
}): number {
  if (forceHideAiShell && activeSidePanelTab === 'ai') return 0;
  return isSidePanelOpenForCurrentTab
    ? (resizePreviewWidth ?? sidePanelWidth)
    : 0;
}

function hasMountedSidePanelContent(ctx: SidePanelContext): boolean {
  const {
    mountedAiTabIds,
    mountedSftpTabIds,
    notesMountedTabIds,
    scriptsMountedTabIds,
    systemMountedTabIds,
    themeMountedTabIds,
    sidePanelOpenTabs,
  } = ctx;

  const anyHistoryOpen = sidePanelOpenTabs instanceof Map
    && Array.from((sidePanelOpenTabs as Map<string, SidePanelTab>).values()).includes('history');
  const anyNotesOpen = sidePanelOpenTabs instanceof Map
    && Array.from((sidePanelOpenTabs as Map<string, SidePanelTab>).values()).includes('notes');

  return !(
    mountedSftpTabIds.length === 0
    && mountedAiTabIds.length === 0
    && notesMountedTabIds.length === 0
    && scriptsMountedTabIds.length === 0
    && systemMountedTabIds.length === 0
    && themeMountedTabIds.length === 0
    && !anyHistoryOpen
    && !anyNotesOpen
  );
}

function TerminalLayerSidePanelSectionInner({ ctx }: { ctx: SidePanelContext }) {
  if (!hasMountedSidePanelContent(ctx)) return null;
  return <TerminalLayerSidePanelInner ctx={ctx} />;
}

/** Skip chrome rebuilds when only live/workspace-focus ticks change. */
export const TerminalLayerSidePanelSection = memo(
  TerminalLayerSidePanelSectionInner,
  (prev, next) => terminalLayerSidePanelStableCtxEqual(prev.ctx, next.ctx),
);
TerminalLayerSidePanelSection.displayName = 'TerminalLayerSidePanelSection';

function TerminalLayerSidePanelInner({ ctx }: { ctx: SidePanelContext }) {
  const activeTabId = useActiveTabId();
  const sidePanelOpenTabs = ctx.sidePanelOpenTabs as Map<string, SidePanelTab>;
  const sidePanelLayouts = ctx.sidePanelLayouts as Map<string, SidePanelLayout>;
  const isSidePanelOpenForCurrentTab = activeTabId ? sidePanelOpenTabs.has(activeTabId) : false;
  const activeSidePanelTab = activeTabId ? sidePanelOpenTabs.get(activeTabId) ?? null : null;
  const activeSidePanelLayout = activeTabId ? sidePanelLayouts.get(activeTabId) ?? null : null;

  const {
    Button: Btn,
    cn,
    followAppTerminalTheme,
    handleCloseSidePanel,
    handleOpenAI,
    handleOpenHistory,
    handleOpenNotes,
    handleOpenScripts,
    handleOpenSystem,
    handleOpenTheme,
    handleFocusSidePanelPane,
    handleSplitSidePanelPane,
    handleCloseSidePanelPane,
    handleResizeSidePanelSplit,
    handleToggleSftpFromBar,
    resolvedPreviewTheme: ctxResolvedPreviewTheme,
    setSidePanelPosition,
    setSidePanelWidth,
    persistSidePanelWidth,
    sidePanelPosition,
    sidePanelWidth,
    t,
    terminalTheme,
  } = ctx;

  // Live theme for chrome when panel is open and not follow-app — stable memo
  // no longer receives focus-driven resolvedPreviewTheme via ctx.
  const subscribeLiveTheme = isSidePanelOpenForCurrentTab && !followAppTerminalTheme;
  const liveSnapshot = useSyncExternalStore(
    (listener) => subscribeSidePanelLiveSnapshot(subscribeLiveTheme, listener),
    () => getSidePanelLiveSnapshot(subscribeLiveTheme),
    () => getSidePanelLiveSnapshot(subscribeLiveTheme),
  );
  const resolvedPreviewTheme = followAppTerminalTheme
    ? null
    : (subscribeLiveTheme
      ? (liveSnapshot.resolvedPreviewTheme ?? ctxResolvedPreviewTheme)
      : ctxResolvedPreviewTheme);

  const [resizePreviewWidth, setResizePreviewWidth] = useState<number | null>(null);
  const [paneHosts, setPaneHosts] = useState<Map<SidePanelTab, HTMLElement>>(new Map());
  const [parkingHost, setParkingHost] = useState<HTMLElement | null>(null);
  const parkingHostRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    setParkingHost(parkingHostRef.current);
    return () => setParkingHost(null);
  }, []);
  const handlePaneHostChange = useCallback((tool: SidePanelTab, host: HTMLElement | null) => {
    setPaneHosts((current) => {
      if (host && current.get(tool) === host) return current;
      if (!host && !current.has(tool)) return current;
      const next = new Map(current);
      if (host) next.set(tool, host);
      else next.delete(tool);
      return next;
    });
  }, []);
  const {
    sidePanelTabOrder,
    setSidePanelTabOrder,
    layout: sidePanelTabLayout,
    setPlacement: setSidePanelTabPlacement,
    move: moveSidePanelTab,
    resetLayout: resetSidePanelTabLayout,
    partition: partitionSidePanelTabs,
  } = useTerminalSidePanelTabOrder();
  const resolvedSidePanelTerminalTheme = useMemo(() => (
    followAppTerminalTheme
      ? terminalTheme
      : (resolvedPreviewTheme ?? terminalTheme)
  ), [followAppTerminalTheme, resolvedPreviewTheme, terminalTheme]);
  const sidePanelTheme = useMemo(
    () => buildSidePanelChromeThemeFromTerminalTheme(resolvedSidePanelTerminalTheme),
    [resolvedSidePanelTerminalTheme],
  );
  const sidePanelCssVars = useMemo(
    () => buildTerminalSidePanelCssVars(resolvedSidePanelTerminalTheme),
    [resolvedSidePanelTerminalTheme],
  );

  useLayoutEffect(() => {
    if (!isSidePanelOpenForCurrentTab) return;
    const chromeTheme = followAppTerminalTheme
      ? terminalTheme
      : (resolvedPreviewTheme ?? terminalTheme);
    injectTerminalLayerChromeSurfaceVars(chromeTheme);
  }, [
    followAppTerminalTheme,
    isSidePanelOpenForCurrentTab,
    resolvedPreviewTheme,
    terminalTheme,
  ]);

  const [dragOverSidePanelTab, setDragOverSidePanelTab] = useState<{
    tab: TerminalSidePanelTabId;
    placement: 'before' | 'after';
  } | null>(null);
  const draggedSidePanelTabRef = useRef<TerminalSidePanelTabId | null>(null);
  const activePaneCount = activeSidePanelLayout
    ? collectSidePanelPanes(activeSidePanelLayout.root).length
    : 0;
  const isAiShellForceHidden = AI_PANEL_FORCE_HIDE_SHELL
    && activeSidePanelTab === 'ai'
    && activePaneCount <= 1;
  const shellWidth = getTerminalSidePanelShellWidth({
    activeSidePanelTab,
    forceHideAiShell: AI_PANEL_FORCE_HIDE_SHELL && activePaneCount <= 1,
    isSidePanelOpenForCurrentTab,
    resizePreviewWidth,
    sidePanelWidth,
  });

  const handleSidePanelResizeStart = useCallback((event: React.MouseEvent) => {
    if (!isSidePanelOpenForCurrentTab) return;
    event.preventDefault();
    terminalLayoutSuppressStore.begin();
    const startX = event.clientX;
    const startWidth = sidePanelWidth;
    let lastWidth = startWidth;
    let rafId: number | null = null;

    const onMouseMove = (moveEvent: MouseEvent) => {
      const delta = moveEvent.clientX - startX;
      lastWidth = Math.max(
        280,
        Math.min(800, startWidth + (sidePanelPosition === 'left' ? delta : -delta)),
      );
      if (rafId !== null) return;
      rafId = requestAnimationFrame(() => {
        rafId = null;
        setResizePreviewWidth(lastWidth);
      });
    };
    const onMouseUp = () => {
      if (rafId !== null) cancelAnimationFrame(rafId);
      setSidePanelWidth(lastWidth);
      persistSidePanelWidth(lastWidth);
      setResizePreviewWidth(null);
      terminalLayoutSuppressStore.end();
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
    };
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
  }, [
    isSidePanelOpenForCurrentTab,
    persistSidePanelWidth,
    setSidePanelWidth,
    sidePanelPosition,
    sidePanelWidth,
  ]);

  const handleSidePanelTabDragStart = useCallback((event: React.DragEvent, tab: TerminalSidePanelTabId) => {
    draggedSidePanelTabRef.current = tab;
    setDragOverSidePanelTab(null);
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData(SIDE_PANEL_TAB_DRAG_MIME, tab);
    event.dataTransfer.setData('text/plain', tab);
  }, []);

  const handleSidePanelTabDrop = useCallback((event: React.DragEvent, targetTab: TerminalSidePanelTabId) => {
    if (!Array.from(event.dataTransfer.types).includes(SIDE_PANEL_TAB_DRAG_MIME)) return;
    event.preventDefault();
    const transferredTab = event.dataTransfer.getData(SIDE_PANEL_TAB_DRAG_MIME) as TerminalSidePanelTabId;
    const draggedTab = draggedSidePanelTabRef.current ?? transferredTab;
    draggedSidePanelTabRef.current = null;
    setDragOverSidePanelTab(null);
    if (!TERMINAL_SIDE_PANEL_TAB_IDS.has(draggedTab)) return;

    const nextOrder = reorderTerminalSidePanelTab(
      sidePanelTabOrder,
      draggedTab,
      targetTab,
      dragOverSidePanelTab?.tab === targetTab ? dragOverSidePanelTab.placement : 'before',
    );
    if (nextOrder !== sidePanelTabOrder) {
      setSidePanelTabOrder(nextOrder);
    }
  }, [dragOverSidePanelTab, setSidePanelTabOrder, sidePanelTabOrder]);

  const handleSidePanelTabDragOver = useCallback((event: React.DragEvent, targetTab: TerminalSidePanelTabId) => {
    if (!Array.from(event.dataTransfer.types).includes(SIDE_PANEL_TAB_DRAG_MIME)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'move';
    const rect = event.currentTarget.getBoundingClientRect();
    const placement = event.clientX > rect.left + (rect.width / 2) ? 'after' : 'before';
    setDragOverSidePanelTab((current) => {
      if (current?.tab === targetTab && current.placement === placement) return current;
      return { tab: targetTab, placement };
    });
  }, []);

  const handleSidePanelTabDragLeave = useCallback((event: React.DragEvent, targetTab: TerminalSidePanelTabId) => {
    if (dragOverSidePanelTab?.tab !== targetTab) return;
    const nextTarget = event.relatedTarget as Node | null;
    if (nextTarget && event.currentTarget.contains(nextTarget)) return;
    setDragOverSidePanelTab(null);
  }, [dragOverSidePanelTab]);

  const sidePanelTabItems = useMemo<SidePanelTabItem[]>(() => [
    { id: 'sftp' as const, label: t('terminal.layer.sftp'), icon: <FolderTree size={15} />, onClick: handleToggleSftpFromBar },
    { id: 'scripts' as const, label: t('terminal.layer.scripts'), icon: <Play size={15} />, onClick: handleOpenScripts },
    { id: 'history' as const, label: t('terminal.layer.history'), icon: <History size={15} />, onClick: handleOpenHistory },
    { id: 'theme' as const, label: t('terminal.layer.theme'), icon: <Palette size={15} />, onClick: handleOpenTheme },
    { id: 'system' as const, label: t('terminal.layer.system'), icon: <Activity size={15} />, onClick: handleOpenSystem },
    { id: 'notes' as const, label: t('terminal.layer.notes'), icon: <NotebookText size={15} />, onClick: handleOpenNotes },
    { id: 'ai' as const, label: t('terminal.layer.aiChat'), icon: <MessageSquare size={15} />, onClick: handleOpenAI },
  ], [
    handleOpenAI,
    handleOpenHistory,
    handleOpenNotes,
    handleOpenScripts,
    handleOpenSystem,
    handleOpenTheme,
    handleToggleSftpFromBar,
    t,
  ]);
  const sidePanelTabItemById = useMemo(
    () => new Map(sidePanelTabItems.map((item) => [item.id, item])),
    [sidePanelTabItems],
  );
  const sidePanelToolLabels = useMemo(
    () => new Map(sidePanelTabItems.map((item) => [item.id, item.label])),
    [sidePanelTabItems],
  );
  const occupiedSidePanelTools = useMemo(
    () => new Set(activeSidePanelLayout
      ? collectSidePanelPanes(activeSidePanelLayout.root).map((pane) => pane.tool)
      : []),
    [activeSidePanelLayout],
  );

  const { shown: shownSidePanelTabs, collapsed: collapsedSidePanelTabs } = useMemo(() => {
    const parts = partitionSidePanelTabs(TERMINAL_SIDE_PANEL_TAB_DEFAULT_ORDER);
    // If an external path opens a hidden tab, still show its chip while active.
    if (
      activeSidePanelTab &&
      !parts.shown.includes(activeSidePanelTab) &&
      !parts.collapsed.includes(activeSidePanelTab)
    ) {
      return {
        shown: [...parts.shown, activeSidePanelTab],
        collapsed: parts.collapsed,
        hidden: parts.hidden.filter((id) => id !== activeSidePanelTab),
      };
    }
    return parts;
  }, [activeSidePanelTab, partitionSidePanelTabs]);

  const sidePanelCustomizeItems = useMemo(
    () =>
      sidePanelTabOrder.map((tabId) => {
        const item = sidePanelTabItemById.get(tabId);
        return {
          id: tabId,
          label: item?.label ?? tabId,
          icon: item?.icon,
        };
      }),
    [sidePanelTabItemById, sidePanelTabOrder],
  );

  return (
    <div
      style={{ width: shellWidth, contain: 'layout paint style' }}
      className={cn(
        'flex-shrink-0 h-full relative z-20',
        shellWidth === 0 && 'overflow-hidden',
        sidePanelPosition === 'right' && 'order-last',
      )}
      data-section="terminal-side-panel-shell"
      data-side-panel-position={sidePanelPosition}
    >
      {isSidePanelOpenForCurrentTab && !isAiShellForceHidden && (
        <div
          className={cn(
            'absolute top-0 h-full w-2 cursor-ew-resize z-30',
            sidePanelPosition === 'left' ? 'right-[-3px]' : 'left-[-3px]',
          )}
          data-section="terminal-side-panel-resizer"
          onMouseDown={handleSidePanelResizeStart}
        />
      )}
      <div
        className={cn(
          'h-full flex flex-col overflow-hidden',
          !isSidePanelOpenForCurrentTab && 'pointer-events-none',
        )}
        data-section={isSidePanelOpenForCurrentTab ? 'terminal-side-panel' : undefined}
        data-open={isSidePanelOpenForCurrentTab ? 'true' : 'false'}
        data-side-panel-tab={isSidePanelOpenForCurrentTab ? (activeSidePanelTab ?? undefined) : undefined}
        style={{
          ...sidePanelCssVars,
          backgroundColor: sidePanelTheme.termBg,
          color: sidePanelTheme.termFg,
          ...(isSidePanelOpenForCurrentTab && sidePanelPosition === 'left'
            ? { borderRight: `1px solid ${sidePanelTheme.separator}` }
            : {}),
          ...(isSidePanelOpenForCurrentTab && sidePanelPosition === 'right'
            ? { borderLeft: `1px solid ${sidePanelTheme.separator}` }
            : {}),
        }}
      >
        {isSidePanelOpenForCurrentTab && !isAiShellForceHidden && (
          <ToolbarCustomizeContextMenu
            items={sidePanelCustomizeItems}
            placementOf={(id) => sidePanelTabLayout.placement[id] ?? 'show'}
            onSetPlacement={(id, placement) => {
              const next = setSidePanelTabPlacement(
                id,
                placement,
                TERMINAL_SIDE_PANEL_TAB_DEFAULT_ORDER,
              );
              // Only close when hide actually stuck (not reverted by requireReachable).
              if (activeSidePanelTab === id && (next.placement[id] ?? 'show') === 'hide') {
                handleCloseSidePanel?.();
              }
            }}
            onMove={(id, direction) =>
              moveSidePanelTab(id, direction, TERMINAL_SIDE_PANEL_TAB_DEFAULT_ORDER)
            }
            onReset={resetSidePanelTabLayout}
            t={t}
            className="flex h-9 items-center px-1.5 py-1 flex-shrink-0 gap-1 w-full"
            dataSection="terminal-side-panel-tabs"
            style={{
              backgroundColor: sidePanelTheme.termBg,
              borderBottom: `1px solid ${sidePanelTheme.separator}`,
            }}
          >
              {shownSidePanelTabs.map((tabId) => {
                const item = sidePanelTabItemById.get(tabId as TerminalSidePanelTabId);
                if (!item) return null;
                const isActive = activeSidePanelTab === item.id;
                const showDropIndicator = dragOverSidePanelTab?.tab === item.id
                  && draggedSidePanelTabRef.current !== null
                  && draggedSidePanelTabRef.current !== item.id;
                return (
                  <Tooltip key={item.id}>
                    <TooltipTrigger asChild>
                      <Btn
                        variant="ghost"
                        size="icon"
                        draggable
                        data-tab-id={item.id}
                        data-tab-type="sidepanel"
                        data-state={isActive ? 'active' : 'inactive'}
                        className="netcatty-tab relative h-7 w-7 rounded-md p-0 hover:bg-transparent"
                        style={{
                          backgroundColor: isActive
                            ? `color-mix(in srgb, ${sidePanelTheme.accent} 24%, transparent)`
                            : 'transparent',
                          color: isActive
                            ? sidePanelTheme.termFg
                            : sidePanelTheme.mutedFg,
                        }}
                        onClick={item.onClick}
                        onDragStart={(event: React.DragEvent) => handleSidePanelTabDragStart(event, item.id)}
                        onDragOver={(event: React.DragEvent) => handleSidePanelTabDragOver(event, item.id)}
                        onDragLeave={(event: React.DragEvent) => handleSidePanelTabDragLeave(event, item.id)}
                        onDrop={(event: React.DragEvent) => handleSidePanelTabDrop(event, item.id)}
                        onDragEnd={() => {
                          draggedSidePanelTabRef.current = null;
                          setDragOverSidePanelTab(null);
                        }}
                      >
                        {showDropIndicator && (
                          <span
                            aria-hidden="true"
                            className={cn(
                              'pointer-events-none absolute top-1 bottom-1 w-0.5 rounded-none',
                              dragOverSidePanelTab?.placement === 'after' ? 'right-0' : 'left-0',
                            )}
                            style={{ backgroundColor: sidePanelTheme.accent }}
                          />
                        )}
                        {item.icon}
                      </Btn>
                    </TooltipTrigger>
                    {/* bottom: left-docked panel tooltips must not cover macOS traffic lights (#2095) */}
                    <TooltipContent side="bottom">{item.label}</TooltipContent>
                  </Tooltip>
                );
              })}
              <ToolbarOverflowMenu
                hasItems={collapsedSidePanelTabs.length > 0}
                label={t('common.more')}
                orientation="horizontal"
                buttonClassName="h-7 w-7 rounded-md p-0 hover:bg-transparent"
                contentClassName="min-w-[10rem] p-1"
              >
                <div className="flex flex-col min-w-[10rem]">
                  {collapsedSidePanelTabs.map((tabId) => {
                    const item = sidePanelTabItemById.get(tabId as TerminalSidePanelTabId);
                    if (!item) return null;
                    const isActive = activeSidePanelTab === item.id;
                    // Leaf click is closed by ToolbarOverflowMenu onClick capture.
                    return (
                      <button
                        key={item.id}
                        type="button"
                        className={cn(
                          'w-full flex items-center gap-2 px-2 py-1.5 text-xs rounded-sm hover:bg-secondary transition-colors text-left',
                          isActive && 'bg-secondary font-medium',
                        )}
                        onClick={item.onClick}
                      >
                        <span className="shrink-0">{item.icon}</span>
                        <span className="truncate">{item.label}</span>
                      </button>
                    );
                  })}
                </div>
              </ToolbarOverflowMenu>
              <div className="flex-1" />
              <SidePanelSplitMenu
                direction="horizontal"
                items={sidePanelTabItems}
                occupiedTools={occupiedSidePanelTools}
                disabled={!activeSidePanelLayout || activePaneCount >= MAX_SIDE_PANEL_PANES}
                onSelect={handleSplitSidePanelPane}
                t={t}
                buttonColor={sidePanelTheme.mutedFg}
              />
              <SidePanelSplitMenu
                direction="vertical"
                items={sidePanelTabItems}
                occupiedTools={occupiedSidePanelTools}
                disabled={!activeSidePanelLayout || activePaneCount >= MAX_SIDE_PANEL_PANES}
                onSelect={handleSplitSidePanelPane}
                t={t}
                buttonColor={sidePanelTheme.mutedFg}
              />
              <Tooltip>
                <TooltipTrigger asChild>
                  <Btn
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 rounded-md p-0 hover:bg-transparent"
                    style={{ color: sidePanelTheme.mutedFg }}
                    onClick={() => setSidePanelPosition((p: 'left' | 'right') => (p === 'left' ? 'right' : 'left'))}
                  >
                    {sidePanelPosition === 'left' ? <PanelRight size={15} /> : <PanelLeft size={15} />}
                  </Btn>
                </TooltipTrigger>
                <TooltipContent side="bottom">
                  {sidePanelPosition === 'left' ? t('terminal.layer.movePanelRight') : t('terminal.layer.movePanelLeft')}
                </TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Btn
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 rounded-md p-0 hover:bg-transparent"
                    style={{ color: sidePanelTheme.mutedFg }}
                    onClick={handleCloseSidePanel}
                  >
                    <X size={15} />
                  </Btn>
                </TooltipTrigger>
                <TooltipContent side="bottom">{t('terminal.layer.closePanel')}</TooltipContent>
              </Tooltip>
          </ToolbarCustomizeContextMenu>
        )}
        <div className="flex-1 min-h-0 min-w-0 relative overflow-hidden" data-section="terminal-side-panel-content">
          {isSidePanelOpenForCurrentTab && activeSidePanelLayout && (
            <SidePanelLayoutTree
              node={activeSidePanelLayout.root}
              layout={activeSidePanelLayout}
              paneCount={activePaneCount}
              labels={sidePanelToolLabels}
              onClose={handleCloseSidePanelPane}
              onFocus={handleFocusSidePanelPane}
              onHostChange={handlePaneHostChange}
              onResize={handleResizeSidePanelSplit}
              separator={sidePanelTheme.separator}
              accent={sidePanelTheme.accent}
            />
          )}
          <div
            ref={parkingHostRef}
            className="hidden absolute inset-0 overflow-hidden [content-visibility:hidden] [contain:strict]"
            aria-hidden="true"
            data-section="terminal-side-panel-parking"
          />
          <MemoizedSidePanelMountedContent
            ctx={ctx}
            paneHosts={paneHosts}
            parkingHost={parkingHost}
          />
        </div>
      </div>
    </div>
  );
}
