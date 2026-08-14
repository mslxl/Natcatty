import type { SearchAddon } from "@xterm/addon-search";
import type { Terminal as XTerm } from "@xterm/xterm";
import { useCallback, useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { useStoredBoolean } from "../../../application/state/useStoredBoolean";
import { STORAGE_KEY_TERMINAL_SEARCH_OPEN } from "../../../infrastructure/config/storageKeys";

type SearchMatchCount = { current: number; total: number } | null;

type SearchAddonResetTarget = Pick<SearchAddon, "findNext" | "clearDecorations"> | null;
type TerminalSearchResetTarget = Pick<XTerm, "refresh" | "rows" | "clearSelection"> | null;
type TerminalSearchGuardTarget = Pick<XTerm, "refresh" | "rows" | "clearSelection"> | null;

const SEARCH_DECORATIONS = {
  matchBackground: "#FFFF0044",
  matchBorder: "#FFFF00",
  matchOverviewRuler: "#FFFF00",
  activeMatchBackground: "#FF880088",
  activeMatchBorder: "#FF8800",
  activeMatchColorOverviewRuler: "#FF8800",
} as const;

const SEARCH_OPTIONS = {
  regex: false,
  caseSensitive: false,
  wholeWord: false,
  decorations: SEARCH_DECORATIONS,
} as const;

/**
 * SearchAddon schedules `_updateMatches` 200ms after writes/resizes and does
 * not cancel that timer from `clearDecorations()`. A timeout that already
 * captured the prior term can revive yellow match decorations after reset —
 * re-clear once past that window (issue #2980).
 */
export const SEARCH_HIGHLIGHT_REVIVAL_GUARD_MS = 250;

/**
 * Delayed re-clear for addon decoration revival only. Do not clearSelection
 * here: reset already cleared the search selection, and a user may have made
 * a new manual selection during the guard window.
 */
export const clearTerminalSearchHighlights = (
  searchAddon: Pick<SearchAddon, "clearDecorations"> | null,
  term?: Pick<XTerm, "refresh" | "rows"> | null,
): void => {
  searchAddon?.clearDecorations();
  if (term && term.rows > 0) {
    term.refresh(0, term.rows - 1);
  }
};

export const resetTerminalSearch = (
  searchAddon: SearchAddonResetTarget,
  searchTermRef: { current: string },
  term?: TerminalSearchResetTarget,
): void => {
  searchTermRef.current = "";
  // Drop decorations and cachedSearchTerm first so any not-yet-running addon
  // `_updateMatches` timeout observes an empty cache and does not revive.
  searchAddon?.clearDecorations();
  // clearDecorations() leaves the active-match selection; clear it explicitly.
  term?.clearSelection();
  // Empty find clears selection via the addon path. Do NOT pass SEARCH_OPTIONS:
  // findNext always assigns lastSearchOptions, and decoration options would
  // keep that latch armed for later write/resize updates.
  try {
    searchAddon?.findNext("");
  } catch {
    // Addon not activated yet.
  }
  // findNext("") assigns cachedSearchTerm back to "". Clear again so the cache
  // is undefined rather than an empty string.
  searchAddon?.clearDecorations();
  // Disposing search decorations does not always repaint cells (observed on
  // Windows after clearing or closing search). Keyword highlighting already
  // forces a refresh after dispose; do the same here so yellow match
  // backgrounds cannot linger.
  if (term && term.rows > 0) {
    term.refresh(0, term.rows - 1);
  }
};

/**
 * Pointer listeners used to tell a user-created selection apart from the
 * addon's delayed findPrevious re-select. Keyboard selections in this 250ms
 * window are rare enough that treating them as addon revival is acceptable.
 */
export const subscribeTerminalUserSelection = (
  term: Pick<XTerm, "element"> | null | undefined,
  mark: () => void,
): (() => void) => {
  const el = term?.element;
  if (!el) return () => {};
  const onPointer = () => mark();
  el.addEventListener("mousedown", onPointer);
  el.addEventListener("touchstart", onPointer);
  return () => {
    el.removeEventListener("mousedown", onPointer);
    el.removeEventListener("touchstart", onPointer);
  };
};

export const armSearchHighlightRevivalGuard = ({
  getSearchAddon,
  getTerm,
  subscribeUserSelection,
  delayMs = SEARCH_HIGHLIGHT_REVIVAL_GUARD_MS,
  setTimeoutFn = setTimeout,
  clearTimeoutFn = clearTimeout,
}: {
  getSearchAddon: () => Pick<SearchAddon, "clearDecorations"> | null;
  getTerm: () => TerminalSearchGuardTarget;
  subscribeUserSelection?: (mark: () => void) => () => void;
  delayMs?: number;
  setTimeoutFn?: typeof setTimeout;
  clearTimeoutFn?: typeof clearTimeout;
}): { arm: () => void; dispose: () => void; markUserSelection: () => void } => {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let userTouchedSelection = false;
  let unsubscribeUserSelection: (() => void) | null = null;

  const markUserSelection = () => {
    userTouchedSelection = true;
  };

  const dispose = () => {
    if (timer !== null) {
      clearTimeoutFn(timer);
      timer = null;
    }
    unsubscribeUserSelection?.();
    unsubscribeUserSelection = null;
  };

  const arm = () => {
    dispose();
    userTouchedSelection = false;
    if (subscribeUserSelection) {
      unsubscribeUserSelection = subscribeUserSelection(markUserSelection);
    }
    timer = setTimeoutFn(() => {
      timer = null;
      unsubscribeUserSelection?.();
      unsubscribeUserSelection = null;
      const term = getTerm();
      clearTerminalSearchHighlights(getSearchAddon(), term);
      // Addon findPrevious re-selects the prior active match. Clear that
      // revived selection unless the user started a new one in this window.
      if (!userTouchedSelection) {
        term?.clearSelection();
      }
    }, delayMs);
  };

  return { arm, dispose, markUserSelection };
};

/** True when this terminal has a local query that shared search-close must clear. */
export const shouldResetOnSharedSearchClose = (localSearchTerm: string): boolean =>
  localSearchTerm !== "";

export const useTerminalSearch = ({
  searchAddonRef,
  termRef,
}: {
  searchAddonRef: RefObject<SearchAddon | null>;
  termRef: RefObject<XTerm | null>;
}) => {
  const [isSearchOpen, setIsSearchOpen] = useStoredBoolean(
    STORAGE_KEY_TERMINAL_SEARCH_OPEN,
    false,
  );
  const [searchMatchCount, setSearchMatchCount] = useState<SearchMatchCount>(null);
  // Bumped each time the search hotkey fires. The SearchBar watches this token
  // to refocus its input — without it, calling setIsSearchOpen(true) when
  // already open is a no-op (React bails on the unchanged boolean) and focus
  // never returns to the input. See issue #1789.
  const [searchFocusToken, setSearchFocusToken] = useState(0);
  const searchTermRef = useRef<string>("");
  const revivalGuardRef = useRef<ReturnType<typeof armSearchHighlightRevivalGuard> | null>(null);

  if (revivalGuardRef.current === null) {
    revivalGuardRef.current = armSearchHighlightRevivalGuard({
      getSearchAddon: () => searchAddonRef.current,
      getTerm: () => termRef.current,
      subscribeUserSelection: (mark) => subscribeTerminalUserSelection(termRef.current, mark),
    });
  }

  useEffect(() => () => {
    revivalGuardRef.current?.dispose();
  }, []);

  const runReset = useCallback(() => {
    resetTerminalSearch(searchAddonRef.current, searchTermRef, termRef.current);
    revivalGuardRef.current?.arm();
  }, [searchAddonRef, termRef]);

  // Search open state is shared via localStorage across terminal sessions. When
  // another session closes search, this session's bar unmounts without going
  // through handleCloseSearch — clear leftover decorations only when this
  // terminal actually searched (otherwise shared false would wipe unrelated
  // manual selections in other splits).
  useEffect(() => {
    if (isSearchOpen) return;
    setSearchMatchCount(null);
    if (!shouldResetOnSharedSearchClose(searchTermRef.current)) return;
    runReset();
  }, [isSearchOpen, runReset]);

  // Invoked by the searchTerminal hotkey (Cmd/Ctrl+F). Always opens the bar
  // and bumps the focus token: when closed, setIsSearchOpen(true) mounts the
  // SearchBar (whose isOpen effect focuses the input); when open, the token
  // bump makes the SearchBar re-run its focus effect and refocus. Doing both
  // unconditionally avoids reading `isSearchOpen` here — the xterm runtime
  // captures this callback once at creation (it only re-runs on host.id /
  // sessionId change), so a stale `isSearchOpen` closure would otherwise pick
  // the wrong branch.
  const requestSearchFocus = useCallback(() => {
    setIsSearchOpen(true);
    setSearchFocusToken((n) => n + 1);
  }, [setIsSearchOpen]);

  const handleToggleSearch = useCallback(() => {
    const next = !isSearchOpen;
    setIsSearchOpen(next);
    if (!next) {
      setSearchMatchCount(null);
      runReset();
    }
  }, [isSearchOpen, runReset, setIsSearchOpen]);

  const handleSearch = useCallback(
    (term: string): boolean => {
      const searchAddon = searchAddonRef.current;
      if (!searchAddon || !term) {
        runReset();
        setSearchMatchCount(null);
        return false;
      }

      searchTermRef.current = term;
      revivalGuardRef.current?.dispose();
      searchAddon.clearDecorations();

      const found = searchAddon.findNext(term, SEARCH_OPTIONS);

      if (found) {
        setSearchMatchCount({ current: 1, total: 1 });
      } else {
        setSearchMatchCount({ current: 0, total: 0 });
      }

      return found;
    },
    [runReset, searchAddonRef],
  );

  const handleFindNext = useCallback((): boolean => {
    const searchAddon = searchAddonRef.current;
    const term = searchTermRef.current;
    if (!searchAddon || !term) return false;
    return searchAddon.findNext(term, SEARCH_OPTIONS);
  }, [searchAddonRef]);

  const handleFindPrevious = useCallback((): boolean => {
    const searchAddon = searchAddonRef.current;
    const term = searchTermRef.current;
    if (!searchAddon || !term) return false;
    return searchAddon.findPrevious(term, SEARCH_OPTIONS);
  }, [searchAddonRef]);

  const handleCloseSearch = useCallback(() => {
    setIsSearchOpen(false);
    setSearchMatchCount(null);
    runReset();
    termRef.current?.focus();
  }, [runReset, setIsSearchOpen, termRef]);

  return {
    isSearchOpen,
    setIsSearchOpen,
    searchMatchCount,
    searchFocusToken,
    requestSearchFocus,
    handleToggleSearch,
    handleSearch,
    handleFindNext,
    handleFindPrevious,
    handleCloseSearch,
  };
};
