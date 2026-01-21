
import { Terminal as XTerm, IDecoration, IDisposable, IMarker } from "@xterm/xterm";
import { KeywordHighlightRule } from "../../types";

import { XTERM_PERFORMANCE_CONFIG } from "../../infrastructure/config/xtermPerformance";

/**
 * Manages terminal decorations for keyword highlighting.
 * Uses xterm.js Decoration API to overlay styles without modifying the data stream.
 * This ensures zero impact on scrolling performance ("lazy" highlighting).
 */
export class KeywordHighlighter implements IDisposable {
  private term: XTerm;
  private rules: KeywordHighlightRule[] = [];
  private decorations: { decoration: IDecoration; marker: IMarker }[] = [];
  private debounceTimer: NodeJS.Timeout | null = null;
  private enabled: boolean = false;
  private disposables: IDisposable[] = [];

  constructor(term: XTerm) {
    this.term = term;

    // Debug logging
    console.log('[KeywordHighlighter] Initialized');

    // Hook into terminal events to trigger highlighting
    this.disposables.push(
      // When user scrolls, refresh visible area
      this.term.onScroll(() => {
        // console.log('[KeywordHighlighter] onScroll');
        this.triggerRefresh();
      }),
      // When new data is written, refresh
      this.term.onWriteParsed(() => {
        // console.log('[KeywordHighlighter] onWriteParsed');
        this.triggerRefresh();
      }),
      // Also refresh on resize as viewport content changes
      this.term.onResize(() => this.triggerRefresh())
    );
  }

  public setRules(rules: KeywordHighlightRule[], enabled: boolean) {
    this.rules = rules.filter((r) => r.enabled && r.patterns.length > 0);
    this.enabled = enabled;

    // Clear existing and force an immediate refresh if enabling
    this.clearDecorations();
    if (this.enabled) {
      this.triggerRefresh();
    }
  }

  public dispose() {
    this.clearDecorations();
    this.disposables.forEach(d => d.dispose());
    this.disposables = [];
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }
  }

  private triggerRefresh() {
    if (!this.enabled || this.rules.length === 0) return;

    // Optimization: Disable highlighting in Alternate Buffer (e.g. Vim, Htop)
    // These apps manage their own highlighting and have rapid repaints.
    if (this.term.buffer.active.type === 'alternate') {
      if (this.decorations.length > 0) {
        this.clearDecorations();
      }
      return;
    }

    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }

    const delay = XTERM_PERFORMANCE_CONFIG.highlighting.debounceMs;
    this.debounceTimer = setTimeout(() => this.refreshViewport(), delay);
  }

  private clearDecorations() {
    this.decorations.forEach(({ decoration, marker }) => {
      decoration.dispose();
      marker.dispose();
    });
    this.decorations = [];
  }

  private refreshViewport() {
    // Safety check just in case
    if (!this.term?.buffer?.active) return;

    const buffer = this.term.buffer.active;
    const viewportY = buffer.viewportY;
    const rows = this.term.rows;
    const cursorY = buffer.cursorY;
    const baseY = buffer.baseY;
    const cursorAbsoluteY = baseY + cursorY;

    // Clear old decorations to avoid duplicates/memory leaks
    this.clearDecorations();

    // Iterate only over the visible rows
    for (let y = 0; y < rows; y++) {
      const lineY = viewportY + y;
      const line = buffer.getLine(lineY);
      if (!line) continue;

      const lineText = line.translateToString(true); // true = trim right whitespace
      if (!lineText) continue;

      // Process each rule
      for (const rule of this.rules) {
        const patterns = rule.patterns;
        for (const pattern of patterns) {
          try {
            // Create regex for this pattern
            const regex = new RegExp(pattern, "gi");
            let match;

            while ((match = regex.exec(lineText)) !== null) {
              const startCol = match.index;
              const matchLen = match[0].length;

              // Calculate offset relative to the absolute cursor position
              // offset = targetLineAbs - (baseY + cursorY)
              const offset = lineY - cursorAbsoluteY;
              const marker = this.term.registerMarker(offset);

              if (marker) {
                const deco = this.term.registerDecoration({
                  marker,
                  x: startCol,
                  width: matchLen,
                  foregroundColor: rule.color,
                });

                if (deco) {
                  this.decorations.push({ decoration: deco, marker });
                } else {
                  // If decoration failed, cleanup marker
                  marker.dispose();
                }
              }


            }

          } catch (err) {
            console.error("Highlight error", err);
          }
        }
      }
    }
  }
}
