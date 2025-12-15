import type { Terminal as XTerm } from "@xterm/xterm";
import { useCallback } from "react";
import type { MutableRefObject } from "react";
import { logger } from "../../../lib/logger";

type TerminalBackendWriteApi = {
  writeToSession: (sessionId: string, data: string) => void;
};

export const useTerminalContextActions = ({
  termRef,
  sessionRef,
  terminalBackend,
  onHasSelectionChange,
}: {
  termRef: MutableRefObject<XTerm | null>;
  sessionRef: MutableRefObject<string | null>;
  terminalBackend: TerminalBackendWriteApi;
  onHasSelectionChange?: (hasSelection: boolean) => void;
}) => {
  const onCopy = useCallback(() => {
    const term = termRef.current;
    if (!term) return;
    const selection = term.getSelection();
    if (selection) {
      navigator.clipboard.writeText(selection);
    }
  }, [termRef]);

  const onPaste = useCallback(async () => {
    const term = termRef.current;
    if (!term) return;
    try {
      const text = await navigator.clipboard.readText();
      if (text && sessionRef.current) terminalBackend.writeToSession(sessionRef.current, text);
    } catch (err) {
      logger.warn("Failed to paste from clipboard", err);
    }
  }, [sessionRef, termRef, terminalBackend]);

  const onSelectAll = useCallback(() => {
    const term = termRef.current;
    if (!term) return;
    term.selectAll();
    onHasSelectionChange?.(true);
  }, [onHasSelectionChange, termRef]);

  const onClear = useCallback(() => {
    const term = termRef.current;
    if (!term) return;
    term.clear();
  }, [termRef]);

  const onSelectWord = useCallback(() => {
    const term = termRef.current;
    if (!term) return;
    term.selectAll();
    onHasSelectionChange?.(true);
  }, [onHasSelectionChange, termRef]);

  return { onCopy, onPaste, onSelectAll, onClear, onSelectWord };
};

