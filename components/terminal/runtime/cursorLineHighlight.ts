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
  private overlayColor = 'rgba(201, 209, 217, 0.18)';
  private marker: IMarker | null = null;
  private decoration: IDecoration | null = null;
  private decorationRenderListener: IDisposable | null = null;
  private decorationDisposeListener: IDisposable | null = null;
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

  setOverlayColor(color: string): void {
    if (this.disposed) return;
    const next = color.trim();
    if (!next || next === this.overlayColor) return;
    this.overlayColor = next;
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
    const color = this.overlayColor;

    if (
      !options.force &&
      absoluteLine === this.activeLine &&
      cols === this.activeCols &&
      color === this.activeColor &&
      this.decoration &&
      this.marker &&
      !this.marker.isDisposed &&
      this.marker.line === absoluteLine
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
    });

    if (!decoration) {
      marker.dispose();
      return;
    }

    this.marker = marker;
    this.decoration = decoration;
    this.decorationRenderListener = decoration.onRender((element) => {
      element.style.backgroundColor = color;
      element.style.pointerEvents = 'none';
      element.setAttribute('aria-hidden', 'true');
    });
    this.decorationDisposeListener = decoration.onDispose(() => {
      if (this.decoration !== decoration) return;
      this.decoration = null;
      this.decorationRenderListener = null;
      this.decorationDisposeListener = null;
      this.activeLine = null;
      this.activeCols = null;
      this.activeColor = null;
    });
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
    this.decorationRenderListener?.dispose();
    this.decorationRenderListener = null;
    this.decorationDisposeListener?.dispose();
    this.decorationDisposeListener = null;
    this.decoration?.dispose();
    this.decoration = null;
    this.marker?.dispose();
    this.marker = null;
    this.activeLine = null;
    this.activeCols = null;
    this.activeColor = null;
  }
}
