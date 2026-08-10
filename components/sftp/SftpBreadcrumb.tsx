/**
 * SFTP Breadcrumb navigation component
 */

import { ChevronDown, ChevronRight, Home, MoreHorizontal } from 'lucide-react';
import React, { memo, useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { getSftpBreadcrumbSegments } from '../../application/state/sftp/utils';
import type { SftpWindowsPathOptions } from '../../application/state/sftp/utils';
import { Dropdown, DropdownContent, DropdownTrigger } from '../ui/dropdown';
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip';
import { cn } from '../../lib/utils';

interface SftpBreadcrumbProps {
    path: string;
    onNavigate: (path: string) => void;
    onHome: () => void;
    /** Maximum number of visible path segments before truncation (default: 4) */
    maxVisibleParts?: number;
    isLocal?: boolean;
    onListDrives?: () => Promise<string[]>;
    /** When true, treat //host/share as Windows UNC (Windows-style panes). */
    acceptForwardSlashUnc?: boolean;
}

type BreadcrumbSegment = ReturnType<typeof getSftpBreadcrumbSegments>['segments'][number];

export type SftpBreadcrumbVisiblePart = {
    segment: BreadcrumbSegment;
    originalIndex: number;
};

/** Clamp the visible-segment budget to a positive integer. */
export function normalizeSftpBreadcrumbMaxVisibleParts(maxVisibleParts: number): number {
    if (!Number.isFinite(maxVisibleParts)) return 1;
    return Math.max(1, Math.floor(maxVisibleParts));
}

/**
 * Prefer the path tail when truncating, but always keep the first segment so the
 * root / drive / UNC share stays clickable. Budget of 1 shows only the first segment.
 */
export function resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts,
}: {
    segments: BreadcrumbSegment[];
    maxVisibleParts: number;
}): {
    visibleParts: SftpBreadcrumbVisiblePart[];
    hiddenParts: SftpBreadcrumbVisiblePart[];
    needsTruncation: boolean;
} {
    const budget = normalizeSftpBreadcrumbMaxVisibleParts(maxVisibleParts);
    if (segments.length <= budget) {
        return {
            visibleParts: segments.map((segment, idx) => ({ segment, originalIndex: idx })),
            hiddenParts: [],
            needsTruncation: false,
        };
    }

    if (budget === 1) {
        return {
            visibleParts: [{ segment: segments[0], originalIndex: 0 }],
            hiddenParts: segments.slice(1).map((segment, idx) => ({
                segment,
                originalIndex: idx + 1,
            })),
            needsTruncation: true,
        };
    }

    const lastPartsCount = budget - 1;
    const lastParts = segments.slice(-lastPartsCount).map((segment, idx) => ({
        segment,
        originalIndex: segments.length - lastPartsCount + idx,
    }));
    const hiddenParts = segments.slice(1, -lastPartsCount).map((segment, idx) => ({
        segment,
        originalIndex: idx + 1,
    }));

    return {
        visibleParts: [{ segment: segments[0], originalIndex: 0 }, ...lastParts],
        hiddenParts,
        needsTruncation: true,
    };
}

/** Scroll a breadcrumb viewport so overflow keeps the trailing path visible. */
export function scrollSftpBreadcrumbViewportToTail(viewport: HTMLElement | null): void {
    if (!viewport) return;
    viewport.scrollLeft = Math.max(0, viewport.scrollWidth - viewport.clientWidth);
}

