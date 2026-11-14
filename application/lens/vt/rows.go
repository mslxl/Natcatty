package vt

import (
	"strings"
)

// rows is Terminal's capture model: a small screen the command writes into, whose rows
// are handed to the lens as they scroll off ("one output line fed to a lens, when
// its row scrolls off an 8-row scratch screen", flow.exe's Luau API definition).
//
// Splitting output on line feeds is not enough: a program that positions the cursor
// draws its rows without one (PowerShell's Get-ChildItem writes "Directory: …" and
// then moves to the next row), a progress bar overwrites a row with "\r", and a long
// row soft-wraps at the width. All of that is screen state, so the tap keeps a
// screen and takes rows from it, as Terminal does.
type rows struct {
	cols int
	rows int

	cells         [][]rune // the glyphs of each row
	soft          []bool   // the row was soft-wrapped: its line continues on the next one
	style         [][]byte // the escapes seen on each row, so a line keeps the program's colours
	curR          int
	curC          int
	pending       strings.Builder // a soft-wrapped line waiting for its last row
	pendingStyled strings.Builder // its escapes, in the order they arrived
}

// scratchRows is the height of the capture screen: Terminal's scratch screen is 8 rows,
// so a lens sees a line once it has scrolled off and the last rows at finish.
const scratchRows = 8

func newRows(cols int) *rows { return newRowsH(cols, scratchRows) }

// newRowsH makes a screen of an explicit height. The tap keeps a tall one because
// programs (and Windows' ConPTY, which reflows output) address rows of the real
// console with absolute cursor moves; a screen shorter than those rows would fold
// them onto its last row and scramble the text.
func newRowsH(cols, height int) *rows {
	if cols < 1 {
		cols = 1
	}
	if height < 1 {
		height = scratchRows
	}
	r := &rows{cols: cols, rows: height}
	r.clear()
	return r
}

func (r *rows) clear() {
	r.cells = make([][]rune, r.rows)
	r.soft = make([]bool, r.rows)
	r.style = make([][]byte, r.rows)
	r.curR, r.curC = 0, 0
	r.pending.Reset()
}

// setCols rebuilds the screen at a new width, keeping nothing: a resize mid-command
// changes what the rows are, and a fresh screen cannot mis-join them.
func (r *rows) setCols(cols int) {
	if cols < 1 {
		cols = 1
	}
	if cols == r.cols {
		return
	}
	r.cols = cols
	r.clear()
}

func (r *rows) put(ch rune, w int) {
	if w < 1 {
		w = 1
	}
	if r.curC+w > r.cols {
		// The row is full: the screen wraps, and the row is marked soft-wrapped so
		// the two display rows are one logical line for the lens. Its text joins the
		// line's pending text now that the row is complete.
		r.soft[r.curR] = true
		text, _ := r.rowLine(r.curR)
		r.pending.WriteString(text)
		r.pendingStyled.WriteString(r.styleFor(r.curR))
		r.cells[r.curR] = nil
		r.style[r.curR] = nil
		r.curR++
		r.curC = 0
		r.scrollIfNeeded(nil)
	}
	row := r.cells[r.curR]
	for len(row) < r.curC+w {
		row = append(row, ' ')
	}
	if row[r.curC] == 0 {
		row[r.curC] = ch
	} else {
		row[r.curC] = ch
	}
	for i := 1; i < w; i++ {
		row[r.curC+i] = 0 // the wide glyph's padding cell
	}
	r.cells[r.curR] = row
	r.curC += w
}

// styleBytes records the program's escapes for the row the cursor is on.
func (r *rows) styleBytes(b []byte) {
	r.style[r.curR] = append(r.style[r.curR], b...)
}

func (r *rows) cr() { r.curC = 0 }

// moveTo positions the cursor, as CSI row;col H does: a program that draws a table
// with cursor addressing puts each of its rows down without a line feed.
func (r *rows) moveTo(row, col int) {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	if row > r.rows-1 {
		row = r.rows - 1
	}
	if col > r.cols-1 {
		col = r.cols - 1
	}
	r.curR, r.curC = row, col
}

// eraseLine blanks the row from the cursor (CSI K), as a progress redraw does.
func (r *rows) eraseLine() {
	row := r.cells[r.curR]
	if len(row) > r.curC {
		row = row[:r.curC]
	}
	r.cells[r.curR] = row
}

// eraseDisplay blanks the screen (CSI 2J), which a program does when it rewrites
// its output rather than appending it.
func (r *rows) eraseDisplay() {
	for i := range r.cells {
		r.cells[i] = nil
		r.soft[i] = false
	}
}

func (r *rows) back() {
	if r.curC > 0 {
		r.curC--
	}
}

