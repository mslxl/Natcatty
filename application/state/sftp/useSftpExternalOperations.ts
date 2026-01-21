import React, { useCallback, useRef } from "react";
import { TransferTask, TransferStatus } from "../../../domain/models";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { logger } from "../../../lib/logger";
import { extractDropEntries, DropEntry } from "../../../lib/sftpFileUtils";
import { SftpPane } from "./types";
import { joinPath } from "./utils";

// Helper to detect root folders from drop entries
function detectRootFolders(entries: DropEntry[]): Map<string, DropEntry[]> {
  const rootFolders = new Map<string, DropEntry[]>();

  for (const entry of entries) {
    const parts = entry.relativePath.split('/');
    // Get the root folder name (first part of the path)
    const rootName = parts[0];

    // Only group if there's more than one part (meaning it's from a folder)
    // or if the entry itself is a directory
    if (parts.length > 1 || entry.isDirectory) {
      if (!rootFolders.has(rootName)) {
        rootFolders.set(rootName, []);
      }
      rootFolders.get(rootName)!.push(entry);
    } else {
      // Standalone file - use its name as key with special prefix
      const key = `__file__${entry.relativePath}`;
      rootFolders.set(key, [entry]);
    }
  }

  return rootFolders;
}

interface UseSftpExternalOperationsParams {
  getActivePane: (side: "left" | "right") => SftpPane | null;
  refresh: (side: "left" | "right") => Promise<void>;
  sftpSessionsRef: React.MutableRefObject<Map<string, string>>;
  addExternalUpload?: (task: TransferTask) => void;
  updateExternalUpload?: (taskId: string, updates: Partial<TransferTask>) => void;
  dismissExternalUpload?: (taskId: string) => void;
}

export interface UploadResult {
  fileName: string;
  success: boolean;
  error?: string;
  cancelled?: boolean;
}

interface SftpExternalOperationsResult {
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
  ) => Promise<UploadResult[]>;
  cancelExternalUpload: () => Promise<void>;
  selectApplication: () => Promise<{ path: string; name: string } | null>;
}

