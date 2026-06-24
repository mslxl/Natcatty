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
const ALT_SCREEN_ENTER = ["\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h"] as const;
const ALT_SCREEN_LEAVE = ["\x1b[?1049l", "\x1b[?1047l", "\x1b[?47l"] as const;

const MARKERS = [
  SYNC_START,
  SYNC_END,
  CLEAR,
  ...ALT_SCREEN_ENTER,
  ...ALT_SCREEN_LEAVE,
] as const;

const maxMarkerPrefixLength = Math.max(...MARKERS.map((marker) => marker.length)) - 1;

const splitPendingMarkerSuffix = (input: string): { emit: string; pending: string } => {
  const maxLength = Math.min(input.length, maxMarkerPrefixLength);
  for (let length = maxLength; length > 0; length -= 1) {
    const suffix = input.slice(-length);
    if (MARKERS.some((marker) => marker.startsWith(suffix) && marker.length > suffix.length)) {
      return {
        emit: input.slice(0, -length),
        pending: suffix,
      };
    }
  }
  return { emit: input, pending: "" };
};

const matchMarker = (input: string, index: number, markers: readonly string[]): string | null => {
  for (const marker of markers) {
    if (input.startsWith(marker, index)) {
      return marker;
    }
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
    const alternateEnter = matchMarker(input, index, ALT_SCREEN_ENTER);
    if (alternateEnter) {
      state.inAlternateScreen = true;
      result += alternateEnter;
      index += alternateEnter.length;
      continue;
    }

    const alternateLeave = matchMarker(input, index, ALT_SCREEN_LEAVE);
    if (alternateLeave) {
      state.inAlternateScreen = false;
      result += alternateLeave;
      index += alternateLeave.length;
      continue;
    }

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
