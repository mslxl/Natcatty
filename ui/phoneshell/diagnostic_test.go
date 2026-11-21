//go:build natcatty_smoke

package phoneshell

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal"
)

// TestDiagnosticRunsEchoTransport opens the diagnostic the way the smoke app
// does — selecting Sessions and pushing /diagnostic on its router — and
// checks the page drew the cells the echo connection returned. Pushing the
// route, rather than calling the page function, proves the shell's own
// navigation reaches the page; Sessions draws its own root, so there is no
// row to click.
func TestDiagnosticRunsEchoTransport(t *testing.T) {
	if os.Getenv("NATCATTY_GHOSTTY_VT") == "" && os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("set NATCATTY_GHOSTTY_VT to a libghostty-vt build")
	}
	shell := New()
	tt := ui.NewTester(shell.View(Sessions), 390, 844)

	// The entry point app/smoke_mobile.go uses.
	shell.Select(Sessions)
	shell.Router(Sessions).Push("/diagnostic")
	tt.Frame()
	if got := shell.Router(Sessions).Path(); got != "/diagnostic" {
		t.Fatalf("path = %q, want /diagnostic", got)
	}

	// Frame until the page has drawn its header and the terminal's reader has
	// delivered the corpus. The deadline is a hang guard; delivery is on the
	// reader goroutine.
	deadline := time.Now().Add(time.Minute)
	for {
		if tt.HasText("Echo connection") && diagnostic.term != nil &&
			diagnostic.term.Text() == terminal.VTCorpusText {
			break
		}
		if time.Now().After(deadline) {
			got := "<no terminal>"
			if diagnostic.term != nil {
				got = diagnostic.term.Text()
			}
			t.Fatalf("the diagnostic page never showed the echo text; it has %q; page texts: %v",
				got, tt.Texts())
		}
		tt.Frame()
		time.Sleep(time.Millisecond)
	}

	// The page names the version the library reported, and it is the version
	// the bindings mirror.
	if v, err := terminal.LibraryVersion(); err != nil || v != "0.1.0-dev" {
		t.Fatalf("library version = %q, %v; want 0.1.0-dev", v, err)
	}
	if diagnostic.version != "0.1.0-dev" {
		t.Fatalf("diagnostic version = %q, want 0.1.0-dev", diagnostic.version)
	}
}
