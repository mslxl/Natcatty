/**
 * SFTP Conflict Resolution Dialog
 */

import { AlertCircle } from 'lucide-react';
import React, { memo, useState } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { Button } from '../ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/dialog';

interface ConflictItem {
    transferId: string;
    fileName: string;
    sourcePath: string;
    targetPath: string;
    existingSize: number;
    newSize: number;
    existingModified: number;
    newModified: number;
}

interface SftpConflictDialogProps {
    conflicts: ConflictItem[];
    onResolve: (conflictId: string, action: 'replace' | 'skip' | 'duplicate') => void;
    formatFileSize: (size: number) => string;
}

const SftpConflictDialogInner: React.FC<SftpConflictDialogProps> = ({ conflicts, onResolve, formatFileSize }) => {
    const { t } = useI18n();
    const [applyToAll, setApplyToAll] = useState(false);
    const conflict = conflicts[0]; // Handle first conflict

    if (!conflict) return null;

    const formatDate = (timestamp: number) => {
        return new Date(timestamp).toLocaleString();
    };

    const handleAction = (action: 'replace' | 'skip' | 'duplicate') => {
        if (applyToAll) {
            // Apply to all conflicts
            conflicts.forEach(c => onResolve(c.transferId, action));
        } else {
            onResolve(conflict.transferId, action);
        }
        setApplyToAll(false);
    };

    return (
        <Dialog open={!!conflict} onOpenChange={() => handleAction('skip')}>
            <DialogContent className="sm:max-w-[480px]">
                <DialogHeader>
                    <DialogTitle className="flex items-center gap-2">
                        <AlertCircle className="h-5 w-5 text-yellow-500" />
                        {t('sftp.conflict.title')}
                    </DialogTitle>
                    <DialogDescription>
                        {t('sftp.conflict.desc')}
                    </DialogDescription>
                </DialogHeader>

                <div className="space-y-4 py-4">
                    <div className="text-sm">
                        <span className="font-medium">{conflict.fileName}</span>
                        <span className="text-muted-foreground ml-1">{t('sftp.conflict.alreadyExistsSuffix')}</span>
                    </div>

                    <div className="grid grid-cols-2 gap-4 text-xs">
                        <div className="p-3 rounded-lg bg-secondary/50 border border-border/60">
                            <div className="font-medium mb-2 text-muted-foreground">{t('sftp.conflict.existingFile')}</div>
                            <div className="space-y-1">
                                <div className="flex justify-between">
                                    <span className="text-muted-foreground">{t('sftp.conflict.size')}</span>
                                    <span>{formatFileSize(conflict.existingSize)}</span>
                                </div>
                                <div className="flex justify-between">
                                    <span className="text-muted-foreground">{t('sftp.conflict.modified')}</span>
                                    <span>{formatDate(conflict.existingModified)}</span>
                                </div>
                            </div>
                        </div>

                        <div className="p-3 rounded-lg bg-primary/10 border border-primary/30">
                            <div className="font-medium mb-2 text-primary">{t('sftp.conflict.newFile')}</div>
                            <div className="space-y-1">
                                <div className="flex justify-between">
                                    <span className="text-muted-foreground">{t('sftp.conflict.size')}</span>
                                    <span>{formatFileSize(conflict.newSize)}</span>
                                </div>
                                <div className="flex justify-between">
                                    <span className="text-muted-foreground">{t('sftp.conflict.modified')}</span>
                                    <span>{formatDate(conflict.newModified)}</span>
                                </div>
                            </div>
                        </div>
                    </div>

                    {conflicts.length > 1 && (
                        <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
                            <input
                                type="checkbox"
                                checked={applyToAll}
                                onChange={(e) => setApplyToAll(e.target.checked)}
                                className="rounded border-border"
                            />
                            {t('sftp.conflict.applyToAll', { count: conflicts.length })}
                        </label>
                    )}
                </div>

                <DialogFooter className="flex gap-2">
                    <Button
                        variant="outline"
                        onClick={() => handleAction('skip')}
                        className="flex-1"
                    >
                        {t('sftp.conflict.action.skip')}
                    </Button>
                    <Button
                        variant="outline"
                        onClick={() => handleAction('duplicate')}
                        className="flex-1"
                    >
                        {t('sftp.conflict.action.keepBoth')}
                    </Button>
                    <Button
                        variant="default"
                        onClick={() => handleAction('replace')}
                        className="flex-1"
                    >
                        {t('sftp.conflict.action.replace')}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
};

export const SftpConflictDialog = memo(SftpConflictDialogInner);
SftpConflictDialog.displayName = 'SftpConflictDialog';
