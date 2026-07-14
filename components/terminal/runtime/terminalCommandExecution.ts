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

  const trimmed = command.trim();
  const { prompt, alignedTyped } = getAlignedPrompt(term, command, true);
  if (!prompt.isAtPrompt) return false;
  if (alignedTyped?.trim() === trimmed) return true;

  if (reconcilePromptWithExternalCommand(prompt, command)) return true;

  // History recall on themed prompts: live userInput still includes cwd/git
  // chrome, but reconcile can attribute it back to the prompt (#2191).
  if (trimmed) {
    const reconciled = reconcilePromptWithTypedInput(prompt, trimmed);
    if (reconciled !== prompt && reconciled.userInput.trim() === trimmed) {
      return true;
    }
  }

  const liveCommand = prompt.userInput.trim();
  if (liveCommand.length === 0) {
    return !isNonPromptLine(`${prompt.promptText}${trimmed}`);
  }
  return liveCommand === trimmed;
};

/** Bare omz/p10k glyph alone — detector often leaves cwd/git chrome in userInput. */
const isBareThemedTerminator = (promptText: string): boolean => {
  const trimmed = promptText.trim();
  if (trimmed.length !== 1) return false;
  const code = trimmed.charCodeAt(0);
  return /[❯❮→➜➤⟩»›]/.test(trimmed) || (code >= 0xE000 && code <= 0xF8FF);
};

/**
 * Peel themed cwd/git chrome from userInput by trying space-aligned suffixes
 * and keeping the split that attributes the most text to the prompt.
 */
const peelThemedCommandFromPrompt = (
  prompt: PromptDetectionResult,
): string => {
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
 * Read the command currently shown on the prompt line, stripping themed
 * prompt chrome (➜  ~ / git status decorations) when needed.
 *
 * lastPromptText is only trusted when the remainder reconciles against the
 * original detector split (avoids partial-cache pollution and over-peeling
 * a clean remainder down to "-"). Complete Powerline prompts keep the
 * detector's multiword userInput (#2191).
 */
export const resolveLiveSubmittedCommand = (
  prompt: PromptDetectionResult,
  lastPromptText?: string,
): string => {
  if (!prompt.isAtPrompt) return "";

  // Clean standard prompts (user@host:~$ su -).
  const direct = getCommandToRecordOnEnter(prompt, null, "", true);
  if (direct) return direct;

  // Incomplete bare-glyph split (➜  + cwd/git in userInput): peel chrome.
  if (isBareThemedTerminator(prompt.promptText)) {
    const peeled = peelThemedCommandFromPrompt(prompt);
    if (peeled) return peeled;
  }

  // Cached full prompt: accept remainder only when it reconciles on the
  // original line (partial cache after git:(main) must not win).
  const cachedPrompt = lastPromptText ?? "";
  if (cachedPrompt) {
    const fullLine = `${prompt.promptText}${prompt.userInput}`;
    if (fullLine.startsWith(cachedPrompt)) {
      const remainder = fullLine.slice(cachedPrompt.length).trim();
      if (remainder && prompt.userInput.endsWith(remainder)) {
        const reconciled = reconcilePromptWithTypedInput(prompt, remainder);
        if (reconciled !== prompt && reconciled.userInput.trim() === remainder) {
          return remainder;
        }
      }
    }
  }

  // Complete themed / Powerline prompts already isolate the command in
  // userInput (including multiword sudo whoami). getCommandToRecordOnEnter
  // refuses multiword themed input, so accept the detector split here.
  if (!isBareThemedTerminator(prompt.promptText)) {
    const liveTrimmed = prompt.userInput.trim();
    if (liveTrimmed && prompt.promptText.trim().length > 0) {
      return liveTrimmed;
    }
  }

  return peelThemedCommandFromPrompt(prompt);
};

/**
 * True when a live "command" is really empty-prompt chrome (cwd / git status)
 * left in userInput by the detector — not a history-recalled command.
 */
const isEmptyPromptDecoration = (
  live: string,
  prompt: PromptDetectionResult,
): boolean => {
  const command = live.trim();
  if (!command) return true;
  if (command === "~" || command.startsWith("~/") || command.startsWith("/")) {
    return true;
  }
  if (/^git:\([^)]*\)/.test(command)) return true;
  if (/^[✗✔+*!]$/.test(command)) return true;

  // Bare-glyph themes often leave a single cwd token in userInput (" git ").
  // A real history recall has decoration + command (multiple tokens) before peel.
  if (isBareThemedTerminator(prompt.promptText)) {
    const rawTokens = prompt.userInput.trim().split(/\s+/).filter(Boolean);
    if (rawTokens.length <= 1) return true;
  }

  return false;
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
  if (!buffered) {
    // Empty Enter on a themed prompt must not treat cwd/git chrome as a command
    // (would pollute history and can false-arm su/sudo assist).
    if (!live || isEmptyPromptDecoration(live, prompt)) return "";
    return live;
  }
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