const SftpBreadcrumbInner: React.FC<SftpBreadcrumbProps> = ({
    path,
    onNavigate,
    onHome,
    maxVisibleParts = 4,
    isLocal,
    onListDrives,
    acceptForwardSlashUnc = false,
}) => {
    const { t } = useI18n();

    const [drives, setDrives] = useState<string[]>([]);
    const [driveDropdownOpen, setDriveDropdownOpen] = useState(false);
    const viewportRef = useRef<HTMLDivElement>(null);
    const trackRef = useRef<HTMLDivElement>(null);

    const handleDriveDropdownOpen = useCallback(async (open: boolean) => {
        setDriveDropdownOpen(open);
        if (open && onListDrives) {
            const result = await onListDrives();
            setDrives(result);
        }
    }, [onListDrives]);

    const pathOptions = useMemo<SftpWindowsPathOptions>(
        () => ({ acceptForwardSlashUnc }),
        [acceptForwardSlashUnc],
    );

    const { segments, isWindowsDrive } = useMemo(
        () => getSftpBreadcrumbSegments(path, pathOptions),
        [path, pathOptions],
    );

    const { visibleParts, hiddenParts, needsTruncation } = useMemo(
        () =>
            resolveSftpBreadcrumbVisibleParts({
                segments,
                maxVisibleParts,
            }),
        [segments, maxVisibleParts],
    );

    const syncTailScroll = useCallback(() => {
        scrollSftpBreadcrumbViewportToTail(viewportRef.current);
    }, []);

    useLayoutEffect(() => {
        syncTailScroll();
        const viewport = viewportRef.current;
        if (!viewport || typeof ResizeObserver === 'undefined') return;
        const ro = new ResizeObserver(() => syncTailScroll());
        ro.observe(viewport);
        const track = trackRef.current;
        if (track) ro.observe(track);
        return () => ro.disconnect();
    }, [syncTailScroll, path, visibleParts, needsTruncation]);

    const showDriveDropdown = isWindowsDrive && isLocal && !!onListDrives;

    // LTR + left-aligned when the path fits. When it overflows, scroll to the
    // trailing edge so the current directory stays visible without RTL layout.
    return (
        <Tooltip>
            <TooltipTrigger asChild>
                <div
                    ref={viewportRef}
                    className="w-full min-w-0 overflow-hidden cursor-default"
                >
                    <div
                        ref={trackRef}
                        className="flex w-max max-w-none items-center gap-1 text-xs text-muted-foreground"
                    >
                        <Tooltip>
                            <TooltipTrigger asChild>
                                <button
                                    onClick={onHome}
                                    className="hover:text-foreground p-1 rounded hover:bg-secondary/60 shrink-0"
                                >
                                    <Home size={12} />
                                </button>
                            </TooltipTrigger>
                            <TooltipContent>{t("sftp.goHome")}</TooltipContent>
                        </Tooltip>
                        <ChevronRight size={12} className="opacity-40 shrink-0" />
                        {visibleParts.map(({ segment, originalIndex }, displayIdx) => {
                            const partPath = segment.path;
                            const isLast = originalIndex === segments.length - 1;
                            const showEllipsisBefore =
                                needsTruncation && displayIdx === 1;

                            return (
                                <React.Fragment key={partPath}>
                                    {showEllipsisBefore && (
                                        <>
                                            <Tooltip>
                                                <TooltipTrigger asChild>
                                                    <span className="px-1 py-0.5 shrink-0 flex items-center text-muted-foreground cursor-default">
                                                        <MoreHorizontal size={14} />
                                                    </span>
                                                </TooltipTrigger>
                                                <TooltipContent>
                                                    {`${t("sftp.showHiddenPaths")}: ${hiddenParts.map(h => h.segment.label).join(' > ')}`}
                                                </TooltipContent>
                                            </Tooltip>
                                            <ChevronRight size={12} className="opacity-40 shrink-0" />
                                        </>
                                    )}
                                    {originalIndex === 0 && showDriveDropdown ? (
                                        <Dropdown open={driveDropdownOpen} onOpenChange={handleDriveDropdownOpen}>
                                            <DropdownTrigger asChild>
                                                <button className="hover:text-foreground px-1 py-0.5 rounded hover:bg-secondary/60 shrink-0 flex items-center gap-0.5">
                                                    {segment.label}
                                                    <ChevronDown size={10} className="opacity-60" />
                                                </button>
                                            </DropdownTrigger>
                                            <DropdownContent align="start" className="w-16 p-1">
                                                {drives.map(drive => (
                                                    <button
                                                        key={drive}
                                                        onClick={() => { onNavigate(drive + '\\'); setDriveDropdownOpen(false); }}
                                                        className={cn(
                                                            "w-full text-left px-2 py-1 text-xs rounded hover:bg-secondary/60",
                                                            drive === segment.label && "bg-secondary font-medium"
                                                        )}
                                                    >
                                                        {drive}
                                                    </button>
                                                ))}
                                            </DropdownContent>
                                        </Dropdown>
                                    ) : (
                                        <Tooltip>
                                            <TooltipTrigger asChild>
                                                <button
                                                    onClick={() => onNavigate(partPath)}
                                                    className={cn(
                                                        "hover:text-foreground px-1 py-0.5 rounded hover:bg-secondary/60 truncate max-w-[160px] shrink-0",
                                                        isLast && "text-foreground font-medium"
                                                    )}
                                                >
                                                    {segment.label}
                                                </button>
                                            </TooltipTrigger>
                                            <TooltipContent>{segment.label}</TooltipContent>
                                        </Tooltip>
                                    )}
                                    {!isLast && <ChevronRight size={12} className="opacity-40 shrink-0" />}
                                </React.Fragment>
                            );
                        })}
                    </div>
                </div>
            </TooltipTrigger>
            <TooltipContent>{path}</TooltipContent>
        </Tooltip>
    );
};

export const SftpBreadcrumb = memo(SftpBreadcrumbInner);
SftpBreadcrumb.displayName = 'SftpBreadcrumb';
