/**
 * Strip `\x1b[2J` (ED — erase display) inside DEC Mode 2026 synchronized-output
 * blocks before data reaches xterm.js.
 *
 * Coding CLIs such as Codex and Claude Code wrap full-screen redraws in
 * `\x1b[?2026h` … `\x1b[?2026l`. Native terminals treat the enclosed clear as
 * part of the atomic update, but xterm.js resets viewportY on every `\x1b[2J`,
 * which yanks scroll position and makes earlier output appear "eaten".
 *
 * @see https://github.com/xtermjs/xterm.js/issues/5801
 * @see https://github.com/openai/codex/issues/14277
 */

export type SyncBlockFilterState = {
  inSyncBlock: boolean;
};

const SYNC_START = "\x1b[?2026h";
const SYNC_END = "\x1b[?2026l";
const CLEAR = "\x1b[2J";

export const filterSyncBlockClears = (
  data: string,
  state: SyncBlockFilterState,
): string => {
  if (!state.inSyncBlock && !data.includes(SYNC_START)) {
    return data;
  }

  let result = "";
  let index = 0;

  while (index < data.length) {
    if (data.startsWith(SYNC_START, index)) {
      state.inSyncBlock = true;
      result += SYNC_START;
      index += SYNC_START.length;
      continue;
    }

    if (data.startsWith(SYNC_END, index)) {
      state.inSyncBlock = false;
      result += SYNC_END;
      index += SYNC_END.length;
      continue;
    }

    if (state.inSyncBlock && data.startsWith(CLEAR, index)) {
      index += CLEAR.length;
      continue;
    }

    result += data[index];
    index += 1;
  }

  return result;
};

export const createSyncBlockFilterState = (): SyncBlockFilterState => ({
  inSyncBlock: false,
});
