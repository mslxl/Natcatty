import assert from 'node:assert/strict';
import test from 'node:test';

import {
  getShortcutModifierRequirements,
  isShortcutModifierHeld,
  shouldReleaseShortcutModifier,
  type ShortcutModifierEvent,
} from './useShortcutModifierHeld.ts';

const event = (overrides: Partial<ShortcutModifierEvent> = {}): ShortcutModifierEvent => ({
  metaKey: false,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  ...overrides,
});

test('shortcut modifier requirements follow the effective Mac or PC binding', () => {
  const mac = getShortcutModifierRequirements('⌘ + [1...9]', 'mac');
  assert.deepEqual(mac, { metaKey: true, ctrlKey: false, altKey: false, shiftKey: false });
  assert.equal(isShortcutModifierHeld(event({ metaKey: true }), mac), true);
  assert.equal(isShortcutModifierHeld(event({ ctrlKey: true }), mac), false);

  const pc = getShortcutModifierRequirements('Alt + Shift + [1...9]', 'pc');
  assert.deepEqual(pc, { metaKey: false, ctrlKey: false, altKey: true, shiftKey: true });
  assert.equal(isShortcutModifierHeld(event({ altKey: true, shiftKey: true }), pc), true);
  assert.equal(isShortcutModifierHeld(event({ altKey: true }), pc), false);
  assert.equal(isShortcutModifierHeld(event({ altKey: true, metaKey: true, shiftKey: true }), pc), false);
});

test('shortcut number preview is unavailable for disabled, non-range, or modifier-free bindings', () => {
  assert.equal(getShortcutModifierRequirements('Disabled', 'mac'), null);
  assert.equal(getShortcutModifierRequirements('⌘ + K', 'mac'), null);
  assert.equal(getShortcutModifierRequirements('[1...9]', 'mac'), null);
  assert.equal(getShortcutModifierRequirements('⌘ + [1...9]', 'disabled'), null);
});

test('shortcut modifier state clears when the required combination stops matching', () => {
  const requirements = getShortcutModifierRequirements('⌘ + [1...9]', 'mac');
  assert.equal(shouldReleaseShortcutModifier(event({ metaKey: true }), requirements), false);
  assert.equal(shouldReleaseShortcutModifier(event(), requirements), true);
});
