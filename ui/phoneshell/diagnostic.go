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
	"encoding/hex"
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
	rec      *inputRecorder
	version  string
	err      error
	logged   bool
	loggedMu sync.Mutex
	shown    string
}

// inputRecorder keeps the bytes the terminal writes to its connection, which
// is what a shell or PTY would receive. The rendered grid cannot tell a
// carriage return from a line feed, so a test that only reads the terminal
// text cannot prove what the on-screen Return key sent; the diagnostic
// shows these bytes for the simulator's keyboard test to read.
type inputRecorder struct {
	*echoconn.Conn
	mu sync.Mutex
	n  []byte
	on bool
}

func (r *inputRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	if r.on {
		r.n = append(r.n, p...)
	}
	r.mu.Unlock()
	return r.Conn.Write(p)
}

// record starts copying after the corpus has been queued as output, so the
// bytes held are only what the user typed.
func (r *inputRecorder) record() {
	r.mu.Lock()
	r.on = true
	r.mu.Unlock()
}

// hex is the bytes typed so far, as the diagnostic shows them: a keyboard
// test reads this through accessibility, where a rendered terminal cell
// cannot say whether Return was CR or LF.
func (r *inputRecorder) hex() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return hex.EncodeToString(r.n)
}

// smokeRoute serves the diagnostic page. It is a route, not a row: the smoke
// app opens it with Router(Sessions).Push("/diagnostic"), and Sessions' root
// is drawn by its own rootDraw, so a page in sec.pages could never be shown
// or clicked. The route is what makes the page reachable and lets a test
// drive the same navigation the app uses.
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
		conn := &inputRecorder{Conn: echoconn.New()}
		term, err := terminal.New(terminal.Options{Conn: conn, NoBlink: true})
		if err != nil {
			diagnostic.err = err
			return
		}
		diagnostic.conn, diagnostic.term, diagnostic.rec = conn.Conn, term, conn
		if _, err := conn.Write([]byte(terminal.VTCorpus)); err != nil {
			diagnostic.err = err
			return
		}
		conn.record()
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
	// The keyboard test reads what the terminal wrote to the connection
	// through this label: the echo connection proves the text arrived, but
	// not whether Return was CR or LF. A write happens on the terminal's
	// goroutine, so ask for one more frame when the bytes change.
	input := diagnostic.rec.hex()
	if input != diagnostic.shown {
		diagnostic.shown = input
		c.After(10 * time.Millisecond)
	}
	ui.Column(c).Fill().Children(func() {
		ui.Text(c, "Echo connection — libghostty-vt "+diagnostic.version).
			Padding(8, 12).TextColor(c.Theme().TextMuted)
		ui.Text(c, "input-bytes: "+input).Padding(0, 12).TextColor(c.Theme().TextMuted)
		terminal.View(c, term).Fill()
	})
}
