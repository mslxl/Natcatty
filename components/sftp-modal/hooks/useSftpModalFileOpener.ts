import { useCallback, useState } from "react";
import type { RemoteFile } from "../../types";
import { toast } from "../../ui/toast";
import { getFileExtension, FileOpenerType, SystemAppInfo } from "../../lib/sftpFileUtils";

interface UseSftpModalFileOpenerParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  sftpAutoSync: boolean;
  getOpenerForFile: (name: string) => { openerType: FileOpenerType; systemApp?: SystemAppInfo } | null;
  setOpenerForExtension: (ext: string, openerType: FileOpenerType, systemApp?: SystemAppInfo) => void;
  downloadSftpToTempAndOpen: (sftpId: string, path: string, fileName: string, appPath: string, opts: { enableWatch: boolean }) => Promise<void>;
  selectApplication: () => Promise<{ path: string; name: string } | null>;
  t: (key: string, params?: Record<string, unknown>) => string;
  handleEditFile: (file: RemoteFile) => Promise<void>;
}

interface UseSftpModalFileOpenerResult {
  showFileOpenerDialog: boolean;
  setShowFileOpenerDialog: (open: boolean) => void;
  fileOpenerTarget: RemoteFile | null;
  openFileOpenerDialog: (file: RemoteFile) => void;
  handleOpenFile: (file: RemoteFile) => Promise<void>;
  handleFileOpenerSelect: (
    openerType: FileOpenerType,
    setAsDefault: boolean,
    systemApp?: SystemAppInfo,
  ) => Promise<void>;
  handleSelectSystemApp: () => Promise<SystemAppInfo | null>;
}

export const useSftpModalFileOpener = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  sftpAutoSync,
  getOpenerForFile,
  setOpenerForExtension,
  downloadSftpToTempAndOpen,
  selectApplication,
  t,
  handleEditFile,
}: UseSftpModalFileOpenerParams): UseSftpModalFileOpenerResult => {
  const [showFileOpenerDialog, setShowFileOpenerDialog] = useState(false);
  const [fileOpenerTarget, setFileOpenerTarget] = useState<RemoteFile | null>(null);

  const openFileOpenerDialog = useCallback((file: RemoteFile) => {
    setFileOpenerTarget(file);
    setShowFileOpenerDialog(true);
  }, []);

  const handleOpenFile = useCallback(async (file: RemoteFile) => {
    const savedOpener = getOpenerForFile(file.name);

    if (savedOpener) {
      if (savedOpener.openerType === "builtin-editor") {
        await handleEditFile(file);
      } else if (savedOpener.openerType === "system-app" && savedOpener.systemApp) {
        try {
          const fullPath = joinPath(currentPath, file.name);
          if (isLocalSession) {
            const bridge = (window as unknown as { netcatty?: NetcattyBridge }).netcatty;
            if (bridge?.openWithApplication) {
              await bridge.openWithApplication(fullPath, savedOpener.systemApp.path);
            }
          } else {
            const sftpId = await ensureSftp();
            await downloadSftpToTempAndOpen(
              sftpId,
              fullPath,
              file.name,
              savedOpener.systemApp.path,
              { enableWatch: sftpAutoSync },
            );
          }
        } catch (e) {
          toast.error(
            e instanceof Error ? e.message : t("sftp.error.openFailed"),
            "SFTP",
          );
        }
      }
    } else {
      openFileOpenerDialog(file);
    }
  }, [currentPath, downloadSftpToTempAndOpen, ensureSftp, getOpenerForFile, handleEditFile, isLocalSession, joinPath, openFileOpenerDialog, sftpAutoSync, t]);

  const handleFileOpenerSelect = useCallback(
    async (openerType: FileOpenerType, setAsDefault: boolean, systemApp?: SystemAppInfo) => {
      if (!fileOpenerTarget) return;

      if (setAsDefault) {
        const ext = getFileExtension(fileOpenerTarget.name);
        setOpenerForExtension(ext, openerType, systemApp);
      }

      setShowFileOpenerDialog(false);

      if (openerType === "builtin-editor") {
        await handleEditFile(fileOpenerTarget);
      } else if (openerType === "system-app" && systemApp) {
        try {
          const fullPath = joinPath(currentPath, fileOpenerTarget.name);
          if (isLocalSession) {
            const bridge = (window as unknown as { netcatty?: NetcattyBridge }).netcatty;
            if (bridge?.openWithApplication) {
              await bridge.openWithApplication(fullPath, systemApp.path);
            }
          } else {
            const sftpId = await ensureSftp();
            await downloadSftpToTempAndOpen(
              sftpId,
              fullPath,
              fileOpenerTarget.name,
              systemApp.path,
              { enableWatch: sftpAutoSync },
            );
          }
        } catch (e) {
          toast.error(
            e instanceof Error ? e.message : t("sftp.error.openFailed"),
            "SFTP",
          );
        }
      }

      setFileOpenerTarget(null);
    },
    [currentPath, downloadSftpToTempAndOpen, ensureSftp, fileOpenerTarget, handleEditFile, isLocalSession, joinPath, sftpAutoSync, setOpenerForExtension, t],
  );

  const handleSelectSystemApp = useCallback(async (): Promise<SystemAppInfo | null> => {
    const result = await selectApplication();
    if (result) {
      return { path: result.path, name: result.name };
    }
    return null;
  }, [selectApplication]);

  return {
    showFileOpenerDialog,
    setShowFileOpenerDialog,
    fileOpenerTarget,
    openFileOpenerDialog,
    handleOpenFile,
    handleFileOpenerSelect,
    handleSelectSystemApp,
  };
};
