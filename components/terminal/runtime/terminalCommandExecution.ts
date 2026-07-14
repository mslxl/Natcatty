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
  reconcilePromptWithTypedInput,
  type PromptDetectionResult,
} from "../autocomplete/promptDetector";
import { getCommandToRecordOnEnter } from "../autocomplete/terminalAutocompletePrompt";

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
 * Read the command currently shown on the prompt line, stripping themed
 * prompt chrome (➜  ~ / git status decorations) when needed.
 *
 * Uses lastPromptText when it still matches; otherwise peels decoration via
 * the same reconcile rules as autocomplete so a stale cache after `cd` does
 * not leave su/sudo unarmed (#2191).
 */
export const resolveLiveSubmittedCommand = (
  prompt: PromptDetectionResult,
  lastPromptText?: string,
): string => {
  if (!prompt.isAtPrompt) return "";

  const cachedPrompt = lastPromptText ?? "";
  if (cachedPrompt) {
    const fullLine = `${prompt.promptText}${prompt.userInput}`;
    if (fullLine.startsWith(cachedPrompt)) {
      const fromCachedPrompt = fullLine.slice(cachedPrompt.length).trim();
      if (fromCachedPrompt) return fromCachedPrompt;
    }
  }

  // Clean standard prompts (user@host:~$ su -).
  const direct = getCommandToRecordOnEnter(prompt, null, "", true);
  if (direct) return direct;

  // Themed prompts: try space-aligned suffixes and keep the split that
  // attributes the most text to the prompt (fullest decoration strip).
  const live = prompt.userInput;
  let best: { command: string; promptLength: number } | null = null;
  for (let start = 0; start < live.length; start += 1) {
    if (start > 0 && live[start - 1] !== " ") continue;
    const candidate = live.slice(start);
    if (!candidate.trim()) continue;
    const reconciled = reconcilePromptWithTypedInput(prompt, candidate);
    if (reconciled === prompt || reconciled.userInput !== candidate) continue;
    const command = candidate.trim();
    if (!command) continue;
    if (!best || reconciled.promptText.length > best.promptLength) {
      best = { command, promptLength: reconciled.promptText.length };
    }
  }
  return best?.command ?? "";
};

/**
 * Resolve the command that Enter is submitting.
 *
 * The keystroke buffer alone is incomplete for shell history recall (↑/↓ /
 * Ctrl+R): those keys redraw the line remotely and never rewrite
 * commandBuffer. Prefer an aligned buffer when reliable; otherwise prefer
 * the live line when it disagrees with a stale prefix (#2191).
 */
export const resolveSubmittedShellCommand = (
  commandBuffer: string,
  term?: XTerm | null,
  lastPromptText?: string,
): string => {
  const buffered = commandBuffer.trim();
  if (!term) return buffered;

  const { prompt, alignedTyped } = getAlignedPrompt(term, commandBuffer, true);
  const aligned = alignedTyped?.trim() ?? "";
  if (aligned) return aligned;
  if (!prompt.isAtPrompt) return buffered;

  const live = resolveLiveSubmittedCommand(prompt, lastPromptText);
  if (!buffered) return live;
  if (!live || live === buffered) return buffered || live;

  // Direct send / incomplete echo: keystroke buffer is the real command even
  // when the themed line still only shows decoration (➜  netcatty  + "ls").
  if (reconcilePromptWithExternalCommand(prompt, buffered)) {
    return buffered;
  }

  // History / reverse-search replaced a typed prefix (buffer "s", live "su -").
  if (live.startsWith(buffered) && live.length > buffered.length) {
    return live;
  }

  // Echo lag: user typed more than the line has echoed yet — keep buffer.
  if (buffered.startsWith(live) && buffered.length > live.length) {
    return buffered;
  }

  // Completely different commands: trust the live line (history replaced it).
  return live;
};

export const recordTerminalCommandExecution = (
  command: string,
  ctx: TerminalCommandExecutionContext,
  term?: XTerm | null,
): string | null => {
  const lastPromptText = ctx.promptLineBreakStateRef?.current?.lastPromptText;
  const cmd = resolveSubmittedShellCommand(command, term, lastPromptText);
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
