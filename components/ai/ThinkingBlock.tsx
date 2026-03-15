/**
 * ThinkingBlock - Collapsible thinking/reasoning display
 *
 * - While streaming: expanded, "Thinking" label with shimmer + elapsed time
 * - When done: auto-collapses to "Thought for Xs", click to expand
 * - Content area has max-height with scroll and top gradient fade
 */

import { ChevronRight } from 'lucide-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { cn } from '../../lib/utils';

interface ThinkingBlockProps {
  content: string;
  isStreaming: boolean;
  durationMs?: number;
}

function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remaining = seconds % 60;
  return `${minutes}m ${remaining}s`;
}

const ThinkingBlock: React.FC<ThinkingBlockProps> = ({
  content,
  isStreaming,
  durationMs,
}) => {
  const { t } = useI18n();
  const [isExpanded, setIsExpanded] = useState(isStreaming);
  const [elapsed, setElapsed] = useState(0);
  const wasStreamingRef = useRef(false);
  const startRef = useRef(Date.now());
  const scrollRef = useRef<HTMLDivElement>(null);

  // Auto-collapse when streaming ends
  useEffect(() => {
    if (wasStreamingRef.current && !isStreaming) {
      setIsExpanded(false);
    }
    wasStreamingRef.current = isStreaming;
  }, [isStreaming]);

  // Expand when streaming starts
  useEffect(() => {
    if (isStreaming) {
      setIsExpanded(true);
      startRef.current = Date.now();
    }
  }, [isStreaming]);

  // Elapsed time ticker
  useEffect(() => {
    if (!isStreaming) return;
    const timer = setInterval(() => {
      setElapsed(Date.now() - startRef.current);
    }, 1000);
    return () => clearInterval(timer);
  }, [isStreaming]);

  // Auto-scroll to bottom while streaming
  useEffect(() => {
    if (isStreaming && isExpanded && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [content, isStreaming, isExpanded]);

  const toggle = useCallback(() => setIsExpanded(e => !e), []);

  const displayDuration = durationMs || elapsed;
  const preview = content.length > 60 ? content.slice(0, 60) + '…' : content;

  return (
    <div className="mb-0.5">
      {/* Header */}
      <button
        onClick={toggle}
        className="group flex items-center gap-1.5 py-0.5 px-1 cursor-pointer text-left w-full rounded hover:bg-white/[0.03] transition-colors"
      >
        <ChevronRight
          size={12}
          className={cn(
            'shrink-0 text-muted-foreground/50 transition-transform duration-200',
            isExpanded && 'rotate-90',
            !isExpanded && 'opacity-50',
          )}
        />
        <span className="text-[12px] font-medium text-muted-foreground/70 whitespace-nowrap shrink-0">
          {isStreaming ? (
            <span className="thinking-shimmer">{t('ai.chat.thinking')}</span>
          ) : (
            displayDuration > 0
              ? t('ai.chat.thoughtFor', { duration: formatDuration(displayDuration) })
              : t('ai.chat.thought')
          )}
        </span>
        {isStreaming && elapsed > 0 && (
          <span className="text-[11px] text-muted-foreground/40 tabular-nums shrink-0">
            {formatDuration(elapsed)}
          </span>
        )}
        {!isExpanded && !isStreaming && preview && (
          <span className="text-[11px] text-muted-foreground/40 truncate min-w-0">
            {preview}
          </span>
        )}
      </button>

      {/* Content */}
      {isExpanded && content && (
        <div className="relative">
          {/* Top gradient fade */}
          {isStreaming && (
            <div className="absolute inset-x-0 top-0 h-4 bg-gradient-to-b from-background to-transparent z-10 pointer-events-none" />
          )}
          <div
            ref={scrollRef}
            className={cn(
              'px-5 text-[12px] text-muted-foreground/60 leading-relaxed whitespace-pre-wrap break-words',
              isStreaming && 'overflow-y-auto scrollbar-hide max-h-36',
              !isStreaming && 'max-h-36 overflow-y-auto scrollbar-hide',
            )}
          >
            {content}
          </div>
        </div>
      )}
    </div>
  );
};

export default React.memo(ThinkingBlock);
