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

/** Bare omz/p10k glyph alone — detector often leaves cwd/git chrome in userInput. */
const isBareThemedTerminator = (promptText: string): boolean => {
  const trimmed = promptText.trim();
  if (trimmed.length !== 1) return false;
  const code = trimmed.charCodeAt(0);
  return /[❯❮→➜➤⟩»›]/.test(trimmed) || (code >= 0xE000 && code <= 0xF8FF);
};

/**
 * Read the full logical input after the prompt, including wrapped continuation
 * rows and text past the cursor (Enter submits the whole line, not the prefix).
 */
const readFullLineAfterPrompt = (
  term: XTerm,
  promptText: string,
): string | null => {
  if (!promptText) return null;
  try {
    const buffer = term.buffer.active;
    const cursorY = buffer.cursorY + buffer.baseY;
    let promptRow = cursorY;
    let line = buffer.getLine(promptRow);
    if (!line) return null;

    // Walk up through wrapped continuation rows to the prompt row.
    while (line.isWrapped && promptRow > 0) {
      promptRow -= 1;
      const prev = buffer.getLine(promptRow);
      if (!prev) return null;
      line = prev;
    }

    let combined = "";
    for (let row = promptRow; ; row += 1) {
      const rowLine = buffer.getLine(row);
      if (!rowLine) break;
      combined += rowLine.translateToString(false);
      const next = buffer.getLine(row + 1);
      if (!next?.isWrapped) break;
    }

    if (!combined.startsWith(promptText)) return null;
    return combined.slice(promptText.length).replace(/\s+$/g, "");
  } catch {
    return null;
  }
};

/**
 * detectPrompt truncates userInput at the cursor. Enter submits the whole line,
 * so expand past the cursor when the tail still looks like the same command
 * (not zsh autosuggest / right-prompt chrome).
 */
const expandPromptUserInputToFullLine = (
  term: XTerm,
  prompt: PromptDetectionResult,
): PromptDetectionResult => {
  if (!prompt.isAtPrompt || !prompt.promptText) return prompt;
  const fullInput = readFullLineAfterPrompt(term, prompt.promptText);
  if (fullInput === null || fullInput === prompt.userInput) return prompt;
  if (
    fullInput.length <= prompt.userInput.length
    || !fullInput.startsWith(prompt.userInput)
  ) {
    return prompt;
  }
  const tail = fullInput.slice(prompt.userInput.length);
  // Continue same token (sudo|whoami mid-word) or next argv ("su" + " -").
  // Skip likely autosuggest/RPROMPT blobs that start a new unrelated word
  // after the cursor without the user having typed into them.
  const okContinuation =
    tail.startsWith(" ")
    || tail.startsWith("-")
    || (
      !prompt.userInput.endsWith(" ")
      && /^[\w@./:-]+/.test(tail)
      && !/\s{2,}/.test(tail)
    );
  if (!okContinuation) return prompt;
  return {
    ...prompt,
    userInput: fullInput,
    cursorOffset: fullInput.length,
  };
};

/** Status / cwd chrome that must not be recorded as a submitted command. */
const isDecorationOnlyCommand = (command: string): boolean => {
  const t = command.trim();
  if (!t) return true;
  if (t === "~" || t.startsWith("~/")) return true;
  if (/^[✗✔+*!]$/.test(t)) return true;
  if (/^git:\([^)]*\)/.test(t)) return true;
  // "git:(main) ✗" leftovers after a partial cache strip
  if (/git:\([^)]*\)/.test(t) || /[✗✔]/.test(t)) {
    const stripped = t
      .replace(/git:\([^)]*\)/g, " ")
      .replace(/[✗✔+*!]/g, " ")
      .replace(/\s+/g, " ")
      .trim();
    if (!stripped) return true;
    if (/^(?:su|sudo|doas)(?:\s|$)/i.test(stripped)) return false;
    if (!/\s/.test(stripped) && !/^(?:su|sudo|doas)$/i.test(stripped)) return true;
  }
  return false;
};

const hasThemedPromptMarker = (promptText: string): boolean => {
  if (isBareThemedTerminator(promptText)) return true;
  if (/[❯❮→➜➤⟩»›]/.test(promptText)) return true;
  for (const ch of promptText) {
    const code = ch.charCodeAt(0);
    if (code >= 0xE000 && code <= 0xF8FF) return true;
  }
  return false;
};

/**
 * When the prompt has no trailing space (`user@host:~$su -`), the detector
 * may not find a boundary. Fall back to the last known prompt prefix.
 */
const resolveFromCachedPromptPrefix = (
  term: XTerm,
  lastPromptText: string | undefined,
): string => {
  const cached = lastPromptText ?? "";
  if (!cached) return "";
  const fullInput = readFullLineAfterPrompt(term, cached)?.trim() ?? "";
  // Reject partial-cache leftovers like "git:(main) ✗" (#2191 review).
  if (!fullInput || isDecorationOnlyCommand(fullInput)) return "";
  return fullInput;
};

