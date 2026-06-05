import assert from "node:assert/strict";
import test from "node:test";

import { ADD_PROVIDER_MENU_CLASS } from "./AddProviderDropdown.tsx";
import { getModelSuggestionsPresentation } from "./ModelSelector.tsx";

test("add provider menu opens toward the left edge of the button and stays width-bounded", () => {
  assert.match(ADD_PROVIDER_MENU_CLASS, /right-0/);
  assert.doesNotMatch(ADD_PROVIDER_MENU_CLASS, /left-0/);
  assert.match(ADD_PROVIDER_MENU_CLASS, /max-w-\[calc\(100vw-2rem\)\]/);
});

test("preset model suggestions stay visible while remote models are loading", () => {
  assert.deepEqual(
    getModelSuggestionsPresentation({
      suggestionsLength: 2,
      isLoading: true,
      error: null,
      hasFetched: false,
      hasPresetModels: true,
    }),
    { showSuggestions: true, emptyState: null, footerState: "loading" },
  );
});

test("preset model suggestions stay visible when remote model discovery fails", () => {
  assert.deepEqual(
    getModelSuggestionsPresentation({
      suggestionsLength: 2,
      isLoading: false,
      error: "Failed to fetch models",
      hasFetched: false,
      hasPresetModels: true,
    }),
    { showSuggestions: true, emptyState: null, footerState: "error" },
  );
});
