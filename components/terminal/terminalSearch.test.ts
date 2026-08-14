import assert from "node:assert/strict";
import test from "node:test";

import { notifyTerminalSearchTermChange } from "./TerminalSearchBar.tsx";
import {
  armSearchHighlightRevivalGuard,
  resetTerminalSearch,
  SEARCH_HIGHLIGHT_REVIVAL_GUARD_MS,
  shouldResetOnSharedSearchClose,
  subscribeTerminalUserSelection,
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

test("search highlight revival guard re-clears decorations and the addon selection", () => {
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

  // SearchAddon._updateMatches can revive decorations and re-select the
  // previous active match. With no user selection in the window, clear both.
  calls.push("revived");
  scheduled?.cb();

  assert.deepEqual(calls, [
    "revived",
    "clearDecorations",
    "refresh:0:9",
    "clearSelection",
  ]);

  guard.dispose();
});

test("search highlight revival guard keeps a user selection made during the window", () => {
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
  guard.markUserSelection();
  scheduled?.cb();

  assert.deepEqual(calls, [
    "clearDecorations",
    "refresh:0:9",
  ]);

  guard.dispose();
});

test("subscribeTerminalUserSelection marks on pointer down and unsubscribes", () => {
  const marks: string[] = [];
  const listeners = new Map<string, EventListener>();
  const element = {
    addEventListener(type: string, listener: EventListener) {
      listeners.set(type, listener);
    },
    removeEventListener(type: string) {
      listeners.delete(type);
    },
  };
  const unsubscribe = subscribeTerminalUserSelection(
    { element: element as unknown as HTMLElement },
    () => marks.push("mark"),
  );

  listeners.get("mousedown")?.(new Event("mousedown"));
  listeners.get("touchstart")?.(new Event("touchstart"));
  assert.deepEqual(marks, ["mark", "mark"]);

  unsubscribe();
  assert.equal(listeners.size, 0);
});

test("revival guard subscribes to user selection only while armed", () => {
  let subscribed = 0;
  let unsubscribed = 0;
  let scheduled: { cb: () => void } | null = null;
  const guard = armSearchHighlightRevivalGuard({
    getSearchAddon: () => ({
      clearDecorations() {},
    }),
    getTerm: () => ({
      rows: 1,
      refresh() {},
      clearSelection() {},
    }),
    subscribeUserSelection: () => {
      subscribed += 1;
      return () => {
        unsubscribed += 1;
      };
    },
    setTimeoutFn: ((cb: () => void) => {
      scheduled = { cb };
      return 1 as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout,
    clearTimeoutFn: (() => {}) as typeof clearTimeout,
  });

  guard.arm();
  assert.equal(subscribed, 1);
  assert.equal(unsubscribed, 0);

  scheduled?.cb();
  assert.equal(unsubscribed, 1);

  guard.dispose();
});

test("shared search close only resets terminals that have a local query", () => {
  assert.equal(shouldResetOnSharedSearchClose(""), false);
  assert.equal(shouldResetOnSharedSearchClose("needle"), true);
});
