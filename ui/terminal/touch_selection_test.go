package terminal

// Touch selection on a phone: a long press selects the word under the finger,
// a drag extends it, the highlight shows while the finger is still down, a tap
// focuses the grid without pressing a selection and leaves a composition alone,
// and a tap reaches a program that takes the mouse.

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// touchDown starts a finger that is both a touch and the pointer a device
// makes of it, as phones send both. The touch lets a view follow the finger
// (Element.Touches); the pointer focuses the grid and clicks it.
func touchDown(tt *ui.Tester, x, y float32) {
	tt.TouchDown(1, x, y)
	tt.FingerDown(x, y)
}

func touchMove(tt *ui.Tester, x, y float32) {
	tt.TouchMove(1, x, y)
	tt.FingerMove(x, y)
}

func touchUp(tt *ui.Tester, x, y float32) {
	tt.TouchUp(1, x, y)
	tt.FingerUp(x, y)
}

func TestTouchLongPressDragSelects(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("hello world\r\nsecond line\r\nthird line"))
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 400, 300)
	tt.Frame()
	before := tt.Image()

	x, y := cellCenter(term, 1, 0)
	touchDown(tt, x, y)
	// Held still, the finger long-presses; a frame at the delay begins the
	// selection even though the finger has not moved.
	tt.Advance(touchSelectDelay + 100*time.Millisecond)
	text, ok := term.SelectionText()
	if !ok || !strings.Contains(text, "hello") {
		t.Fatalf("long press selected %q (%v), want the word hello", text, ok)
	}
	if strings.Contains(text, "second") {
		t.Fatalf("long press selected past its word: %q", text)
	}
	// The highlight shows while the finger is still down.
	v := term.v
	selected := tt.Image().RGBAAt(v.ox+v.cellW/2, v.oy+1)
	plain := before.RGBAAt(v.ox+v.cellW/2, v.oy+1)
	if selected == plain {
		t.Fatalf("the selected cell is not highlighted: both %v", selected)
	}

	// Dragging extends the selection over the next word.
	x2, y2 := cellCenter(term, 8, 0)
	touchMove(tt, x2, y2)
	text, ok = term.SelectionText()
	if !ok || !strings.Contains(text, "hello") || !strings.Contains(text, "world") {
		t.Fatalf("drag selected %q (%v), want hello world", text, ok)
	}
	touchUp(tt, x2, y2)
	tt.Frame()
	if text, ok := term.SelectionText(); !ok || !strings.Contains(text, "world") {
		t.Fatalf("the selection is gone after the finger lifts: %q (%v)", text, ok)
	}
}

// TestTouchLongPressKeepsFocusAndComposition is the ticket's promise that a
// selection does not steal the keyboard or break an input method's composition.
func TestTouchLongPressKeepsFocusAndComposition(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("hello world"))
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 400, 300)
	tt.Frame()
	tt.Compose("ni", 2)
	if _, ok := term.v.MarkedRange(); !ok {
		t.Fatal("the composition did not begin")
	}

	x, y := cellCenter(term, 0, 0)
	touchDown(tt, x, y)
	tt.Advance(touchSelectDelay + 100*time.Millisecond)
	x2, y2 := cellCenter(term, 4, 0)
	touchMove(tt, x2, y2)
	touchUp(tt, x2, y2)
	tt.Frame()

	if !tt.Focused("Terminal") {
		t.Fatal("the touch selection took the terminal's focus")
	}
	if active, _ := tt.TextInputActive(); !active {
		t.Fatal("the terminal is no longer a text target after selecting")
	}
	if _, ok := term.v.MarkedRange(); !ok {
		t.Fatal("selecting released the composition")
	}
	if got := term.v.preedit; got != "ni" {
		t.Fatalf("the composition after selecting is %q, want ni", got)
	}
}

// TestTouchTapReportsMouseMode: a tap on a program that asks for the mouse
// reaches it as a pointer press and release, so tmux panes and editor splits
// respond to a finger.
func TestTouchTapReportsMouseMode(t *testing.T) {
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 400, 300)
	term.Feed([]byte("\x1b[?1000h\x1b[?1006h"))
	tt.Frame()

	x, y := cellCenter(term, 2, 3)
	touchDown(tt, x, y)
	touchUp(tt, x, y)
	got := conn.take(term)
	if !strings.Contains(got, "\x1b[<0;3;4M") || !strings.Contains(got, "\x1b[<0;3;4m") {
		t.Fatalf("a tap sent %q, want a press and a release at 3;4", got)
	}
	if _, ok := term.SelectionText(); ok {
		t.Fatal("a tap selected text instead of reaching the program")
	}
}

// TestTouchTapReportsOnce: the platform may deliver the touch and the pointer
// it makes of it in either order. Whichever lifts first, the program sees one
// press and one release, even where an older selection is showing.
func TestTouchTapReportsOnce(t *testing.T) {
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 400, 300)
	term.Feed([]byte("hello world\x1b[?1000h\x1b[?1006h"))
	tt.Command("selectAll")
	tt.Frame()

	x, y := cellCenter(term, 2, 3)
	tt.TouchDown(1, x, y)
	tt.FingerDown(x, y)
	// The touch lifts before the pointer the platform makes of it.
	tt.TouchUp(1, x, y)
	tt.FingerUp(x, y)
	tt.Frame()
	got := conn.take(term)
	if n := strings.Count(got, ";3;4M"); n != 1 {
		t.Fatalf("a tap sent %d presses in %q, want 1", n, got)
	}
	if n := strings.Count(got, ";3;4m"); n != 1 {
		t.Fatalf("a tap sent %d releases in %q, want 1", n, got)
	}
}

// TestTouchTapDismissesSelection: without mouse reporting a tap keeps the
// grid's normal behaviour, focusing it and dismissing a selection, and does not
// start one of its own.
func TestTouchTapDismissesSelection(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("hello world"))
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 400, 300)
	tt.Frame()

	x, y := cellCenter(term, 1, 0)
	touchDown(tt, x, y)
	tt.Advance(touchSelectDelay + 100*time.Millisecond)
	if _, ok := term.SelectionText(); !ok {
		t.Fatal("the long press made no selection to dismiss")
	}
	touchUp(tt, x, y)
	tt.Frame()

	x, y = cellCenter(term, 5, 0)
	touchDown(tt, x, y)
	touchUp(tt, x, y)
	tt.Frame()
	if _, ok := term.SelectionText(); ok {
		t.Fatal("a tap did not dismiss the selection")
	}
	if !tt.Focused("Terminal") {
		t.Fatal("a tap did not focus the terminal")
	}
}
