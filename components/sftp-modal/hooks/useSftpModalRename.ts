import { useCallback, useState } from "react";
import type { RemoteFile } from "../../types";
import { toast } from "../../ui/toast";

interface UseSftpModalRenameParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  loadFiles: (path: string, options?: { force?: boolean }) => Promise<void>;
  renameSftp: (sftpId: string, oldPath: string, newPath: string) => Promise<void>;
  t: (key: string, params?: Record<string, unknown>) => string;
}

interface UseSftpModalRenameResult {
  showRenameDialog: boolean;
  setShowRenameDialog: (open: boolean) => void;
  renameTarget: RemoteFile | null;
  renameName: string;
  setRenameName: (value: string) => void;
  isRenaming: boolean;
  openRenameDialog: (file: RemoteFile) => void;
  handleRename: () => Promise<void>;
}

export const useSftpModalRename = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  loadFiles,
  renameSftp,
  t,
}: UseSftpModalRenameParams): UseSftpModalRenameResult => {
  const [showRenameDialog, setShowRenameDialog] = useState(false);
  const [renameTarget, setRenameTarget] = useState<RemoteFile | null>(null);
  const [renameName, setRenameName] = useState("");
  const [isRenaming, setIsRenaming] = useState(false);

  const openRenameDialog = useCallback((file: RemoteFile) => {
    setRenameTarget(file);
    setRenameName(file.name);
    setShowRenameDialog(true);
  }, []);

  const handleRename = useCallback(async () => {
    if (!renameTarget || !renameName.trim() || isRenaming) return;
    if (renameName.trim() === renameTarget.name) {
      setShowRenameDialog(false);
      return;
    }
    setIsRenaming(true);
    try {
      const oldPath = joinPath(currentPath, renameTarget.name);
      const newPath = joinPath(currentPath, renameName.trim());
      if (isLocalSession) {
        toast.error("Local rename not implemented", "SFTP");
      } else {
        await renameSftp(await ensureSftp(), oldPath, newPath);
      }
      setShowRenameDialog(false);
      setRenameTarget(null);
      setRenameName("");
      await loadFiles(currentPath, { force: true });
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t("sftp.error.renameFailed"),
        "SFTP",
      );
    } finally {
      setIsRenaming(false);
    }
  }, [currentPath, ensureSftp, isLocalSession, joinPath, loadFiles, renameName, renameSftp, renameTarget, t, isRenaming]);

  return {
    showRenameDialog,
    setShowRenameDialog,
    renameTarget,
    renameName,
    setRenameName,
    isRenaming,
    openRenameDialog,
    handleRename,
  };
};
