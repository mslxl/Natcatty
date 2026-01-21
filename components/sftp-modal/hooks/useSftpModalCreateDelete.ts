import { useCallback } from "react";
import type { RemoteFile } from "../../types";
import { toast } from "../../ui/toast";

interface UseSftpModalCreateDeleteParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  loadFiles: (path: string, options?: { force?: boolean }) => Promise<void>;
  deleteLocalFile: (path: string) => Promise<void>;
  deleteSftp: (sftpId: string, path: string) => Promise<void>;
  mkdirLocal: (path: string) => Promise<void>;
  mkdirSftp: (sftpId: string, path: string) => Promise<void>;
  writeLocalFile: (path: string, data: ArrayBuffer) => Promise<void>;
  writeSftpBinary: (sftpId: string, path: string, data: ArrayBuffer) => Promise<void>;
  writeSftp: (sftpId: string, path: string, data: string) => Promise<void>;
  t: (key: string, params?: Record<string, unknown>) => string;
}

interface UseSftpModalCreateDeleteResult {
  handleDelete: (file: RemoteFile) => Promise<void>;
  handleCreateFolder: () => Promise<void>;
  handleCreateFile: () => Promise<void>;
}

export const useSftpModalCreateDelete = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  loadFiles,
  deleteLocalFile,
  deleteSftp,
  mkdirLocal,
  mkdirSftp,
  writeLocalFile,
  writeSftpBinary,
  writeSftp,
  t,
}: UseSftpModalCreateDeleteParams): UseSftpModalCreateDeleteResult => {
  const handleDelete = useCallback(
    async (file: RemoteFile) => {
      if (file.name === "..") return;
      if (!confirm(t("sftp.deleteConfirm.single", { name: file.name }))) return;

      try {
        const fullPath = joinPath(currentPath, file.name);
        if (isLocalSession) {
          await deleteLocalFile(fullPath);
        } else {
          await deleteSftp(await ensureSftp(), fullPath);
        }
        await loadFiles(currentPath, { force: true });
      } catch (e) {
        toast.error(
          e instanceof Error ? e.message : t("sftp.error.deleteFailed"),
          "SFTP",
        );
      }
    },
    [currentPath, deleteLocalFile, deleteSftp, ensureSftp, isLocalSession, joinPath, loadFiles, t],
  );

  const handleCreateFolder = useCallback(async () => {
    const folderName = prompt(t("sftp.prompt.newFolderName"));
    if (!folderName) return;
    try {
      const fullPath = joinPath(currentPath, folderName);
      if (isLocalSession) {
        await mkdirLocal(fullPath);
      } else {
        await mkdirSftp(await ensureSftp(), fullPath);
      }
      await loadFiles(currentPath, { force: true });
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t("sftp.error.createFolderFailed"),
        "SFTP",
      );
    }
  }, [currentPath, ensureSftp, isLocalSession, joinPath, loadFiles, mkdirLocal, mkdirSftp, t]);

  const handleCreateFile = useCallback(async () => {
    const fileName = prompt(t("sftp.fileName.placeholder"));
    if (!fileName) return;
    try {
      const fullPath = joinPath(currentPath, fileName);
      if (isLocalSession) {
        await writeLocalFile(fullPath, new ArrayBuffer(0));
      } else {
        try {
          await writeSftpBinary(await ensureSftp(), fullPath, new ArrayBuffer(0));
        } catch {
          await writeSftp(await ensureSftp(), fullPath, "");
        }
      }
      await loadFiles(currentPath, { force: true });
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t("sftp.error.createFileFailed"),
        "SFTP",
      );
    }
  }, [currentPath, ensureSftp, isLocalSession, joinPath, loadFiles, t, writeLocalFile, writeSftp, writeSftpBinary]);

  return { handleDelete, handleCreateFolder, handleCreateFile };
};
