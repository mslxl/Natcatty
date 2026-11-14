package appview

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// cellWidth and cellHeight are one cell of the live grid, in DIPs, measured from a
// monospaced "M": pointer positions divide by them to find the cell a program's
// mouse report names.
var cellWidth, cellHeight float32

// measureCells reads the cell size from a clipped, invisible row so the text in it
// never shows. Bounds come from the previous layout, which is enough: the font does
// not change from frame to frame.
//
// The row is a RichText with one span, exactly what the live grid draws for each
// screen row: MyGo's RichText and Text do not have the same line box (18.62 vs 16.27
// DIP for the same monospace at the theme size), and measuring a Text made the cell
// 2.3 DIP shorter than every row drawn. Pointer rows drifted with it, the grid asked
// for more rows than fit, and a finished command's plain cells were shorter than the
// grid rows they replaced.
func measureCells(c *ui.Context) {
	// Zero height keeps the row out of the layout and Clip hides what it draws. The
	// width has to come from a relative position: a text element fills the room it is
	// given, so its own width is not the text's.
	ui.Box(c).Height(0).Clip().Children(func() {
		var wide, one ui.Element
		// Zero height alone does not stop the text painting: the row's children
		// overflow the box and the live rows below are drawn over the "M"s. Fully
		// transparent keeps it measurable and invisible.
		ui.Row(c).Gap(0).Opacity(0).Children(func() {
			wide = ui.RichText(c, ui.Span{Text: strings.Repeat("M", 10), Font: "monospace"}).SingleLine()
			one = ui.RichText(c, ui.Span{Text: "M", Font: "monospace"}).SingleLine()
		})
		rw, ro := wide.Bounds(), one.Bounds()
		if ro.X > rw.X {
			cellWidth = (ro.X - rw.X) / 10
		}
		if ro.H > 0 {
			cellHeight = ro.H
		}
	})
}

// cellAt turns a pointer position, relative to the live rows, into a cell.
func cellAt(x, y float32) (col, row int, ok bool) {
	if cellWidth <= 0 || cellHeight <= 0 {
		return 0, 0, false
	}
	col = int(x / cellWidth)
	row = int(y / cellHeight)
	if col < 0 {
		col = 0
	}
	if row < 0 {
		row = 0
	}
	return col, row, true
}

// CellSize is the measured cell, for tests and diagnostics.
func CellSize() (w, h float32) { return cellWidth, cellHeight }

// availableWidth and availableHeight are the room the live screen gets, measured from
// the flow's own layout (a scroller knows its viewport in the same frame it is built).
var availableWidth, availableHeight float32

// noteAvailable records the viewport the flow was laid out in.
func noteAvailable(w, h float32) {
	if w > 0 && h > 0 {
		availableWidth, availableHeight = w, h
	}
}

// Available is the viewport the live screen was last laid out in.
func Available() (w, h float32) { return availableWidth, availableHeight }

// GridFor is the cell grid the window has room for: the measured monospace cell
// divides the room the live screen gets. A terminal sizes itself to its pane, because
// a fixed grid wraps a line where the reader sees no edge (the prompt that broke after
// a letter) or clips the right side in a narrow window.
func GridFor() (Size, bool) {
	w, h := CellSize()
	availW, availH := Available()
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
