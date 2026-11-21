package phoneshell

// The phone terminal's copy, paste and scrollback controls: Copy puts the
// terminal's real selection on the system clipboard through MyGo's clipboard
// service, Paste hands the clipboard to the terminal's own paste path so
// bracketed paste mode is honoured with a real libghostty-vt terminal, and
// History/Latest moves the terminal's viewport into the scrollback and back
// without a gesture the grid also needs.

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/terminal"
)

// takeWritten returns and clears what the terminal wrote to the Session.
func (h *fakeHandle) takeWritten() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.written.String()
	h.written.Reset()
	return s
}

// newControlTerminal builds a real libghostty-vt terminal over a Session and a
// tester drawing only the accessory bar and its grid, as the session page does.
func newControlTerminal(t *testing.T, output string) (*terminal.Terminal, *ghosttyScreen, *ui.Tester, *fakeHandle) {
	t.Helper()
	handle := newFakeHandle(output)
	term, err := terminal.New(terminal.Options{Conn: handle})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	screen := &ghosttyScreen{term: term}
	acc := &accessoryState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			accessoryBar(c, acc, screen, nil)
			screen.View(c).Fill()
		})
	}, 390, 500)
	tt.Frame()
	// The terminal reads its connection in the background; wait on the text
	// it parsed, not a fixed delay.
	deadline := time.Now().Add(5 * time.Second)
	for strings.TrimSpace(term.Text()) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("terminal text = %q, want the Session's output", term.Text())
		}
		tt.Frame()
	}
	return term, screen, tt, handle
}

func TestPhoneAccessoryCopiesTheTerminalSelection(t *testing.T) {
	term, _, tt, _ := newControlTerminal(t, "hello world\r\nsecond line\r\n")
	// Focus the grid and select it all with the terminal's own selection API,
	// as a mouse or long press would.
	tt.ClickAt(20, 200)
	tt.Command("selectAll")
	tt.Frame()
	if text, ok := term.SelectionText(); !ok || !strings.Contains(text, "hello world") {
		t.Fatalf("the terminal has no selection: %q (%v)", text, ok)
	}

	tt.SetClipboard("stale")
	if err := tt.Click("Copy"); err != nil {
		t.Fatalf("click Copy: %v", err)
	}
	tt.Frame()
	if got := tt.Clipboard(); !strings.Contains(got, "hello world") || !strings.Contains(got, "second line") {
		t.Fatalf("Copy put %q on the system clipboard, want the selection", got)
	}
	if !tt.Focused("Terminal") {
		t.Fatal("Copy took the keyboard focus from the terminal")
	}
}

func TestPhoneAccessoryPastesRespectingBracketedPaste(t *testing.T) {
	term, _, tt, handle := newControlTerminal(t, "ready\r\n")
	const text = "line one\nline two"
	// Focus the terminal, as a tap does, so the bar's KeepFocus buttons can be
	// seen to keep it.
	tt.ClickAt(20, 200)
	tt.Frame()
	if !tt.Focused("Terminal") {
		t.Fatal("the grid did not take the focus")
	}

	// Without bracketed paste mode the payload is typed as it is pasted: a
	// line break becomes Return, which would run each line as a command.
	tt.SetClipboard(text)
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	waitWritten(t, handle, "line one\rline two", "the plain paste")

	// A program that enabled bracketed paste (mode 2004), as a shell or an
	// editor does, gets the payload wrapped, so a multi-line paste is not run
	// as separate commands.
	term.Feed([]byte("\x1b[?2004h"))
	tt.Frame()
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	waitWritten(t, handle, "\x1b[200~"+text+"\x1b[201~", "the bracketed paste")
	if !tt.Focused("Terminal") {
		t.Fatal("Paste took the keyboard focus from the terminal")
	}
}

func TestPhoneAccessoryScrollbackToggles(t *testing.T) {
	_, screen, tt, _ := newControlTerminal(t, "ready\r\n")
	term := screen.term
	for i := range 60 {
		term.Feed([]byte(strings.Repeat("x", 20) + "\r\n"))
		_ = i
	}
	tt.Frame()
	if screen.InScrollback() {
		t.Fatal("the viewport starts in the scrollback")
	}
	if err := tt.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	tt.Frame()
	if !screen.InScrollback() {
		t.Fatal("History did not enter the scrollback")
	}
	if !tt.HasText("Latest") {
		t.Fatalf("the control does not offer the way back; texts: %q", tt.Texts())
	}
	if err := tt.Click("Latest"); err != nil {
		t.Fatalf("click Latest: %v", err)
	}
	tt.Frame()
	if screen.InScrollback() {
		t.Fatal("Latest did not return to the live screen")
	}
}

