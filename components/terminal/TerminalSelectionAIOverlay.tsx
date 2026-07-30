/**
 * Owns selection-driven "Add to AI" chrome so selection change does not re-render
 * the whole Terminal / TerminalView tree (common when focus moves to the AI input).
 */
import React, { memo, useEffect, useState, type RefObject } from 'react';
import type { Terminal as XTerm } from '@xterm/xterm';
import { Sparkles } from 'lucide-react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { getTerminalSelectionForClipboard } from './normalizeTerminalSelection';
import { resolveSelectionOverlayPosition } from './useTerminalEffects';
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip';
import { shouldShowSelectionAIOverlay } from './TerminalView';

type SelectionOverlayPosition = { left: number; top: number } | null;

const areSelectionOverlayPositionsEqual = (
  a: SelectionOverlayPosition,
  b: SelectionOverlayPosition,
): boolean => {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.left === b.left && a.top === b.top;
};

type Props = {
  termRef: RefObject<XTerm | null>;
  containerRef: RefObject<HTMLElement | null>;
  showSelectionAIAction?: boolean;
  onAddSelectionToAI?: () => void;
  copyOnSelect?: boolean;
  normalizeTextOnCopy?: boolean;
  isVisible?: boolean;
};

function TerminalSelectionAIOverlayInner({
  termRef,
  containerRef,
  showSelectionAIAction,
  onAddSelectionToAI,
  copyOnSelect,
  normalizeTextOnCopy = true,
  isVisible = true,
}: Props) {
  const { t } = useI18n();
  const [hasSelection, setHasSelection] = useState(false);
  const [selectionOverlayPosition, setSelectionOverlayPosition] = useState<SelectionOverlayPosition>(null);

  useEffect(() => {
    const term = termRef.current;
    if (!term || !isVisible) return;

    let overlayRafId: number | null = null;
    let copyTimer: ReturnType<typeof setTimeout> | null = null;
    let lastHasSelection: boolean | null = null;
    let lastOverlayPosition: SelectionOverlayPosition = null;
    const requestFrame = typeof requestAnimationFrame === 'function'
      ? requestAnimationFrame
      : (callback: FrameRequestCallback) => setTimeout(() => callback(Date.now()), 0) as unknown as number;
    const cancelFrame = typeof cancelAnimationFrame === 'function'
      ? cancelAnimationFrame
      : (id: number) => clearTimeout(id);

    const publishSelectionOverlayPosition = () => {
      overlayRafId = null;
      const nextPosition = resolveSelectionOverlayPosition(term, containerRef.current);
      if (areSelectionOverlayPositionsEqual(lastOverlayPosition, nextPosition)) return;
      lastOverlayPosition = nextPosition;
      setSelectionOverlayPosition(nextPosition);
    };

    const scheduleSelectionOverlayPosition = () => {
      if (lastHasSelection === false) return;
      if (overlayRafId !== null) return;
      overlayRafId = requestFrame(publishSelectionOverlayPosition);
    };

    const onSelectionChange = () => {
      const rawSelection = term.getSelection();
      const hasText = !!rawSelection && rawSelection.length > 0;
      if (lastHasSelection !== hasText) {
        lastHasSelection = hasText;
        setHasSelection(hasText);
      }
      if (copyTimer) {
        clearTimeout(copyTimer);
        copyTimer = null;
      }
      if (!hasText) {
        if (lastOverlayPosition !== null) {
          lastOverlayPosition = null;
          setSelectionOverlayPosition(null);
        }
        return;
      }
      scheduleSelectionOverlayPosition();

      if (hasText && copyOnSelect) {
        const selection = getTerminalSelectionForClipboard(term, normalizeTextOnCopy);
        if (!selection) return;
        copyTimer = setTimeout(() => {
          void navigator.clipboard.writeText(selection).catch(() => {
            /* ignore clipboard failures */
          });
        }, 80);
      }
    };

    const selectionDisposable = term.onSelectionChange(onSelectionChange);
    const scrollDisposable = term.onScroll?.(scheduleSelectionOverlayPosition);
    const resizeDisposable = term.onResize?.(scheduleSelectionOverlayPosition);
    const resizeObserver = typeof ResizeObserver === 'undefined'
      ? null
      : new ResizeObserver(scheduleSelectionOverlayPosition);
    if (containerRef.current) {
      resizeObserver?.observe(containerRef.current);
    }
    onSelectionChange();

    return () => {
      if (overlayRafId !== null) cancelFrame(overlayRafId);
      if (copyTimer) clearTimeout(copyTimer);
      selectionDisposable.dispose();
      scrollDisposable?.dispose();
      resizeDisposable?.dispose();
      resizeObserver?.disconnect();
    };
  }, [
    termRef,
    containerRef,
    copyOnSelect,
    normalizeTextOnCopy,
    isVisible,
  ]);

  if (!shouldShowSelectionAIOverlay({
    hasSelection,
    selectionOverlayPosition,
    onAddSelectionToAI,
    showSelectionAIAction,
  }) || !onAddSelectionToAI || !selectionOverlayPosition) {
    return null;
  }

  return (
    <div
      className="absolute z-30 pointer-events-none"
      style={{
        left: selectionOverlayPosition.left,
        top: selectionOverlayPosition.top,
        transform: 'translate(-100%, -100%)',
      }}
    >
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            className="pointer-events-auto inline-flex h-7 min-w-max items-center gap-1.5 whitespace-nowrap rounded-md border px-2 text-[11px] font-medium shadow-lg backdrop-blur-md transition-colors hover:bg-[color:var(--terminal-toolbar-btn-hover)]"
            style={{
              backgroundColor: 'color-mix(in srgb, var(--terminal-ui-bg) 86%, transparent)',
              borderColor: 'var(--terminal-ui-border)',
              color: 'var(--terminal-ui-fg)',
            }}
            onMouseDown={(e) => {
              e.preventDefault();
              e.stopPropagation();
            }}
            onClick={onAddSelectionToAI}
            aria-label={t('terminal.selection.addToAI')}
          >
            <Sparkles size={12} />
            <span>{t('terminal.selection.addToAI')}</span>
          </button>
        </TooltipTrigger>
        <TooltipContent>{t('terminal.selection.addToAIDesc')}</TooltipContent>
      </Tooltip>
    </div>
  );
}

export const TerminalSelectionAIOverlay = memo(TerminalSelectionAIOverlayInner);
TerminalSelectionAIOverlay.displayName = 'TerminalSelectionAIOverlay';
