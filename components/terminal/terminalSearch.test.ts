import assert from "node:assert/strict";
import test from "node:test";

import { notifyTerminalSearchTermChange } from "./TerminalSearchBar.tsx";
import {
  armSearchHighlightRevivalGuard,
  resetTerminalSearch,
  SEARCH_HIGHLIGHT_REVIVAL_GUARD_MS,
  shouldResetOnSharedSearchClose,
} from "./hooks/useTerminalSearch.ts";

test("clearing the search input notifies the terminal search handler", () => {
  const terms: string[] = [];
  const onSearch = (term: string) => {
    terms.push(term);
    return false;
  };

  let previousTerm = notifyTerminalSearchTermChange("needle", "", onSearch);
  previousTerm = notifyTerminalSearchTermChange("", previousTerm, onSearch);

  assert.equal(previousTerm, "");
  assert.deepEqual(terms, ["needle", ""]);
});

test("unchanged search input does not repeat a search", () => {
  const terms: string[] = [];

  const previousTerm = notifyTerminalSearchTermChange("needle", "needle", (term) => {
    terms.push(term);
    return false;
  });

  assert.equal(previousTerm, "needle");
  assert.deepEqual(terms, []);
});

test("resetting terminal search clears both match decorations and active selection", () => {
  let decorationsVisible = true;
  let activeSelectionVisible = true;
  const searchedTerms: string[] = [];
  const searchAddon = {
    findNext(term: string) {
      searchedTerms.push(term);
      if (term === "") {
        decorationsVisible = false;
        activeSelectionVisible = false;
      }
      return false;
    },
    clearDecorations() {
      decorationsVisible = false;
    },
  };
  const term = {
    rows: 24,
    refresh() {},
    clearSelection() {
      activeSelectionVisible = false;
    },
  };
  const searchTermRef = { current: "needle" };

  resetTerminalSearch(searchAddon, searchTermRef, term);

  assert.equal(searchTermRef.current, "");
  assert.deepEqual(searchedTerms, [""]);
  assert.equal(decorationsVisible, false);
  assert.equal(activeSelectionVisible, false);
});

test("resetting terminal search clears cache before empty find and refreshes", () => {
  // SearchAddon keeps a 200ms onWriteParsed/onResize timer that can revive
  // highlights after reset if cachedSearchTerm was not cleared first. Also,
  // findNext("", { decorations }) re-arms lastSearchOptions.decorations — so
  // the empty find must not pass decoration options. clearDecorations alone
  // leaves the active-match selection; clear it explicitly and refresh so
  // Windows/WebGL does not keep yellow match backgrounds.
  const calls: string[] = [];
  const findNextArgs: unknown[] = [];
  const searchAddon = {
    findNext(term: string, options?: unknown) {
      calls.push(`findNext:${term}`);
      findNextArgs.push(options);
      return false;
    },
    clearDecorations() {
      calls.push("clearDecorations");
    },
  };
  const term = {
    rows: 24,
    refresh(start: number, end: number) {
      calls.push(`refresh:${start}:${end}`);
    },
    clearSelection() {
      calls.push("clearSelection");
    },
  };
  const searchTermRef = { current: "needle" };

  resetTerminalSearch(searchAddon, searchTermRef, term);

  assert.equal(searchTermRef.current, "");
  assert.deepEqual(calls, [
    "clearDecorations",
    "clearSelection",
    "findNext:",
    "clearDecorations",
    "refresh:0:23",
  ]);
  assert.equal(findNextArgs[0], undefined);
});

test("search highlight revival guard re-clears decorations without touching selection", () => {
  assert.ok(SEARCH_HIGHLIGHT_REVIVAL_GUARD_MS > 200);

  const calls: string[] = [];
  let scheduled: { cb: () => void; ms: number } | null = null;
  const searchAddon = {
    clearDecorations() {
      calls.push("clearDecorations");
    },
  };
  const term = {
    rows: 10,
    refresh(start: number, end: number) {
      calls.push(`refresh:${start}:${end}`);
    },
    clearSelection() {
      calls.push("clearSelection");
    },
  };

  const guard = armSearchHighlightRevivalGuard({
    getSearchAddon: () => searchAddon,
    getTerm: () => term,
    setTimeoutFn: ((cb: () => void, ms: number) => {
      scheduled = { cb, ms };
      return 1 as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout,
    clearTimeoutFn: (() => {}) as typeof clearTimeout,
  });

  guard.arm();
  assert.ok(scheduled);
  assert.equal(scheduled?.ms, SEARCH_HIGHLIGHT_REVIVAL_GUARD_MS);
  assert.deepEqual(calls, []);

  // Simulate SearchAddon._updateMatches reviving decorations, then the guard.
  // Manual selections made during the guard window must survive.
  calls.push("revived");
  scheduled?.cb();

  assert.deepEqual(calls, [
    "revived",
    "clearDecorations",
    "refresh:0:9",
  ]);

  guard.dispose();
});

test("shared search close only resets terminals that have a local query", () => {
  assert.equal(shouldResetOnSharedSearchClose(""), false);
  assert.equal(shouldResetOnSharedSearchClose("needle"), true);
});
