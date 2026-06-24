import type { Terminal as XTerm } from "@xterm/xterm";

/**
 * Strip `\x1b[2J` (ED — erase display) inside DEC Mode 2026 synchronized-output
 * blocks before data reaches xterm.js.
 *
 * Coding CLIs such as Codex and Claude Code wrap full-screen redraws in
 * `\x1b[?2026h` … `\x1b[?2026l`. Native terminals treat the enclosed clear as
 * part of the atomic update, but xterm.js resets viewportY on every `\x1b[2J`
 * when the user has scrolled up in the normal buffer, which yanks scroll
 * position and makes earlier output appear "eaten".
 *
 * Alternate-screen TUIs still need the clear to remove stale cells from shorter
 * redraw frames. Because PTY chunks can enter alternate screen before xterm has
 * applied the switch, this filter tracks alternate-screen transitions in-band
 * and only strips clears while the normal buffer is scrolled up.
 *
 * PTY/IPC chunks can split escape sequences at arbitrary byte boundaries, so a
 * trailing partial marker is held in `pending` until the next chunk completes it.
 *
 * @see https://github.com/xtermjs/xterm.js/issues/5801
 * @see https://github.com/openai/codex/issues/14277
 */

export type SyncBlockFilterState = {
  inSyncBlock: boolean;
  inAlternateScreen: boolean;
  /** Trailing bytes that may complete a marker in the next chunk. */
  pending: string;
};

export type SyncBlockClearFilterResult = {
  output: string;
  startedSyncBlock: boolean;
};

const SYNC_START = "\x1b[?2026h";
const SYNC_END = "\x1b[?2026l";
const CLEAR = "\x1b[2J";

const MARKERS = [SYNC_START, SYNC_END, CLEAR] as const;
const ALTERNATE_SCREEN_MODES = new Set([47, 1047, 1049]);

const maxMarkerPrefixLength = Math.max(...MARKERS.map((marker) => marker.length)) - 1;

const isCsiFinal = (ch: string): boolean => ch >= "@" && ch <= "~";

const isIncompleteEscapePrefix = (suffix: string): boolean => {
  if (!suffix.startsWith("\x1b")) {
    return false;
  }

  let index = 0;
  while (index < suffix.length) {
    if (suffix.startsWith("\x1b[", index)) {
      let hasFinal = false;
      for (let i = index + 2; i < suffix.length; i += 1) {
        if (isCsiFinal(suffix[i])) {
          index = i + 1;
          hasFinal = true;
          break;
        }
      }
      if (!hasFinal) {
        return true;
      }
      continue;
    }

    if (suffix[index] === "\x1b") {
      if (index === suffix.length - 1) {
        return true;
      }
      index += 1;
      continue;
    }

    index += 1;
  }

  return false;
};

const splitPendingMarkerSuffix = (input: string): { emit: string; pending: string } => {
  const markerMax = Math.min(input.length, maxMarkerPrefixLength);
  for (let length = markerMax; length > 0; length -= 1) {
    const suffix = input.slice(-length);
    if (MARKERS.some((marker) => marker.startsWith(suffix) && marker.length > suffix.length)) {
      return {
        emit: input.slice(0, -length),
        pending: suffix,
      };
    }
  }

  for (let length = input.length; length > 0; length -= 1) {
    const suffix = input.slice(-length);
    if (isIncompleteEscapePrefix(suffix)) {
      return {
        emit: input.slice(0, -length),
        pending: suffix,
      };
    }
  }

  return { emit: input, pending: "" };
};

const readPrivateModeCsi = (
  input: string,
  index: number,
): { raw: string; end: number; setsAlternate: boolean | null } | null => {
  if (!input.startsWith("\x1b[?", index)) {
    return null;
  }

  for (let end = index + 3; end < input.length; end += 1) {
    const final = input[end];
    if (!isCsiFinal(final)) {
      continue;
    }

    if (final !== "h" && final !== "l") {
      return {
        raw: input.slice(index, end + 1),
        end: end + 1,
        setsAlternate: null,
      };
    }

    const params = input.slice(index + 3, end).split(";");
    let setsAlternate: boolean | null = null;
    for (const param of params) {
      const mode = Number.parseInt(param, 10);
      if (ALTERNATE_SCREEN_MODES.has(mode)) {
        setsAlternate = final === "h";
      }
    }

    return {
      raw: input.slice(index, end + 1),
      end: end + 1,
      setsAlternate,
    };
  }

  return null;
};

const shouldStripClearInSyncBlock = (
  state: SyncBlockFilterState,
  term: XTerm,
): boolean => {
  if (state.inAlternateScreen) {
    return false;
  }
  return isTerminalViewportScrolledUp(term);
};

const scanSyncBlockClears = (
  input: string,
  state: SyncBlockFilterState,
  term: XTerm,
): SyncBlockClearFilterResult => {
  let result = "";
  let startedSyncBlock = false;
  let index = 0;

  while (index < input.length) {
    if (input.startsWith(SYNC_START, index)) {
      state.inSyncBlock = true;
      startedSyncBlock = true;
      result += SYNC_START;
      index += SYNC_START.length;
      continue;
    }

    if (input.startsWith(SYNC_END, index)) {
      state.inSyncBlock = false;
      result += SYNC_END;
      index += SYNC_END.length;
      continue;
    }

    const privateMode = readPrivateModeCsi(input, index);
    if (privateMode) {
      if (privateMode.setsAlternate === true) {
        state.inAlternateScreen = true;
      } else if (privateMode.setsAlternate === false) {
        state.inAlternateScreen = false;
      }
      result += privateMode.raw;
      index = privateMode.end;
      continue;
    }

    if (
      state.inSyncBlock
      && shouldStripClearInSyncBlock(state, term)
      && input.startsWith(CLEAR, index)
    ) {
      index += CLEAR.length;
      continue;
    }

    result += input[index];
    index += 1;
  }

  return { output: result, startedSyncBlock };
};

export const filterSyncBlockClearsWithMeta = (
  data: string,
  state: SyncBlockFilterState,
  term: XTerm,
): SyncBlockClearFilterResult => {
  const { emit, pending } = splitPendingMarkerSuffix(`${state.pending}${data}`);
  state.pending = pending;
  if (!emit) {
    return { output: "", startedSyncBlock: false };
  }

  return scanSyncBlockClears(emit, state, term);
};

export const filterSyncBlockClears = (
  data: string,
  state: SyncBlockFilterState,
  term: XTerm,
): string => filterSyncBlockClearsWithMeta(data, state, term).output;

export const createSyncBlockFilterState = (
  term?: Pick<XTerm, "buffer">,
): SyncBlockFilterState => ({
  inSyncBlock: false,
  inAlternateScreen: term?.buffer?.active?.type === "alternate",
  pending: "",
});

export const isTerminalViewportScrolledUp = (term: XTerm): boolean => {
  const buffer = term.buffer?.active;
  if (!buffer || buffer.type === "alternate") {
    return false;
  }

  return buffer.viewportY < buffer.baseY;
};

export const shouldStripSyncBlockClears = (term: XTerm): boolean =>
  isTerminalViewportScrolledUp(term);
