import React from "react";
import { Loader2 } from "lucide-react";
import type { Host, SftpFileEntry } from "../../types";
import type { FileOpenerType, SystemAppInfo } from "../../lib/sftpFileUtils";
import type { useSftpState } from "../../application/state/useSftpState";
import { Button } from "../ui/button";
import FileOpenerDialog from "../FileOpenerDialog";
import TextEditorModal from "../TextEditorModal";
import { SftpConflictDialog, SftpHostPicker, SftpPermissionsDialog, SftpTransferItem } from "./index";

type SftpState = ReturnType<typeof useSftpState>;

interface SftpOverlaysProps {
  hosts: Host[];
  sftp: SftpState;
  visibleTransfers: SftpState["transfers"];
  showHostPickerLeft: boolean;
  showHostPickerRight: boolean;
  hostSearchLeft: string;
  hostSearchRight: string;
  setShowHostPickerLeft: (open: boolean) => void;
  setShowHostPickerRight: (open: boolean) => void;
  setHostSearchLeft: (value: string) => void;
  setHostSearchRight: (value: string) => void;
  handleHostSelectLeft: (host: Host | "local") => void;
  handleHostSelectRight: (host: Host | "local") => void;
  permissionsState: { file: SftpFileEntry; side: "left" | "right" } | null;
  setPermissionsState: (state: { file: SftpFileEntry; side: "left" | "right" } | null) => void;
  showTextEditor: boolean;
  setShowTextEditor: (open: boolean) => void;
  textEditorTarget: { file: SftpFileEntry; side: "left" | "right"; fullPath: string } | null;
  setTextEditorTarget: (target: { file: SftpFileEntry; side: "left" | "right"; fullPath: string } | null) => void;
  textEditorContent: string;
  setTextEditorContent: (content: string) => void;
  handleSaveTextFile: (content: string) => Promise<void>;
  showFileOpenerDialog: boolean;
  setShowFileOpenerDialog: (open: boolean) => void;
  fileOpenerTarget: { file: SftpFileEntry; side: "left" | "right"; fullPath: string } | null;
  setFileOpenerTarget: (target: { file: SftpFileEntry; side: "left" | "right"; fullPath: string } | null) => void;
  handleFileOpenerSelect: (openerType: FileOpenerType, setAsDefault: boolean, systemApp?: SystemAppInfo) => void;
  handleSelectSystemApp: (systemApp: { path: string; name: string }) => void;
  t: (key: string, params?: Record<string, unknown>) => string;
}