func (r *rows) tab() {
	next := (r.curC/8 + 1) * 8
	for r.curC < next && r.curC < r.cols {
		r.put(' ', 1)
	}
}

// down moves to the next row, scrolling the screen (and emitting a line) when it
// moves past the last one.
func (r *rows) down(out *[]Line) {
	r.curC = 0
	r.curR++
	r.scrollIfNeeded(out)
}

// advanceTo moves the cursor to a later row, emitting the rows it passes in order:
// a program that jumps down with absolute addressing (git show --stat does) must
// not have the rows it skipped arrive after the ones it wrote below them.
func (r *rows) advanceTo(row int, out *[]Line) {
	if row > r.rows-1 {
		row = r.rows - 1
	}
	for r.curR < row && r.curR < r.rows {
		text, soft := r.rowLine(r.curR)
		r.pending.WriteString(text)
		r.pendingStyled.WriteString(r.styleFor(r.curR))
		if !soft {
			if out != nil {
				*out = append(*out, Line{Text: r.pending.String(), Styled: r.pendingStyled.String()})
			}
			r.pending.Reset()
			r.pendingStyled.Reset()
		}
		r.cells[r.curR] = nil
		r.soft[r.curR] = false
		r.style[r.curR] = nil
		r.curR++
	}
	r.curC = 0
}

// newline ends the current row. When the row ends a logical line it is emitted at
// once, so a running block shows what has been printed without waiting for the
// scratch screen to fill; a row the console wrapped keeps its pending text for the
// row that follows.
func (r *rows) newline(out *[]Line) {
	if r.curR >= 0 && r.curR < r.rows {
		text, soft := r.rowLine(r.curR)
		r.pending.WriteString(text)
		r.pendingStyled.WriteString(r.styleFor(r.curR))
		if !soft {
			*out = append(*out, Line{Text: r.pending.String(), Styled: r.pendingStyled.String()})
			r.pending.Reset()
			r.pendingStyled.Reset()
		}
		r.cells[r.curR] = nil
		r.soft[r.curR] = false
		r.style[r.curR] = nil
	}
	r.curC = 0
	r.curR++
	r.scrollIfNeeded(out)
}

// scrollIfNeeded scrolls the screen up while the cursor is below it, emitting the
// row that leaves the top.
func (r *rows) scrollIfNeeded(out *[]Line) {
	for r.curR >= r.rows {
		r.curR--
		r.scrollOne(out)
	}
}

func (r *rows) scrollOne(out *[]Line) {
	line, soft := r.rowLine(0)
	r.cells = append(r.cells[1:], nil)
	r.soft = append(r.soft[1:], false)
	r.style = append(r.style[1:], nil)
	r.pending.WriteString(line)
	r.pendingStyled.WriteString(r.styleFor(0))
	if soft {
		return // the logical line goes on in the row that followed
	}
	if out != nil {
		*out = append(*out, Line{Text: r.pending.String(), Styled: r.pendingStyled.String()})
	}
	r.pending.Reset()
	r.pendingStyled.Reset()
}

// styleFor is the SGR text a row carries, in the order it arrived.
func (r *rows) styleFor(i int) string { return string(r.style[i]) }

// rowLine is a row's text, wide glyphs kept and padding cells dropped.
func (r *rows) rowLine(i int) (text string, soft bool) {
	var b strings.Builder
	for _, ch := range r.cells[i] {
		if ch == 0 {
			continue
		}
		b.WriteRune(ch)
	}
	return strings.TrimRight(b.String(), " "), r.soft[i]
}

// take returns the lines emitted so far, including a soft-wrapped line that is
// waiting for a row that scrolls off.
func (r *rows) flush(out *[]Line) {
	// Trailing blank rows are the empty rest of the screen and are not output; a
	// blank row before the content is a blank line of the command's output.
	last := -1
	for i := 0; i < r.rows; i++ {
		if text, _ := r.rowLine(i); text != "" {
			last = i
		}
	}
	if last < 0 {
		r.clear()
		return
	}
	var b, sb strings.Builder
	b.WriteString(r.pending.String())
	sb.WriteString(r.pendingStyled.String())
	r.pending.Reset()
	r.pendingStyled.Reset()
	for i := 0; i <= last; i++ {
		text, soft := r.rowLine(i)
		b.WriteString(text)
		sb.WriteString(r.styleFor(i))
		if soft {
			continue
		}
		*out = append(*out, Line{Text: b.String(), Styled: sb.String()})
		b.Reset()
		sb.Reset()
	}
	if b.Len() > 0 {
		*out = append(*out, Line{Text: b.String(), Styled: sb.String()})
	}
	r.clear()
}

// Line is one captured output line, from the screen.
type Line struct {
	Text   string
	Styled string
}
