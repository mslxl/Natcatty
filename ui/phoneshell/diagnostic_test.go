//go:build natcatty_smoke

package phoneshell

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal"
)

// TestDiagnosticRunsEchoTransport opens the diagnostic from Sessions and
// checks the page's terminal holds the text the echo connection returned.
// The terminal suites assert the styles and cursor on the same corpus; this
// test pins the page seam CI drives on the simulator.
func TestDiagnosticRunsEchoTransport(t *testing.T) {
	if os.Getenv("NATCATTY_GHOSTTY_VT") == "" && os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("set NATCATTY_GHOSTTY_VT to a libghostty-vt build")
	}
	shell := New()
	tt := ui.NewTester(shell.View(Sessions), 390, 844)
	if err := tt.Click("Terminal diagnostic"); err != nil {
		t.Fatalf("click Terminal diagnostic: %v", err)
	}
	if got := shell.Router(Sessions).Path(); got != "/diagnostic" {
		t.Fatalf("path = %q, want /diagnostic", got)
	}
	term, err := diagnosticTerminal()
	if err != nil {
		t.Fatalf("the diagnostic terminal did not start: %v", err)
	}
	// The echo delivers on the terminal's reader goroutine; frame until the
	// text arrives, with a deadline rather than a fixed wait.
	deadline := time.Now().Add(time.Minute)
	for term.Text() != terminal.VTCorpusText {
		if time.Now().After(deadline) {
			t.Fatalf("the diagnostic never showed the echo text; it has %q", term.Text())
		}
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if v, err := terminal.LibraryVersion(); err != nil || v != "0.1.0-dev" {
		t.Fatalf("library version = %q, %v; want 0.1.0-dev", v, err)
	}
}
