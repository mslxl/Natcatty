package sessionui

import (
	"io"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/mslxl/natcatty/application/lens/graphics"
	"github.com/mslxl/natcatty/application/lens/lens"
	_ "github.com/mslxl/natcatty/application/lens/lens/builtin"
	lensmodel "github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/application/lens/vt"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// terminalFlow is the command-lens layer for one Natcatty Session. It observes
// the Session's decoded terminal stream, so the existing terminal remains the
// sole owner of input, VT state, resize and protocol behavior while this layer
// derives command blocks from the same bytes.
type terminalFlow struct {
	store  *lensmodel.Store
	engine *lens.Engine
	tap    *vt.Tap
	gfx    *graphics.Parser
	redraw func()

	unsubscribe func()
	closeOnce   sync.Once
	tapMu       sync.Mutex
	imageMu     sync.RWMutex
	images      []lens.Image

	// bellTerm is a private VT mirror fed from the decoded stream before the
	// Session gives that stream to the visible terminal. It lets us snapshot
	// the cursor at the exact BEL byte, like mygo-flow's PTY reader does,
	// instead of sampling the visible terminal on a later frame.
	bellMu      sync.Mutex
	bellTerm    *terminal.Terminal
	bellConn    *bellTrackerConn
	bellScanner vt.BellScanner
	bellSize    appview.Size
	bellQueue   []appview.Bell
	bellBacklog [][]byte

	// boundary tracks shell integration prompt/command marks on the same
	// private terminal mirror used for exact BEL positions. The latest prompt
	// run is therefore identified by its terminal row, independently of the
	// prompt's text.
	boundary           osc133BoundaryScanner
	promptRow          int
	promptRowKnown     bool
	promptSize         appview.Size
	commandSincePrompt bool
}

func newTerminalFlow(session *appsession.Session, redraw func()) *terminalFlow {
	engine := lens.NewEngine()
	store := lensmodel.NewStore(engine)
	engine.Attach(store)
	tap := vt.New(store).WithCols(200)
	gfx := graphics.New()
	surface := &terminalFlow{
		store:  store,
		engine: engine,
		tap:    tap,
		gfx:    gfx,
		redraw: redraw,
	}
	surface.unsubscribe = session.SubscribeOutputReplay(func(data []byte) {
		surface.tapMu.Lock()
		tap.Feed(data)
		for _, image := range gfx.Feed(data) {
			surface.imageMu.Lock()
			surface.images = append(surface.images, lens.Image{Data: image.Data, Alt: image.Name})
			if len(surface.images) > 4 {
				surface.images = append([]lens.Image(nil), surface.images[len(surface.images)-4:]...)
			}
			surface.imageMu.Unlock()
		}
		surface.tapMu.Unlock()
		surface.feedBell(data)
		if redraw != nil {
			redraw()
		}
	})
	return surface
}

func (s *terminalFlow) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.unsubscribe != nil {
			s.unsubscribe()
		}
		s.bellMu.Lock()
		tracker, conn := s.bellTerm, s.bellConn
		s.bellTerm, s.bellConn = nil, nil
		s.bellMu.Unlock()
		if tracker != nil {
			_ = tracker.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func (s *terminalFlow) imagesSnapshot() []lens.Image {
	if s == nil {
		return nil
	}
	s.imageMu.RLock()
	defer s.imageMu.RUnlock()
	return append([]lens.Image(nil), s.images...)
}

func (s *terminalFlow) clear() {
	if s == nil {
		return
	}
	s.tapMu.Lock()
	s.gfx = graphics.New()
	s.tapMu.Unlock()
	s.store.Clear()
	s.imageMu.Lock()
	s.images = nil
	s.imageMu.Unlock()
}

func (s *terminalFlow) setCols(cols int) {
	if s == nil || cols <= 0 {
		return
	}
	s.tapMu.Lock()
	s.tap.SetCols(cols)
	s.tapMu.Unlock()
	s.bellMu.Lock()
	if s.bellTerm != nil {
		rows := s.bellSize.Rows
		if rows <= 0 {
			_, rows = s.bellTerm.Size()
		}
		s.bellSize = appview.Size{Cols: cols, Rows: rows}
		s.bellTerm.Resize(cols, rows)
		s.promptSize = s.bellSize
	}
	s.bellMu.Unlock()
}

// promptBoundary returns the screen row where the current shell prompt run
// starts. The row is meaningful only while the mirror has the same dimensions
// as the visible terminal.
func (s *terminalFlow) promptBoundary(size appview.Size) (int, bool) {
	if s == nil {
		return 0, false
	}
	s.bellMu.Lock()
	defer s.bellMu.Unlock()
	if !s.promptRowKnown || s.promptSize != size {
		return 0, false
	}
	return s.promptRow, true
}

// bindBellTracker attaches the exact-BEL cursor mirror to the visible terminal.
// The visible terminal may already have replayed a Session snapshot, so seed
// the mirror from that snapshot before applying any output chunks that arrived
// while the Surface was being attached.
func (s *terminalFlow) bindBellTracker(term *terminal.Terminal) {
	if s == nil || term == nil {
		return
	}
	cols, rows := term.Size()
	conn := newBellTrackerConn()
	tracker, err := terminal.New(terminal.Options{
		Conn:       conn,
		Scrollback: -1,
	})
	if err != nil {
		_ = conn.Close()
		return
	}
	tracker.Resize(cols, rows)
	tracker.Feed(term.Snapshot())

	s.bellMu.Lock()
	old := s.bellTerm
	oldConn := s.bellConn
	s.bellTerm, s.bellConn = tracker, conn
	s.bellSize = appview.Size{Cols: cols, Rows: rows}
	s.promptSize = s.bellSize
	s.promptRow = 0
	s.promptRowKnown = false
	s.commandSincePrompt = false
	backlog := s.bellBacklog
	s.bellBacklog = nil
	s.bellMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if oldConn != nil {
		_ = oldConn.Close()
	}
	for _, data := range backlog {
		s.feedBell(data)
	}
}

// feedBell mirrors one decoded output chunk and queues a cursor snapshot for
// every ground-state BEL in it. OSC terminators are consumed by BellScanner and
// therefore never create ripples.
func (s *terminalFlow) feedBell(data []byte) {
	if s == nil || len(data) == 0 {
		return
	}
	s.bellMu.Lock()
	defer s.bellMu.Unlock()
	if s.bellTerm == nil {
		s.bellBacklog = append(s.bellBacklog, append([]byte(nil), data...))
		return
	}
	s.boundary.feed(data, func(chunk []byte, marker string) {
		s.feedBellChunk(chunk)
		if marker != "" {
			s.recordOSC133(marker)
		}
	})
}

func (s *terminalFlow) feedBellChunk(data []byte) {
	for len(data) > 0 {
		i := s.bellScanner.Next(data)
		if i < 0 {
			s.bellTerm.Feed(data)
			return
		}
		s.bellTerm.Feed(data[:i+1])
		snapshot := s.bellTerm.Snapshot()
		col, row, ok := appview.CursorCell(snapshot, s.bellSize)
		if !ok {
			col, row = -1, -1
		}
		s.bellQueue = append(s.bellQueue, appview.Bell{Fired: true, Col: col, Row: row})
		data = data[i+1:]
	}
}

// recordOSC133 consumes a complete OSC 133 payload (the leading "133;" is
// included) after the mirror has replayed it, so CursorCell observes the exact
// row at which the marker occurred.
func (s *terminalFlow) recordOSC133(marker string) {
	if !strings.HasPrefix(marker, "133;") || s.bellTerm == nil {
		return
	}
	sub, args, _ := strings.Cut(strings.TrimPrefix(marker, "133;"), ";")
	switch sub {
	case "C":
		s.commandSincePrompt = true
	case "B":
		_, row, ok := appview.CursorCell(s.bellTerm.Snapshot(), s.bellSize)
		if !ok {
			return
		}
		if s.commandSincePrompt || !s.promptRowKnown || row < s.promptRow {
			s.promptRow = row
		}
		s.promptRowKnown = true
		s.promptSize = s.bellSize
		s.commandSincePrompt = false
	case "D":
		// D ends a command, but the next B decides whether a new prompt starts
		// a fresh live run or extends the run of empty prompts.
		_ = args
	}
}

// osc133BoundaryScanner splits a byte stream at complete OSC 133 markers while
// preserving parser state across reads. Marker bytes do not move the terminal
// cursor, so the callback can query the mirror immediately after each segment.
type osc133BoundaryScanner struct {
	state   byte
	payload []byte
}

const (
	osc133Ground byte = iota
	osc133Esc
	osc133OSC
	osc133OSCEsc
)

func (s *osc133BoundaryScanner) reset() {
	s.state = osc133Ground
	s.payload = s.payload[:0]
}

func (s *osc133BoundaryScanner) feed(data []byte, emit func([]byte, string)) {
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
		if marker != "" && strings.HasPrefix(marker, "133;") {
			emit(data[start:i+1], marker)
			start = i + 1
		}
	}
	if start < len(data) {
		emit(data[start:], "")
	}
}

func (s *terminalFlow) takeBell() (appview.Bell, bool) {
	if s == nil {
		return appview.Bell{}, false
	}
	s.bellMu.Lock()
	defer s.bellMu.Unlock()
	if len(s.bellQueue) == 0 {
		return appview.Bell{}, false
	}
	bell := s.bellQueue[0]
	s.bellQueue = s.bellQueue[1:]
	return bell, true
}

// bellTrackerConn keeps a private terminal emulator alive without starting a
// process. Terminal.New starts its reader goroutine for any Conn, so Read
// blocks until Close while Write simply accepts VT replies.
type bellTrackerConn struct {
	closed chan struct{}
	once   sync.Once
}

func newBellTrackerConn() *bellTrackerConn {
	return &bellTrackerConn{closed: make(chan struct{})}
}

func (c *bellTrackerConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *bellTrackerConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *bellTrackerConn) Resize(int, int) error       { return nil }
func (c *bellTrackerConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

var _ io.ReadWriteCloser = (*bellTrackerConn)(nil)
