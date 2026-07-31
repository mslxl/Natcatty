import test from 'node:test';
import assert from 'node:assert/strict';

import { CursorLineHighlighter } from './cursorLineHighlight.ts';

type Handler = () => void;

const createFakeTerm = (cols = 80) => {
  let cursorY = 0;
  let baseY = 0;
  const cursorMoveHandlers: Handler[] = [];
  const resizeHandlers: Handler[] = [];
  const decorations: Array<{ options: Record<string, unknown>; disposed: boolean }> = [];
  const markers: Array<{ line: number; disposed: boolean }> = [];

  const term = {
    cols,
    buffer: {
      active: {
        get baseY() {
          return baseY;
        },
        get cursorY() {
          return cursorY;
        },
      },
    },
    onCursorMove(handler: Handler) {
      cursorMoveHandlers.push(handler);
      return { dispose() {} };
    },
    onResize(handler: Handler) {
      resizeHandlers.push(handler);
      return { dispose() {} };
    },
    registerMarker(offset: number) {
      const marker = {
        line: baseY + cursorY + offset,
        disposed: false,
        get isDisposed() {
          return this.disposed;
        },
        dispose() {
          this.disposed = true;
        },
      };
      markers.push(marker);
      return marker;
    },
    registerDecoration(options: Record<string, unknown>) {
      const decoration = {
        options,
        disposed: false,
        dispose() {
          this.disposed = true;
        },
      };
      decorations.push(decoration);
      return decoration;
    },
    moveCursor(nextY: number) {
      cursorY = nextY;
      for (const handler of cursorMoveHandlers) handler();
    },
    setCols(nextCols: number) {
      term.cols = nextCols;
      for (const handler of resizeHandlers) handler();
    },
    decorations,
    markers,
  };

  return term;
};

test('CursorLineHighlighter paints a full-width decoration on the cursor line when enabled', () => {
  const term = createFakeTerm(100);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setBackgroundColor('#1a2332');
  highlighter.setEnabled(true);

  assert.equal(term.decorations.length, 1);
  assert.equal(term.decorations[0]?.options.width, 100);
  assert.equal(term.decorations[0]?.options.backgroundColor, '#1a2332');
  assert.equal(term.decorations[0]?.options.layer, 'bottom');
  highlighter.dispose();
});

test('CursorLineHighlighter follows cursor moves and clears when disabled', () => {
  const term = createFakeTerm(80);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  assert.equal(term.decorations.length, 1);

  term.moveCursor(3);
  assert.equal(term.decorations.length, 2);
  assert.equal(term.decorations[0]?.disposed, true);
  assert.equal(term.decorations[1]?.disposed, false);

  highlighter.setEnabled(false);
  assert.equal(term.decorations[1]?.disposed, true);
  highlighter.dispose();
});

test('CursorLineHighlighter recreates on resize and theme color changes', () => {
  const term = createFakeTerm(40);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  highlighter.setBackgroundColor('#112233');
  assert.equal(term.decorations.at(-1)?.options.backgroundColor, '#112233');
  assert.equal(term.decorations.at(-1)?.options.width, 40);

  term.setCols(120);
  assert.equal(term.decorations.at(-1)?.options.width, 120);

  highlighter.setBackgroundColor('#445566');
  assert.equal(term.decorations.at(-1)?.options.backgroundColor, '#445566');
  highlighter.dispose();
});
