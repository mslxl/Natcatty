import type { RemoteFile } from "../../types";
import { useSftpModalCreateDelete } from "./useSftpModalCreateDelete";
import { useSftpModalRename } from "./useSftpModalRename";
import { useSftpModalPermissions } from "./useSftpModalPermissions";
import { useSftpModalTextEditor } from "./useSftpModalTextEditor";
import { useSftpModalFileOpener } from "./useSftpModalFileOpener";
import type { FileOpenerType, SystemAppInfo } from "../../lib/sftpFileUtils";

interface UseSftpModalFileActionsParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  loadFiles: (path: string, options?: { force?: boolean }) => Promise<void>;
  readLocalFile: (path: string) => Promise<ArrayBuffer>;
  readSftp: (sftpId: string, path: string) => Promise<string>;
  writeLocalFile: (path: string, data: ArrayBuffer) => Promise<void>;
  writeSftp: (sftpId: string, path: string, data: string) => Promise<void>;
  writeSftpBinary: (sftpId: string, path: string, data: ArrayBuffer) => Promise<void>;
  deleteLocalFile: (path: string) => Promise<void>;
  deleteSftp: (sftpId: string, path: string) => Promise<void>;
  mkdirLocal: (path: string) => Promise<void>;
  mkdirSftp: (sftpId: string, path: string) => Promise<void>;
  renameSftp: (sftpId: string, oldPath: string, newPath: string) => Promise<void>;
  chmodSftp: (sftpId: string, path: string, permissions: string) => Promise<void>;
  statSftp: (sftpId: string, path: string) => Promise<{ permissions?: string }>;
  t: (key: string, params?: Record<string, unknown>) => string;
  sftpAutoSync: boolean;
  getOpenerForFile: (name: string) => { openerType: FileOpenerType; systemApp?: SystemAppInfo } | null;
  setOpenerForExtension: (ext: string, openerType: FileOpenerType, systemApp?: SystemAppInfo) => void;
  downloadSftpToTempAndOpen: (sftpId: string, path: string, fileName: string, appPath: string, opts: { enableWatch: boolean }) => Promise<void>;
  selectApplication: () => Promise<{ path: string; name: string } | null>;
}

interface UseSftpModalFileActionsResult {
  handleDelete: (file: RemoteFile) => Promise<void>;
  handleCreateFolder: () => Promise<void>;
  handleCreateFile: () => Promise<void>;
  showRenameDialog: boolean;
  setShowRenameDialog: (open: boolean) => void;
  renameTarget: RemoteFile | null;
  renameName: string;
  setRenameName: (value: string) => void;
  isRenaming: boolean;
  openRenameDialog: (file: RemoteFile) => void;
  handleRename: () => Promise<void>;
  showPermissionsDialog: boolean;
  setShowPermissionsDialog: (open: boolean) => void;
  permissionsTarget: RemoteFile | null;
  permissions: {
    owner: { read: boolean; write: boolean; execute: boolean };
    group: { read: boolean; write: boolean; execute: boolean };
    others: { read: boolean; write: boolean; execute: boolean };
  };
  isChangingPermissions: boolean;
  openPermissionsDialog: (file: RemoteFile) => Promise<void>;
  togglePermission: (role: "owner" | "group" | "others", perm: "read" | "write" | "execute") => void;
  getOctalPermissions: () => string;
  getSymbolicPermissions: () => string;
  handleSavePermissions: () => Promise<void>;
  showFileOpenerDialog: boolean;
  setShowFileOpenerDialog: (open: boolean) => void;
  fileOpenerTarget: RemoteFile | null;
  openFileOpenerDialog: (file: RemoteFile) => void;
  handleFileOpenerSelect: (
    openerType: FileOpenerType,
    setAsDefault: boolean,
    systemApp?: SystemAppInfo,
  ) => Promise<void>;
  handleSelectSystemApp: () => Promise<SystemAppInfo | null>;
  showTextEditor: boolean;
  setShowTextEditor: (open: boolean) => void;
  textEditorTarget: RemoteFile | null;
  textEditorContent: string;
  setTextEditorContent: (value: string) => void;
  loadingTextContent: boolean;
  handleEditFile: (file: RemoteFile) => Promise<void>;
  handleSaveTextFile: (content: string) => Promise<void>;
  handleOpenFile: (file: RemoteFile) => Promise<void>;
}

export const useSftpModalFileActions = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  loadFiles,
  readLocalFile,
  readSftp,
  writeLocalFile,
  writeSftp,
  writeSftpBinary,
  deleteLocalFile,
  deleteSftp,
  mkdirLocal,
  mkdirSftp,
  renameSftp,
  chmodSftp,
  statSftp,
  t,
  sftpAutoSync,
  getOpenerForFile,
  setOpenerForExtension,
  downloadSftpToTempAndOpen,
  selectApplication,
}: UseSftpModalFileActionsParams): UseSftpModalFileActionsResult => {
  const { handleDelete, handleCreateFolder, handleCreateFile } =
    useSftpModalCreateDelete({
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
    });

  const {
    showRenameDialog,
    setShowRenameDialog,
    renameTarget,
    renameName,
    setRenameName,
    isRenaming,
    openRenameDialog,
    handleRename,
  } = useSftpModalRename({
    currentPath,
    isLocalSession,
    joinPath,
    ensureSftp,
    loadFiles,
    renameSftp,
    t,
  });

  const {
    showPermissionsDialog,
    setShowPermissionsDialog,
    permissionsTarget,
    permissions,
    isChangingPermissions,
    openPermissionsDialog,
    togglePermission,
    getOctalPermissions,
    getSymbolicPermissions,
    handleSavePermissions,
  } = useSftpModalPermissions({
    currentPath,
    isLocalSession,
    joinPath,
    ensureSftp,
    loadFiles,
    chmodSftp,
    statSftp,
    t,
  });

  const {
    showTextEditor,
    setShowTextEditor,
    textEditorTarget,
    textEditorContent,
    setTextEditorContent,
    loadingTextContent,
    handleEditFile,
    handleSaveTextFile,
  } = useSftpModalTextEditor({
    currentPath,
    isLocalSession,
    joinPath,
    ensureSftp,
    readLocalFile,
    readSftp,
    writeLocalFile,
    writeSftp,
    t,
  });

  const {
    showFileOpenerDialog,
    setShowFileOpenerDialog,
    fileOpenerTarget,
    openFileOpenerDialog,
    handleOpenFile,
    handleFileOpenerSelect,
    handleSelectSystemApp,
  } = useSftpModalFileOpener({
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
  });

  return {
    handleDelete,
    handleCreateFolder,
    handleCreateFile,
    showRenameDialog,
    setShowRenameDialog,
    renameTarget,
    renameName,
    setRenameName,
    isRenaming,
    openRenameDialog,
    handleRename,
    showPermissionsDialog,
    setShowPermissionsDialog,
    permissionsTarget,
    permissions,
    isChangingPermissions,
    openPermissionsDialog,
    togglePermission,
    getOctalPermissions,
    getSymbolicPermissions,
    handleSavePermissions,
    showFileOpenerDialog,
    setShowFileOpenerDialog,
    fileOpenerTarget,
    openFileOpenerDialog,
    handleFileOpenerSelect,
    handleSelectSystemApp,
    showTextEditor,
    setShowTextEditor,
    textEditorTarget,
    textEditorContent,
    setTextEditorContent,
    loadingTextContent,
    handleEditFile,
    handleSaveTextFile,
    handleOpenFile,
  };
};
