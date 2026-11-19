package terminal

import (
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/ui/terminal/echoconn"
	"github.com/mslxl/natcatty/ui/terminal/internal/vt"
)

// waitText waits until the terminal's text is want, polling with a hard
// deadline: the echo transport delivers on the terminal's reader goroutine.
func waitText(t *testing.T, term *Terminal, want string) {
	t.Helper()
	deadline := time.Now().Add(waitGuard)
	for term.Text() != want {
		if time.Now().After(deadline) {
			t.Fatalf("the terminal's text never became %q; it is %q", want, term.Text())
		}
		time.Sleep(time.Millisecond)
	}
}

// styleRun is a run of cells of one style and the text they hold.
type styleRun struct {
	text  string
	style vt.Style
}

// styleRuns reads a row of the render state as maximal runs of one style,
// with the blank cells after the row's text left out.
func styleRuns(r *vt.RenderState, y int) []styleRun {
	r.Rows()
	for i := 0; i <= y; i++ {
		if !r.NextRow() {
			return nil
		}
	}
	cells := r.RowCells()
	// The cells after the row's text are empty; the library still gives
	// them the style of the run before, so cut them off first.
	end := len(cells)
	for end > 0 && cells[end-1].Codepoint == 0 && cells[end-1].Wide != vt.SpacerTail {
		end--
	}
	var runs []styleRun
	for x, c := range cells[:end] {
		if c.Wide == vt.SpacerTail {
			continue
		}
		text := " "
		if c.Codepoint != 0 {
			text = string(c.Codepoint)
		}
		st := r.CellStyle(x)
		if n := len(runs); n > 0 && runs[n-1].style == st {
			runs[n-1].text += text
			continue
		}
		runs = append(runs, styleRun{text: text, style: st})
	}
	return runs
}

func red() vt.Style { return vt.Style{FG: vt.Color{Kind: 1, Index: 1}} }
func greenBold() vt.Style {
	return vt.Style{FG: vt.Color{Kind: 1, Index: 2}, Bold: true}
}
func truecolor() vt.Style {
	return vt.Style{FG: vt.Color{Kind: 2, RGB: vt.RGB{R: 1, G: 2, B: 3}}}
}
func inverse() vt.Style { return vt.Style{Inverse: true} }
func blueBG() vt.Style  { return vt.Style{BG: vt.Color{Kind: 1, Index: 4}} }

// TestVTCorpus checks the whole chain a phone relies on: bytes written to
// the echo connection arrive in the native library, and the render state
// reports the expected text, style runs, cursor cell and cursor shape. A
// correct-text-but-wrong-colour or misplaced-cursor change fails here.
func TestVTCorpus(t *testing.T) {
	loadLib(t)
	conn := echoconn.New()
	term, err := New(Options{Conn: conn, NoBlink: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, err := conn.Write([]byte(VTCorpus)); err != nil {
		t.Fatal(err)
	}
	waitText(t, term, VTCorpusText)

	term.mu.Lock()
	defer term.mu.Unlock()
	rs := term.rs
	if err := rs.Update(term.term); err != nil {
		t.Fatal(err)
	}

	// The text of every row, as the cells hold it.
	var rows []string
	rs.Rows()
	for rs.NextRow() {
		var b strings.Builder
		for _, c := range rs.RowCells() {
			switch {
			case c.Wide == vt.SpacerTail:
			case c.Codepoint == 0:
				b.WriteByte(' ')
			default:
				b.WriteRune(c.Codepoint)
			}
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	wantRows := []string{"hello", "red green true inv", "blue-bg plain"}
	for i, want := range wantRows {
		if got := rows[i]; got != want {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}

	wantRuns := [][]styleRun{
		{{text: "hello", style: vt.Style{}}},
		{
			{text: "red", style: red()},
			{text: " ", style: vt.Style{}},
			{text: "green", style: greenBold()},
			{text: " ", style: vt.Style{}},
			{text: "true", style: truecolor()},
			{text: " ", style: vt.Style{}},
			{text: "inv", style: inverse()},
		},
		{
			{text: "blue-bg", style: blueBG()},
			{text: " plain", style: vt.Style{}},
		},
	}
	for y, want := range wantRuns {
		got := styleRuns(rs, y)
		if len(got) != len(want) {
			t.Errorf("row %d style runs = %+v, want %+v", y, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("row %d run %d = %+v, want %+v", y, i, got[i], want[i])
			}
		}
	}

	c := rs.Cursor()
	if !c.InView || c.X != VTCorpusCursorX || c.Y != VTCorpusCursorY {
		t.Errorf("cursor is at %d,%d in view %v, want %d,%d", c.X, c.Y, c.InView, VTCorpusCursorX, VTCorpusCursorY)
	}
	if !c.Visible || c.Style != vt.CursorBar || c.Blinking {
		t.Errorf("cursor is visible=%v style=%d blinking=%v, want a steady bar", c.Visible, c.Style, c.Blinking)
	}
}

// TestVTCorpusThroughEcho pins that the corpus went through the echo
// transport rather than being fed straight in: a terminal whose connection
// never wrote has no text.
func TestVTCorpusThroughEcho(t *testing.T) {
	loadLib(t)
	conn := echoconn.New()
	term, err := New(Options{Conn: conn, NoBlink: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if got := term.Text(); got != "" {
		t.Fatalf("a fresh echo terminal has text %q", got)
	}
	if _, err := conn.Write([]byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	waitText(t, term, "hello")
}
