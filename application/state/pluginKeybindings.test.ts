import assert from 'node:assert/strict';
import test from 'node:test';

import {
  normalizePluginKeyboardEvent,
  normalizePluginShortcut,
  resolvePluginShortcutPlatform,
} from './pluginKeybindings';

test('plugin keybindings canonicalize aliases, named keys, and modifier order', () => {
  assert.equal(normalizePluginShortcut('Control + Space', 'linux'), 'ctrl+space');
  assert.equal(normalizePluginShortcut('Esc', 'linux'), 'escape');
  assert.equal(normalizePluginShortcut('Ctrl+Up', 'linux'), 'ctrl+arrowup');
  assert.equal(normalizePluginShortcut('Shift+Ctrl+P', 'linux'), 'ctrl+shift+p');
  assert.equal(normalizePluginShortcut('Mod+P', 'mac'), 'meta+p');
  assert.equal(normalizePluginShortcut('Mod+P', 'windows'), 'ctrl+p');
});

test('browser keyboard events use the same canonical shortcut representation', () => {
  assert.equal(normalizePluginKeyboardEvent({
    key: ' ', metaKey: false, ctrlKey: true, altKey: false, shiftKey: false,
  }), 'ctrl+space');
  assert.equal(normalizePluginKeyboardEvent({
    key: 'Esc', metaKey: false, ctrlKey: false, altKey: false, shiftKey: false,
  }), 'escape');
  assert.equal(normalizePluginKeyboardEvent({
    key: 'ArrowUp', metaKey: false, ctrlKey: true, altKey: false, shiftKey: false,
  }), 'ctrl+arrowup');
  assert.equal(normalizePluginKeyboardEvent({
    key: '!', code: 'Digit1', metaKey: false, ctrlKey: false, altKey: false, shiftKey: true,
  }), 'shift+1');
});

test('plugin keybindings reject ambiguous declarations and resolve host platforms', () => {
  assert.equal(normalizePluginShortcut('Ctrl+Ctrl+P', 'linux'), null);
  assert.equal(normalizePluginShortcut('Ctrl+Meta+P', 'mac'), null);
  assert.equal(normalizePluginShortcut('P+Shift', 'linux'), null);
  assert.equal(resolvePluginShortcutPlatform('MacIntel'), 'mac');
  assert.equal(resolvePluginShortcutPlatform('Win32'), 'windows');
  assert.equal(resolvePluginShortcutPlatform('Linux x86_64'), 'linux');
});
