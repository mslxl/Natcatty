package appview

// The live screen is a grid: every row is one measured cell tall and every
// column starts at col*cellWidth, whatever glyph the program wrote there. A
// glyph the font falls back for (an emoji, a Nerd Font icon) is centered in its
// cell instead of moving the cells after it, a wide rune fills its two columns
// with one glyph, and a right-to-left rune keeps its logical column. These
// tests pin the pointer, selection, mouse reporting, drop and cursor to that
// grid, so a fallback glyph's own advance can no longer move a cell.
//
// They run a real terminal snapshot through the pane and address cells by the
// measured cell size, exactly the arithmetic the pane uses.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/ui/terminal/effects"
)

// TestPointerNamesTheCellUnderTheGlyph presses near the bottom of a row holding
// fallback glyphs, inside the first cell of text after them. The cell is the
// grid's own: row*cellHeight and col*cellWidth, with the glyphs above centered
// in their cells and not stretching the row.
func TestPointerNamesTheCellUnderTheGlyph(t *testing.T) {
	const (
		glyphRows = 8
		glyphs    = 20
		width     = 900
		height    = 300
	)
	prefix := strings.Repeat("❄", glyphs)
	var text strings.Builder
	for i := 0; i < glyphRows; i++ {
		fmt.Fprintf(&text, "❄ row %02d\r\n", i)
	}
	text.WriteString(prefix + "TARGET")

	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var ripple effects.Ripple
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			&ripple, Bell{Col: -1, Row: -1},
			// No prompt boundary: the whole screen is live, so every glyph row
			// is above the row the press names.
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{}, func(string) {})
	}, width, height)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}

	rect, ok := findTextPrefix(tester, prefix+"TARGET")
	if !ok {
		t.Fatalf("the target row is not drawn: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	if cellW <= 0 {
		t.Fatal("the cell was not measured")
	}
	first, ok := findTextPrefix(tester, "❄ row 00")
	if !ok {
		t.Fatalf("the first row is not drawn: %q", tester.Texts())
	}
	x := float32(rect.X) + glyphs*cellW + 3
	y := float32(rect.Y+rect.H) - 2
	tester.Press(x, y)
	tester.Release(x, y)

	// The ripple's drop point is the pointer's cell, with no file needed to
	// read it back.
	if !ripple.StartDrop(tester.Now()) {
		t.Fatal("the press did not reach the live rows")
	}
	col, row := ripple.Cell()
	if row != glyphRows {
		t.Errorf("the press landed on row %d, want %d: y %.1f is inside the row's %v box",
			row, glyphRows, y, rect)
	}
	if col != glyphs {
		t.Errorf("the press landed on column %d, want %d: x %.1f is in the cell from %.1f to %.1f",
			col, glyphs, x, x-3, x-3+cellW)
	}
	// The wave starts where the pointer was, not at the cell's nominal centre.
	at, exact := ripple.Origin()
	if !exact {
		t.Fatal("the drop ripple has no point to start from")
	}
	wantOrigin := ui.Point{X: livePad + x - float32(first.X), Y: livePad + y - float32(first.Y)}
	if at.X < wantOrigin.X-0.5 || at.X > wantOrigin.X+0.5 || at.Y < wantOrigin.Y-0.5 || at.Y > wantOrigin.Y+0.5 {
		t.Errorf("the ripple starts at %v, want the pointer's %v", at, wantOrigin)
	}
}

// TestPointerFollowsRowsThatChanged changes the rows between two frames: the
// first frame is ASCII, and the next lays fallback glyphs over the rows above
// it. Every row is still one cell tall, so a press near the bottom of the last
// glyph row names that row in both frames.
func TestPointerFollowsRowsThatChanged(t *testing.T) {
	const (
		width  = 900
		height = 300
	)
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	var text strings.Builder
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&text, "plain %02d\r\n", i)
	}
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var ripple effects.Ripple
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			&ripple, Bell{Col: -1, Row: -1},
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{}, func(string) {})
	}, width, height)
	tester.Frame()

	// Overwrite the first eight screen rows with fallback glyphs; those rows
	// were ASCII in the frame just built.
	text.Reset()
	text.WriteString("\x1b[H")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&text, "❄ row %02d\r\n", i)
	}
	term.Feed([]byte(text.String()))
	tester.Frame()

	rect, ok := findTextPrefix(tester, "❄ row 07")
	if !ok {
		t.Fatalf("the last glyph row is not drawn: %q", tester.Texts())
	}
	x := float32(rect.X) + 2
	y := float32(rect.Y+rect.H) - 1
	tester.Press(x, y)
	tester.Release(x, y)
	if !ripple.StartDrop(tester.Now()) {
		t.Fatal("the press did not reach the live rows")
	}
	if _, row := ripple.Cell(); row != 7 {
		t.Errorf("the press landed on row %d, want the glyph row 7 at y %.1f", row, y)
	}
}

