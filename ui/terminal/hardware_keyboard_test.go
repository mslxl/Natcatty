package terminal

// A hardware keyboard on a phone: the host sends each text key as a press,
// then its text, then the release, as macOS does. The view must send the
// character once, not once for the press and again for the text, and a
// Control combination, which comes with no text, must still reach the shell.

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestHardwareKeyboardTypesOnceAndControlReachesTheShell(t *testing.T) {
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 390, 300)
	tt.Frame()

	var sent strings.Builder
	typeKey := func(mods ui.Modifiers, key ui.Key, text string) {
		tt.TypeKey(mods, key, text)
		sent.WriteString(conn.take(term))
	}
	// A command with a repeated space and a symbol, every key typed the way
	// a hardware keyboard reports it.
	typeKey(0, ui.KeyL, "l")
	typeKey(0, ui.KeyS, "s")
	typeKey(0, ui.KeySpace, " ")
	typeKey(0, ui.KeySpace, " ")
	typeKey(0, ui.KeyMinus, "-")
	typeKey(0, ui.KeyL, "l")
	typeKey(0, ui.KeyA, "a")
	// Control combinations send no text; they are key presses only.
	tt.Key(ui.Ctrl, ui.KeyC)
	sent.WriteString(conn.take(term))
	tt.Key(ui.Ctrl, ui.KeyD)
	sent.WriteString(conn.take(term))

	if got, want := sent.String(), "ls  -la\x03\x04"; got != want {
		t.Fatalf("the shell got %q, want %q typed once each", got, want)
	}
}