export const shouldRecordShellHistory = (
  command: string,
  term?: XTerm | null,
): boolean => {
  if (!term) return true;

  const trimmed = command.trim();
  const alignedResult = getAlignedPrompt(term, command, true);
  const prompt = expandPromptUserInputToFullLine(term, alignedResult.prompt);
  if (!prompt.isAtPrompt) return false;
  if (alignedResult.alignedTyped?.trim() === trimmed) return true;

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

  // Cached full prompt first: handles space-containing dirs ("➜  My Project ")
  // before peel can mis-split on the path (#2191 review).
  const cachedPrompt = lastPromptText ?? "";
  if (cachedPrompt) {
    const fullLine = `${prompt.promptText}${prompt.userInput}`;
    if (fullLine.startsWith(cachedPrompt)) {
      const remainder = fullLine.slice(cachedPrompt.length).trim();
      if (remainder && !isDecorationOnlyCommand(remainder)) {
        if (prompt.userInput.endsWith(remainder)) {
          const reconciled = reconcilePromptWithTypedInput(prompt, remainder);
          if (reconciled !== prompt && reconciled.userInput.trim() === remainder) {
            return remainder;
          }
        }
        // Exact cache prefix on the rendered line (no-space / multi-word dirs).
        return remainder;
      }
    }
  }

  // Incomplete bare-glyph split (➜  + cwd/git in userInput): peel chrome.
  if (isBareThemedTerminator(prompt.promptText)) {
    const peeled = peelThemedCommandFromPrompt(prompt);
    if (peeled) return peeled;
  }

  // Themed prompts (including prefixed terminators like "⚡ ➜ "): peel cwd/path
  // chrome before accepting userInput (⚡ ➜  ~ su - → su -).
  if (hasThemedPromptMarker(prompt.promptText)) {
    const peeled = peelThemedCommandFromPrompt(prompt);
    if (peeled) return peeled;
  }

  // Complete Powerline / multi-glyph prompts may already isolate multiword
  // commands (sudo whoami) when peel has nothing left to strip.
  if (!isBareThemedTerminator(prompt.promptText)) {
    const liveTrimmed = prompt.userInput.trim();
    if (
      liveTrimmed
      && prompt.promptText.trim().length > 0
      && !isDecorationOnlyCommand(liveTrimmed)
    ) {
      const rawTokens = liveTrimmed.split(/\s+/).filter(Boolean);
      if (
        rawTokens.length <= 1
        && hasThemedPromptMarker(prompt.promptText)
        && !/^(?:su|sudo|doas)$/i.test(liveTrimmed)
      ) {
        return "";
      }
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
  if (isDecorationOnlyCommand(command)) return true;

  // Bare glyph or multi-glyph themed prompts can leave a single cwd token.
  if (!hasThemedPromptMarker(prompt.promptText)) return false;

  const rawTokens = prompt.userInput.trim().split(/\s+/).filter(Boolean);
  if (rawTokens.length <= 1) {
    // One-word history of su/sudo/doas must still arm password assist (❯ su).
    if (/^(?:su|sudo|doas)$/i.test(command)) return false;
    return true;
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

  const alignedResult = getAlignedPrompt(term, commandBuffer, true);

  // Expand past the cursor / across wraps so Enter sees the full recalled command.
  const prompt = expandPromptUserInputToFullLine(term, alignedResult.prompt);
  const liveFromPrompt = prompt.isAtPrompt
    ? resolveLiveSubmittedCommand(prompt, lastPromptText)
    : "";

  const aligned = alignedResult.alignedTyped?.trim() ?? "";
  // Aligned buffer can match a stale mid-line prefix after history recall
  // (typed "s", recalled "su -", cursor after "s"), or only a suffix when
  // history prepended text (typed "whoami", recalled "sudo whoami").
  if (aligned) {
    if (
      liveFromPrompt
      && liveFromPrompt.length > aligned.length
      && (
        liveFromPrompt.startsWith(aligned)
        || liveFromPrompt.endsWith(aligned)
        || liveFromPrompt.endsWith(` ${aligned}`)
      )
    ) {
      return liveFromPrompt;
    }
    return aligned;
  }

  if (!prompt.isAtPrompt) {
    // No-space prompts (`user@host:~$su -`) often fail boundary detection;
    // recover via the last fully-detected prompt prefix (#2191 review).
    if (!buffered) {
      return resolveFromCachedPromptPrefix(term, lastPromptText);
    }
    return buffered;
  }

  const live = liveFromPrompt;
  if (!buffered) {
    // Empty Enter on a themed prompt must not treat cwd/git chrome as a command
    // (would pollute history and can false-arm su/sudo assist).
    if (!live || isEmptyPromptDecoration(live, prompt)) {
      // Last chance: no-space / partial detect with a known prompt prefix.
      return resolveFromCachedPromptPrefix(term, lastPromptText);
    }
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

  // Echo lag: live is a same-command prefix of the buffer (e.g. "su" → "su -").
  // Do not treat accidental word prefixes as lag ("su" vs "sudo whoami").
  if (buffered.startsWith(live) && buffered.length > live.length) {
    const next = buffered[live.length] ?? "";
    if (next === " " || next === "" || live.length === 0) {
      return buffered;
    }
    // History replaced a longer typed command with a shorter different one.
    return live;
  }

  // Live ends with the typed buffer: either path chrome + typed command
  // ("Project su -" + "su -") or history that grew leftward ("sudo whoami"
  // after typing "whoami"). Prefer live for sudo/su wrappers; else buffer.
  if (live.endsWith(buffered) || live.endsWith(` ${buffered}`)) {
    if (
      live !== buffered
      && /^(?:sudo|su|doas|command|builtin)\s/i.test(live)
    ) {
      return live;
    }
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
