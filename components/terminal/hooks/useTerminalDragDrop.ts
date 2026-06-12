import { Terminal as XTerm } from "@xterm/xterm";
import type React from "react";
import { useRef, useState } from "react";

import { logger } from "../../../lib/logger";
import {
  buildZmodemDragDropFiles,
  supportsZmodemTerminalDragDrop,
  type ZmodemDragDropFile,
} from "../../../lib/zmodemDragDrop";
import { extractDropEntries, type DropEntry } from "../../../lib/sftpFileUtils";
import type { Host, TerminalSession } from "../../../types";
import { toast } from "../../ui/toast";
import {
  extractRootPathsFromDropEntries,
  type TerminalProps,
} from "../terminalHelpers";

interface UseTerminalDragDropOptions {
  host: Host;
  isLocalConnection: boolean;
  isNetworkDevice?: boolean;
  onOpenSftp?: TerminalProps["onOpenSftp"];
  resolveSftpInitialPath: (options?: { preferFreshBackend?: boolean }) => Promise<string | undefined>;
  scrollToBottomAfterProgrammaticInput: (data: string) => void;
  sessionId: string;
  sessionRef: React.MutableRefObject<string | null>;
  status: TerminalSession["status"];
  t: (key: string) => string;
  terminalBackend: {
    writeToSession: (sessionId: string, data: string, options?: { automated?: boolean }) => void;
    startZmodemDragDropUpload?: (
      sessionId: string,
      files: ZmodemDragDropFile[],
      uploadCommand?: string,
    ) => Promise<{ success: boolean; error?: string }>;
  };
  termRef: React.MutableRefObject<XTerm | null>;
}

export async function resolveTerminalDropUploadInitialPath(
  resolveSftpInitialPath: UseTerminalDragDropOptions["resolveSftpInitialPath"],
): Promise<string | undefined> {
  return resolveSftpInitialPath({ preferFreshBackend: true });
}

export async function handleTerminalDropEntries({
  dropEntries,
  host,
  isLocalConnection,
  isNetworkDevice = false,
  onOpenSftp,
  resolveSftpInitialPath,
  scrollToBottomAfterProgrammaticInput,
  sessionId,
  sessionRef,
  terminalBackend,
  termRef,
}: Pick<
  UseTerminalDragDropOptions,
  | "host"
  | "isLocalConnection"
  | "isNetworkDevice"
  | "onOpenSftp"
  | "resolveSftpInitialPath"
  | "scrollToBottomAfterProgrammaticInput"
  | "sessionId"
  | "sessionRef"
  | "terminalBackend"
  | "termRef"
> & {
  dropEntries: DropEntry[];
}): Promise<void> {
  if (dropEntries.length === 0) {
    return;
  }

  if (isLocalConnection) {
    const paths = extractRootPathsFromDropEntries(dropEntries);

    if (paths.length > 0 && termRef.current && sessionRef.current) {
      const pathsText = paths.join(" ");
      terminalBackend.writeToSession(sessionRef.current, pathsText);
      scrollToBottomAfterProgrammaticInput(pathsText);
      termRef.current.focus();
    }
    return;
  }

  if (supportsZmodemTerminalDragDrop(host, isNetworkDevice)) {
    const files = await buildZmodemDragDropFiles(dropEntries);
    if (files.length === 0) {
      throw new Error("No files to upload");
    }

    if (!terminalBackend.startZmodemDragDropUpload) {
      throw new Error("ZMODEM drag-drop upload is unavailable");
    }

    const result = await terminalBackend.startZmodemDragDropUpload(sessionId, files);
    if (!result.success) {
      throw new Error(result.error || "ZMODEM upload failed");
    }
  } else if (onOpenSftp) {
    const initialPath = await resolveTerminalDropUploadInitialPath(resolveSftpInitialPath);
    onOpenSftp(host, initialPath, dropEntries, sessionId);
  }
}

export function useTerminalDragDrop({
  host,
  isLocalConnection,
  isNetworkDevice = false,
  onOpenSftp,
  resolveSftpInitialPath,
  scrollToBottomAfterProgrammaticInput,
  sessionId,
  sessionRef,
  status,
  t,
  terminalBackend,
  termRef,
}: UseTerminalDragDropOptions) {
  const [isDraggingOver, setIsDraggingOver] = useState(false);
  const dragCounterRef = useRef(0);

  const handleDragEnter = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    dragCounterRef.current++;
    if (e.dataTransfer.types.includes("Files")) {
      setIsDraggingOver(true);
    }
  };

  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer.types.includes("Files")) {
      e.dataTransfer.dropEffect = "copy";
    }
  };

  const handleDragLeave = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    dragCounterRef.current--;
    if (dragCounterRef.current === 0) {
      setIsDraggingOver(false);
    }
  };

  const handleDrop = async (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    dragCounterRef.current = 0;
    setIsDraggingOver(false);

    if (!e.dataTransfer.types.includes("Files")) {
      return;
    }

    if (status !== "connected") {
      toast.error(t("terminal.dragDrop.notConnected"), t("terminal.dragDrop.errorTitle"));
      return;
    }

    try {
      const dropEntries = await extractDropEntries(e.dataTransfer);
      await handleTerminalDropEntries({
        dropEntries,
        host,
        isLocalConnection,
        isNetworkDevice,
        onOpenSftp,
        resolveSftpInitialPath,
        scrollToBottomAfterProgrammaticInput,
        sessionId,
        sessionRef,
        terminalBackend,
        termRef,
      });
    } catch (error) {
      logger.error("Failed to handle file drop", error);
      const message = error instanceof Error && error.message === "No files to upload"
        ? t("terminal.dragDrop.noFiles")
        : t("terminal.dragDrop.errorMessage");
      toast.error(message, t("terminal.dragDrop.errorTitle"));
    }
  };

  return {
    handleDragEnter,
    handleDragLeave,
    handleDragOver,
    handleDrop,
    isDraggingOver,
  };
}
