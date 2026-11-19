# The live terminal is painted on the cell grid

Every row of the live terminal in the Lens flow is one measured cell tall, and
every glyph is painted at the logical cell the program wrote it to
(`col * cellWidth`, `row * cellHeight`). MyGo's text engine may shape a run —
fallback fonts, ligatures, combining marks — but it must not lay the row out
as a paragraph: no bidi reordering, no per-row line boxes, and no glyph
advance deciding where a cell begins. A wide rune owns two columns and is
centered in them; a fallback glyph is centered in its cell and shrunk when it
is wider than the cells it owns. The pointer, selection, mouse reports,
file-drop ripple and cursor overlay all resolve positions with the same cell
arithmetic, never by inverting what was drawn.

`ui/terminal/appview` owns this. `cells.go` measures one monospace cell per
frame with `Context.MeasureText` and converts points with `pointerCell`,
`clusterStart` and `cellOrigin`. `grid.go` paints each row through the public
`Element.Draw` and `Painter.Glyphs`. `pane.go` gives every row
`Height(cellHeight)` and places the cursor at `CursorCol * cellWidth`. Painting
a row in one `Draw` keeps the live screen a normal child of the Lens flow (one
`ui.Scroll`, finished blocks before it), while glyph placement stays per cell.

**Status**: accepted and implemented.

**Considered Options**:
- Whole-row `ui.RichText` paragraphs (the previous path): fallback glyphs look
  right, but MyGo owns the pen positions. Pointers, the cursor and the ripple
  then have to invert `rowExtents`/`colInExtents`/`penLeft`, variable line
  heights drift rows apart, and a pure-RTL paragraph cannot be mapped back to
  cells.
- One `ui.Text` per row: a fixed line height helps, but the paragraph engine
  still decides advances, so a fallback glyph moves the cells after it, and
  MyGo's Text line box differs from RichText's.
- One element per cell: exactly the terminal model, but thousands of elements
  per frame for a full screen.
- Custom painting on the cell grid, one `Draw` per row (chosen): placement is
  per cell, while element count stays one per screen row.
- Embedding `plugins/terminal.View`: the plugin's canvas cannot carry the Lens
  flow's per-row labels, scroll pinning and cursor overlay; its per-cell
  painting algorithm is ported instead.

**Consequences**:
- Row height is uniform, `cellHeight`; no row can grow because a font fell
  back. `blockview.CellRowHeight` is set to the same value, so a finished
  command's plain cells and the live rows align.
- Screen positions are grid arithmetic. Any new pointer, selection, cursor or
  effect feature must use `pointerCell`/`cellOrigin` (or share their
  arithmetic); reintroducing inverse layout geometry is a regression.
- A wide rune's continuation column names the rune's first column. RTL runes
  keep the logical column that libghostty-vt stores them in; visual reordering
  is not allowed.
- Text shaping is kept only for glyph appearance, and a glyph's advance may only
  center it inside its cells. Runs go through `shapeSpan`, which caches glyphs
  for frames that repeat the same text.
- Selection backgrounds, underlines, wavy underlines and strikethroughs are
  painted across each run's cells, covering the blanks a program styled.
- Each row still carries its text as `Label`, so accessibility text and
  `Tester.Find`/`Tester.Texts` see the rows as before.
- The cell is measured synchronously with `Context.MeasureText` in the frame
  the rows are built. Measuring by drawing an invisible RichText and reading a
  previous frame's `Bounds` is not allowed: it lags the first frame and its line
  box is not the cell.
- Tests pin uniform row heights, the cell mapping of pointer, selection, mouse
  reporting, drop and cursor, wide-rune cluster mapping, and that glyphs painted
  through `Draw` land inside the row's box (the `Draw` rect is in canvas
  coordinates, not element-local ones).
