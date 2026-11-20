package appview

import (
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// newKeyEchoPane builds a live Pane wired like a Session's: the keyboard has a
// text-input client whose commits go straight to the program, as the shell
// reads them. input is the Session's Input hook (autocomplete), nil when the
// test has none.
func newKeyEchoPane(t *testing.T, input func(ui.InputEvent) bool) (*nopConn, *ui.Tester) {
	t.Helper()
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	term.Resize(40, 6)

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	client := NewInputClient(func(b []byte) { term.Send(b) })
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 6}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil,
			Hooks{Key: "key-echo", Focus: true, TextInput: client, Input: input},
			func(string) {})
	}, 900, 300)
	tester.Frame()
	return conn, tester
}

// waitForProgram waits until the program received want, then gives a duplicate
// that might follow the same key's commit time to arrive. Without the pause a
// second byte sent after the first would race the assertion.
func waitForProgram(t *testing.T, conn *nopConn, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && conn.Written() != want {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := conn.Written(); got != want {
		t.Fatalf("program received %q, want %q", got, want)
	}
}

// A key the Pane encodes reaches the program once. On macOS the key down of a
// key that types text is followed by the characters AppKit commits; for an
// Option-modified letter the Pane has already sent Meta-letter, so the
// composed character must not follow it. Tab and Return are command selectors
// on macOS and control characters that mygo drops on Windows; their cases pin
// the same contract for a platform that does commit them.
func TestPaneSendsAnEncodedKeyOnce(t *testing.T) {
	cases := []struct {
		name string
		mods ui.Modifiers
		key  ui.Key
		text string
		want string
	}{
		{"option-letter", ui.Alt, ui.KeyA, "å", "\x1ba"},
		{"tab", 0, ui.KeyTab, "\t", "\t"},
		{"enter", 0, ui.KeyEnter, "\r", "\r"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, tester := newKeyEchoPane(t, nil)
			tester.TypeKey(c.mods, c.key, c.text)
			waitForProgram(t, conn, c.want)
		})
	}
}

// A key the Pane leaves to the platform still types through the client: the
// echo suppression covers only keys that carried their own encoding.
func TestPaneSendsPlainTypingThroughTheTextClient(t *testing.T) {
	conn, tester := newKeyEchoPane(t, nil)
	tester.TypeKey(0, ui.KeyX, "x")
	waitForProgram(t, conn, "x")
}

// When a handler takes the key (the shell's line editor owns Option-A) and
// the platform still commits the character, nothing of that key may reach the
// program.
func TestPaneDropsTextOfAKeyItsHandlerTook(t *testing.T) {
	var took bool
	conn, tester := newKeyEchoPane(t, func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyA {
			took = true
			return true
		}
		return false
	})
	tester.TypeKey(ui.Alt, ui.KeyA, "å")
	time.Sleep(30 * time.Millisecond)
	if !took {
		t.Fatal("the Input hook did not see the Tab")
	}
	if got := conn.Written(); got != "" {
		t.Fatalf("a taken key still reached the program: %q", got)
	}
}