export const useSftpExternalOperations = (
  params: UseSftpExternalOperationsParams
): SftpExternalOperationsResult => {
  const { getActivePane, refresh, sftpSessionsRef, addExternalUpload, updateExternalUpload, dismissExternalUpload } = params;

  // Track cancel state and current transfer for cancellation
  const cancelUploadRef = useRef(false);
  const currentTransferIdRef = useRef<string>("");
  // Track all active file upload IDs for proper cancellation (each file has unique backend ID)
  const activeFileTransferIdsRef = useRef<Set<string>>(new Set());

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

      return await bridge.readSftp(sftpId, filePath);
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

      return await bridge.readSftpBinary(sftpId, filePath);
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

      await bridge.writeSftp(sftpId, filePath, content);
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
      const localTempPath = await bridge.downloadSftpToTemp(sftpId, remotePath, fileName);
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
          const result = await bridge.startFileWatch(localTempPath, remotePath, sftpId);
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
    async (side: "left" | "right", dataTransfer: DataTransfer): Promise<UploadResult[]> => {
      const pane = getActivePane(side);
      if (!pane?.connection) {
        throw new Error("No active connection");
      }

      const bridge = netcattyBridge.get();
      if (!bridge) {
        throw new Error("Bridge not available");
      }

      // Reset cancel state
      cancelUploadRef.current = false;
      currentTransferIdRef.current = "";

      // Create a "scanning" placeholder task to show user something is happening
      const scanningTaskId = crypto.randomUUID();
      if (addExternalUpload && pane.connection) {
        const scanningTask: TransferTask = {
          id: scanningTaskId,
          fileName: "Scanning files...",
          sourcePath: "local",
          targetPath: pane.connection.currentPath,
          sourceConnectionId: "external",
          targetConnectionId: pane.connection.id,
          direction: "upload",
          status: "pending" as TransferStatus,
          totalBytes: 0,
          transferredBytes: 0,
          speed: 0,
          startTime: Date.now(),
          isDirectory: true,
        };
        addExternalUpload(scanningTask);
      }

      let entries: DropEntry[];
      try {
        entries = await extractDropEntries(dataTransfer);
      } finally {
        // Remove the scanning placeholder
        if (dismissExternalUpload) {
          dismissExternalUpload(scanningTaskId);
        }
      }

      const results: UploadResult[] = [];
      const createdDirs = new Set<string>();

      const ensureDirectory = async (dirPath: string, sftpId: string | null) => {
        if (createdDirs.has(dirPath)) return;

        try {
          if (pane.connection?.isLocal) {
            if (bridge.mkdirLocal) {
              await bridge.mkdirLocal(dirPath);
            }
          } else if (sftpId) {
            await bridge.mkdirSftp(sftpId, dirPath);
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

      // Group entries by root folder to create bundled tasks
      const rootFolders = detectRootFolders(entries);

      // Sort entries: directories first, then by path depth
      const sortedEntries = [...entries].sort((a, b) => {
        if (a.isDirectory && !b.isDirectory) return -1;
        if (!a.isDirectory && b.isDirectory) return 1;
        const aDepth = a.relativePath.split('/').length;
        const bDepth = b.relativePath.split('/').length;
        return aDepth - bDepth;
      });

      let wasCancelled = false;
      const yieldToMain = () => new Promise<void>(resolve => setTimeout(resolve, 0));

      // Track bundled task progress
      // Map: bundleTaskId -> { totalBytes, transferredBytes, fileCount, completedCount, currentSpeed }
      const bundleProgress = new Map<string, {
        totalBytes: number;
        transferredBytes: number;
        fileCount: number;
        completedCount: number;
        currentSpeed: number;
        completedFilesBytes: number; // Sum of bytes from fully completed files
      }>();

      // Create bundled tasks for each root folder
      const bundleTaskIds = new Map<string, string>(); // rootName -> bundleTaskId
      for (const [rootName, rootEntries] of rootFolders) {
        const isStandaloneFile = rootName.startsWith("__file__");

        if (isStandaloneFile) {
          // Standalone files don't need bundling
          continue;
        }

        // Calculate total bytes for this folder
        let totalBytes = 0;
        let fileCount = 0;
        for (const entry of rootEntries) {
          if (!entry.isDirectory && entry.file) {
            totalBytes += entry.file.size;
            fileCount++;
          }
        }

        if (fileCount === 0) continue;

        const bundleTaskId = crypto.randomUUID();
        bundleTaskIds.set(rootName, bundleTaskId);
        bundleProgress.set(bundleTaskId, {
          totalBytes,
          transferredBytes: 0,
          fileCount,
          completedCount: 0,
          currentSpeed: 0,
          completedFilesBytes: 0,
        });

        // Create the bundled task
        if (addExternalUpload && pane.connection) {
          const displayName = fileCount === 1
            ? rootName
            : `${rootName} (${fileCount} files)`;
          const bundleTask: TransferTask = {
            id: bundleTaskId,
            fileName: displayName,
            sourcePath: "local",
            targetPath: joinPath(pane.connection.currentPath, rootName),
            sourceConnectionId: "external",
            targetConnectionId: pane.connection.id,
            direction: "upload",
            status: "transferring" as TransferStatus,
            totalBytes,
            transferredBytes: 0,
            speed: 0,
            startTime: Date.now(),
            isDirectory: true,
          };
          addExternalUpload(bundleTask);
        }
      }

      // Helper to get bundle task ID for an entry
      const getBundleTaskId = (entry: DropEntry): string | null => {
        const parts = entry.relativePath.split('/');
        const rootName = parts[0];
        if (parts.length > 1 || entry.isDirectory) {
          return bundleTaskIds.get(rootName) || null;
        }
        return null;
      };

      try {
        for (const entry of sortedEntries) {
          await yieldToMain();
          if (cancelUploadRef.current) {
            logger.info("[SFTP] External upload cancelled by user");
            wasCancelled = true;
            break;
          }

          const targetPath = joinPath(pane.connection.currentPath, entry.relativePath);
          const bundleTaskId = getBundleTaskId(entry);
          let standaloneTransferId = "";
          let fileTotalBytes = 0;

          try {
            if (entry.isDirectory) {
              await ensureDirectory(targetPath, sftpId);
            } else if (entry.file) {
              fileTotalBytes = entry.file.size;

              // For standalone files (not in a folder), create individual task
              if (!bundleTaskId) {
                standaloneTransferId = crypto.randomUUID();
                currentTransferIdRef.current = standaloneTransferId;

                if (addExternalUpload && pane.connection) {
                  const task: TransferTask = {
                    id: standaloneTransferId,
                    fileName: entry.relativePath,
                    sourcePath: "local",
                    targetPath,
                    sourceConnectionId: "external",
                    targetConnectionId: pane.connection.id,
                    direction: "upload",
                    status: "transferring" as TransferStatus,
                    totalBytes: fileTotalBytes,
                    transferredBytes: 0,
                    speed: 0,
                    startTime: Date.now(),
                    isDirectory: false,
                  };
                  addExternalUpload(task);
                }
              } else {
                currentTransferIdRef.current = bundleTaskId;
              }

              // Ensure parent directories exist
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
                    if (!cancelUploadRef.current) {
                      pendingProgressUpdate = { transferred, total, speed };

                      if (!rafScheduled) {
                        rafScheduled = true;
                        requestAnimationFrame(() => {
                          rafScheduled = false;
                          const update = pendingProgressUpdate;
                          pendingProgressUpdate = null;
                          if (update && !cancelUploadRef.current && updateExternalUpload) {
                            if (bundleTaskId) {
                              // Update bundle progress
                              const progress = bundleProgress.get(bundleTaskId);
                              if (progress) {
                                // Current file's transferred bytes + all completed files' bytes
                                const newTransferred = progress.completedFilesBytes + update.transferred;
                                progress.transferredBytes = newTransferred;
                                progress.currentSpeed = update.speed;
                                updateExternalUpload(bundleTaskId, {
                                  transferredBytes: newTransferred,
                                  speed: update.speed,
                                });
                              }
                            } else if (standaloneTransferId) {
                              updateExternalUpload(standaloneTransferId, {
                                transferredBytes: update.transferred,
                                totalBytes: update.total,
                                speed: update.speed,
                              });
                            }
                          }
                        });
                      }
                    }
                  };

                  // Use unique file transfer ID for backend cancellation tracking
                  // bundleTaskId/standaloneTransferId is for UI display, fileTransferId is for backend
                  const fileTransferId = crypto.randomUUID();
                  activeFileTransferIdsRef.current.add(fileTransferId);
                  currentTransferIdRef.current = fileTransferId;

                  let result;
                  try {
                    result = await bridge.writeSftpBinaryWithProgress(
                      sftpId,
                      targetPath,
                      arrayBuffer,
                      fileTransferId,
                      onProgress,
                      undefined,
                      undefined,
                    );
                  } finally {
                    activeFileTransferIdsRef.current.delete(fileTransferId);
                  }

                  if (result?.cancelled) {
                    logger.info("[SFTP] File upload cancelled:", entry.relativePath);
                    wasCancelled = true;
                    if (updateExternalUpload) {
                      const taskId = bundleTaskId || standaloneTransferId;
                      if (taskId) {
                        updateExternalUpload(taskId, {
                          status: "cancelled" as TransferStatus,
                          endTime: Date.now(),
                          speed: 0,
                        });
                      }
                    }
                    break;
                  }

                  if (!result || result.success === false) {
                    if (bridge.writeSftpBinary) {
                      await bridge.writeSftpBinary(sftpId, targetPath, arrayBuffer);
                    } else {
                      throw new Error("Upload failed and no fallback method available");
                    }
                  }
                } else if (bridge.writeSftpBinary) {
                  await bridge.writeSftpBinary(sftpId, targetPath, arrayBuffer);
                } else {
                  throw new Error("No SFTP write method available");
                }
              }

              currentTransferIdRef.current = "";
              results.push({ fileName: entry.relativePath, success: true });

              // Update progress tracking
              if (bundleTaskId) {
                const progress = bundleProgress.get(bundleTaskId);
                if (progress) {
                  progress.completedCount++;
                  progress.completedFilesBytes += fileTotalBytes;
                  progress.transferredBytes = progress.completedFilesBytes;

                  // Check if all files in bundle are complete
                  if (progress.completedCount >= progress.fileCount) {
                    if (updateExternalUpload) {
                      updateExternalUpload(bundleTaskId, {
                        status: "completed" as TransferStatus,
                        endTime: Date.now(),
                        transferredBytes: progress.totalBytes,
                        speed: 0,
                      });
                    }
                  } else {
                    // Update progress
                    if (updateExternalUpload) {
                      updateExternalUpload(bundleTaskId, {
                        transferredBytes: progress.completedFilesBytes,
                      });
                    }
                  }
                }
              } else if (standaloneTransferId && updateExternalUpload) {
                updateExternalUpload(standaloneTransferId, {
                  status: "completed" as TransferStatus,
                  endTime: Date.now(),
                  transferredBytes: fileTotalBytes,
                  speed: 0,
                });
              }
            }
          } catch (error) {
            currentTransferIdRef.current = "";

            // Check if this was a cancellation - if so, break out of the loop
            if (cancelUploadRef.current) {
              logger.info("[SFTP] Upload cancelled, stopping remaining files");
              wasCancelled = true;
              if (updateExternalUpload) {
                const taskId = bundleTaskId || standaloneTransferId;
                if (taskId) {
                  updateExternalUpload(taskId, {
                    status: "cancelled" as TransferStatus,
                    endTime: Date.now(),
                    speed: 0,
                  });
                }
              }
              break;
            }

            if (!entry.isDirectory) {
              logger.error(`Failed to upload ${entry.relativePath}:`, error);
              results.push({
                fileName: entry.relativePath,
                success: false,
                error: error instanceof Error ? error.message : String(error),
              });

              // Mark as failed
              if (updateExternalUpload) {
                const taskId = bundleTaskId || standaloneTransferId;
                if (taskId) {
                  updateExternalUpload(taskId, {
                    status: "failed" as TransferStatus,
                    endTime: Date.now(),
                    error: error instanceof Error ? error.message : String(error),
                    speed: 0,
                  });
                }
              }
            }
          }
        }
      } finally {
        currentTransferIdRef.current = "";
      }

      // Add cancelled flag to results if upload was cancelled
      if (wasCancelled) {
        results.push({ fileName: "", success: false, cancelled: true });
      }

      await refresh(side);

      return results;
    },
    [getActivePane, refresh, sftpSessionsRef, addExternalUpload, updateExternalUpload, dismissExternalUpload],
  );

  const cancelExternalUpload = useCallback(async () => {
    cancelUploadRef.current = true;

    const bridge = netcattyBridge.get();
    if (!bridge?.cancelSftpUpload) {
      return;
    }

    // Cancel all active file uploads
    const activeIds = Array.from(activeFileTransferIdsRef.current);
    for (const transferId of activeIds) {
      try {
        await bridge.cancelSftpUpload(transferId);
        logger.info("[SFTP] Cancelled file upload:", transferId);
      } catch (err) {
        logger.warn("[SFTP] Failed to cancel file upload:", transferId, String(err));
      }
    }

    // Also try to cancel the current one if not in the set
    const currentTransferId = currentTransferIdRef.current;
    if (currentTransferId && !activeIds.includes(currentTransferId)) {
      try {
        await bridge.cancelSftpUpload(currentTransferId);
        logger.info("[SFTP] Cancelled current file upload:", currentTransferId);
      } catch (err) {
        logger.warn("[SFTP] Failed to cancel current file upload:", err);
      }
    }
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
    readTextFile,
    readBinaryFile,
    writeTextFile,
    downloadToTempAndOpen,
    uploadExternalFiles,
    cancelExternalUpload,
    selectApplication,
  };
};
