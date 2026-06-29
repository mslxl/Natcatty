import type { SearchAddon } from "@xterm/addon-search";
import type { Terminal as XTerm } from "@xterm/xterm";
import { useCallback, useRef, useState } from "react";
import type { RefObject } from "react";
import { useStoredBoolean } from "../../../application/state/useStoredBoolean";
import { STORAGE_KEY_TERMINAL_SEARCH_OPEN } from "../../../infrastructure/config/storageKeys";

type SearchMatchCount = { current: number; total: number } | null;

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
  // Bumped each time the search hotkey fires while the bar is already open.
  // The SearchBar watches this token to refocus its input — without it, calling
  // setIsSearchOpen(true) when already open is a no-op (React bails on the
  // unchanged boolean) and focus never returns to the input. See issue #1789.
  const [searchFocusToken, setSearchFocusToken] = useState(0);
  const searchTermRef = useRef<string>("");

  // Invoked by the searchTerminal hotkey (Cmd/Ctrl+F). Opens the bar if closed;
  // refocuses the input if already open (focus may have moved elsewhere).
  const requestSearchFocus = useCallback(() => {
    if (isSearchOpen) {
      setSearchFocusToken((n) => n + 1);
    } else {
      setIsSearchOpen(true);
    }
  }, [isSearchOpen, setIsSearchOpen]);

  const clearSearchDecorations = useCallback(() => {
    searchAddonRef.current?.clearDecorations();
  }, [searchAddonRef]);

  const handleToggleSearch = useCallback(() => {
    const next = !isSearchOpen;
    setIsSearchOpen(next);
    if (!next) {
      setSearchMatchCount(null);
      clearSearchDecorations();
    }
  }, [clearSearchDecorations, isSearchOpen, setIsSearchOpen]);

  const handleSearch = useCallback(
    (term: string): boolean => {
      const searchAddon = searchAddonRef.current;
      if (!searchAddon || !term) {
        setSearchMatchCount(null);
        return false;
      }

      searchTermRef.current = term;
      searchAddon.clearDecorations();

      const found = searchAddon.findNext(term, SEARCH_OPTIONS);

      if (found) {
        setSearchMatchCount({ current: 1, total: 1 });
      } else {
        setSearchMatchCount({ current: 0, total: 0 });
      }

      return found;
    },
    [searchAddonRef],
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
    clearSearchDecorations();
    termRef.current?.focus();
  }, [clearSearchDecorations, setIsSearchOpen, termRef]);

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
