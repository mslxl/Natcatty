import type { IDecoration, IDisposable, IMarker, Terminal as XTerm } from '@xterm/xterm';

type CursorLineTerminal = Pick<
  XTerm,
  | 'cols'
  | 'buffer'
  | 'registerMarker'
  | 'registerDecoration'
  | 'onCursorMove'
  | 'onResize'
  | 'onWriteParsed'
>;

/**
 * Highlights the buffer row under the cursor with a full-width xterm decoration.
 * Hidden on the alternate screen so full-screen applications keep their own styling.
 */
export class CursorLineHighlighter implements IDisposable {
  private enabled = false;
  private backgroundColor = '#1a2332';
  private marker: IMarker | null = null;
  private decoration: IDecoration | null = null;
  private activeLine: number | null = null;
  private activeCols: number | null = null;
  private activeColor: string | null = null;
  private readonly disposables: IDisposable[] = [];
  private disposed = false;

  constructor(private readonly term: CursorLineTerminal) {
    this.disposables.push(
      this.term.onCursorMove(() => this.refresh()),
      this.term.onWriteParsed(() => this.refresh()),
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

    const buffer = this.term.buffer.active;
    if (buffer.type === 'alternate') {
      this.clear();
      return;
    }
    const absoluteLine = buffer.baseY + buffer.cursorY;
    const cols = Math.max(1, this.term.cols || 1);
    const color = this.backgroundColor;

    if (
      !options.force &&
      absoluteLine === this.activeLine &&
      cols === this.activeCols &&
      color === this.activeColor &&
      this.decoration &&
      this.marker &&
      !this.marker.isDisposed
    ) {
      return;
    }

    this.clear();

    const marker = this.term.registerMarker(0);
    if (!marker) return;

    const decoration = this.term.registerDecoration({
      marker,
      x: 0,
      width: cols,
      backgroundColor: color,
      layer: 'bottom',
    });

    if (!decoration) {
      marker.dispose();
      return;
    }

    this.marker = marker;
    this.decoration = decoration;
    this.activeLine = absoluteLine;
    this.activeCols = cols;
    this.activeColor = color;
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

  private clear(): void {
    this.decoration?.dispose();
    this.decoration = null;
    this.marker?.dispose();
    this.marker = null;
    this.activeLine = null;
    this.activeCols = null;
    this.activeColor = null;
  }
}
