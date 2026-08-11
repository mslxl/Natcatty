import assert from 'node:assert/strict';
import test from 'node:test';

import {
  isShortcutModifierHeld,
  shouldReleaseShortcutModifier,
  type ShortcutModifierEvent,
} from './useShortcutModifierHeld.ts';

const event = (overrides: Partial<ShortcutModifierEvent> = {}): ShortcutModifierEvent => ({
  key: 'a',
  metaKey: false,
  ctrlKey: false,
  ...overrides,
});

test('shortcut modifier state follows the configured Mac or PC scheme', () => {
  assert.equal(isShortcutModifierHeld(event({ key: 'Meta' }), 'mac'), true);
  assert.equal(isShortcutModifierHeld(event({ metaKey: true }), 'mac'), true);
  assert.equal(isShortcutModifierHeld(event({ ctrlKey: true }), 'mac'), false);

  assert.equal(isShortcutModifierHeld(event({ key: 'Control' }), 'pc'), true);
  assert.equal(isShortcutModifierHeld(event({ ctrlKey: true }), 'pc'), true);
  assert.equal(isShortcutModifierHeld(event({ metaKey: true }), 'pc'), false);
  assert.equal(isShortcutModifierHeld(event({ metaKey: true, ctrlKey: true }), 'disabled'), false);
});

test('shortcut modifier state clears on modifier release or a missed modifier keyup', () => {
  assert.equal(shouldReleaseShortcutModifier(event({ key: 'Meta' }), 'mac'), true);
  assert.equal(shouldReleaseShortcutModifier(event({ key: 'a', metaKey: true }), 'mac'), false);
  assert.equal(shouldReleaseShortcutModifier(event({ key: 'a' }), 'mac'), true);

  assert.equal(shouldReleaseShortcutModifier(event({ key: 'Control' }), 'pc'), true);
  assert.equal(shouldReleaseShortcutModifier(event({ key: 'a', ctrlKey: true }), 'pc'), false);
  assert.equal(shouldReleaseShortcutModifier(event({ key: 'a' }), 'pc'), true);
});
