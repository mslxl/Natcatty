package terminal

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// SendKey is what the phone accessory bar uses: it must take the same
// encoder path as a hardware key, so Control combinations, application
// cursor mode and the Kitty keyboard protocol apply to a bar key too.

func TestSendKeyEncodesPlainAndControlKeys(t *testing.T) {
	term, conn, _ := newClientTerm(t)
	term.SendKey(ui.KeyEscape, 0)
	term.SendKey(ui.KeyTab, 0)
	term.SendKey(ui.KeyC, ui.Ctrl)
	term.SendKey(ui.KeySlash, 0)
	// | is Shift+\ on a US layout.
	term.SendKey(ui.KeyBackslash, ui.Shift)
	if got := conn.take(term); got != "\x1b\t\x03/|" {
		t.Fatalf("bar keys sent %q, want %q", got, "\x1b\t\x03/|")
	}
}

func TestSendKeyFollowsApplicationCursorMode(t *testing.T) {
	term, conn, _ := newClientTerm(t)
	term.Feed([]byte("\x1b[?1h"))
	term.SendKey(ui.KeyUp, 0)
	if got := conn.take(term); got != "\x1bOA" {
		t.Fatalf("Up in application cursor mode sent %q, want %q", got, "\x1bOA")
	}
	term.Feed([]byte("\x1b[?1l"))
	term.SendKey(ui.KeyUp, 0)
	if got := conn.take(term); got != "\x1b[A" {
		t.Fatalf("Up in normal cursor mode sent %q, want %q", got, "\x1b[A")
	}
}

func TestSendKeyIgnoresAKeyWithNoEncoding(t *testing.T) {
	term, conn, _ := newClientTerm(t)
	term.SendKey(ui.KeyUnknown, 0)
	if got := conn.take(term); got != "" {
		t.Fatalf("an unknown key sent %q", got)
	}
}
