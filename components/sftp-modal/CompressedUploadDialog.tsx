import React from "react";
import { useI18n } from "../../application/i18n/I18nProvider";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";

interface CompressedUploadDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: (useCompressed: boolean) => void;
  folderCount: number;
}

export function CompressedUploadDialog({ 
  open, 
  onClose, 
  onConfirm, 
  folderCount 
}: CompressedUploadDialogProps) {
  const { t } = useI18n();

  const handleUseCompressed = () => {
    onConfirm(true);
    onClose();
  };

  const handleUseRegular = () => {
    onConfirm(false);
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("sftp.compressedUpload.dialog.title")}</DialogTitle>
          <DialogDescription>
            {folderCount === 1 
              ? t("sftp.compressedUpload.dialog.descSingle")
              : t("sftp.compressedUpload.dialog.descMultiple", { count: folderCount })
            }
          </DialogDescription>
        </DialogHeader>
        
        <div className="space-y-3 py-4">
          <div className="text-sm">
            <div className="font-medium text-green-600 mb-1">
              {t("sftp.compressedUpload.dialog.compressed.title")}
            </div>
            <div className="text-muted-foreground">
              {t("sftp.compressedUpload.dialog.compressed.desc")}
            </div>
          </div>
          
          <div className="text-sm">
            <div className="font-medium text-blue-600 mb-1">
              {t("sftp.compressedUpload.dialog.regular.title")}
            </div>
            <div className="text-muted-foreground">
              {t("sftp.compressedUpload.dialog.regular.desc")}
            </div>
          </div>
        </div>

        <DialogFooter className="flex-col sm:flex-row gap-2">
          <Button variant="outline" onClick={handleUseRegular} className="w-full sm:w-auto">
            {t("sftp.compressedUpload.dialog.useRegular")}
          </Button>
          <Button onClick={handleUseCompressed} className="w-full sm:w-auto">
            {t("sftp.compressedUpload.dialog.useCompressed")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}