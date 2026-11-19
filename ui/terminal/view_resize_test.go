package terminal

// What a phone's keyboard and rotation need from the terminal view: when the
// window loses room to the keyboard, or turns, the view fits a new grid and
// the Session is told the new column and row counts. The transports a phone
// uses have no window-change signal of their own, so this report is the only
// way the shell learns its size.

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// awaitSize waits for the view to report a size that ok accepts. Sizes the
// view passes through are skipped; the deadline is a hang guard.
func awaitSize(t *testing.T, sizes <-chan [2]int, what string, ok func([2]int) bool) [2]int {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-sizes:
			if ok(s) {
				return s
			}
		case <-deadline:
			t.Fatalf("the Session was never told the size of %s", what)
		}
	}
}

func TestViewReportsTheKeyboardInsetAndRotation(t *testing.T) {
	loadLib(t)
	conn := sizedPipe{newPipe(), make(chan [2]int, 16)}
	term, err := New(Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 400, 600)
	tt.Frame()

	initial := awaitSize(t, conn.sizes, "the whole window", func(s [2]int) bool {
		return s[0] > 0 && s[1] > 0
	})

	// The soft keyboard covers the bottom of the screen, as an inset the
	// window root lays the content out inside.
	tt.SetInsets(0, 0, 240, 0)
	shrunk := awaitSize(t, conn.sizes, "the window above the keyboard", func(s [2]int) bool {
		return s[1] < initial[1]
	})
	if shrunk[0] != initial[0] {
		t.Errorf("the keyboard changed the columns: %v -> %v", initial, shrunk)
	}

	// Rotation gives the keyboard back and swaps the window's sides. The
	// first report may still hold the inset, so wait for the turned size.
	tt.SetInsets(0, 0, 0, 0)
	tt.SetSize(600, 400)
	rotated := awaitSize(t, conn.sizes, "the rotated window", func(s [2]int) bool {
		return s[0] > initial[0] && s[1] < initial[1]
	})
	if rotated[0] <= initial[0] || rotated[1] >= initial[1] {
		t.Errorf("rotation reported %v, want more columns and fewer rows than %v", rotated, initial)
	}

	// The screen itself follows the view on a transport that has no size of
	// its own.
	if c, r := term.Size(); c != rotated[0] || r != rotated[1] {
		t.Errorf("the screen is %dx%d, want the view's %v", c, r, rotated)
	}
}
