import test from "node:test";
import assert from "node:assert/strict";

import {
  getHostPickerTriggerRange,
  isPointerInsideLinkActionHoverZone,
  resolveHostPickerPopupPosition,
  shouldHandleHostPickerNavigationKey,
} from "./InlineMarkdownEditor.tsx";

test("host picker navigation keys are handled even before a query is typed", () => {
  assert.equal(shouldHandleHostPickerNavigationKey(true, "ArrowDown", 3), true);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "ArrowUp", 3), true);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "Enter", 3), true);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "Tab", 3), true);
});

test("host picker still lets ordinary trigger text continue through the editor", () => {
  assert.equal(shouldHandleHostPickerNavigationKey(true, "@", 3), false);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "/", 3), false);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "a", 3), false);
});

test("host picker does not consume submit keys when there are no hosts to choose", () => {
  assert.equal(shouldHandleHostPickerNavigationKey(true, "ArrowDown", 0), false);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "Enter", 0), false);
  assert.equal(shouldHandleHostPickerNavigationKey(true, "Escape", 0), true);
});

test("link action hover zone keeps the open button reachable but not sticky", () => {
  const action = { href: "https://example.com", label: "example", left: 100, top: 50 };

  assert.equal(isPointerInsideLinkActionHoverZone(action, 105, 55), true);
  assert.equal(isPointerInsideLinkActionHoverZone(action, 95, 45), true);
  assert.equal(isPointerInsideLinkActionHoverZone(action, 160, 55), false);
  assert.equal(isPointerInsideLinkActionHoverZone(null, 105, 55), false);
});

test("host picker trigger range only covers the typed trigger and query", () => {
  const text = "before\n\n@10.2.0.32";
  const range = getHostPickerTriggerRange(text);

  assert.deepEqual(range, {
    query: "10.2.0.32",
    startOffset: "before\n\n".length,
    trigger: "@",
  });
  assert.equal(text.slice(0, range?.startOffset), "before\n\n");
});

test("host picker trigger range supports slash without stealing ordinary text", () => {
  assert.deepEqual(getHostPickerTriggerRange("run /prod"), {
    query: "prod",
    startOffset: "run ".length,
    trigger: "/",
  });
  assert.equal(getHostPickerTriggerRange("email foo@bar"), null);
});

test("host picker opens above the caret when the bottom edge has no room", () => {
  const position = resolveHostPickerPopupPosition({
    anchorRect: { left: 520, top: 910, bottom: 930, width: 1, height: 20 },
    containerRect: { left: 400, top: 40, bottom: 960, width: 1200, height: 920 },
    availableHostCount: 8,
    viewportHeight: 960,
  });

  assert.equal(position.left, 120);
  assert.ok(position.top < 870);
});

test("host picker stays below the caret when there is enough room", () => {
  const position = resolveHostPickerPopupPosition({
    anchorRect: { left: 520, top: 160, bottom: 180, width: 1, height: 20 },
    containerRect: { left: 400, top: 40, bottom: 960, width: 1200, height: 920 },
    availableHostCount: 4,
    viewportHeight: 960,
  });

  assert.equal(position.left, 120);
  assert.equal(position.top, 150);
});
