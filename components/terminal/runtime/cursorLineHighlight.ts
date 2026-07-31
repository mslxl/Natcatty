import type {
  IDecoration,
  IDisposable,
  IMarker,
  Terminal as XTerm,
} from '@xterm/xterm';

type CursorLineTerminal = Pick<
  XTerm,
  | 'cols'
  | 'buffer'
  | 'registerMarker'
  | 'registerDecoration'
  | 'onCursorMove'
  | 'onResize'
  | 'onWriteParsed'
  | 'onRender'
  | 'onSelectionChange'
  | 'hasSelection'
>;

type HighlightRange = { x: number; width: number };

/**
 * Highlights the buffer row under the cursor without tinting its glyphs.
 * ANSI-colored cells keep their own background; only default-background cells
 * receive the opaque theme color.
 */
export class CursorLineHighlighter implements IDisposable {
  private enabled = false;
  private backgroundColor = '#263449';
  private marker: IMarker | null = null;
  private decorations: IDecoration[] = [];
  private decorationDisposeListeners: IDisposable[] = [];
  private activeLine: number | null = null;
  private activeCols: number | null = null;
  private activeColor: string | null = null;
  private activeRanges: HighlightRange[] = [];
  private activeTailRanges: HighlightRange[] = [];
  private readonly disposables: IDisposable[] = [];
  private disposed = false;

  constructor(
    private readonly term: CursorLineTerminal,
    private readonly getProtectedRanges: (absoluteLine: number) => readonly HighlightRange[] = () => [],
  ) {
    this.disposables.push(
      this.term.onCursorMove(() => this.refresh()),
      this.term.onWriteParsed(() => this.refresh()),
      this.term.onRender(() => this.refresh()),
      this.term.onSelectionChange(() => this.refresh({ force: true })),
      this.term.onResize(() => this.refresh({ force: true })),
      this.term.buffer.onBufferChange(() => this.refresh({ force: true })),
    );
  }

  setEnabled(enabled: boolean): void {
    if (this.disposed) return;
    if (this.enabled === enabled) {
      if (enabled) this.refresh();
      return;
    }
    this.enabled = enabled;
    if (!enabled) {
      this.clear();
      return;
    }
    this.refresh({ force: true });
  }

  setBackgroundColor(color: string): void {
    if (this.disposed) return;
    const next = color.trim();
    if (!next || next === this.backgroundColor) return;
    this.backgroundColor = next;
    if (this.enabled) this.refresh({ force: true });
  }

  refresh(options: { force?: boolean } = {}): void {
    if (this.disposed || !this.enabled) return;

    if (this.term.hasSelection()) {
      this.clear();
      return;
    }

    const buffer = this.term.buffer.active;
    if (buffer.type === 'alternate') {
      this.clear();
      return;
    }
    const absoluteLine = buffer.baseY + buffer.cursorY;
    const cols = Math.max(1, this.term.cols || 1);
    const color = this.backgroundColor;
    const line = buffer.getLine(absoluteLine);
    const { ranges, contentEnd } = this.getDefaultBackgroundRanges(
      line,
      cols,
      buffer.getNullCell(),
      this.getProtectedRanges(absoluteLine),
    );
    const tailRanges = this.getTailRanges(contentEnd, cols);

    if (
      !options.force &&
      absoluteLine === this.activeLine &&
      cols === this.activeCols &&
      color === this.activeColor &&
      rangesEqual(ranges, this.activeRanges) &&
      rangesEqual(tailRanges, this.activeTailRanges) &&
      this.marker &&
      !this.marker.isDisposed &&
      this.marker.line === absoluteLine
    ) {
      return;
    }

    this.clear();

    const marker = this.term.registerMarker(0);
    if (!marker) return;

    const decorations: IDecoration[] = [];
    for (const range of ranges) {
      const decoration = this.term.registerDecoration({
        marker,
        x: range.x,
        width: range.width,
        backgroundColor: color,
        layer: 'bottom',
      });
      if (decoration) decorations.push(decoration);
    }
    for (const tailRange of tailRanges) {
      const tailDecoration = this.term.registerDecoration({
        marker,
        x: tailRange.x,
        width: tailRange.width,
        backgroundColor: color,
        layer: 'bottom',
      });
      if (tailDecoration) {
        decorations.push(tailDecoration);
      }
    }

    this.marker = marker;
    this.decorations = decorations;
    this.decorationDisposeListeners = decorations.map((decoration) =>
      decoration.onDispose(() => {
        if (this.decorations.includes(decoration)) {
          this.activeLine = null;
          this.activeCols = null;
          this.activeColor = null;
          this.activeRanges = [];
        }
      }),
    );
    this.activeLine = absoluteLine;
    this.activeCols = cols;
    this.activeColor = color;
    this.activeRanges = ranges;
    this.activeTailRanges = tailRanges;
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.clear();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
    this.disposables.length = 0;
  }

  private getDefaultBackgroundRanges(
    line: ReturnType<CursorLineTerminal['buffer']['active']['getLine']>,
    cols: number,
    cell: ReturnType<CursorLineTerminal['buffer']['active']['getNullCell']>,
    protectedRanges: readonly HighlightRange[],
  ): { ranges: HighlightRange[]; contentEnd: number } {
    const ranges: HighlightRange[] = [];
    let rangeStart: number | null = null;
    let contentEnd = 0;
    for (let x = 0; x < cols; x += 1) {
      const currentCell = line?.getCell(x, cell);
      if (
        currentCell &&
        (currentCell.getChars() !== '' || !currentCell.isAttributeDefault())
      ) {
        contentEnd = Math.min(cols, x + Math.max(1, currentCell.getWidth()));
      }
      const isProtected = protectedRanges.some(
        (range) => x >= range.x && x < range.x + range.width,
      );
      const isHighlightable =
        !isProtected &&
        (currentCell === undefined ||
          (currentCell.isBgDefault() &&
            currentCell.isFgDefault() &&
            !currentCell.isInverse()));
      if (isHighlightable && rangeStart === null) rangeStart = x;
      if ((!isHighlightable || x === cols - 1) && rangeStart !== null) {
        const end = isHighlightable && x === cols - 1 ? x + 1 : x;
        ranges.push({ x: rangeStart, width: end - rangeStart });
        rangeStart = null;
      }
    }
    return {
      ranges: ranges
        .map((range) => {
          const end = Math.min(range.x + range.width, contentEnd);
          return { x: range.x, width: end - range.x };
        })
        .filter((range) => range.width > 0),
      contentEnd,
    };
  }

  private getTailRanges(contentEnd: number, cols: number): HighlightRange[] {
    if (contentEnd >= cols) return [];
    return [{ x: contentEnd, width: cols - contentEnd }];
  }

  private clear(): void {
    for (const disposable of this.decorationDisposeListeners) disposable.dispose();
    this.decorationDisposeListeners = [];
    for (const decoration of this.decorations) decoration.dispose();
    this.decorations = [];
    this.marker?.dispose();
    this.marker = null;
    this.activeLine = null;
    this.activeCols = null;
    this.activeColor = null;
    this.activeRanges = [];
    this.activeTailRanges = [];
  }
}

const rangesEqual = (left: HighlightRange[], right: HighlightRange[]): boolean =>
  left.length === right.length && left.every((range, index) => {
    const other = right[index];
    return other?.x === range.x && other.width === range.width;
  });