// TestAccessoryControlsWithFakeScreen covers the wiring without the native
// library, so a plain `go test` run still sees the controls exist and call the
// screen seam.
func TestAccessoryControlsWithFakeScreen(t *testing.T) {
	handle := newFakeHandle("")
	screen := &fakeScreen{handle: handle, selected: "picked text"}
	acc := &accessoryState{}
	tt := ui.NewTester(func(c *ui.Context) { accessoryBar(c, acc, screen, nil) }, 390, 200)
	tt.Frame()

	if err := tt.Click("Copy"); err != nil {
		t.Fatalf("click Copy: %v", err)
	}
	if got := tt.Clipboard(); got != "picked text" {
		t.Fatalf("Copy put %q on the clipboard", got)
	}

	tt.SetClipboard("from clipboard")
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	if text, _ := screen.pasted.Read(transfer.Text); string(text) != "from clipboard" {
		t.Fatalf("Paste handed the screen %q", text)
	}

	if err := tt.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	tt.Frame()
	if !screen.scrolled || !tt.HasText("Latest") {
		t.Fatalf("History did not enter the scrollback: scrolled=%v texts=%q", screen.scrolled, tt.Texts())
	}
	if err := tt.Click("Latest"); err != nil {
		t.Fatalf("click Latest: %v", err)
	}
	tt.Frame()
	if screen.scrolled {
		t.Fatal("Latest did not return to the live screen")
	}
}

// TestAccessoryPasteProtectionAsksFirst drives the phone paste path with the
// shared PasteProtection rule: a multi-line payload is held and shown with a
// line count until the user confirms, a cancel discards it, and protection
// off sends the next paste directly.
func TestAccessoryPasteProtectionAsksFirst(t *testing.T) {
	handle := newFakeHandle("")
	screen := &fakeScreen{handle: handle}
	acc := &accessoryState{}
	protect := true
	guard := func(text string) bool {
		return domainterminal.ShouldConfirmPaste(text, protect, 2)
	}
	tt := ui.NewTester(func(c *ui.Context) { accessoryBar(c, acc, screen, guard) }, 390, 300)
	tt.Frame()

	tt.SetClipboard("one\ntwo\nthree")
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	tt.Frame()
	if _, err := screen.pasted.Read(transfer.Text); err == nil {
		t.Fatal("the paste reached the terminal before the user confirmed")
	}
	if !tt.HasText("Paste 3 lines?") {
		t.Fatalf("the confirmation does not name the line count; texts: %q", tt.Texts())
	}
	if err := tt.Click("Paste anyway"); err != nil {
		t.Fatalf("click Paste anyway: %v", err)
	}
	tt.Frame()
	if text, err := screen.pasted.Read(transfer.Text); err != nil || string(text) != "one\ntwo\nthree" {
		t.Fatalf("confirmed paste = %q (%v), want the clipboard text", text, err)
	}

	// Cancel keeps the held text from ever reaching the terminal.
	screen.pasted = transfer.Data{}
	tt.SetClipboard("left\nright")
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tt.Frame()
	if _, err := screen.pasted.Read(transfer.Text); err == nil {
		t.Fatal("a cancelled paste still reached the terminal")
	}

	// Protection off is the original's default: the next paste is direct.
	protect = false
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste with protection off: %v", err)
	}
	tt.Frame()
	if text, err := screen.pasted.Read(transfer.Text); err != nil || string(text) != "left\nright" {
		t.Fatalf("unguarded paste = %q (%v), want the clipboard text", text, err)
	}
}

// waitWritten waits until exactly want has reached the Session.
func waitWritten(t *testing.T, handle *fakeHandle, want, what string) {
	t.Helper()
	var got strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for {
		got.WriteString(handle.takeWritten())
		if got.String() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s wrote %q, want %q", what, got.String(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
