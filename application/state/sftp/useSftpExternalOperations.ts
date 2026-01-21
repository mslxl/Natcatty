import React, { useCallback, useRef, useState } from "react";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { logger } from "../../../lib/logger";
import { extractDropEntries } from "../../../lib/sftpFileUtils";
import { SftpPane, FolderUploadProgress } from "./types";
import { joinPath } from "./utils";

interface UseSftpExternalOperationsParams {
  getActivePane: (side: "left" | "right") => SftpPane | null;
  refresh: (side: "left" | "right") => Promise<void>;
  sftpSessionsRef: React.MutableRefObject<Map<string, string>>;
}

interface SftpExternalOperationsResult {
  folderUploadProgress: FolderUploadProgress;
  readTextFile: (side: "left" | "right", filePath: string) => Promise<string>;
  readBinaryFile: (side: "left" | "right", filePath: string) => Promise<ArrayBuffer>;
  writeTextFile: (side: "left" | "right", filePath: string, content: string) => Promise<void>;
  downloadToTempAndOpen: (
    side: "left" | "right",
    remotePath: string,
    fileName: string,
    appPath: string,
    options?: { enableWatch?: boolean }
  ) => Promise<{ localTempPath: string; watchId?: string }>;
  uploadExternalFiles: (
    side: "left" | "right",
    dataTransfer: DataTransfer
  ) => Promise<{ fileName: string; success: boolean; error?: string }[]>;
  cancelFolderUpload: () => Promise<void>;
  selectApplication: () => Promise<{ path: string; name: string } | null>;
}

