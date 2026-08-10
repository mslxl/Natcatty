/**
 * SFTP Breadcrumb navigation component
 */

import { ChevronDown, ChevronRight, Home, MoreHorizontal } from 'lucide-react';
import React, { memo, useCallback, useMemo, useState } from 'react';
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

/** Prefer the path tail when truncating; keep a Windows drive letter when present. */
export function resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts,
    keepLeadingDrive,
}: {
    segments: BreadcrumbSegment[];
    maxVisibleParts: number;
    keepLeadingDrive: boolean;
}): {
    visibleParts: SftpBreadcrumbVisiblePart[];
    hiddenParts: SftpBreadcrumbVisiblePart[];
    needsTruncation: boolean;
} {
    if (segments.length <= maxVisibleParts) {
        return {
            visibleParts: segments.map((segment, idx) => ({ segment, originalIndex: idx })),
            hiddenParts: [],
            needsTruncation: false,
        };
    }

    const lastPartsCount = keepLeadingDrive
        ? Math.max(1, maxVisibleParts - 1)
        : maxVisibleParts;
    const lastParts = segments.slice(-lastPartsCount).map((segment, idx) => ({
        segment,
        originalIndex: segments.length - lastPartsCount + idx,
    }));
    const hiddenStart = keepLeadingDrive ? 1 : 0;
    const hiddenParts = segments.slice(hiddenStart, -lastPartsCount).map((segment, idx) => ({
        segment,
        originalIndex: hiddenStart + idx,
    }));
    const visibleParts = keepLeadingDrive
        ? [{ segment: segments[0], originalIndex: 0 }, ...lastParts]
        : lastParts;

    return {
        visibleParts,
        hiddenParts,
        needsTruncation: true,
    };
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
                keepLeadingDrive: isWindowsDrive,
            }),
        [segments, maxVisibleParts, isWindowsDrive],
    );

    const showDriveDropdown = isWindowsDrive && isLocal && !!onListDrives;

    // Keep the track LTR and left-aligned. Deep paths already prefer trailing
    // segments via resolveSftpBreadcrumbVisibleParts, so avoid RTL clip (it
    // right-aligns short paths like /root).
    return (
        <Tooltip>
            <TooltipTrigger asChild>
                <div className="w-full min-w-0 overflow-hidden cursor-default">
                    <div className="flex items-center gap-1 text-xs text-muted-foreground">
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
                        {needsTruncation && !isWindowsDrive && (
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
                        {visibleParts.map(({ segment, originalIndex }, displayIdx) => {
                            const partPath = segment.path;
                            const isLast = originalIndex === segments.length - 1;
                            const showEllipsisBefore =
                                needsTruncation && isWindowsDrive && displayIdx === 1;

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
