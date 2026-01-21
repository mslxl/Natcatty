import { useCallback, useState } from "react";
import type { RemoteFile } from "../../types";
import { toast } from "../../ui/toast";
import { extractDropEntries } from "../../lib/sftpFileUtils";

interface UploadTask {
  id: string;
  fileName: string;
  status: "pending" | "uploading" | "completed" | "failed";
  progress: number;
  totalBytes: number;
  transferredBytes: number;
  speed: number;
  startTime: number;
  error?: string;
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
  ) => Promise<boolean>;
  writeSftpBinary: (sftpId: string, path: string, data: ArrayBuffer) => Promise<void>;
  writeSftp: (sftpId: string, path: string, data: string) => Promise<void>;
  mkdirLocal: (path: string) => Promise<void>;
  mkdirSftp: (sftpId: string, path: string) => Promise<void>;
  setLoading: (loading: boolean) => void;
  t: (key: string, params?: Record<string, unknown>) => string;
}

interface UseSftpModalTransfersResult {
  uploading: boolean;
  uploadTasks: UploadTask[];
  dragActive: boolean;
  handleDownload: (file: RemoteFile) => Promise<void>;
  handleUploadMultiple: (fileList: FileList) => Promise<void>;
  handleUploadFromDrop: (dataTransfer: DataTransfer) => Promise<void>;
  handleFileSelect: (e: React.ChangeEvent<HTMLInputElement>) => void;
  handleDrag: (e: React.DragEvent) => void;
  handleDrop: (e: React.DragEvent) => void;
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
  writeSftp,
  mkdirLocal,
  mkdirSftp,
  setLoading,
  t,
}: UseSftpModalTransfersParams): UseSftpModalTransfersResult => {
  const [uploading, setUploading] = useState(false);
  const [uploadTasks, setUploadTasks] = useState<UploadTask[]>([]);
  const [dragActive, setDragActive] = useState(false);

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

  const handleUploadFile = async (
    file: File,
    taskId: string,
    relativePath?: string,
  ): Promise<boolean> => {
    const startTime = Date.now();
    const displayName = relativePath || file.name;

    setUploadTasks((prev) =>
      prev.map((t) =>
        t.id === taskId
          ? {
            ...t,
            status: "uploading" as const,
            totalBytes: file.size,
            startTime,
            speed: 0,
          }
          : t,
      ),
    );

    try {
      const arrayBuffer = await file.arrayBuffer();
      const fullPath = joinPath(currentPath, displayName);

      if (isLocalSession) {
        await writeLocalFile(fullPath, arrayBuffer);
        const totalTime = (Date.now() - startTime) / 1000;
        const finalSpeed = totalTime > 0 ? file.size / totalTime : 0;
        setUploadTasks((prev) =>
          prev.map((t) =>
            t.id === taskId
              ? {
                ...t,
                status: "completed" as const,
                progress: 100,
                transferredBytes: file.size,
                speed: finalSpeed,
              }
              : t,
          ),
        );
        return true;
      }

      const sftpId = await ensureSftp();

      const progressResult = await writeSftpBinaryWithProgress(
        sftpId,
        fullPath,
        arrayBuffer,
        taskId,
        (transferred: number, total: number, speed: number) => {
          const progress = total > 0 ? Math.round((transferred / total) * 100) : 0;
          setUploadTasks((prev) =>
            prev.map((t) =>
              t.id === taskId && t.status === "uploading"
                ? { ...t, transferredBytes: transferred, progress, speed }
                : t,
            ),
          );
        },
        () => {
          const totalTime = (Date.now() - startTime) / 1000;
          const finalSpeed = totalTime > 0 ? file.size / totalTime : 0;
          setUploadTasks((prev) =>
            prev.map((t) =>
              t.id === taskId
                ? {
                  ...t,
                  status: "completed" as const,
                  progress: 100,
                  transferredBytes: file.size,
                  speed: finalSpeed,
                }
                : t,
            ),
          );
        },
        (error: string) => {
          setUploadTasks((prev) =>
            prev.map((t) =>
              t.id === taskId
                ? { ...t, status: "failed" as const, error }
                : t,
            ),
          );
        },
      );
      if (progressResult) return true;

      try {
        await writeSftpBinary(sftpId, fullPath, arrayBuffer);
      } catch {
        const text = await file.text();
        await writeSftp(sftpId, fullPath, text);
      }

      const totalTime = (Date.now() - startTime) / 1000;
      const finalSpeed = totalTime > 0 ? file.size / totalTime : 0;

      setUploadTasks((prev) =>
        prev.map((t) =>
          t.id === taskId
            ? {
              ...t,
              status: "completed" as const,
              progress: 100,
              transferredBytes: file.size,
              speed: finalSpeed,
            }
            : t,
        ),
      );
      return true;
    } catch (e) {
      setUploadTasks((prev) =>
        prev.map((task) =>
          task.id === taskId
            ? {
              ...task,
              status: "failed" as const,
              error: e instanceof Error ? e.message : t("sftp.error.uploadFailed"),
            }
            : task,
        ),
      );
      return false;
    }
  };

  const handleUploadMultiple = useCallback(
    async (fileList: FileList) => {
      if (fileList.length === 0) return;

      const newTasks: UploadTask[] = Array.from(fileList).map((file) => ({
        id: crypto.randomUUID(),
        fileName: file.name,
        status: "pending" as const,
        progress: 0,
        totalBytes: file.size,
        transferredBytes: 0,
        speed: 0,
        startTime: 0,
      }));

      setUploadTasks((prev) => [...prev, ...newTasks]);
      setUploading(true);

      const filesToUpload = Array.from(fileList);
      for (let i = 0; i < filesToUpload.length; i++) {
        await handleUploadFile(filesToUpload[i], newTasks[i].id);
      }

      setUploading(false);
      await loadFiles(currentPath, { force: true });

      setTimeout(() => {
        setUploadTasks((prev) => prev.filter((t) => t.status !== "completed"));
      }, 3000);
    },
    [currentPath, loadFiles],
  );

  const handleUploadFromDrop = useCallback(
    async (dataTransfer: DataTransfer) => {
      const entries = await extractDropEntries(dataTransfer);
      if (entries.length === 0) return;

      const createdDirs = new Set<string>();
      const ensureDirectory = async (dirPath: string) => {
        if (createdDirs.has(dirPath)) return;
        try {
          if (isLocalSession) {
            await mkdirLocal(dirPath);
          } else {
            const sftpId = await ensureSftp();
            await mkdirSftp(sftpId, dirPath);
          }
          createdDirs.add(dirPath);
        } catch {
          createdDirs.add(dirPath);
        }
      };

      const sortedEntries = [...entries].sort((a, b) => {
        if (a.isDirectory && !b.isDirectory) return -1;
        if (!a.isDirectory && b.isDirectory) return 1;
        const aDepth = a.relativePath.split("/").length;
        const bDepth = b.relativePath.split("/").length;
        return aDepth - bDepth;
      });

      const fileEntries = sortedEntries.filter((e) => !e.isDirectory);

      const newTasks: UploadTask[] = fileEntries.map((entry) => ({
        id: crypto.randomUUID(),
        fileName: entry.relativePath,
        status: "pending" as const,
        progress: 0,
        totalBytes: entry.file.size,
        transferredBytes: 0,
        speed: 0,
        startTime: 0,
      }));

      if (newTasks.length > 0) {
        setUploadTasks((prev) => [...prev, ...newTasks]);
      }
      setUploading(true);

      let taskIndex = 0;
      for (const entry of sortedEntries) {
        const targetPath = joinPath(currentPath, entry.relativePath);

        if (entry.isDirectory) {
          await ensureDirectory(targetPath);
        } else if (entry.file) {
          const pathParts = entry.relativePath.split("/");
          if (pathParts.length > 1) {
            let parentPath = currentPath;
            for (let i = 0; i < pathParts.length - 1; i++) {
              parentPath = joinPath(parentPath, pathParts[i]);
              await ensureDirectory(parentPath);
            }
          }

          await handleUploadFile(entry.file, newTasks[taskIndex].id, entry.relativePath);
          taskIndex++;
        }
      }

      setUploading(false);
      await loadFiles(currentPath, { force: true });

      setTimeout(() => {
        setUploadTasks((prev) => prev.filter((t) => t.status !== "completed"));
      }, 3000);
    },
    [currentPath, ensureSftp, isLocalSession, joinPath, loadFiles, mkdirLocal, mkdirSftp],
  );

  const handleFileSelect = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      if (e.target.files && e.target.files.length > 0) {
        void handleUploadMultiple(e.target.files);
      }
      e.target.value = "";
    },
    [handleUploadMultiple],
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

  return {
    uploading,
    uploadTasks,
    dragActive,
    handleDownload,
    handleUploadMultiple,
    handleUploadFromDrop,
    handleFileSelect,
    handleDrag,
    handleDrop,
  };
};
