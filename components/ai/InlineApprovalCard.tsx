/**
 * InlineApprovalCard - Inline tool approval card rendered within chat messages.
 *
 * Replaces the modal PermissionDialog. Shows tool name, arguments, and
 * approve/reject buttons. Keyboard shortcuts: Enter to approve, Escape to reject.
 */

import { Check, ShieldAlert, X } from 'lucide-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { Badge } from '../ui/badge';
import { Button } from '../ui/button';

interface InlineApprovalCardProps {
  toolName: string;
  toolArgs: Record<string, unknown>;
  status: 'pending' | 'approved' | 'denied';
  onApprove: () => void;
  onReject: () => void;
}

const InlineApprovalCard: React.FC<InlineApprovalCardProps> = ({
  toolName,
  toolArgs,
  status,
  onApprove,
  onReject,
}) => {
  const { t } = useI18n();
  const cardRef = useRef<HTMLDivElement>(null);
  const approveBtnRef = useRef<HTMLButtonElement>(null);
  const isPending = status === 'pending';
  const [responded, setResponded] = useState(false);

  // Use refs to always access the latest callbacks without re-registering the listener
  const onApproveRef = useRef(onApprove);
  const onRejectRef = useRef(onReject);
  onApproveRef.current = onApprove;
  onRejectRef.current = onReject;

  const isDisabled = !isPending || responded;

  const handleApprove = useCallback(() => {
    if (isDisabled) return;
    setResponded(true);
    onApproveRef.current();
  }, [isDisabled]);

  const handleReject = useCallback(() => {
    if (isDisabled) return;
    setResponded(true);
    onRejectRef.current();
  }, [isDisabled]);

  // Keyboard shortcuts: handled via local onKeyDown on the focusable card element
  // to avoid conflicts when multiple InlineApprovalCard instances exist simultaneously.
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (isDisabled) return;
    if (e.key === 'Enter') {
      e.preventDefault();
      handleApprove();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      handleReject();
    }
  }, [isDisabled, handleApprove, handleReject]);

  // Auto-focus approve button and auto-scroll into view when mounted as pending
  useEffect(() => {
    if (isPending && cardRef.current) {
      cardRef.current.scrollIntoView({ behavior: 'smooth', block: 'end' });
      approveBtnRef.current?.focus();
    }
  }, [isPending]);

  let formattedArgs: string;
  try {
    formattedArgs = JSON.stringify(toolArgs, null, 2);
  } catch {
    formattedArgs = String(toolArgs);
  }

  // Extract target session info if present
  const sessionId = toolArgs?.sessionId as string | undefined;

  return (
    <div
      ref={cardRef}
      tabIndex={0}
      role="alertdialog"
      aria-label="Tool execution approval required"
      onKeyDown={handleKeyDown}
      className={`rounded-md border overflow-hidden text-[12px] mt-1.5 outline-none ${
        isPending
          ? 'border-yellow-500/30 bg-yellow-500/[0.04]'
          : status === 'approved'
            ? 'border-green-500/20 bg-green-500/[0.03]'
            : 'border-red-500/20 bg-red-500/[0.03]'
      }`}
    >
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5">
        <ShieldAlert
          size={13}
          className={
            isPending
              ? 'text-yellow-500/70 shrink-0'
              : status === 'approved'
                ? 'text-green-400/70 shrink-0'
                : 'text-red-400/70 shrink-0'
          }
        />
        <span className="text-[11px] font-medium text-foreground/70">
          {t('ai.chat.toolApprovalTitle')}
        </span>
        {!isPending && (
          <Badge
            className={`ml-auto text-[10px] px-1.5 py-0 ${
              status === 'approved'
                ? 'bg-green-600/20 text-green-400 border-green-600/30'
                : 'bg-red-600/20 text-red-400 border-red-600/30'
            }`}
          >
            {status === 'approved' ? t('ai.chat.toolApproved') : t('ai.chat.toolDenied')}
          </Badge>
        )}
      </div>

      {/* Tool info */}
      <div className="px-3 pb-2 space-y-1.5">
        <div className="flex items-center gap-2">
          <span className="text-[10px] text-muted-foreground/40 uppercase tracking-wider">{t('ai.chat.toolLabel')}</span>
          <code className="text-[11px] font-mono text-muted-foreground/70 bg-muted/30 px-1.5 py-0.5 rounded">
            {toolName}
          </code>
        </div>

        {sessionId && (
          <div className="flex items-center gap-2">
            <span className="text-[10px] text-muted-foreground/40 uppercase tracking-wider">{t('ai.chat.targetLabel')}</span>
            <code className="text-[11px] font-mono text-muted-foreground/50 bg-muted/30 px-1.5 py-0.5 rounded">
              {sessionId}
            </code>
          </div>
        )}

        {/* Arguments */}
        <div className="rounded border border-border/20 bg-muted/10 p-2 max-h-32 overflow-auto">
          <pre className="text-[11px] font-mono whitespace-pre-wrap break-all text-muted-foreground/50">
            {formattedArgs}
          </pre>
        </div>

        {/* Actions or hint */}
        {isPending && (
          <div className="flex items-center justify-between pt-0.5">
            <span className="text-[10px] text-muted-foreground/30">
              {t('ai.chat.toolApprovalHint')}
            </span>
            <div className="flex items-center gap-1.5">
              <Button
                variant="outline"
                size="sm"
                disabled={responded}
                className={`h-6 px-2 text-[11px] border-red-500/20 text-red-400/80 hover:bg-red-500/10 hover:text-red-400 ${responded ? 'opacity-50 cursor-not-allowed' : ''}`}
                onClick={handleReject}
              >
                <X size={11} className="mr-0.5" />
                {t('ai.chat.reject')}
              </Button>
              <Button
                ref={approveBtnRef}
                size="sm"
                disabled={responded}
                className={`h-6 px-2.5 text-[11px] bg-green-600/80 hover:bg-green-600 text-white ${responded ? 'opacity-50 cursor-not-allowed' : ''}`}
                onClick={handleApprove}
              >
                <Check size={11} className="mr-0.5" />
                {t('ai.chat.approve')}
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

InlineApprovalCard.displayName = 'InlineApprovalCard';

export default InlineApprovalCard;
export { InlineApprovalCard };
export type { InlineApprovalCardProps };
