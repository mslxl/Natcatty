package appview

import (
	"runtime"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// Cell is a position on the replayed screen. Row counts from the first line
// the terminal keeps, scrollback included, so a selection stays on the text it
// started on while output scrolls underneath it.
type Cell struct{ Row, Col int }

// Selection is the live screen's text selection, dragged with the pointer and
// copied with the platform's Copy gesture.
//
// MyGo's own paragraph selection cannot serve the live screen: it paints and
// copies only while the selected paragraph holds the keyboard, and the live
// input box must hold it for typing to reach the program. A selection that
// took the keyboard therefore made the terminal untypable, and one that left
// it there was invisible. Tracking cells keeps typing and selection together,
// and the cell grid is exact where the drawn rows are not: wide runes take two
// columns and the scrollback rows are addressable.
//
// The Pane is rebuilt every frame, so the caller owns the value and passes it
// through Hooks.Selection.
type Selection struct {
	anchor, caret Cell
	dragging      bool
	// alt is the screen the selection was made on. The same row numbers name
	// different text on the main and alternate screens, so a switch clears it.
	alt bool
}

// SyncScreen drops the selection when the replay switched between the main and
// alternate screens. Copying across the switch would take the wrong screen's
// characters: a selection made over the shell was applied to vim's rows.
func (s *Selection) SyncScreen(alt bool) {
	if s.alt == alt {
		return
	}
	s.Clear()
	s.alt = alt
}

// Start begins a selection at a cell, replacing the previous one.
func (s *Selection) Start(row, col int) {
	s.anchor, s.caret = Cell{Row: row, Col: col}, Cell{Row: row, Col: col}
	s.dragging = true
}

// Drag moves the selection's free end while the pointer is held.
func (s *Selection) Drag(row, col int) { s.caret = Cell{Row: row, Col: col} }

// Release ends the drag. The selection stays until it is replaced or cleared,
// so the user can let go and then copy it.
func (s *Selection) Release() { s.dragging = false }

// Dragging reports whether the pointer is still choosing the free end.
func (s *Selection) Dragging() bool { return s.dragging }

// Clear forgets the selection.
func (s *Selection) Clear() { *s = Selection{} }

// Empty reports whether the selection holds nothing: a click, or none.
func (s *Selection) Empty() bool { return s.anchor == s.caret }

// Caret is the selection's free end.
func (s *Selection) Caret() Cell { return s.caret }

// Bounds returns the selection's ends in reading order.
func (s *Selection) Bounds() (Cell, Cell) {
	a, b := s.anchor, s.caret
	if b.Row < a.Row || b.Row == a.Row && b.Col < a.Col {
		return b, a
	}
	return a, b
}

// SelectAll selects the whole replay, the scrollback included, as Terminal's
// Select All does.
func (s *Selection) SelectAll(sc *screen) {
	if sc == nil || len(sc.grid) == 0 || sc.cols <= 0 {
		return
	}
	s.anchor = Cell{Row: 0, Col: 0}
	s.caret = Cell{Row: len(sc.grid) - 1, Col: sc.cols - 1}
	s.dragging = false
}

// SelectWord selects the run of non-blank cells around a cell on a replay row,
// as a double click does. A double click on blank space leaves a click: there
// is no word there to take.
func (s *Selection) SelectWord(sc *screen, row, col int) {
	s.dragging = false
	if sc == nil || sc.cols <= 0 || row < 0 || row >= len(sc.grid) {
		return
	}
	col = max(0, min(col, sc.cols-1))
	cells := sc.grid[row]
	// A wide rune's second half names the rune before it.
	for col > 0 && cells[col].cont {
		col--
	}
	inWord := func(c int) bool {
		if cells[c].cont {
			return true
		}
		return cells[c].s != "" && strings.TrimSpace(cells[c].s) != ""
	}
	if !inWord(col) {
		s.anchor, s.caret = Cell{Row: row, Col: col}, Cell{Row: row, Col: col}
		return
	}
	start, end := col, col
	for start > 0 && inWord(start-1) {
		start--
	}
	for end+1 < sc.cols && inWord(end+1) {
		end++
	}
	s.anchor, s.caret = Cell{Row: row, Col: start}, Cell{Row: row, Col: end}
}

// SelectLine selects a whole replay row, as a triple click does.
func (s *Selection) SelectLine(sc *screen, row int) {
	s.dragging = false
	if sc == nil || sc.cols <= 0 || row < 0 || row >= len(sc.grid) {
		return
	}
	s.anchor, s.caret = Cell{Row: row, Col: 0}, Cell{Row: row, Col: sc.cols - 1}
}

// SelectionColor is the colour behind selected text: the terminal theme's
// selection, or the foreground at the alpha Terminal uses when the theme leaves
// it unset.
func SelectionColor(c *ui.Context, theme *terminal.Theme) ui.Color {
	if theme != nil && theme.Selection.A != 0 {
		return theme.Selection
	}
	fg, _ := TermColors(c, theme)
	return fg.Alpha(0.3)
}

// CopyKey reports whether ev is the platform's Copy gesture: the Edit menu's
// Copy, or a chord a terminal can take without stealing the program's own
// Ctrl+C.
func CopyKey(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputCommand:
		return ev.Text == "copy"
	case ui.InputKeyDown:
		if ev.Key != ui.KeyC {
			return false
		}
		if runtime.GOOS == "darwin" {
			return ev.Mods == ui.Cmd
		}
		return ev.Mods == ui.Cmd|ui.Shift
	}
	return false
}