// TestSelectionStartsOnTheCellUnderTheGlyph checks the selection path names the
// same cell as the pointer: a press in the first cell after fallback glyphs
// starts the selection there, not one cell off.
func TestSelectionStartsOnTheCellUnderTheGlyph(t *testing.T) {
	const (
		glyphRows = 8
		glyphs    = 20
		width     = 900
		height    = 300
	)
	prefix := strings.Repeat("❄", glyphs)
	var text strings.Builder
	for i := 0; i < glyphRows; i++ {
		fmt.Fprintf(&text, "❄ row %02d\r\n", i)
	}
	text.WriteString(prefix + "TARGET")

	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var sel Selection
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{Selection: &sel}, func(string) {})
	}, width, height)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	rect, ok := findTextPrefix(tester, prefix+"TARGET")
	if !ok {
		t.Fatalf("the target row is not drawn: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	if cellW <= 0 {
		t.Fatal("the cell was not measured")
	}
	x := float32(rect.X) + glyphs*cellW + 3
	y := float32(rect.Y+rect.H) - 2
	tester.Press(x, y)
	if !sel.Dragging() {
		t.Fatal("the press did not start a selection")
	}
	if care := sel.Caret(); care.Row != glyphRows || care.Col != glyphs {
		t.Errorf("the selection starts at %+v, want the target cell (%d,%d)", care, glyphRows, glyphs)
	}
}

// TestMouseReportNamesTheCellUnderTheGlyph checks a program that asked for the
// mouse hears the cell the click landed on, by the grid's arithmetic.
func TestMouseReportNamesTheCellUnderTheGlyph(t *testing.T) {
	const (
		glyphRows = 8
		glyphs    = 20
		width     = 900
		height    = 300
	)
	prefix := strings.Repeat("❄", glyphs)
	var text strings.Builder
	for i := 0; i < glyphRows; i++ {
		fmt.Fprintf(&text, "❄ row %02d\r\n", i)
	}
	text.WriteString(prefix + "TARGET\x1b[?1000h\x1b[?1006h")

	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{}, func(string) {})
	}, width, height)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	rect, ok := findTextPrefix(tester, prefix+"TARGET")
	if !ok {
		t.Fatalf("the target row is not drawn: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	if cellW <= 0 {
		t.Fatal("the cell was not measured")
	}
	x := float32(rect.X) + glyphs*cellW + 3
	y := float32(rect.Y+rect.H) - 2
	tester.Press(x, y)
	// SGR reports count from one: the first cell is 1;1. The report reaches
	// the connection from the input path, so on a loaded runner it can land
	// just after Press returns; give it a bounded moment.
	want := fmt.Sprintf("\x1b[<0;%d;%dM", glyphs+1, glyphRows+1)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(conn.Written(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the mouse press reported %q, want %q", conn.Written(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestDropRipplesAtThePointerCell checks the file-drop path uses the cell and
// point the pointer was at, not a cell centre: a drop on text after fallback
// glyphs ripples from that cell, at the pointer's own point.
func TestDropRipplesAtThePointerCell(t *testing.T) {
	const (
		glyphRows = 8
		glyphs    = 20
		width     = 900
		height    = 300
	)
	prefix := strings.Repeat("❄", glyphs)
	var text strings.Builder
	for i := 0; i < glyphRows; i++ {
		fmt.Fprintf(&text, "❄ row %02d\r\n", i)
	}
	text.WriteString(prefix + "TARGET")

	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var ripple effects.Ripple
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			&ripple, Bell{Col: -1, Row: -1},
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{}, func(string) {})
	}, width, height)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	rect, ok := findTextPrefix(tester, prefix+"TARGET")
	if !ok {
		t.Fatalf("the target row is not drawn: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	if cellW <= 0 {
		t.Fatal("the cell was not measured")
	}
	first, ok := findTextPrefix(tester, "❄ row 00")
	if !ok {
		t.Fatalf("the first row is not drawn: %q", tester.Texts())
	}
	x := float32(rect.X) + glyphs*cellW + 3
	y := float32(rect.Y+rect.H) - 2
	// The file channel of the tester's drop carries no point of its own: the
	// last move names the cell, and the drop must ripple there.
	tester.Move(x, y)
	if !tester.DropFiles(x, y, "/tmp/natcatty-drop") {
		t.Fatalf("the drop was not taken: %q", tester.Texts())
	}
	if !ripple.HasPointer() {
		t.Fatal("the drop did not reach the live rows")
	}
	col, row := ripple.Cell()
	if row != glyphRows || col != glyphs {
		t.Errorf("the drop ripples at (%d,%d), want the target cell (%d,%d)", col, row, glyphs, glyphRows)
	}
	at, exact := ripple.Origin()
	if !exact {
		t.Fatal("the drop ripple has no point to start from")
	}
	wantOrigin := ui.Point{X: livePad + x - float32(first.X), Y: livePad + y - float32(first.Y)}
	if at.X < wantOrigin.X-0.5 || at.X > wantOrigin.X+0.5 || at.Y < wantOrigin.Y-0.5 || at.Y > wantOrigin.Y+0.5 {
		t.Errorf("the drop ripple starts at %v, want the pointer's %v", at, wantOrigin)
	}
}

// TestCursorSitsOnTheGridAfterGlyphRows checks the cursor overlay's horizontal
// position when the prompt's own row starts with fallback glyphs: the overlay
// sits on the cell's place in the grid, col*cellWidth, not on a drawn pen.
func TestCursorSitsOnTheGridAfterGlyphRows(t *testing.T) {
	const (
		glyphRows = 8
		glyphs    = 20
		width     = 900
		height    = 300
	)
	prefix := strings.Repeat("❄", glyphs)
	var text strings.Builder
	for i := 0; i < glyphRows; i++ {
		fmt.Fprintf(&text, "❄ row %02d\r\n", i)
	}
	// Two spans, so the prompt is painted as two runs, as a coloured prompt is.
	text.WriteString("\x1b[34m" + prefix + "\x1b[32mPROMPT> \x1b[0m")

	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{}, func(string) {})
	}, width, height)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}

	rect, ok := findTextPrefix(tester, prefix+"PROMPT>")
	if !ok {
		t.Fatalf("the prompt row is not drawn: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	if cellW <= 0 {
		t.Fatal("the live cell was not measured")
	}
	// The prompt starts at column 20 and is eight grid cells ("PROMPT>" and
	// the cursor's blank); the cursor sits at column 28.
	wantLeft := float32(rect.X) + (glyphs+8)*cellW
	top, bottom, ok := cursorBandNear(tester.Image(), int(rect.X)-2, int(rect.Y))
	if !ok {
		t.Fatalf("no cursor is drawn on the prompt row at (%.1f,%.1f)", rect.X, rect.Y)
	}
	left, right := cursorXRange(tester.Image(), top, bottom, int(rect.X)-2)
	if left < wantLeft-2 || left > wantLeft+2 {
		t.Fatalf("the cursor is not on the cursor cell: left %.1f, want %.1f (cell %.3f)",
			left, wantLeft, cellW)
	}
	if got := right - left + 1; got < cellW-2 || got > cellW+2 {
		t.Errorf("the cursor width is wrong: %.0f..%.0f (%.0f DIP), want %.1f", left, right, right-left+1, cellW)
	}
}

