import React from "react";
import { Download, Trash2 } from "lucide-react";
import { Button } from "../ui/button";
import type { RemoteFile } from "../types";

interface SftpModalFooterProps {
  t: (key: string, params?: Record<string, unknown>) => string;
  files: RemoteFile[];
  selectedFiles: Set<string>;
  loading: boolean;
  uploading: boolean;
  onDownloadSelected: () => void;
  onDeleteSelected: () => void;
}

export const SftpModalFooter: React.FC<SftpModalFooterProps> = ({
  t,
  files,
  selectedFiles,
  loading,
  uploading,
  onDownloadSelected,
  onDeleteSelected,
}) => (
  <div className="px-4 py-2 border-t border-border/60 flex items-center justify-between text-xs text-muted-foreground bg-muted/30 flex-shrink-0">
    <span>
      {t("sftp.itemsCount", { count: files.length })}
      {selectedFiles.size > 0 && (
        <>
          <span className="mx-2">|</span>
          <span className="text-primary">
            {t("sftp.selectedCount", { count: selectedFiles.size })}
          </span>
          <Button
            variant="ghost"
            size="sm"
            className="h-5 px-2 ml-2 text-xs text-primary hover:text-primary"
            onClick={onDownloadSelected}
          >
            <Download size={10} className="mr-1" /> {t("sftp.context.download")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-5 px-2 text-xs text-destructive hover:text-destructive"
            onClick={onDeleteSelected}
          >
            <Trash2 size={10} className="mr-1" /> {t("sftp.context.delete")}
          </Button>
        </>
      )}
    </span>
    <span>
      {loading
        ? t("sftp.status.loading")
        : uploading
          ? t("sftp.status.uploading")
          : t("sftp.status.ready")}
    </span>
  </div>
);