// SelectAllKey reports whether ev is Select All: the Edit menu's item, or a
// chord the shell does not use for its own line editing (Ctrl+A on anything
// but macOS).
func SelectAllKey(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputCommand:
		return ev.Text == "selectAll"
	case ui.InputKeyDown:
		if ev.Key != ui.KeyA {
			return false
		}
		if runtime.GOOS == "darwin" {
			return ev.Mods == ui.Cmd
		}
		return ev.Mods == ui.Cmd|ui.Shift
	}
	return false
}

// SelectedLines is the frame's replay starting at row from, count rows long,
// with the selection painted on the cells it covers. Rows outside the
// selection render exactly as Lines gives them.
func (f Frame) SelectedLines(from, count int, sel Selection, bg ui.Color) [][]ui.Span {
	if f.sc == nil || count <= 0 {
		if from >= 0 && from < len(f.Lines) {
			return f.Lines[from:min(from+count, len(f.Lines))]
		}
		return nil
	}
	a, b := sel.Bounds()
	lines := make([][]ui.Span, count)
	for i := range lines {
		row := from + i
		colFrom, colTo := 0, -1
		if row >= a.Row && row <= b.Row {
			colFrom, colTo = 0, f.sc.cols-1
			if row == a.Row {
				colFrom = max(0, a.Col)
			}
			if row == b.Row {
				colTo = min(f.sc.cols-1, b.Col)
			}
		}
		lines[i] = f.sc.rowSpans(row, colFrom, colTo, bg)
	}
	return lines
}

// SelectionText is the text the selection copies: one line per row, wide runes
// kept whole, trailing blanks and empty trailing rows trimmed as Terminal's
// copy does.
func (f Frame) SelectionText(sel Selection) string {
	if f.sc == nil || sel.Empty() {
		return ""
	}
	return f.sc.selectionText(sel)
}

// selectionText is SelectionText against this screen.
func (s *screen) selectionText(sel Selection) string {
	if len(s.grid) == 0 || s.cols <= 0 {
		return ""
	}
	a, b := sel.Bounds()
	first := max(0, min(a.Row, len(s.grid)-1))
	last := max(0, min(b.Row, len(s.grid)-1))
	var out strings.Builder
	for r := first; r <= last; r++ {
		colFrom, colTo := 0, s.cols-1
		if r == a.Row {
			colFrom = a.Col
		}
		if r == b.Row {
			colTo = b.Col
		}
		colFrom = max(0, min(colFrom, s.cols-1))
		colTo = max(colFrom, min(colTo, s.cols-1))
		var line strings.Builder
		for c := colFrom; c <= colTo; c++ {
			cl := s.grid[r][c]
			if cl.cont {
				// The wide rune before this cell already took its two columns.
				continue
			}
			if cl.s == "" {
				line.WriteByte(' ')
				continue
			}
			line.WriteString(cl.s)
		}
		if r > first {
			out.WriteByte('\n')
		}
		out.WriteString(strings.TrimRight(line.String(), " "))
	}
	return strings.TrimRight(out.String(), "\n")
}
