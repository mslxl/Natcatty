/**
 * FileOpenerDialog - Dialog for choosing how to open a file
 */
import { Edit2, Eye } from 'lucide-react';
import React, { useCallback, useState } from 'react';
import { useI18n } from '../application/i18n/I18nProvider';
import type { FileOpenerType } from '../lib/sftpFileUtils';
import { getFileExtension, isImageFile, isTextFile } from '../lib/sftpFileUtils';
import { Button } from './ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './ui/dialog';

interface FileOpenerDialogProps {
  open: boolean;
  onClose: () => void;
  fileName: string;
  onSelect: (openerType: FileOpenerType, setAsDefault: boolean) => void;
}

export const FileOpenerDialog: React.FC<FileOpenerDialogProps> = ({
  open,
  onClose,
  fileName,
  onSelect,
}) => {
  const { t } = useI18n();
  const [setAsDefault, setSetAsDefault] = useState(false);
  const extension = getFileExtension(fileName);
  const canEdit = isTextFile(fileName);
  const canPreview = isImageFile(fileName);

  const handleSelect = useCallback((openerType: FileOpenerType) => {
    onSelect(openerType, setAsDefault);
    onClose();
  }, [onSelect, setAsDefault, onClose]);

  return (
    <Dialog open={open} onOpenChange={(isOpen) => !isOpen && onClose()}>
      <DialogContent className="sm:max-w-[400px]">
        <DialogHeader>
          <DialogTitle>{t('sftp.opener.title')}</DialogTitle>
          <DialogDescription className="truncate">
            {fileName}
          </DialogDescription>
        </DialogHeader>

        <div className="py-4 space-y-2">
          {canEdit && (
            <Button
              variant="outline"
              className="w-full justify-start gap-3 h-12"
              onClick={() => handleSelect('builtin-editor')}
            >
              <Edit2 size={18} className="text-primary" />
              <div className="text-left">
                <div className="font-medium text-sm">{t('sftp.opener.builtInEditor')}</div>
                <div className="text-xs text-muted-foreground">Edit text files</div>
              </div>
            </Button>
          )}
          
          {canPreview && (
            <Button
              variant="outline"
              className="w-full justify-start gap-3 h-12"
              onClick={() => handleSelect('builtin-image-viewer')}
            >
              <Eye size={18} className="text-primary" />
              <div className="text-left">
                <div className="font-medium text-sm">{t('sftp.opener.builtInImageViewer')}</div>
                <div className="text-xs text-muted-foreground">Preview images</div>
              </div>
            </Button>
          )}
          
          {!canEdit && !canPreview && (
            <div className="text-sm text-muted-foreground text-center py-4">
              {t('sftp.opener.noAppsAvailable')}
            </div>
          )}
        </div>

        {(canEdit || canPreview) && extension !== 'file' && (
          <div className="flex items-center gap-2 pb-2">
            <input
              type="checkbox"
              id="set-as-default"
              checked={setAsDefault}
              onChange={(e) => setSetAsDefault(e.target.checked)}
              className="rounded border-border"
            />
            <label htmlFor="set-as-default" className="text-sm text-muted-foreground cursor-pointer">
              {t('sftp.opener.setDefault', { ext: extension })}
            </label>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default FileOpenerDialog;
