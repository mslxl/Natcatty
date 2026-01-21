import { useCallback, useRef } from "react";
import type { RemoteFile } from "../../types";

interface UseSftpModalSelectionParams {
  files: RemoteFile[];
  setSelectedFiles: (value: Set<string> | ((prev: Set<string>) => Set<string>)) => void;
  currentPath: string;
  joinPath: (base: string, name: string) => string;
  onNavigate: (path: string) => void;
  onOpenFile: (file: RemoteFile) => void;
  onNavigateUp: () => void;
}

interface UseSftpModalSelectionResult {
  handleFileClick: (file: RemoteFile, index: number, e: React.MouseEvent) => void;
  handleFileDoubleClick: (file: RemoteFile) => void;
}

export const useSftpModalSelection = ({
  files,
  setSelectedFiles,
  currentPath,
  joinPath,
  onNavigate,
  onOpenFile,
  onNavigateUp,
}: UseSftpModalSelectionParams): UseSftpModalSelectionResult => {
  const lastSelectedIndexRef = useRef<number | null>(null);

  const handleFileClick = useCallback(
    (file: RemoteFile, index: number, e: React.MouseEvent) => {
      if (file.name === "..") return;

      if (file.type === "directory") {
        if (e.shiftKey && lastSelectedIndexRef.current !== null) {
          const start = Math.min(lastSelectedIndexRef.current, index);
          const end = Math.max(lastSelectedIndexRef.current, index);
          const newSelection = new Set<string>();
          for (let i = start; i <= end; i++) {
            if (files[i] && files[i].type !== "directory") {
              newSelection.add(files[i].name);
            }
          }
          setSelectedFiles(newSelection);
        } else if (e.ctrlKey || e.metaKey) {
          setSelectedFiles((prev) => {
            const next = new Set(prev);
            return next;
          });
        }
        return;
      }

      if (e.shiftKey && lastSelectedIndexRef.current !== null) {
        const start = Math.min(lastSelectedIndexRef.current, index);
        const end = Math.max(lastSelectedIndexRef.current, index);
        const newSelection = new Set<string>();
        for (let i = start; i <= end; i++) {
          if (files[i] && files[i].type !== "directory") {
            newSelection.add(files[i].name);
          }
        }
        setSelectedFiles(newSelection);
      } else if (e.ctrlKey || e.metaKey) {
        setSelectedFiles((prev) => {
          const next = new Set(prev);
          if (next.has(file.name)) {
            next.delete(file.name);
          } else {
            next.add(file.name);
          }
          return next;
        });
        lastSelectedIndexRef.current = index;
      } else {
        setSelectedFiles(new Set([file.name]));
        lastSelectedIndexRef.current = index;
      }
    },
    [files, setSelectedFiles],
  );

  const handleFileDoubleClick = useCallback(
    (file: RemoteFile) => {
      if (file.name === "..") {
        onNavigateUp();
        return;
      }
      if (file.type === "directory" || (file.type === "symlink" && file.linkTarget === "directory")) {
        onNavigate(joinPath(currentPath, file.name));
      } else {
        onOpenFile(file);
      }
    },
    [currentPath, joinPath, onNavigate, onNavigateUp, onOpenFile],
  );

  return { handleFileClick, handleFileDoubleClick };
};
