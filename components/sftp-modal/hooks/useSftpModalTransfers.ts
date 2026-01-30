import React, { useCallback, useState, useRef, useMemo } from "react";
import type { RemoteFile } from "../../../types";
import { toast } from "../../ui/toast";
import {
  UploadController,
  uploadFromDataTransfer,
  uploadFromFileList,
  UploadBridge,
  UploadCallbacks,
  UploadTaskInfo,
  UploadProgress,
} from "../../../lib/uploadService";

interface UploadTask {
  id: string;
  fileName: string;
  status: "pending" | "uploading" | "completed" | "failed" | "cancelled";
  progress: number;
  totalBytes: number;
  transferredBytes: number;
  speed: number;
  startTime: number;
  error?: string;
  isDirectory?: boolean;
  fileCount?: number;
  completedCount?: number;
}

interface UseSftpModalTransfersParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  loadFiles: (path: string, options?: { force?: boolean }) => Promise<void>;
  readLocalFile: (path: string) => Promise<ArrayBuffer>;
  readSftp: (sftpId: string, path: string) => Promise<string>;
  writeLocalFile: (path: string, data: ArrayBuffer) => Promise<void>;
  writeSftpBinaryWithProgress: (
    sftpId: string,
    path: string,
    data: ArrayBuffer,
    taskId: string,
    onProgress: (transferred: number, total: number, speed: number) => void,
    onComplete: () => void,
    onError: (error: string) => void,
  ) => Promise<{ success: boolean; transferId: string; cancelled?: boolean }>;
  writeSftpBinary: (sftpId: string, path: string, data: ArrayBuffer) => Promise<void>;
  writeSftp: (sftpId: string, path: string, data: string) => Promise<void>;
  mkdirLocal: (path: string) => Promise<void>;
  mkdirSftp: (sftpId: string, path: string) => Promise<void>;
  cancelSftpUpload?: (taskId: string) => Promise<unknown>;
  startStreamTransfer?: (
    options: {
      transferId: string;
      sourcePath: string;
      targetPath: string;
      sourceType: 'local' | 'sftp';
      targetType: 'local' | 'sftp';
      sourceSftpId?: string;
      targetSftpId?: string;
      totalBytes?: number;
    },
    onProgress?: (transferred: number, total: number, speed: number) => void,
    onComplete?: () => void,
    onError?: (error: string) => void
  ) => Promise<{ transferId: string; totalBytes?: number; error?: string }>;
  cancelTransfer?: (transferId: string) => Promise<void>;
  setLoading: (loading: boolean) => void;
  t: (key: string, params?: Record<string, unknown>) => string;
  useCompressedUpload?: boolean; // Enable compressed folder uploads
}

interface UseSftpModalTransfersResult {
  uploading: boolean;
  uploadTasks: UploadTask[];
  dragActive: boolean;
  handleDownload: (file: RemoteFile) => Promise<void>;
  handleUploadMultiple: (fileList: FileList) => Promise<void>;
  handleUploadFromDrop: (dataTransfer: DataTransfer) => Promise<void>;
  handleFileSelect: (e: React.ChangeEvent<HTMLInputElement>) => void;
  handleFolderSelect: (e: React.ChangeEvent<HTMLInputElement>) => void;
  handleDrag: (e: React.DragEvent) => void;
  handleDrop: (e: React.DragEvent) => void;
  cancelUpload: () => Promise<void>;
  dismissTask: (taskId: string) => void;
}

