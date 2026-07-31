import test from 'node:test';
import assert from 'node:assert/strict';

import {
  CURSOR_LINE_HIGHLIGHT_BLEND,
  resolveCursorLineHighlightBackground,
} from './cursorLineHighlight.ts';

test('resolveCursorLineHighlightBackground mixes the selection with the theme background', () => {
  const color = resolveCursorLineHighlightBackground({
    background: '#0d1117',
    foreground: '#c9d1d9',
    selection: '#264f78',
  });
  assert.equal(color, '#1b334c');
});

test('resolveCursorLineHighlightBackground falls back to foreground when selection is invalid', () => {
  const withSelection = resolveCursorLineHighlightBackground({
    background: '#000000',
    foreground: '#ffffff',
    selection: '#808080',
  });
  const withoutSelection = resolveCursorLineHighlightBackground({
    background: '#000000',
    foreground: '#ffffff',
    selection: 'not-a-color',
  });
  assert.equal(withSelection, '#464646');
  assert.equal(withoutSelection, '#8c8c8c');
});

test('resolveCursorLineHighlightBackground expands short hex and strips alpha', () => {
  const short = resolveCursorLineHighlightBackground({
    background: '#000',
    foreground: '#fff',
    selection: '#88888888',
  });
  assert.equal(short, '#4b4b4b');
});

test('CURSOR_LINE_HIGHLIGHT_BLEND stays a visible fraction', () => {
  assert.ok(CURSOR_LINE_HIGHLIGHT_BLEND > 0 && CURSOR_LINE_HIGHLIGHT_BLEND < 1);
});
