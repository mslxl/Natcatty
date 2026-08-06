import React, { memo } from 'react';
import { cn } from '../../lib/utils';

interface ResourceBarProps {
  label: string;
  value: number | null | undefined;
  className?: string;
}

export const ResourceBar = memo(function ResourceBar({ label, value, className }: ResourceBarProps) {
  const finite = typeof value === 'number' && Number.isFinite(value);
  const clamped = finite ? Math.max(0, Math.min(100, value)) : 0;
  return (
    <div className={cn('flex items-center gap-2 min-w-0', className)}>
      <span className="text-[10px] text-muted-foreground w-7 shrink-0">{label}</span>
      <div className="flex-1 h-1 rounded-full bg-muted/50 overflow-hidden min-w-[48px]">
        <div
          className={cn(
            'h-full rounded-full',
            finite && clamped > 85 ? 'bg-destructive/70' : 'bg-primary/70',
            !finite && 'opacity-0',
          )}
          style={{ width: `${clamped}%` }}
        />
      </div>
      <span className="text-[10px] tabular-nums text-muted-foreground w-10 text-right shrink-0">
        {finite ? `${value.toFixed(1)}%` : '--'}
      </span>
    </div>
  );
});
