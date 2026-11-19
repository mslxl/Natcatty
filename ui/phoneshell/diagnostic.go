//go:build natcatty_smoke

// The terminal diagnostic: a natcatty_smoke build opens it from Sessions and
// it runs the whole chain with no network — the staged object, the static
// library, cgo symbol addresses, the grid and the paint.
//
// It runs an in-memory echo connection (a test transport, not a Session
// kind: no PTY, no local shell), writes the shared VT corpus through it and
// draws the terminal. The build is a CI and simulator probe only; it is not
// part of a shipped phone app.
package phoneshell

import (
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/echoconn"
)

var diagnostic struct {
	once     sync.Once
	term     *terminal.Terminal
	conn     *echoconn.Conn
	version  string
	err      error
	logged   bool
	loggedMu sync.Mutex
}

// smokePages adds the diagnostic to Sessions.
func smokePages(sec Section) []page {
	if sec != Sessions {
		return nil
	}
	return []page{{path: "/diagnostic", title: "Terminal diagnostic",
		text: "The terminal diagnostic shows the echo connection's cells."}}
}

// smokeRoute serves the diagnostic page.
func smokeRoute(c *ui.Context, r *ui.Route) bool {
	if !r.Match("/diagnostic") {
		return false
	}
	r.Title("Terminal diagnostic")
	smokeDiagnostic(c)
	return true
}

func diagnosticTerminal() (*terminal.Terminal, error) {
	diagnostic.once.Do(func() {
		if err := terminal.Load(); err != nil {
			diagnostic.err = err
			return
		}
		v, err := terminal.LibraryVersion()
		if err != nil {
			diagnostic.err = err
			return
		}
		diagnostic.version = v
		conn := echoconn.New()
		term, err := terminal.New(terminal.Options{Conn: conn, NoBlink: true})
		if err != nil {
			diagnostic.err = err
			return
		}
		diagnostic.conn, diagnostic.term = conn, term
		if _, err := conn.Write([]byte(terminal.VTCorpus)); err != nil {
			diagnostic.err = err
		}
	})
	return diagnostic.term, diagnostic.err
}

func smokeDiagnostic(c *ui.Context) {
	term, err := diagnosticTerminal()
	if err != nil {
		ui.Column(c).Fill().Padding(16).Gap(8).Children(func() {
			ui.Text(c, "Terminal unavailable").Bold()
			ui.Text(c, err.Error()).TextColor(c.Theme().TextMuted)
		})
		return
	}
	// The echo delivers on the terminal's reader goroutine. Once the bytes
	// come back, log the text the render state holds: CI greps this line.
	if text := term.Text(); text == terminal.VTCorpusText {
		diagnostic.loggedMu.Lock()
		if !diagnostic.logged {
			diagnostic.logged = true
			log.Printf("natcatty_smoke: libghostty-vt %s, echo text %q", diagnostic.version, text)
		}
		diagnostic.loggedMu.Unlock()
	} else {
		c.After(10 * time.Millisecond)
	}
	ui.Column(c).Fill().Children(func() {
		ui.Text(c, "Echo connection — libghostty-vt "+diagnostic.version).
			Padding(8, 12).TextColor(c.Theme().TextMuted)
		terminal.View(c, term).Fill()
	})
}