export const useSftpModalTransfers = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  loadFiles,
  readLocalFile,
  readSftp,
  writeLocalFile,
  writeSftpBinaryWithProgress,
  writeSftpBinary,
  mkdirLocal,
  mkdirSftp,
  cancelSftpUpload,
  startStreamTransfer,
  cancelTransfer,
  setLoading,
  t,
  useCompressedUpload = false,
}: UseSftpModalTransfersParams): UseSftpModalTransfersResult => {
  const [uploading, setUploading] = useState(false);
  const [uploadTasks, setUploadTasks] = useState<UploadTask[]>([]);
  const [dragActive, setDragActive] = useState(false);

  // Upload controller for cancellation support
  const uploadControllerRef = useRef<UploadController | null>(null);

  // Cached SFTP ID to avoid multiple calls to ensureSftp
  const cachedSftpIdRef = useRef<string | null>(null);

  // Track cancelled transfer IDs to detect cancellation in bridge wrapper
  const cancelledTransferIdsRef = useRef<Set<string>>(new Set());

  // Create upload bridge that adapts the modal's functions to the service interface
  const createUploadBridge = useMemo((): UploadBridge => {
    return {
      writeLocalFile,
      mkdirLocal,
      mkdirSftp: async (sftpId: string, path: string) => {
        await mkdirSftp(sftpId, path);
      },
      writeSftpBinary: async (sftpId: string, path: string, data: ArrayBuffer) => {
        await writeSftpBinary(sftpId, path, data);
      },
      writeSftpBinaryWithProgress: async (
        sftpId: string,
        path: string,
        data: ArrayBuffer,
        taskId: string,
        onProgress: (transferred: number, total: number, speed: number) => void,
        onComplete?: () => void,
        onError?: (error: string) => void
      ) => {
        try {
          const result = await writeSftpBinaryWithProgress(
            sftpId,
            path,
            data,
            taskId,
            onProgress,
            onComplete || (() => { }),
            onError || (() => { })
          );
          
          // Check if this transfer was cancelled
          const wasCancelled = cancelledTransferIdsRef.current.has(taskId);
          if (wasCancelled) {
            cancelledTransferIdsRef.current.delete(taskId);
          }
          return { success: result.success, transferId: result.transferId, cancelled: wasCancelled || result.cancelled };
        } catch (error) {
          // Check if this was a user-initiated cancellation
          const wasCancelled = cancelledTransferIdsRef.current.has(taskId);
          if (wasCancelled) {
            cancelledTransferIdsRef.current.delete(taskId);
            return { success: false, transferId: taskId, cancelled: true };
          }
          // Real error - propagate it by re-throwing
          throw error;
        }
      },
      cancelSftpUpload,
      startStreamTransfer: startStreamTransfer ? async (
        options,
        onProgress,
        onComplete,
        onError
      ) => {
        try {
          const result = await startStreamTransfer(options, onProgress, onComplete, onError);
          const wasCancelled = cancelledTransferIdsRef.current.has(options.transferId);
          if (wasCancelled) {
            cancelledTransferIdsRef.current.delete(options.transferId);
          }
          // Handle case where result might be undefined (bridge not available)
          if (!result) {
            return { transferId: options.transferId, error: 'Stream transfer not available' };
          }
          return { ...result, cancelled: wasCancelled };
        } catch (error) {
          const wasCancelled = cancelledTransferIdsRef.current.has(options.transferId);
          if (wasCancelled) {
            cancelledTransferIdsRef.current.delete(options.transferId);
            return { transferId: options.transferId, cancelled: true };
          }
          return { transferId: options.transferId, error: error instanceof Error ? error.message : String(error) };
        }
      } : undefined,
      cancelTransfer,
    };
  }, [writeLocalFile, mkdirLocal, mkdirSftp, writeSftpBinary, writeSftpBinaryWithProgress, cancelSftpUpload, startStreamTransfer, cancelTransfer]);

  // Create upload callbacks
  const createUploadCallbacks = useCallback((): UploadCallbacks => {
    return {
      onScanningStart: (taskId: string) => {
        const scanningTask: UploadTask = {
          id: taskId,
          fileName: t("sftp.upload.scanning"),
          status: "pending",
          progress: 0,
          totalBytes: 0,
          transferredBytes: 0,
          speed: 0,
          startTime: Date.now(),
          isDirectory: true,
        };
        setUploadTasks(prev => [...prev, scanningTask]);
      },
      onScanningEnd: (taskId: string) => {
        setUploadTasks(prev => prev.filter(t => t.id !== taskId));
      },
      onTaskCreated: (task: UploadTaskInfo) => {
        const uploadTask: UploadTask = {
          id: task.id,
          fileName: task.displayName,
          status: "pending",
          progress: 0,
          totalBytes: task.totalBytes,
          transferredBytes: 0,
          speed: 0,
          startTime: Date.now(),
          isDirectory: task.isDirectory,
        };
        setUploadTasks(prev => [...prev, uploadTask]);
      },
      onTaskProgress: (taskId: string, progress: UploadProgress) => {
        console.log(`[useSftpModalTransfers] onTaskProgress called for taskId: ${taskId}, progress: ${progress.percent}%`);
        setUploadTasks(prev =>
          prev.map(task => {
            if (task.id !== taskId) return task;
            
            // Don't update progress if task is already completed, failed, or cancelled
            if (task.status === "completed" || task.status === "failed" || task.status === "cancelled") {
              console.log(`[useSftpModalTransfers] Ignoring progress update for ${taskId} - task already ${task.status}`);
              return task;
            }
            
            return {
              ...task,
              status: "uploading" as const,
              progress: progress.percent,
              transferredBytes: progress.transferred,
              speed: progress.speed,
            };
          })
        );
      },
      onTaskCompleted: (taskId: string, totalBytes: number) => {
        console.log(`[useSftpModalTransfers] onTaskCompleted called for taskId: ${taskId}`);
        setUploadTasks(prev => {
          console.log(`[useSftpModalTransfers] Current tasks before update:`, prev.map(t => ({ id: t.id, status: t.status, fileName: t.fileName })));
          const updated = prev.map(task =>
            task.id === taskId
              ? {
                ...task,
                status: "completed" as const,
                progress: 100,
                transferredBytes: totalBytes,
                speed: 0,
              }
              : task
          );
          console.log(`[useSftpModalTransfers] Updated tasks:`, updated.map(t => ({ id: t.id, status: t.status, fileName: t.fileName })));
          return updated;
        });
      },
      onTaskFailed: (taskId: string, error: string) => {
        console.error(`Upload task ${taskId} failed:`, error);
        setUploadTasks(prev =>
          prev.map(task =>
            task.id === taskId
              ? {
                ...task,
                status: "failed" as const,
                speed: 0,
                error,
              }
              : task
          )
        );
      },
      onTaskCancelled: (taskId: string) => {
        setUploadTasks(prev =>
          prev.map(task =>
            task.id === taskId
              ? {
                ...task,
                status: "cancelled" as const,
                speed: 0,
              }
              : task
          )
        );
      },
      onTaskNameUpdate: (taskId: string, newName: string) => {
        // Parse the phase format: "folderName|phase"
        let displayName = newName;
        if (newName.includes('|')) {
          const [folderName, phase] = newName.split('|');
          const phaseLabel = phase === 'compressing' ? t('sftp.upload.phase.compressing')
            : phase === 'extracting' ? t('sftp.upload.phase.extracting')
            : phase === 'uploading' ? t('sftp.upload.phase.uploading')
            : t('sftp.upload.phase.compressed');
          displayName = `${folderName} (${phaseLabel})`;
        }
        setUploadTasks(prev =>
          prev.map(task =>
            task.id === taskId
              ? {
                ...task,
                fileName: displayName,
              }
              : task
          )
        );
      },
    };
  }, [t]);

  // Helper function to perform upload with compression setting from user preference
  const performUpload = useCallback(async (
    files: FileList | File[], 
    useCompressed: boolean
  ): Promise<void> => {
    console.log('[useSftpModalTransfers] performUpload called', {
      length: files.length,
      currentPath,
      isLocalSession,
      useCompressed
    });
    if (files.length === 0) return;

    setUploading(true);

    // Get SFTP ID for remote sessions
    let sftpId: string | null = null;
    if (!isLocalSession) {
      sftpId = await ensureSftp();
      cachedSftpIdRef.current = sftpId;
    }

    // Create controller for cancellation
    const controller = new UploadController();
    uploadControllerRef.current = controller;

    const callbacks = createUploadCallbacks();

    try {
      await uploadFromFileList(
        files,
        {
          targetPath: currentPath,
          sftpId,
          isLocal: isLocalSession,
          bridge: createUploadBridge,
          joinPath,
          callbacks,
          useCompressedUpload: useCompressed,
        },
        controller
      );

      await loadFiles(currentPath, { force: true });
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t("sftp.error.uploadFailed"),
        "SFTP"
      );
    } finally {
      // Upload process is complete - clear uploading state and controller
      setUploading(false);
      uploadControllerRef.current = null;
      cachedSftpIdRef.current = null;
    }
  }, [currentPath, createUploadBridge, createUploadCallbacks, ensureSftp, isLocalSession, joinPath, loadFiles, t]);

  const handleDownload = useCallback(
    async (file: RemoteFile) => {
      try {
        const fullPath = joinPath(currentPath, file.name);
        setLoading(true);
        const content = isLocalSession
          ? await readLocalFile(fullPath)
          : await readSftp(await ensureSftp(), fullPath);
        const blob = new Blob([content], { type: "application/octet-stream" });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = file.name;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } catch (e) {
        toast.error(
          e instanceof Error ? e.message : t("sftp.error.downloadFailed"),
          "SFTP",
        );
      } finally {
        setLoading(false);
      }
    },
    [currentPath, ensureSftp, isLocalSession, joinPath, readLocalFile, readSftp, setLoading, t],
  );



  const handleUploadMultiple = useCallback(
    async (fileList: FileList) => {
      console.log('[useSftpModalTransfers] handleUploadMultiple called', {
        length: fileList.length,
        currentPath,
        isLocalSession
      });
      if (fileList.length === 0) return;

      // Use compressed upload if enabled in settings (auto-fallback is handled in uploadService)
      await performUpload(fileList, useCompressedUpload);
    },
    [currentPath, isLocalSession, performUpload, useCompressedUpload],
  );

  const handleUploadFromDrop = useCallback(
    async (dataTransfer: DataTransfer) => {
      setUploading(true);

      // Get SFTP ID for remote sessions
      let sftpId: string | null = null;
      if (!isLocalSession) {
        sftpId = await ensureSftp();
        cachedSftpIdRef.current = sftpId;
      }

      // Create controller for cancellation
      const controller = new UploadController();
      uploadControllerRef.current = controller;

      const callbacks = createUploadCallbacks();

      try {
        await uploadFromDataTransfer(
          dataTransfer,
          {
            targetPath: currentPath,
            sftpId,
            isLocal: isLocalSession,
            bridge: createUploadBridge,
            joinPath,
            callbacks,
            useCompressedUpload,
          },
          controller
        );

        await loadFiles(currentPath, { force: true });
      } catch (error) {
        toast.error(
          error instanceof Error ? error.message : t("sftp.error.uploadFailed"),
          "SFTP"
        );
      } finally {
        // Upload process is complete - clear uploading state and controller
        setUploading(false);
        uploadControllerRef.current = null;
        cachedSftpIdRef.current = null;
      }
    },
    [currentPath, createUploadBridge, createUploadCallbacks, ensureSftp, isLocalSession, joinPath, loadFiles, t, useCompressedUpload],
  );

  // Handle upload from File array (used by file input after copying files)
  const handleUploadFromFiles = useCallback(
    async (files: File[]) => {
      console.log('[useSftpModalTransfers] handleUploadFromFiles called', {
        length: files.length,
        currentPath,
        isLocalSession
      });
      if (files.length === 0) return;

      // Use compressed upload if enabled in settings (auto-fallback is handled in uploadService)
      await performUpload(files, useCompressedUpload);
    },
    [currentPath, isLocalSession, performUpload, useCompressedUpload],
  );

  const handleFileSelect = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      console.log('[useSftpModalTransfers] handleFileSelect called', {
        files: e.target.files,
        length: e.target.files?.length
      });
      if (e.target.files && e.target.files.length > 0) {
        console.log('[useSftpModalTransfers] Starting file upload for', e.target.files.length, 'files');
        // Copy the files before clearing the input, because clearing the input
        // will also clear the FileList reference
        const files = Array.from(e.target.files);
        // Clear input first to allow selecting the same files again
        e.target.value = "";
        // Now start the upload with the copied files
        void handleUploadFromFiles(files);
      } else {
        e.target.value = "";
      }
    },
    [handleUploadFromFiles],
  );

  const handleFolderSelect = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      console.log('[useSftpModalTransfers] handleFolderSelect called', {
        files: e.target.files,
        length: e.target.files?.length
      });
      if (e.target.files && e.target.files.length > 0) {
        console.log('[useSftpModalTransfers] Starting folder upload for', e.target.files.length, 'files');
        // Copy the files before clearing the input, because clearing the input
        // will also clear the FileList reference
        const files = Array.from(e.target.files);
        // Clear input first to allow selecting the same folder again
        e.target.value = "";
        // Now start the upload with the copied files
        void handleUploadFromFiles(files);
      } else {
        e.target.value = "";
      }
    },
    [handleUploadFromFiles],
  );

  const handleDrag = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (e.type === "dragenter" || e.type === "dragover") {
      setDragActive(true);
    } else if (e.type === "dragleave") {
      setDragActive(false);
    }
  }, []);

  const handleDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      setDragActive(false);
      if (e.dataTransfer.items && e.dataTransfer.items.length > 0) {
        void handleUploadFromDrop(e.dataTransfer);
      }
    },
    [handleUploadFromDrop],
  );

  const cancelUpload = useCallback(async () => {
    console.log('[useSftpModalTransfers] cancelUpload called');
    const controller = uploadControllerRef.current;
    if (controller) {
      // Mark all active transfer IDs as cancelled before calling cancel
      const activeIds = controller.getActiveTransferIds();
      console.log('[useSftpModalTransfers] Active transfer IDs:', activeIds);
      for (const id of activeIds) {
        cancelledTransferIdsRef.current.add(id);
      }
      await controller.cancel();
      console.log('[useSftpModalTransfers] controller.cancel() completed');
    } else {
      console.log('[useSftpModalTransfers] No controller found - tasks may have already completed');
    }

    // Always clear all uploading/pending tasks immediately, even without controller
    setUploadTasks(prev => {
      const hasActiveTasks = prev.some(t => t.status === "uploading" || t.status === "pending");
      if (!hasActiveTasks) {
        console.log('[useSftpModalTransfers] No active tasks to cancel');
        return prev;
      }

      console.log('[useSftpModalTransfers] Cancelling active tasks');
      return prev.map(task =>
        task.status === "uploading" || task.status === "pending"
          ? { ...task, status: "cancelled" as const, speed: 0 }
          : task
      );
    });

    // Also reset uploading state
    setUploading(false);
  }, []);

  const dismissTask = useCallback((taskId: string) => {
    setUploadTasks(prev => prev.filter(t => t.id !== taskId));
  }, []);

  return {
    uploading,
    uploadTasks,
    dragActive,
    handleDownload,
    handleUploadMultiple,
    handleUploadFromDrop,
    handleFileSelect,
    handleFolderSelect,
    handleDrag,
    handleDrop,
    cancelUpload,
    dismissTask,
  };
};
