import { useCallback, useState } from "react";
import type { RemoteFile } from "../../../types";
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
  handleCreateFolder: () => void;
  handleCreateFile: () => void;
  // Create dialog state
  showCreateDialog: boolean;
  setShowCreateDialog: (open: boolean) => void;
  createType: "file" | "folder";
  createName: string;
  setCreateName: (value: string) => void;
  isCreating: boolean;
  handleCreateSubmit: () => Promise<void>;
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
  const [showCreateDialog, setShowCreateDialog] = useState(false);
  const [createType, setCreateType] = useState<"file" | "folder">("folder");
  const [createName, setCreateName] = useState("");
  const [isCreating, setIsCreating] = useState(false);

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

  const handleCreateFolder = useCallback(() => {
    setCreateType("folder");
    setCreateName("");
    setShowCreateDialog(true);
  }, []);

  const handleCreateFile = useCallback(() => {
    setCreateType("file");
    setCreateName("");
    setShowCreateDialog(true);
  }, []);

  const handleCreateSubmit = useCallback(async () => {
    const name = createName.trim();
    if (!name || isCreating) return;
    setIsCreating(true);
    try {
      const fullPath = joinPath(currentPath, name);
      if (createType === "folder") {
        if (isLocalSession) {
          await mkdirLocal(fullPath);
        } else {
          await mkdirSftp(await ensureSftp(), fullPath);
        }
      } else {
        if (isLocalSession) {
          await writeLocalFile(fullPath, new ArrayBuffer(0));
        } else {
          try {
            await writeSftpBinary(await ensureSftp(), fullPath, new ArrayBuffer(0));
          } catch {
            await writeSftp(await ensureSftp(), fullPath, "");
          }
        }
      }
      setShowCreateDialog(false);
      setCreateName("");
      await loadFiles(currentPath, { force: true });
    } catch (e) {
      toast.error(
        e instanceof Error
          ? e.message
          : t(createType === "folder" ? "sftp.error.createFolderFailed" : "sftp.error.createFileFailed"),
        "SFTP",
      );
    } finally {
      setIsCreating(false);
    }
  }, [createName, createType, currentPath, ensureSftp, isCreating, isLocalSession, joinPath, loadFiles, mkdirLocal, mkdirSftp, t, writeLocalFile, writeSftp, writeSftpBinary]);

  return {
    handleDelete,
    handleCreateFolder,
    handleCreateFile,
    showCreateDialog,
    setShowCreateDialog,
    createType,
    createName,
    setCreateName,
    isCreating,
    handleCreateSubmit,
  };
};