// TestPointerNamesTheFirstColumnOfAWideRune checks the grid bookkeeping apart
// from the glyph: a wide rune is one glyph over two grid columns, and a press on
// its right half names the column it starts in, as a selection there must take
// the whole rune.
func TestPointerNamesTheFirstColumnOfAWideRune(t *testing.T) {
	const (
		width  = 900
		height = 300
	)
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte("A你B"))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var ripple effects.Ripple
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			&ripple, Bell{Col: -1, Row: -1},
			Tail{}, Cursor{Style: terminal.CursorBlock, Color: ui.RGBA(255, 0, 0, 1)},
			nil, Hooks{}, func(string) {})
	}, width, height)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}

	rect, ok := findTextPrefix(tester, "A你B")
	if !ok {
		t.Fatalf("the wide-rune row is not drawn: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	if cellW <= 0 {
		t.Fatal("the cell was not measured")
	}
	// The right half of 你: column 2, the wide rune's continuation column.
	x := float32(rect.X) + 2.25*cellW
	y := float32(rect.Y) + float32(rect.H)/2
	tester.Press(x, y)
	tester.Release(x, y)

	if !ripple.StartDrop(tester.Now()) {
		t.Fatal("the press did not reach the live rows")
	}
	col, row := ripple.Cell()
	if row != 0 || col != 1 {
		t.Fatalf("the press landed on (%d,%d), want the wide rune's first column (1,0)", col, row)
	}
}

