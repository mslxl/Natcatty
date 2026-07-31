import test from 'node:test';
import assert from 'node:assert/strict';

import { CursorLineHighlighter } from './cursorLineHighlight.ts';

type Handler = () => void;
const createFakeTerm = (cols = 80) => {
  let cursorY = 0;
  let baseY = 0;
  let bufferType: 'normal' | 'alternate' = 'normal';
  const coloredBackgrounds = new Set<number>();
  const coloredForegrounds = new Set<number>();
  const inverseCells = new Set<number>();
  const cursorMoveHandlers: Handler[] = [];
  const writeParsedHandlers: Handler[] = [];
  const resizeHandlers: Handler[] = [];
  const bufferChangeHandlers: Handler[] = [];
  const decorations: Array<{
    options: Record<string, unknown>;
    disposed: boolean;
    dispose: () => void;
    onDispose: (handler: Handler) => { dispose: () => void };
  }> = [];
  const markers: Array<{ line: number; disposed: boolean }> = [];

  const term = {
    cols,
    buffer: {
      active: {
        get type() {
          return bufferType;
        },
        get baseY() {
          return baseY;
        },
        get cursorY() {
          return cursorY;
        },
        getLine() {
          return {
            getCell(x: number) {
              return {
                isBgDefault: () => !coloredBackgrounds.has(x),
                isFgDefault: () => !coloredForegrounds.has(x),
                isInverse: () => (inverseCells.has(x) ? 1 : 0),
              };
            },
          };
        },
        getNullCell() {
          return { isBgDefault: () => true };
        },
      },
      onBufferChange(handler: Handler) {
        bufferChangeHandlers.push(handler);
        return { dispose() {} };
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
    onWriteParsed(handler: Handler) {
      writeParsedHandlers.push(handler);
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
      const disposeHandlers = new Set<Handler>();
      const decoration = {
        options,
        disposed: false,
        dispose() {
          if (this.disposed) return;
          this.disposed = true;
          for (const handler of disposeHandlers) handler();
          disposeHandlers.clear();
        },
        onDispose(handler: Handler) {
          disposeHandlers.add(handler);
          return { dispose: () => disposeHandlers.delete(handler) };
        },
      };
      decorations.push(decoration);
      return decoration;
    },
    moveCursor(nextY: number) {
      cursorY = nextY;
      for (const handler of cursorMoveHandlers) handler();
    },
    scrollOutput(lines: number) {
      baseY += lines;
      for (const handler of writeParsedHandlers) handler();
    },
    trimScrollback(lines: number) {
      for (const marker of markers) {
        if (!marker.disposed) marker.line -= lines;
      }
      for (const handler of writeParsedHandlers) handler();
    },
    setBufferType(nextType: 'normal' | 'alternate') {
      bufferType = nextType;
      for (const handler of bufferChangeHandlers) handler();
    },
    setColoredBackgrounds(columns: number[]) {
      coloredBackgrounds.clear();
      for (const column of columns) coloredBackgrounds.add(column);
      for (const handler of writeParsedHandlers) handler();
    },
    setColoredForegrounds(columns: number[]) {
      coloredForegrounds.clear();
      for (const column of columns) coloredForegrounds.add(column);
      for (const handler of writeParsedHandlers) handler();
    },
    setInverseCells(columns: number[]) {
      inverseCells.clear();
      for (const column of columns) inverseCells.add(column);
      for (const handler of writeParsedHandlers) handler();
    },
    resetDecorations() {
      for (const decoration of decorations) decoration.dispose();
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

test('CursorLineHighlighter paints an opaque background without changing text', () => {
  const term = createFakeTerm(100);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setBackgroundColor('#263449');
  highlighter.setEnabled(true);

  assert.equal(term.decorations.length, 1);
  assert.equal(term.decorations[0]?.options.width, 100);
  assert.equal(term.decorations[0]?.options.backgroundColor, '#263449');
  assert.equal(term.decorations[0]?.options.layer, 'bottom');
  highlighter.dispose();
});

test('CursorLineHighlighter leaves ANSI background cells untouched', () => {
  const term = createFakeTerm(10);
  term.setColoredBackgrounds([2, 3, 7]);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setBackgroundColor('#263449');
  highlighter.setEnabled(true);

  assert.deepEqual(
    term.decorations.map(({ options }) => ({ x: options.x, width: options.width })),
    [
      { x: 0, width: 2 },
      { x: 4, width: 3 },
      { x: 8, width: 2 },
    ],
  );
  highlighter.dispose();
});

test('CursorLineHighlighter leaves colored and inverse cells untouched', () => {
  const term = createFakeTerm(10);
  term.setColoredForegrounds([2, 3]);
  term.setInverseCells([7]);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setBackgroundColor('#263449');
  highlighter.setEnabled(true);

  assert.deepEqual(
    term.decorations.map(({ options }) => ({ x: options.x, width: options.width })),
    [
      { x: 0, width: 2 },
      { x: 4, width: 3 },
      { x: 8, width: 2 },
    ],
  );
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

test('CursorLineHighlighter recreates on resize and overlay color changes', () => {
  const term = createFakeTerm(40);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  highlighter.setBackgroundColor('#112233');
  assert.equal(
    term.decorations.at(-1)?.options.backgroundColor,
    '#112233',
  );
  assert.equal(term.decorations.at(-1)?.options.width, 40);

  term.setCols(120);
  assert.equal(term.decorations.at(-1)?.options.width, 120);

  highlighter.setBackgroundColor('#445566');
  assert.equal(
    term.decorations.at(-1)?.options.backgroundColor,
    '#445566',
  );
  highlighter.dispose();
});

test('CursorLineHighlighter follows bottom-row output when only baseY changes', () => {
  const term = createFakeTerm(80);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  assert.equal(term.markers.at(-1)?.line, 0);

  term.scrollOutput(3);
  assert.equal(term.decorations.length, 2);
  assert.equal(term.decorations[0]?.disposed, true);
  assert.equal(term.markers.at(-1)?.line, 3);
  assert.equal(term.decorations.at(-1)?.disposed, false);
  highlighter.dispose();
});

test('CursorLineHighlighter refreshes when saturated scrollback moves its marker', () => {
  const term = createFakeTerm(80);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  const originalMarker = term.markers.at(-1);
  assert.equal(originalMarker?.line, 0);

  term.trimScrollback(1);
  assert.equal(originalMarker?.disposed, true);
  assert.equal(term.decorations.length, 2);
  assert.equal(term.markers.at(-1)?.line, 0);
  assert.equal(term.decorations.at(-1)?.disposed, false);
  highlighter.dispose();
});

test('CursorLineHighlighter restores after the terminal resets its decorations', () => {
  const term = createFakeTerm(80);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  assert.equal(term.decorations.length, 1);

  term.resetDecorations();
  assert.equal(term.decorations[0]?.disposed, true);

  term.scrollOutput(0);
  assert.equal(term.decorations.length, 2);
  assert.equal(term.decorations.at(-1)?.disposed, false);
  highlighter.dispose();
});

test('CursorLineHighlighter clears in the alternate buffer and restores in normal buffer', () => {
  const term = createFakeTerm(80);
  const highlighter = new CursorLineHighlighter(term as never);
  highlighter.setEnabled(true);
  assert.equal(term.decorations.at(-1)?.disposed, false);

  term.setBufferType('alternate');
  assert.equal(term.decorations.at(-1)?.disposed, true);

  const decorationCount = term.decorations.length;
  term.moveCursor(4);
  term.scrollOutput(1);
  assert.equal(term.decorations.length, decorationCount);

  term.setBufferType('normal');
  assert.equal(term.decorations.length, decorationCount + 1);
  assert.equal(term.decorations.at(-1)?.disposed, false);
  highlighter.dispose();
});