export const useSftpExternalOperations = (
  params: UseSftpExternalOperationsParams
): SftpExternalOperationsResult => {
  const { getActivePane, refresh, sftpSessionsRef } = params;

  const [folderUploadProgress, setFolderUploadProgress] = useState<FolderUploadProgress>({
    isUploading: false,
    currentFile: "",
    currentIndex: 0,
    totalFiles: 0,
    cancelled: false,
    currentFileBytes: 0,
    currentFileTotalBytes: 0,
    currentFileSpeed: 0,
    currentTransferId: "",
  });
  const cancelFolderUploadRef = useRef(false);
  const currentFolderUploadTransferIdRef = useRef<string>("");

  const readTextFile = useCallback(
    async (side: "left" | "right", filePath: string): Promise<string> => {
      const pane = getActivePane(side);
      if (!pane?.connection) {
        throw new Error("No connection available");
      }

      if (pane.connection.isLocal) {
        const bridge = netcattyBridge.get();
        if (bridge?.readLocalFile) {
          const buffer = await bridge.readLocalFile(filePath);
          return new TextDecoder().decode(buffer);
        }
        throw new Error("Local file reading not supported");
      }

      const sftpId = sftpSessionsRef.current.get(pane.connection.id);
      if (!sftpId) {
        throw new Error("SFTP session not found");
      }

      const bridge = netcattyBridge.get();
      if (!bridge) {
        throw new Error("Bridge not available");
      }

      return await bridge.readSftp(sftpId, filePath, pane.filenameEncoding);
    },
    [getActivePane, sftpSessionsRef],
  );

  const readBinaryFile = useCallback(
    async (side: "left" | "right", filePath: string): Promise<ArrayBuffer> => {
      const pane = getActivePane(side);
      if (!pane?.connection) {
        throw new Error("No connection available");
      }

      if (pane.connection.isLocal) {
        const bridge = netcattyBridge.get();
        if (bridge?.readLocalFile) {
          return await bridge.readLocalFile(filePath);
        }
        throw new Error("Local file reading not supported");
      }

      const sftpId = sftpSessionsRef.current.get(pane.connection.id);
      if (!sftpId) {
        throw new Error("SFTP session not found");
      }

      const bridge = netcattyBridge.get();
      if (!bridge?.readSftpBinary) {
        throw new Error("Binary file reading not supported");
      }

      return await bridge.readSftpBinary(sftpId, filePath, pane.filenameEncoding);
    },
    [getActivePane, sftpSessionsRef],
  );

  const writeTextFile = useCallback(
    async (side: "left" | "right", filePath: string, content: string): Promise<void> => {
      const pane = getActivePane(side);
      if (!pane?.connection) {
        throw new Error("No connection available");
      }

      if (pane.connection.isLocal) {
        const bridge = netcattyBridge.get();
        if (bridge?.writeLocalFile) {
          const data = new TextEncoder().encode(content);
          await bridge.writeLocalFile(filePath, data.buffer);
          return;
        }
        throw new Error("Local file writing not supported");
      }

      const sftpId = sftpSessionsRef.current.get(pane.connection.id);
      if (!sftpId) {
        throw new Error("SFTP session not found");
      }

      const bridge = netcattyBridge.get();
      if (!bridge) {
        throw new Error("Bridge not available");
      }

      await bridge.writeSftp(sftpId, filePath, content, pane.filenameEncoding);
    },
    [getActivePane, sftpSessionsRef],
  );

  const downloadToTempAndOpen = useCallback(
    async (
      side: "left" | "right",
      remotePath: string,
      fileName: string,
      appPath: string,
      options?: { enableWatch?: boolean }
    ): Promise<{ localTempPath: string; watchId?: string }> => {
      const pane = getActivePane(side);
      if (!pane?.connection) {
        throw new Error("No connection available");
      }

      const bridge = netcattyBridge.get();
      if (!bridge?.downloadSftpToTemp || !bridge?.openWithApplication) {
        throw new Error("System app opening not supported");
      }

      if (pane.connection.isLocal) {
        await bridge.openWithApplication(remotePath, appPath);
        return { localTempPath: remotePath };
      }

      const sftpId = sftpSessionsRef.current.get(pane.connection.id);
      if (!sftpId) {
        throw new Error("SFTP session not found");
      }

      console.log("[SFTP] Downloading file to temp", { sftpId, remotePath, fileName });
      const localTempPath = await bridge.downloadSftpToTemp(
        sftpId,
        remotePath,
        fileName,
        pane.filenameEncoding,
      );
      console.log("[SFTP] File downloaded to temp", { localTempPath });

      if (bridge.registerTempFile) {
        try {
          await bridge.registerTempFile(sftpId, localTempPath);
        } catch (err) {
          console.warn("[SFTP] Failed to register temp file for cleanup:", err);
        }
      }

      console.log("[SFTP] Opening with application", { localTempPath, appPath });
      await bridge.openWithApplication(localTempPath, appPath);
      console.log("[SFTP] Application launched");

      let watchId: string | undefined;
      console.log("[SFTP] Auto-sync enabled check", { enableWatch: options?.enableWatch, hasStartFileWatch: !!bridge.startFileWatch });
      if (options?.enableWatch && bridge.startFileWatch) {
        try {
          console.log("[SFTP] Starting file watch", { localTempPath, remotePath, sftpId });
          const result = await bridge.startFileWatch(
            localTempPath,
            remotePath,
            sftpId,
            pane.filenameEncoding,
          );
          watchId = result.watchId;
          console.log("[SFTP] File watch started successfully", { watchId, localTempPath, remotePath });
        } catch (err) {
          console.warn("[SFTP] Failed to start file watch:", err);
        }
      } else {
        console.log("[SFTP] File watching not enabled or not available");
      }

      return { localTempPath, watchId };
    },
    [getActivePane, sftpSessionsRef],
  );

  const uploadExternalFiles = useCallback(
    async (side: "left" | "right", dataTransfer: DataTransfer) => {
      const pane = getActivePane(side);
      if (!pane?.connection) {
        throw new Error("No active connection");
      }

      const bridge = netcattyBridge.get();
      if (!bridge) {
        throw new Error("Bridge not available");
      }

      const entries = await extractDropEntries(dataTransfer);

      const results: { fileName: string; success: boolean; error?: string }[] = [];
      const createdDirs = new Set<string>();

      const ensureDirectory = async (dirPath: string, sftpId: string | null) => {
        if (createdDirs.has(dirPath)) return;

        try {
          if (pane.connection?.isLocal) {
            if (bridge.mkdirLocal) {
              await bridge.mkdirLocal(dirPath);
            }
          } else if (sftpId) {
            await bridge.mkdirSftp(sftpId, dirPath, pane.filenameEncoding);
          }
          createdDirs.add(dirPath);
        } catch {
          createdDirs.add(dirPath);
        }
      };

      const sftpId = pane.connection.isLocal
        ? null
        : sftpSessionsRef.current.get(pane.connection.id) || null;

      if (!pane.connection.isLocal && !sftpId) {
        throw new Error("SFTP session not found");
      }

      const sortedEntries = [...entries].sort((a, b) => {
        if (a.isDirectory && !b.isDirectory) return -1;
        if (!a.isDirectory && b.isDirectory) return 1;
        const aDepth = a.relativePath.split('/').length;
        const bDepth = b.relativePath.split('/').length;
        return aDepth - bDepth;
      });

      const fileEntries = sortedEntries.filter(e => !e.isDirectory && e.file);
      const totalFiles = fileEntries.length;

      cancelFolderUploadRef.current = false;
      currentFolderUploadTransferIdRef.current = "";
      if (totalFiles > 1) {
        setFolderUploadProgress({
          isUploading: true,
          currentFile: "",
          currentIndex: 0,
          totalFiles,
          cancelled: false,
          currentFileBytes: 0,
          currentFileTotalBytes: 0,
          currentFileSpeed: 0,
          currentTransferId: "",
        });
      }

      let fileIndex = 0;
      const yieldToMain = () => new Promise<void>(resolve => setTimeout(resolve, 0));

      try {
        for (const entry of sortedEntries) {
          await yieldToMain();
          if (cancelFolderUploadRef.current) {
            logger.info("[SFTP] Folder upload cancelled by user");
            setFolderUploadProgress(prev => ({ ...prev, cancelled: true, isUploading: false }));
            break;
          }

          const targetPath = joinPath(pane.connection.currentPath, entry.relativePath);

          try {
            if (entry.isDirectory) {
              await ensureDirectory(targetPath, sftpId);
            } else if (entry.file) {
              fileIndex++;
              const transferId = crypto.randomUUID();
              const fileTotalBytes = entry.file.size;

              currentFolderUploadTransferIdRef.current = transferId;

              if (totalFiles > 1) {
                setFolderUploadProgress({
                  isUploading: true,
                  currentFile: entry.relativePath,
                  currentIndex: fileIndex,
                  totalFiles,
                  cancelled: false,
                  currentFileBytes: 0,
                  currentFileTotalBytes: fileTotalBytes,
                  currentFileSpeed: 0,
                  currentTransferId: transferId,
                });
              }

              const pathParts = entry.relativePath.split('/');
              if (pathParts.length > 1) {
                let parentPath = pane.connection.currentPath;
                for (let i = 0; i < pathParts.length - 1; i++) {
                  parentPath = joinPath(parentPath, pathParts[i]);
                  await ensureDirectory(parentPath, sftpId);
                }
              }

              const arrayBuffer = await entry.file.arrayBuffer();

              if (pane.connection.isLocal) {
                if (!bridge.writeLocalFile) {
                  throw new Error("writeLocalFile not available");
                }
                await bridge.writeLocalFile(targetPath, arrayBuffer);
              } else if (sftpId) {
                if (bridge.writeSftpBinaryWithProgress) {
                  let pendingProgressUpdate: { transferred: number; total: number; speed: number } | null = null;
                  let rafScheduled = false;

                  const onProgress = (transferred: number, total: number, speed: number) => {
                    if (totalFiles > 1 && !cancelFolderUploadRef.current) {
                      pendingProgressUpdate = { transferred, total, speed };

                      if (!rafScheduled) {
                        rafScheduled = true;
                        requestAnimationFrame(() => {
                          rafScheduled = false;
                          const update = pendingProgressUpdate;
                          pendingProgressUpdate = null;
                          if (update && !cancelFolderUploadRef.current) {
                            setFolderUploadProgress(prev => ({
                              ...prev,
                              currentFileBytes: update.transferred,
                              currentFileTotalBytes: update.total,
                              currentFileSpeed: update.speed,
                            }));
                          }
                        });
                      }
                    }
                  };

                  const result = await bridge.writeSftpBinaryWithProgress(
                    sftpId,
                    targetPath,
                    arrayBuffer,
                    transferId,
                    pane.filenameEncoding,
                    onProgress,
                    undefined,
                    undefined,
                  );

                  if (result?.cancelled) {
                    logger.info("[SFTP] File upload cancelled:", entry.relativePath);
                    break;
                  }

                  if (!result || result.success === false) {
                    if (bridge.writeSftpBinary) {
                      await bridge.writeSftpBinary(
                        sftpId,
                        targetPath,
                        arrayBuffer,
                        pane.filenameEncoding,
                      );
                    } else {
                      throw new Error("Upload failed and no fallback method available");
                    }
                  }
                } else if (bridge.writeSftpBinary) {
                  await bridge.writeSftpBinary(
                    sftpId,
                    targetPath,
                    arrayBuffer,
                    pane.filenameEncoding,
                  );
                } else {
                  throw new Error("No SFTP write method available");
                }
              }

              currentFolderUploadTransferIdRef.current = "";
              results.push({ fileName: entry.relativePath, success: true });
            }
          } catch (error) {
            currentFolderUploadTransferIdRef.current = "";
            if (!entry.isDirectory) {
              logger.error(`Failed to upload ${entry.relativePath}:`, error);
              results.push({
                fileName: entry.relativePath,
                success: false,
                error: error instanceof Error ? error.message : String(error),
              });
            }
          }
        }
      } finally {
        currentFolderUploadTransferIdRef.current = "";
        setFolderUploadProgress({
          isUploading: false,
          currentFile: "",
          currentIndex: 0,
          totalFiles: 0,
          cancelled: cancelFolderUploadRef.current,
          currentFileBytes: 0,
          currentFileTotalBytes: 0,
          currentFileSpeed: 0,
          currentTransferId: "",
        });
      }

      await refresh(side);

      return results;
    },
    [getActivePane, refresh, sftpSessionsRef],
  );

  const cancelFolderUpload = useCallback(async () => {
    cancelFolderUploadRef.current = true;

    const currentTransferId = currentFolderUploadTransferIdRef.current;
    if (currentTransferId) {
      const bridge = netcattyBridge.get();
      if (bridge?.cancelSftpUpload) {
        try {
          await bridge.cancelSftpUpload(currentTransferId);
          logger.info("[SFTP] Current file upload cancelled:", currentTransferId);
        } catch (err) {
          logger.warn("[SFTP] Failed to cancel current file upload:", err);
        }
      }
    }

    setFolderUploadProgress(prev => ({
      ...prev,
      cancelled: true,
      isUploading: false,
    }));
  }, []);

  const selectApplication = useCallback(
    async (): Promise<{ path: string; name: string } | null> => {
      const bridge = netcattyBridge.get();
      if (!bridge?.selectApplication) {
        return null;
      }
      return await bridge.selectApplication();
    },
    [],
  );

  return {
    folderUploadProgress,
    readTextFile,
    readBinaryFile,
    writeTextFile,
    downloadToTempAndOpen,
    uploadExternalFiles,
    cancelFolderUpload,
    selectApplication,
  };
};