export const SftpOverlays: React.FC<SftpOverlaysProps> = ({
  hosts,
  sftp,
  visibleTransfers,
  showHostPickerLeft,
  showHostPickerRight,
  hostSearchLeft,
  hostSearchRight,
  setShowHostPickerLeft,
  setShowHostPickerRight,
  setHostSearchLeft,
  setHostSearchRight,
  handleHostSelectLeft,
  handleHostSelectRight,
  permissionsState,
  setPermissionsState,
  showTextEditor,
  setShowTextEditor,
  textEditorTarget,
  setTextEditorTarget,
  textEditorContent,
  setTextEditorContent,
  handleSaveTextFile,
  showFileOpenerDialog,
  setShowFileOpenerDialog,
  fileOpenerTarget,
  setFileOpenerTarget,
  handleFileOpenerSelect,
  handleSelectSystemApp,
  t,
}) => {
  return (
    <>
      {/* Host pickers for adding new tabs */}
      <SftpHostPicker
        open={showHostPickerLeft}
        onOpenChange={setShowHostPickerLeft}
        hosts={hosts}
        side="left"
        hostSearch={hostSearchLeft}
        onHostSearchChange={setHostSearchLeft}
        onSelectLocal={() => handleHostSelectLeft("local")}
        onSelectHost={handleHostSelectLeft}
      />
      <SftpHostPicker
        open={showHostPickerRight}
        onOpenChange={setShowHostPickerRight}
        hosts={hosts}
        side="right"
        hostSearch={hostSearchRight}
        onHostSearchChange={setHostSearchRight}
        onSelectLocal={() => handleHostSelectRight("local")}
        onSelectHost={handleHostSelectRight}
      />

      {/* Transfer status area - shows folder uploads and file transfers */}
      {(sftp.transfers.length > 0 || sftp.folderUploadProgress.isUploading) && (
        <div className="border-t border-border/70 bg-secondary/80 backdrop-blur-sm shrink-0">
          <div className="flex items-center justify-between px-4 py-2 text-xs text-muted-foreground border-b border-border/40">
            <span className="font-medium">
              Transfers
              {(sftp.activeTransfersCount > 0 || sftp.folderUploadProgress.isUploading) && (
                <span className="ml-2 text-primary">
                  ({sftp.activeTransfersCount + (sftp.folderUploadProgress.isUploading ? 1 : 0)} active)
                </span>
              )}
            </span>
            {sftp.transfers.some(
              (t) => t.status === "completed" || t.status === "cancelled",
            ) && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 px-2 text-xs"
                  onClick={sftp.clearCompletedTransfers}
                >
                  Clear completed
                </Button>
              )}
          </div>
          <div className="max-h-40 overflow-auto">
            {/* Folder upload progress - shown at top when active */}
            {sftp.folderUploadProgress.isUploading && (
              <div className="flex items-center gap-3 px-4 py-2 border-b border-border/30 bg-primary/5">
                <div className="flex-shrink-0">
                  <Loader2 size={16} className="animate-spin text-primary" />
                </div>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center justify-between text-sm">
                    <span className="font-medium truncate">
                      {t("sftp.upload.progress", {
                        current: sftp.folderUploadProgress.currentIndex,
                        total: sftp.folderUploadProgress.totalFiles,
                      })}
                    </span>
                    {sftp.folderUploadProgress.currentFileTotalBytes > 0 && (
                      <span className="text-xs text-muted-foreground ml-2 flex-shrink-0">
                        {sftp.formatFileSize(sftp.folderUploadProgress.currentFileBytes)} / {sftp.formatFileSize(sftp.folderUploadProgress.currentFileTotalBytes)}
                        {sftp.folderUploadProgress.currentFileSpeed > 0 && (
                          <> ({sftp.formatFileSize(sftp.folderUploadProgress.currentFileSpeed)}/s)</>
                        )}
                      </span>
                    )}
                  </div>
                  {sftp.folderUploadProgress.currentFileTotalBytes > 0 && (
                    <div className="w-full bg-muted/30 rounded-full h-1.5 mt-1">
                      <div
                        className="bg-primary h-1.5 rounded-full transition-all duration-150 ease-out"
                        style={{
                          width: `${Math.min((sftp.folderUploadProgress.currentFileBytes / Math.max(sftp.folderUploadProgress.currentFileTotalBytes, 1)) * 100, 100)}%`,
                        }}
                      />
                    </div>
                  )}
                  {sftp.folderUploadProgress.currentFile && (
                    <div className="text-xs text-muted-foreground truncate mt-0.5">
                      {sftp.folderUploadProgress.currentFile}
                    </div>
                  )}
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 px-2 text-xs flex-shrink-0"
                  onClick={() => sftp.cancelFolderUpload()}
                >
                  {t("sftp.upload.cancel")}
                </Button>
              </div>
            )}
            {visibleTransfers.map((task) => (
              <SftpTransferItem
                key={task.id}
                task={task}
                onCancel={() => sftp.cancelTransfer(task.id)}
                onRetry={() => sftp.retryTransfer(task.id)}
                onDismiss={() => sftp.dismissTransfer(task.id)}
              />
            ))}
          </div>
        </div>
      )}

      <SftpConflictDialog
        conflicts={sftp.conflicts}
        onResolve={sftp.resolveConflict}
        formatFileSize={sftp.formatFileSize}
      />

      <SftpPermissionsDialog
        open={!!permissionsState}
        onOpenChange={(open) => !open && setPermissionsState(null)}
        file={permissionsState?.file ?? null}
        onSave={(file, permissions) => {
          if (permissionsState) {
            const fullPath = sftp.joinPath(
              permissionsState.side === "left"
                ? sftp.leftPane.connection?.currentPath || ""
                : sftp.rightPane.connection?.currentPath || "",
              file.name,
            );
            sftp.changePermissions(
              permissionsState.side,
              fullPath,
              permissions,
            );
          }
          setPermissionsState(null);
        }}
      />

      {/* Text Editor Modal */}
      <TextEditorModal
        open={showTextEditor}
        onClose={() => {
          setShowTextEditor(false);
          setTextEditorTarget(null);
          setTextEditorContent("");
        }}
        fileName={textEditorTarget?.file.name || ""}
        initialContent={textEditorContent}
        onSave={handleSaveTextFile}
      />

      {/* File Opener Dialog */}
      <FileOpenerDialog
        open={showFileOpenerDialog}
        onClose={() => {
          setShowFileOpenerDialog(false);
          setFileOpenerTarget(null);
        }}
        fileName={fileOpenerTarget?.file.name || ""}
        onSelect={handleFileOpenerSelect}
        onSelectSystemApp={handleSelectSystemApp}
      />
    </>
  );
};
