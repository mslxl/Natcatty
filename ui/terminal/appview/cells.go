package appview

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/ui/terminal/surface"
)

// cellWidth and cellHeight are one cell of the live grid, in DIPs. The live rows
// are painted cell by cell at these positions (see gridRow), so every row is one
// cell tall and every column starts at col*cellWidth. That is the terminal's own
// model: a program names cells, not pen positions, and a glyph the font falls
// back for is centered in its cell rather than moving the ones after it.
var cellWidth, cellHeight float32

// cellFamily and cellFontSize are the font the grid measures and paints with:
// the terminal's own configured font (Appearance), because these rows are
// terminal text and must look exactly as the terminal plugin draws them. The
// size falls back to the UI theme's when no terminal font was reported.
var cellFamily = "monospace"
var cellFontSize float32

// measureCell reads the cell size from an "M" at the terminal's configured font.
// MeasureText lays the text out now, in the same frame the rows are built, so the
// first frame already has its cells (an element's Bounds would still be empty on
// that frame).
func measureCell(c *ui.Context) {
	family, size := surface.TerminalFont()
	if family == "" {
		// The terminal plugin's own default when Font.Family is unset; a test
		// or a view with no Appearance setting still draws on a cell grid.
		family = "monospace"
	}
	if size <= 0 {
		size = themeFontSize(c)
	}
	cellFamily, cellFontSize = family, size
	if w, h := c.MeasureText(0, ui.Span{Text: "M", Font: family, Size: size}); w > 0 && h > 0 {
		cellWidth, cellHeight = w, h
	}
}

// themeFontSize is the UI theme's base text size, the fallback when no terminal
// font size was reported.
func themeFontSize(c *ui.Context) float32 {
	if t := c.Theme(); t != nil && t.FontSize > 0 {
		return t.FontSize
	}
	return 13
}

// pointerCell turns a pointer position in the live rows into a grid cell. The
// rows are laid out at the measured cell, so the mapping is the grid's own
// arithmetic: a row is one cell tall and a column one cell wide, whatever glyph
// stands in a cell. A pointer on the continuation column of a wide rune names
// the rune's first column, as selecting either half must take the whole rune.
func pointerCell(lines [][]ui.Span, x, y float32, cols int) (col, row int, ok bool) {
	if cellWidth <= 0 || cellHeight <= 0 {
		return 0, 0, false
	}
	col, row = int(x/cellWidth), int(y/cellHeight)
	if col < 0 {
		col = 0
	}
	if row < 0 {
		row = 0
	}
	if cols > 0 && col >= cols {
		col = cols - 1
	}
	if row < len(lines) {
		if start, found := clusterStart(lines[row], col); found {
			col = start
		}
	}
	return col, row, true
}

// clusterStart returns the column the cluster that covers col starts at. A wide
// rune covers two columns, so the right half names the rune's first column; a
// combining mark adds no column of its own. Columns past the row's text are not
// in a cluster, and keep their own column.
func clusterStart(spans []ui.Span, col int) (int, bool) {
	cell := 0
	for _, sp := range spans {
		for _, r := range sp.Text {
			w := parse.RuneWidth(r)
			if w <= 0 {
				continue
			}
			if col >= cell && col < cell+w {
				return cell, true
			}
			cell += w
		}
	}
	return col, false
}

// cellOrigin is where a cell's centre lies in the live grid's border box. The
// rows are uniform now, so the centre is the cell's place in the grid, counted
// from the box's corner outside the padding. The wave's shader is applied to
// the whole pane instead, so the caller maps this point out of the grid's box
// with paneWaveOrigin; a pointer origin is already in this box's space.
func cellOrigin(cellX, cellY, cols int) ui.Point {
	if cellX < 0 {
		cellX = 0
	}
	if cols > 0 && cellX >= cols {
		cellX = cols - 1
	}
	if cellY < 0 {
		cellY = 0
	}
	return ui.Point{
		X: livePad + (float32(cellX)+0.5)*cellWidth,
		Y: livePad + (float32(cellY)+0.5)*cellHeight,
	}
}

// spanFont is the font a live row's span is drawn with: the terminal's
// configured family and size, so a row looks the same here, in a finished
// command's plain cells, and in the terminal plugin's own grid. A span that
// names a family or size of its own still wins.
func spanFont(sp ui.Span, size float32) ui.Font {
	f := ui.Font{Family: cellFamily, Size: size, Weight: sp.Weight, Italic: sp.Italic, Features: sp.Features, Lang: sp.Lang}
	if sp.Font != "" {
		f.Family = sp.Font
	}
	if sp.Size > 0 {
		f.Size = sp.Size
	}
	return f
}

// CellSize is the measured cell, for tests and diagnostics.
func CellSize() (w, h float32) { return cellWidth, cellHeight }

// Viewport is the room one Pane's live screen gets, measured from that Pane's
// own flow layout: the scroll's content box, with the flow's padding removed.
// It belongs to the Pane, never to the package: a sibling Pane's viewport must
// not decide this Pane's grid.
type Viewport struct{ W, H float32 }

// GridFor is the cell grid a Pane's own viewport has room for: the measured
// monospace cell divides the room the live screen gets. A terminal sizes itself
// to its pane, because a fixed grid wraps a line where the reader sees no edge
// (the prompt that broke after a letter) or clips the right side in a narrow
// window.
func GridFor(viewport Viewport) (Size, bool) {
	w, h := CellSize()
	availW, availH := viewport.W, viewport.H
	if w <= 0 || h <= 0 || availW <= 0 || availH <= 0 {
		return Size{}, false
	}
	// The live box pads its rows and the scroller shows a bar at the edge.
	cols := int((availW - 12 - 14) / w)
	rows := int((availH - 12) / h)
	if cols < 20 {
		cols = 20
	}
	if rows < 8 {
		rows = 8
	}
	if cols > 400 {
		cols = 400
	}
	if rows > 100 {
		rows = 100
	}
	return Size{Cols: cols, Rows: rows}, true
}
