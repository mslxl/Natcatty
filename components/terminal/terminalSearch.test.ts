import assert from "node:assert/strict";
import test from "node:test";

import { notifyTerminalSearchTermChange } from "./TerminalSearchBar.tsx";
import { resetTerminalSearch } from "./hooks/useTerminalSearch.ts";

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
  };
  const searchTermRef = { current: "needle" };

  resetTerminalSearch(searchAddon, searchTermRef);

  assert.equal(searchTermRef.current, "");
  assert.deepEqual(searchedTerms, [""]);
  assert.equal(decorationsVisible, false);
  assert.equal(activeSelectionVisible, false);
});
