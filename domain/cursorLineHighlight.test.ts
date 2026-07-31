import test from 'node:test';
import assert from 'node:assert/strict';

import {
  CURSOR_LINE_HIGHLIGHT_OPACITY,
  resolveCursorLineHighlightOverlay,
} from './cursorLineHighlight.ts';

test('resolveCursorLineHighlightOverlay creates a translucent selection overlay', () => {
  const color = resolveCursorLineHighlightOverlay({
    background: '#0d1117',
    foreground: '#c9d1d9',
    selection: '#264f78',
  });
  assert.equal(color, 'rgba(38, 79, 120, 0.18)');
});

test('resolveCursorLineHighlightOverlay falls back to foreground when selection is invalid', () => {
  const withSelection = resolveCursorLineHighlightOverlay({
    background: '#000000',
    foreground: '#ffffff',
    selection: '#808080',
  });
  const withoutSelection = resolveCursorLineHighlightOverlay({
    background: '#000000',
    foreground: '#ffffff',
    selection: 'not-a-color',
  });
  assert.equal(withSelection, 'rgba(128, 128, 128, 0.18)');
  assert.equal(withoutSelection, 'rgba(255, 255, 255, 0.18)');
});

test('resolveCursorLineHighlightOverlay expands short hex and strips alpha', () => {
  const short = resolveCursorLineHighlightOverlay({
    background: '#000',
    foreground: '#fff',
    selection: '#88888888',
  });
  assert.equal(short, 'rgba(136, 136, 136, 0.18)');
});

test('CURSOR_LINE_HIGHLIGHT_OPACITY stays a subtle fraction', () => {
  assert.ok(CURSOR_LINE_HIGHLIGHT_OPACITY > 0 && CURSOR_LINE_HIGHLIGHT_OPACITY < 0.5);
});
