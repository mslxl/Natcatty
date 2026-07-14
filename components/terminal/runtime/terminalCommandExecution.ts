import type { RefObject } from "react";
import type { Terminal as XTerm } from "@xterm/xterm";
import type { Host } from "../../../types";
import {
  markPromptLineBreakCommandPending,
  type PromptLineBreakState,
} from "./promptLineBreak";
import {
  getAlignedPrompt,
  isNonPromptLine,
  reconcilePromptWithExternalCommand,
} from "../autocomplete/promptDetector";

type TerminalCommandExecutionContext = {
  host: Pick<Host, "id" | "label">;
  sessionId: string;
  onCommandExecuted?: (
    command: string,
    hostId: string,
    hostLabel: string,
    sessionId: string,
  ) => void;
  onCommandSubmitted?: (
    command: string,
    hostId: string,
    hostLabel: string,
    sessionId: string,
  ) => void;
  commandBufferRef: RefObject<string>;
  promptLineBreakStateRef?: RefObject<PromptLineBreakState>;
};

export const shouldRecordShellHistory = (
  command: string,
  term?: XTerm | null,
): boolean => {
  if (!term) return true;

  const { prompt, alignedTyped } = getAlignedPrompt(term, command, true);
  if (!prompt.isAtPrompt) return false;
  if (alignedTyped?.trim() === command.trim()) return true;

  if (reconcilePromptWithExternalCommand(prompt, command)) return true;

  const liveCommand = prompt.userInput.trim();
  if (liveCommand.length === 0) {
    return !isNonPromptLine(`${prompt.promptText}${command.trim()}`);
  }
  return liveCommand === command.trim();
};

/**
 * Resolve the command that Enter is submitting.
 *
 * The keystroke buffer alone is incomplete for shell history recall (↑/↓):
 * those keys redraw the line remotely and never append the recalled text to
 * commandBuffer. Fall back to the live prompt line so su/sudo arming, shell
 * history, and command hooks still see the real command (#2191).
 */
export const resolveSubmittedShellCommand = (
  commandBuffer: string,
  term?: XTerm | null,
): string => {
  const buffered = commandBuffer.trim();
  if (!term) return buffered;

  const { prompt, alignedTyped } = getAlignedPrompt(term, commandBuffer, true);
  const aligned = alignedTyped?.trim() ?? "";
  if (aligned) return aligned;

  if (prompt.isAtPrompt) {
    const live = prompt.userInput.trim();
    // Empty buffer + live line: classic ↑ history recall before Enter.
    if (live && !buffered) return live;
  }

  return buffered;
};

export const recordTerminalCommandExecution = (
  command: string,
  ctx: TerminalCommandExecutionContext,
  term?: XTerm | null,
): string | null => {
  const cmd = resolveSubmittedShellCommand(command, term);
  if (cmd) {
    ctx.onCommandSubmitted?.(cmd, ctx.host.id, ctx.host.label, ctx.sessionId);
  }
  if (cmd && shouldRecordShellHistory(cmd, term)) {
    ctx.onCommandExecuted?.(cmd, ctx.host.id, ctx.host.label, ctx.sessionId);
    ctx.commandBufferRef.current = "";
    markPromptLineBreakCommandPending(ctx.promptLineBreakStateRef, term, cmd);
    return cmd;
  }
  ctx.commandBufferRef.current = "";
  markPromptLineBreakCommandPending(ctx.promptLineBreakStateRef, term, cmd || command);
  return null;
};
