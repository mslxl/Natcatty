import test from "node:test";
import assert from "node:assert/strict";

import {
  computeAutocompletePopupPlacement,
  type PopupPlacementInput,
} from "./autocomplete/terminalAutocompleteLayout.ts";

const baseInput: PopupPlacementInput = {
  anchorTop: 100,
  anchorBottom: 120,
  anchorLeft: 200,
  viewportWidth: 1200,
  viewportHeight: 800,
  desiredHeight: 232,
  totalWidth: 400,
  maxHeight: 240,
  anchorGap: 8,
  viewportPadding: 8,
  expandUpwardHint: false,
};

test("renders downward with full height when there is ample room below", () => {
  const p = computeAutocompletePopupPlacement(baseInput);
  assert.equal(p.renderUpward, false);
  assert.equal(p.top, baseInput.anchorBottom + baseInput.anchorGap); // 128
  assert.equal(p.maxHeight, 240);
  // Whole popup stays within the viewport vertically.
  assert.ok(p.top + p.maxHeight <= baseInput.viewportHeight - baseInput.viewportPadding);
});

test("flips upward when the cursor sits at the very bottom of the viewport", () => {
  // Anchor line flush with the viewport bottom — classic "typing at the
  // bottom of the terminal" case from the bug report.
  const p = computeAutocompletePopupPlacement({
    ...baseInput,
    anchorTop: 780,
    anchorBottom: 800,
  });
  assert.equal(p.renderUpward, true);
  // Bottom edge of the *rendered content* sits just above the input line.
  // p.maxHeight is only the scroll budget; actual content is min(budget,desired).
  const contentHeight = Math.min(p.maxHeight, baseInput.desiredHeight);
  assert.ok(p.top + contentHeight <= 780 - baseInput.anchorGap + 0.001);
  assert.ok(p.top >= baseInput.viewportPadding);
});

test("never lets a downward popup overflow the viewport bottom", () => {
  // A little room below but not enough for the full desired height: the popup
  // must shrink + clamp so its bottom never crosses the viewport edge.
  const p = computeAutocompletePopupPlacement({
    ...baseInput,
    anchorTop: 600,
    anchorBottom: 620,
    desiredHeight: 232,
  });
  const bottom = p.top + p.maxHeight;
  assert.ok(
    bottom <= baseInput.viewportHeight - baseInput.viewportPadding + 0.001,
    `popup bottom ${bottom} should stay within viewport`,
  );
});

test("clamps height to the larger side when neither side fully fits", () => {
  // Short viewport: cursor in the middle, not enough above or below for 232px.
  const p = computeAutocompletePopupPlacement({
    ...baseInput,
    viewportHeight: 300,
    anchorTop: 140,
    anchorBottom: 160,
    desiredHeight: 232,
  });
  // More space below (300-160-16=124) than above (140-16=124 ~ tie) — either
  // way the rendered height must fit the chosen side and stay on-screen.
  const contentHeight = Math.min(p.maxHeight, 232);
  if (p.renderUpward) {
    assert.ok(p.top >= baseInput.viewportPadding);
    assert.ok(p.top + contentHeight <= 160 - baseInput.anchorGap + 0.001);
  } else {
    assert.ok(p.top + contentHeight <= 300 - baseInput.viewportPadding + 0.001);
  }
  assert.ok(p.maxHeight > 0);
});

test("honors the upward hint to break ties when neither side fits", () => {
  const shared = {
    ...baseInput,
    viewportHeight: 300,
    anchorTop: 150,
    anchorBottom: 170,
    desiredHeight: 232,
  };
  const withHint = computeAutocompletePopupPlacement({ ...shared, expandUpwardHint: true });
  // spaceAbove = 150-16 = 134; min(spaceBelow,80) -> spaceBelow=300-170-16=114, min=80
  // 134 >= 80 so the hint flips it upward.
  assert.equal(withHint.renderUpward, true);
});

test("clamps the left edge so the FULL assembly (with sub-dir panels) fits", () => {
  // Cursor near the right edge, popup widened by cascading sub-dir panels.
  // The whole assembly (1100px wide) must slide left to stay on-screen.
  const totalWidth = 1100; // main 400 + panels + detail
  const p = computeAutocompletePopupPlacement({
    ...baseInput,
    anchorLeft: 1150,
    totalWidth,
  });
  assert.ok(p.left >= baseInput.viewportPadding);
  assert.ok(
    p.left + totalWidth <= baseInput.viewportWidth - baseInput.viewportPadding + 0.001,
    `right edge ${p.left + totalWidth} should stay within viewport`,
  );
});

test("pins to the left padding when the assembly is wider than the viewport", () => {
  const p = computeAutocompletePopupPlacement({
    ...baseInput,
    anchorLeft: 600,
    viewportWidth: 500,
    totalWidth: 900,
  });
  // Can't fit no matter what — keep the primary list visible at the left edge.
  assert.equal(p.left, baseInput.viewportPadding);
});

test("does not shift left when the popup already fits at the cursor", () => {
  const p = computeAutocompletePopupPlacement({
    ...baseInput,
    anchorLeft: 200,
    totalWidth: 400,
  });
  assert.equal(p.left, 200);
});
