/**
 * SFTP Breadcrumb navigation component
 */

import { ChevronRight, Home, MoreHorizontal } from 'lucide-react';
import React, { memo, useMemo } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { cn } from '../../lib/utils';

interface SftpBreadcrumbProps {
    path: string;
    onNavigate: (path: string) => void;
    onHome: () => void;
    /** Maximum number of visible path segments before truncation (default: 4) */
    maxVisibleParts?: number;
}

const SftpBreadcrumbInner: React.FC<SftpBreadcrumbProps> = ({ 
    path, 
    onNavigate, 
    onHome,
    maxVisibleParts = 4 
}) => {
    const { t } = useI18n();

    // Handle both Windows (C:\path) and Unix (/path) style paths
    const isWindowsPath = /^[A-Za-z]:/.test(path);
    const separator = isWindowsPath ? /[\\/]/ : /\//;
    const parts = path.split(separator).filter(Boolean);

    // For Windows, first part might be drive letter like "C:"
    const buildPath = (index: number) => {
        if (isWindowsPath) {
            return parts.slice(0, index + 1).join('\\');
        }
        return '/' + parts.slice(0, index + 1).join('/');
    };

    // Determine which parts to show (always truncate, no expansion)
    const { visibleParts, hiddenParts, needsTruncation } = useMemo(() => {
        if (parts.length <= maxVisibleParts) {
            return { 
                visibleParts: parts.map((part, idx) => ({ part, originalIndex: idx })), 
                hiddenParts: [] as { part: string; originalIndex: number }[], 
                needsTruncation: false 
            };
        }

        // Show first part + ellipsis + last (maxVisibleParts - 1) parts
        const firstPart = [{ part: parts[0], originalIndex: 0 }];
        const lastPartsCount = maxVisibleParts - 1;
        const lastParts = parts.slice(-lastPartsCount).map((part, idx) => ({
            part,
            originalIndex: parts.length - lastPartsCount + idx
        }));
        const hidden = parts.slice(1, -lastPartsCount).map((part, idx) => ({
            part,
            originalIndex: idx + 1
        }));

        return { 
            visibleParts: [...firstPart, ...lastParts], 
            hiddenParts: hidden, 
            needsTruncation: true 
        };
    }, [parts, maxVisibleParts]);

    return (
        <div 
            className="flex items-center gap-1 text-xs text-muted-foreground overflow-hidden"
            title={path}
        >
            <button
                onClick={onHome}
                className="hover:text-foreground p-1 rounded hover:bg-secondary/60 shrink-0"
                title={t("sftp.goHome")}
            >
                <Home size={12} />
            </button>
            <ChevronRight size={12} className="opacity-40 shrink-0" />
            {visibleParts.map(({ part, originalIndex }, displayIdx) => {
                const partPath = buildPath(originalIndex);
                const isLast = originalIndex === parts.length - 1;
                const showEllipsisBefore = needsTruncation && displayIdx === 1;
                
                return (
                    <React.Fragment key={partPath}>
                        {showEllipsisBefore && (
                            <>
                                <span
                                    className="px-1 py-0.5 shrink-0 flex items-center text-muted-foreground cursor-default"
                                    title={`${t("sftp.showHiddenPaths")}: ${hiddenParts.map(h => h.part).join(' > ')}`}
                                >
                                    <MoreHorizontal size={14} />
                                </span>
                                <ChevronRight size={12} className="opacity-40 shrink-0" />
                            </>
                        )}
                        <button
                            onClick={() => onNavigate(partPath)}
                            className={cn(
                                "hover:text-foreground px-1 py-0.5 rounded hover:bg-secondary/60 truncate max-w-[120px] shrink-0",
                                isLast && "text-foreground font-medium"
                            )}
                            title={part}
                        >
                            {part}
                        </button>
                        {!isLast && <ChevronRight size={12} className="opacity-40 shrink-0" />}
                    </React.Fragment>
                );
            })}
        </div>
    );
};

export const SftpBreadcrumb = memo(SftpBreadcrumbInner);
SftpBreadcrumb.displayName = 'SftpBreadcrumb';
