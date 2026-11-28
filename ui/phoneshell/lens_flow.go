package phoneshell

// The phone's inline Lens flow. It is the same pipeline the desktop runs —
// the shared output tap fills a lens model, the shared lens engine claims
// commands, and ui/terminal/appview lays the finished blocks and the live
// screen in one scroll space — so a command block scrolls with the terminal
// and the global Lens terminal mode decides whether the flow is shown
// (ADR-0022). Nothing here re-implements a lens or a link rule.
//
// The phone has no application Session subscription on its screen seam: the
// screen owns the Session view connection, so the tap is fed by wrapping the
// connection the terminal itself reads. A private terminal mirror is fed the
// same bytes purely to record the shell's OSC 133;B prompt row at the exact
// marker, as the desktop does.

import (
	"io"
	"strings"
	"sync"

	"github.com/mslxl/natcatty/application/lens/lens"
	_ "github.com/mslxl/natcatty/application/lens/lens/builtin"
	lensmodel "github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/application/lens/vt"
	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// phoneLensScrollback is the terminal's scrollback, and the mirror keeps the
// same amount. A row recorded at a marker then names the same replay row the
// view draws.
const phoneLensScrollback = 256 << 10

// phoneLensFlow owns one phone terminal's command blocks.
type phoneLensFlow struct {
	store  *lensmodel.Store
	engine *lens.Engine
	tap    *vt.Tap
	// redraw requests a frame after output. It is set once the screen first
	// draws, from the Context's own invalidate, which is safe from any
	// goroutine; the terminal's reader is the caller.
	redrawMu sync.RWMutex
	redraw   func()

	// ready closes once the mirror is bound, so the terminal's reader cannot
	// feed the tap before its boundary mirror exists.
	ready     chan struct{}
	readyOnce sync.Once

	mu         sync.Mutex
	mirror     *terminal.Terminal
	mirrorConn *lensMirrorConn
	mirrorSize appview.Size
	scanner    vt.BellScanner
	boundary   phoneOSC133Scanner

	promptRow          int
	promptKnown        bool
	promptAfter        bool
	commandSincePrompt bool
}

// newPhoneLensFlow builds the tap and the store over a shared engine.
func newPhoneLensFlow(redraw func()) *phoneLensFlow {
	engine := lens.NewEngine()
	store := lensmodel.NewStore(engine)
	engine.Attach(store)
	return &phoneLensFlow{
		store:  store,
		engine: engine,
		tap:    vt.New(store).WithCols(200),
		redraw: redraw,
		ready:  make(chan struct{}),
	}
}

// bind attaches the private boundary mirror. The terminal's reader waits on
// ready until this returns, so no marker is seen without a mirror.
func (f *phoneLensFlow) bind(term *terminal.Terminal) {
	conn := newLensMirrorConn()
	mirror, err := terminal.New(terminal.Options{Conn: conn, Scrollback: phoneLensScrollback})
	if err != nil {
		_ = conn.Close()
		f.readyOnce.Do(func() { close(f.ready) })
		return
	}
	cols, rows := term.Size()
	if cols > 0 && rows > 0 {
		mirror.Resize(cols, rows)
	}
	f.mu.Lock()
	f.mirror, f.mirrorConn = mirror, conn
	f.mirrorSize = appview.Size{Cols: cols, Rows: rows}
	f.mu.Unlock()
	f.readyOnce.Do(func() { close(f.ready) })
}

// feed parses one output chunk and returns the bytes that should reach the
// visible terminal (with captured binary payloads stripped). It runs on the
// terminal's read goroutine, in the order the bytes arrive.
func (f *phoneLensFlow) feed(data []byte) []byte {
	if f == nil || len(data) == 0 {
		return data
	}
	f.mu.Lock()
	// The tap is shared with the UI thread's setSize: a resize and a read must
	// not rewrap its rows at once.
	filtered := f.tap.FeedFilter(data)
	if f.mirror == nil {
		f.mu.Unlock()
		return filtered
	}
	if len(filtered) > 0 {
		// Split at the integration's markers so the mirror is snapshotted at the
		// cursor position the marker names, not at wherever later output moved it.
		f.boundary.feed(filtered, func(chunk []byte, marker string) {
			if len(chunk) > 0 {
				f.feedMirror(chunk)
			}
			if marker != "" {
				f.recordMarker(marker)
			}
		})
	}
	f.mu.Unlock()
	f.requestRedraw()
	return filtered
}

// setRedraw installs the frame request the reader wakes on output.
func (f *phoneLensFlow) setRedraw(redraw func()) {
	if f == nil || redraw == nil {
		return
	}
	f.redrawMu.Lock()
	f.redraw = redraw
	f.redrawMu.Unlock()
}

func (f *phoneLensFlow) requestRedraw() {
	f.redrawMu.RLock()
	redraw := f.redraw
	f.redrawMu.RUnlock()
	if redraw != nil {
		redraw()
	}
}

// feedMirror feeds a chunk to the boundary mirror and re-anchors the prompt on
// a main-screen clear, as a shell's Ctrl+L repaints the prompt without a new
// OSC 133;B.
func (f *phoneLensFlow) feedMirror(chunk []byte) {
	for len(chunk) > 0 {
		i, ev := f.scanner.NextEvent(chunk)
		if i < 0 {
			f.mirror.Feed(chunk)
			return
		}
		f.mirror.Feed(chunk[:i+1])
		if ev == vt.ClearEvent {
			f.anchorAfterClear()
		}
		chunk = chunk[i+1:]
	}
}

// anchorAfterClear moves the prompt row to where the cursor is after a clear.
// Only the prompt's own clear matters: a running command's echo still anchors
// the live area, and a full-screen program clears its alternate screen.
func (f *phoneLensFlow) anchorAfterClear() {
	if f.mirror == nil || !f.promptKnown || f.commandSincePrompt {
		return
	}
	row, alt, ok := appview.CursorReplayRow(f.mirror.Snapshot(), f.mirrorSize)
	if !ok || alt {
		return
	}
	f.promptRow = row
}

// recordMarker consumes one complete OSC 133 payload after the mirror replayed
// it, so the snapshot names the exact row the shell marked.
func (f *phoneLensFlow) recordMarker(marker string) {
	if f.mirror == nil || !strings.HasPrefix(marker, "133;") {
		return
	}
	sub, args, _ := strings.Cut(strings.TrimPrefix(marker, "133;"), ";")
	switch sub {
	case "C":
		f.commandSincePrompt = true
	case "B":
		row, _, ok := appview.CursorReplayRow(f.mirror.Snapshot(), f.mirrorSize)
		if !ok {
			return
		}
		afterCommand := f.commandSincePrompt
		if afterCommand || !f.promptKnown || row < f.promptRow {
			f.promptRow = row
		}
		f.promptKnown = true
		f.promptAfter = afterCommand
		f.commandSincePrompt = false
	case "D":
		_ = args
	}
}

// setSize keeps the tap and the mirror at the terminal's grid.
func (f *phoneLensFlow) setSize(cols, rows int) {
	if f == nil || cols <= 0 || rows <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Feed and SetCols share the tap's rows; one lock covers both.
	f.tap.SetCols(cols)
	if f.mirror == nil {
		return
	}
	if f.mirrorSize.Cols == cols && f.mirrorSize.Rows == rows {
		return
	}
	reflow := f.mirrorSize.Cols != cols
	f.mirror.Resize(cols, rows)
	f.mirrorSize = appview.Size{Cols: cols, Rows: rows}
	if reflow {
		// A width change re-wraps the scrollback, so the recorded row names a
		// different line. Forget it until the shell reports a fresh prompt.
		f.promptKnown = false
	}
}

// promptBoundary returns the row where the current prompt run starts, counted
// from the replay's first row, and whether it is known.
func (f *phoneLensFlow) promptBoundary() (int, bool) {
	if f == nil {
		return 0, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promptRow, f.promptKnown
}

// promptFollowsCommand reports whether the current prompt follows a command.
func (f *phoneLensFlow) promptFollowsCommand() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promptAfter
}

// screenCleared reports whether the running command cleared the main screen.
// The Pane then draws the screen from its first row, not from the shell's
// prompt row, which a main-screen TUI (procps top) has overwritten.
func (f *phoneLensFlow) screenCleared() bool {
	return f != nil && f.tap != nil && f.tap.ScreenCleared()
}

// running reports whether a command's output is still streaming.
func (f *phoneLensFlow) running() bool {
	return f != nil && f.store.HasRunning()
}

// waitReady lets the terminal's reader begin only once the mirror is bound.
func (f *phoneLensFlow) waitReady() {
	if f == nil {
		return
	}
	<-f.ready
}

// close releases the mirror and its dummy connection.
func (f *phoneLensFlow) close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	mirror, conn := f.mirror, f.mirrorConn
	f.mirror, f.mirrorConn = nil, nil
	f.mu.Unlock()
	if mirror != nil {
		_ = mirror.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// lensTapConn feeds the flow before the terminal consumes the bytes, stripping
// binary-captured payloads so they do not paint garbage cells.
type lensTapConn struct {
	conn    io.ReadWriteCloser
	flow    *phoneLensFlow
	pending []byte
}

func (c *lensTapConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.flow.waitReady()
	for {
		if len(c.pending) > 0 {
			n := copy(p, c.pending)
			c.pending = c.pending[n:]
			return n, nil
		}
		n, err := c.conn.Read(p)
		if n <= 0 {
			return n, err
		}
		filtered := c.flow.feed(p[:n])
		if len(filtered) == 0 {
			if err != nil {
				return 0, err
			}
			continue
		}
		copied := copy(p, filtered)
		if copied < len(filtered) {
			c.pending = append(c.pending[:0], filtered[copied:]...)
		}
		return copied, err
	}
}

func (c *lensTapConn) Write(p []byte) (int, error) { return c.conn.Write(p) }
func (c *lensTapConn) Close() error                { return c.conn.Close() }

// Resize keeps the Session view's own size channel reachable: the terminal
// probes its Conn for one, and wrapping it must not hide the method. A view
// without one accepts the call as a no-op.
func (c *lensTapConn) Resize(cols, rows int) error {
	if resizable, ok := c.conn.(interface{ Resize(int, int) error }); ok {
		return resizable.Resize(cols, rows)
	}
	return nil
}

// lensMirrorConn keeps a private terminal emulator alive without a process.
type lensMirrorConn struct {
	closed chan struct{}
	once   sync.Once
}

func newLensMirrorConn() *lensMirrorConn {
	return &lensMirrorConn{closed: make(chan struct{})}
}

func (c *lensMirrorConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *lensMirrorConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *lensMirrorConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// phoneOSC133Scanner splits a byte stream at complete OSC strings while
// preserving parser state across reads. Only OSCs are surfaced; their bytes do
// not move the cursor, so the mirror can be snapshotted right after each one.
type phoneOSC133Scanner struct {
	state   byte
	payload []byte
}

const (
	osc133Ground byte = iota
	osc133Esc
	osc133OSC
	osc133OSCEsc
)

func (s *phoneOSC133Scanner) reset() {
	s.state = osc133Ground
	s.payload = s.payload[:0]
}

func (s *phoneOSC133Scanner) feed(data []byte, emit func([]byte, string)) {
	start := 0
	for i, c := range data {
		var marker string
		switch s.state {
		case osc133Ground:
			if c == 0x1b {
				s.state = osc133Esc
			}
		case osc133Esc:
			if c == ']' {
				s.state = osc133OSC
				s.payload = s.payload[:0]
			} else if c != 0x1b {
				s.state = osc133Ground
			}
		case osc133OSC:
			switch c {
			case 0x07:
				marker = string(s.payload)
				s.reset()
			case 0x1b:
				s.state = osc133OSCEsc
			default:
				if len(s.payload) < 256 {
					s.payload = append(s.payload, c)
				}
			}
		case osc133OSCEsc:
			if c == '\\' {
				marker = string(s.payload)
				s.reset()
			} else {
				if len(s.payload) < 256 {
					s.payload = append(s.payload, 0x1b, c)
				}
				s.state = osc133OSC
			}
		}
		if marker != "" {
			emit(data[start:i+1], marker)
			start = i + 1
		}
	}
	if start < len(data) {
		emit(data[start:], "")
	}
}
