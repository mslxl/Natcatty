import { describe, it } from "node:test";
import assert from "node:assert/strict";

import {
  COPILOT_LIGHT_UI_THEMES,
  LIGHT_UI_THEMES,
  getUiThemeById,
} from "./uiThemes";

describe("Copilot light UI themes", () => {
  it("adds every Copilot preset to the light theme list", () => {
    const expectedIds = [
      "github",
      "fox",
      "ic-orange-ppl",
      "monochrome",
      "noctis-azureus",
      "notionish",
      "polychrome",
      "selene-selenized",
      "tokyo-night",
      "xotopia",
    ];

    assert.deepEqual(COPILOT_LIGHT_UI_THEMES.map((theme) => theme.id), expectedIds);
    for (const id of expectedIds) {
      assert.equal(getUiThemeById("light", id).id, id);
    }
  });

  it("keeps theme accents distinct from the original default blue", () => {
    const originalDefaultBlue = "221.2 83.2% 53.3%";
    const accents = new Set(COPILOT_LIGHT_UI_THEMES.map((theme) => theme.tokens.accent));

    assert.equal(COPILOT_LIGHT_UI_THEMES.length, 10);
    assert.equal(LIGHT_UI_THEMES.filter((theme) => theme.collection === "copilot").length, 10);
    assert.ok(accents.size > 6);
    assert.ok(!accents.has(originalDefaultBlue));
  });
});