// TestPointerCellMapsTheGrid pins the mapping the pointer, selection and mouse
// reporting share: columns and rows are the measured cell, a wide rune's second
// column names its first, and columns past the row's text keep their own. A
// point outside the pane is clamped into it.
func TestPointerCellMapsTheGrid(t *testing.T) {
	cellWidth, cellHeight = 8, 16
	defer func() { cellWidth, cellHeight = 0, 0 }()
	lines := [][]ui.Span{
		{{Text: "A你B", Font: "monospace"}},
		{{Text: "plain", Font: "monospace"}},
	}
	for _, c := range []struct {
		x, y     float32
		cols     int
		col, row int
	}{
		{x: 0, y: 0, cols: 40, col: 0, row: 0},
		{x: 8.5, y: 0, cols: 40, col: 1, row: 0},
		{x: 17, y: 0, cols: 40, col: 1, row: 0},   // the wide rune's second column
		{x: 24.5, y: 0, cols: 40, col: 3, row: 0}, // B
		{x: 32.5, y: 0, cols: 40, col: 4, row: 0}, // past the text
		{x: -3, y: -4, cols: 40, col: 0, row: 0},
		{x: 5, y: 16.5, cols: 40, col: 0, row: 1},
		{x: 5, y: 33, cols: 40, col: 0, row: 2}, // a blank grid row below the drawn ones
		{x: 400, y: 0, cols: 30, col: 29, row: 0},
		{x: 8.5, y: 0, cols: 3, col: 1, row: 0},
	} {
		col, row, ok := pointerCell(lines, c.x, c.y, c.cols)
		if !ok {
			t.Fatalf("pointerCell(%v,%v) had no cells", c.x, c.y)
		}
		if col != c.col || row != c.row {
			t.Errorf("pointerCell(%v,%v) = (%d,%d), want (%d,%d)", c.x, c.y, col, row, c.col, c.row)
		}
	}
	// Columns past the grid's width are clamped before the cluster lookup.
	if col, _, _ := pointerCell(lines, 400, 0, 2); col != 1 {
		t.Errorf("a point past a two-column grid named column %d, want 1", col)
	}
	// Without a measurement there is no cell to name.
	cellWidth, cellHeight = 0, 0
	if _, _, ok := pointerCell(lines, 10, 10, 40); ok {
		t.Errorf("an unmeasured cell reported a position")
	}
}

// TestCellOriginUsesTheCellGrid pins where a cell-named ripple (the bell)
// starts: the centre of the cell in the pane, from the same cell arithmetic the
// rows use.
func TestCellOriginUsesTheCellGrid(t *testing.T) {
	cellWidth, cellHeight = 8, 16
	defer func() { cellWidth, cellHeight = 0, 0 }()
	got := cellOrigin(2, 1, 40)
	want := ui.Point{X: livePad + 2*cellWidth + cellWidth/2, Y: livePad + cellHeight + cellHeight/2}
	if got != want {
		t.Errorf("cell (2,1) starts at %v, want its grid centre %v", got, want)
	}
	if got := cellOrigin(-1, -1, 40); got != (ui.Point{X: livePad + cellWidth/2, Y: livePad + cellHeight/2}) {
		t.Errorf("a negative cell starts at %v, want the first cell", got)
	}
	if got := cellOrigin(40, 0, 40); got != (ui.Point{X: livePad + 39*cellWidth + cellWidth/2, Y: livePad + cellHeight/2}) {
		t.Errorf("a column past the grid starts at %v, want the last column", got)
	}
}

// TestRowHeightsAreTheCell pins the layout the pointer and cursor rely on: a
// row is one measured cell tall, and the next row starts exactly one cell
// below it, whatever glyphs the rows hold.
func TestRowHeightsAreTheCell(t *testing.T) {
	const width, height = 900, 300
	var text strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&text, "❄ %s row %02d\r\n", "你", i)
	}
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte(text.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil, Hooks{}, func(string) {})
	}, width, height)
	for i := 0; i < 2; i++ {
		tester.Frame()
	}
	_, cellH := CellSize()
	if cellH <= 0 {
		t.Fatal("the cell was not measured")
	}
	var lastY float32
	for i := 0; i < 8; i++ {
		rect, ok := findTextPrefix(tester, fmt.Sprintf("❄ 你 row %02d", i))
		if !ok {
			t.Fatalf("row %d is not drawn: %q", i, tester.Texts())
		}
		if rect.H < cellH-0.5 || rect.H > cellH+0.5 {
			t.Errorf("row %d is %.3f tall, want the cell %.3f", i, rect.H, cellH)
		}
		if i > 0 {
			if d := rect.Y - lastY; d < cellH-0.5 || d > cellH+0.5 {
				t.Errorf("row %d starts %.3f below row %d, want the cell %.3f", i, d, i-1, cellH)
			}
		}
		lastY = rect.Y
	}
}
