import { useCallback, useState } from "react";
import type { RemoteFile } from "../../types";
import { toast } from "../../ui/toast";

interface UseSftpModalTextEditorParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  readLocalFile: (path: string) => Promise<ArrayBuffer>;
  readSftp: (sftpId: string, path: string) => Promise<string>;
  writeLocalFile: (path: string, data: ArrayBuffer) => Promise<void>;
  writeSftp: (sftpId: string, path: string, data: string) => Promise<void>;
  t: (key: string, params?: Record<string, unknown>) => string;
}

interface UseSftpModalTextEditorResult {
  showTextEditor: boolean;
  setShowTextEditor: (open: boolean) => void;
  textEditorTarget: RemoteFile | null;
  textEditorContent: string;
  setTextEditorContent: (value: string) => void;
  loadingTextContent: boolean;
  handleEditFile: (file: RemoteFile) => Promise<void>;
  handleSaveTextFile: (content: string) => Promise<void>;
}

export const useSftpModalTextEditor = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  readLocalFile,
  readSftp,
  writeLocalFile,
  writeSftp,
  t,
}: UseSftpModalTextEditorParams): UseSftpModalTextEditorResult => {
  const [showTextEditor, setShowTextEditor] = useState(false);
  const [textEditorTarget, setTextEditorTarget] = useState<RemoteFile | null>(null);
  const [textEditorContent, setTextEditorContent] = useState("");
  const [loadingTextContent, setLoadingTextContent] = useState(false);

  const handleEditFile = useCallback(async (file: RemoteFile) => {
    try {
      setLoadingTextContent(true);
      setTextEditorTarget(file);
      const fullPath = joinPath(currentPath, file.name);
      const content = isLocalSession
        ? await readLocalFile(fullPath).then((buf) => new TextDecoder().decode(buf))
        : await readSftp(await ensureSftp(), fullPath);
      setTextEditorContent(content);
      setShowTextEditor(true);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t("sftp.error.loadFailed"),
        "SFTP",
      );
    } finally {
      setLoadingTextContent(false);
    }
  }, [currentPath, ensureSftp, isLocalSession, joinPath, readLocalFile, readSftp, t]);

  const handleSaveTextFile = useCallback(async (content: string) => {
    if (!textEditorTarget) return;
    const fullPath = joinPath(currentPath, textEditorTarget.name);
    if (isLocalSession) {
      const encoder = new TextEncoder();
      await writeLocalFile(fullPath, encoder.encode(content).buffer);
    } else {
      await writeSftp(await ensureSftp(), fullPath, content);
    }
  }, [currentPath, ensureSftp, isLocalSession, joinPath, textEditorTarget, writeLocalFile, writeSftp]);

  return {
    showTextEditor,
    setShowTextEditor,
    textEditorTarget,
    textEditorContent,
    setTextEditorContent,
    loadingTextContent,
    handleEditFile,
    handleSaveTextFile,
  };
};
