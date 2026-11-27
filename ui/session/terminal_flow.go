package sessionui

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/application/lens/graphics"
	"github.com/mslxl/natcatty/application/lens/lens"
	_ "github.com/mslxl/natcatty/application/lens/lens/builtin"
	lensmodel "github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/application/lens/vt"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/terminal"
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
	// pictures tells that a program placed a picture since the pane last drew
	// its own replay: a full-screen one then needs the terminal's own view,
	// which is the only renderer that draws placements.
	pictures atomic.Bool

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
	promptAfterCommand bool
	promptSize         appview.Size
	commandSincePrompt bool
	// promptPending is set between a command's OSC 133;D and the next
	// OSC 133;B: the shell has not drawn its next prompt yet, and the live
	// replay still holds the finished command below the block it just
	// entered. The Pane crops the live area at the cursor meanwhile, so the
	// output is not shown twice until the prompt replaces it.
	promptPending bool
	// session is the terminal the flow reads marks and state from. It is nil
	// for a flow built without one (the tests that drive the tap directly).
	session *appsession.Session

	// promptTheme is the live terminal scheme the captured prompt rows are
	// resolved with. It is set from the Surface's theme each frame; a capture
	// keeps the colours it had when the shell drew the prompt.
	promptThemeMu sync.Mutex
	promptTheme   *terminal.Theme

	// The prompt rows a finished block shows above it. Every OSC 133;C gets one
	// mark from the tap (the block it started, or -1) and one capture from the
	// private mirror (the rows from the prompt boundary through the echoed
	// input). pairPrompts matches them in stream order; promptRows keeps the
	// result per block id, so a block draws the shell's own prompt — starship,
	// powerlevel10k, any PS1 — instead of one this app invents.
	promptMu       sync.Mutex
	promptMarks    []vt.CommandMark
	promptCaptures [][][]ui.Span
	promptRows     map[int]promptCapture
}

// promptCapture is one OSC 133;C capture: the rows the shell drew, and the
// store epoch the mark was recorded under. Block ids restart at zero after
// Clear, so an epoch mismatch means the rows belong to a block that no longer
// exists.
type promptCapture struct {
	epoch uint64
	rows  [][]ui.Span
}

// maxPromptCaptures bounds the two pairing queues. They drain after every feed;
// the cap only matters while the mirror is unbound, and both queues keep the
// same tail so their pairing stays aligned.
const maxPromptCaptures = 4096

func newTerminalFlow(session *appsession.Session, redraw func()) *terminalFlow {
	engine := lens.NewEngine()
	store := lensmodel.NewStore(engine)
	engine.Attach(store)
	tap := vt.New(store).WithCols(200)
	gfx := graphics.New()
	surface := &terminalFlow{
		store:   store,
		engine:  engine,
		tap:     tap,
		gfx:     gfx,
		redraw:  redraw,
		session: session,
	}
	surface.unsubscribe = session.SubscribeOutputFilterReplay(func(data []byte) []byte {
		surface.tapMu.Lock()
		// Pictures belong to the command that printed them. The tap has to see
		// the bytes first (that is what starts and ends its block), so remember
		// which block owns this chunk: the one already running, or the one the
		// chunk itself created when a fast command arrives whole (its output,
		// picture and OSC 133;D in one read).
		runningID, running := surface.store.LastID(), surface.store.HasRunning()
		filtered := tap.FeedFilter(data)
		// The tap's OSC 133;C marks line up with the mirror's captures below;
		// take them now, before this chunk's captures exist, so both queues
		// grow in the same order.
		surface.notePromptMarks(tap.TakeCommandMarks())
		var images []graphics.Image
		if len(filtered) > 0 {
			images = surface.gfx.Feed(filtered)
		}
		// What the screen is now decides whether pictures show: on the
		// alternate screen only the terminal's own view can draw one, so the
		// pane asks for it. A program that merely queries the protocol draws
		// with it too (yazi asks before it previews), so any graphics sequence
		// counts, not only a picture this parser could decode. Leaving that
		// screen clears the request (see clearPictures), and the next
		// full-screen program is drawn by the pane's replay again.
		if surface.gfx.SawGraphics() {
			surface.pictures.Store(true)
		}
		if len(images) > 0 {
			owner, owned := -1, false
			switch last := surface.store.LastID(); {
			case running:
				owner, owned = runningID, true
			case last != runningID:
				owner, owned = last, true
			}
			for _, image := range images {
				if owned {
					surface.store.AddImage(owner, lensmodel.Image{Data: image.Data})
					continue
				}
				// A picture printed outside any command (a prompt-time program,
				// or a shell with no integration marks) has no block to live in;
				// it stays in the flow's own strip above the live screen.
				surface.imageMu.Lock()
				surface.images = append(surface.images, lens.Image{Data: image.Data})
				if len(surface.images) > 4 {
					surface.images = append([]lens.Image(nil), surface.images[len(surface.images)-4:]...)
				}
				surface.imageMu.Unlock()
			}
		}
		surface.tapMu.Unlock()
		if len(filtered) > 0 {
			surface.feedBell(filtered)
		}
		surface.pairPrompts()
		if redraw != nil {
			redraw()
		}
		return filtered
	})
	return surface
}

// hasPictures reports that a program used the graphics protocol since the pane
// last drew the screen its own way.
func (s *terminalFlow) hasPictures() bool {
	return s != nil && s.pictures.Load()
}

// clearPictures forgets the pictures placed so far, which the pane calls when it
// draws the screen itself again: the next full-screen program starts clean.
func (s *terminalFlow) clearPictures() {
	if s == nil {
		return
	}
	s.pictures.Store(false)
	// The program that asked for the view is gone: the next one starts without
	// graphics activity, so an ordinary full-screen program keeps the pane's
	// replay.
	s.tapMu.Lock()
	if s.gfx != nil {
		s.gfx.ForgetGraphics()
	}
	s.tapMu.Unlock()
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
	// Block ids restart at zero with the cleared store, so the rows captured
	// for the old blocks must not attach to the new ones. The queues are left
	// alone: a capture whose C is still in flight must stay paired with its
	// own mark, and its old epoch makes promptRowsFor reject the result.
	s.promptMu.Lock()
	s.promptRows = nil
	s.promptMu.Unlock()
}

func (s *terminalFlow) setSize(cols, rows int) {
	if s == nil || cols <= 0 || rows <= 0 {
		return
	}
	if s.tap != nil {
		s.tapMu.Lock()
		s.tap.SetCols(cols)
		s.tapMu.Unlock()
	}
	s.bellMu.Lock()
	if s.bellTerm != nil {
		if s.bellSize.Cols != cols || s.bellSize.Rows != rows {
			reflow := s.bellSize.Cols != cols
			s.bellTerm.Resize(cols, rows)
			s.bellSize = appview.Size{Cols: cols, Rows: rows}
			s.promptSize = s.bellSize
			if reflow {
				// A width change re-wraps the scrollback, so the recorded
				// absolute row now names a different line. Forget it until the
				// shell redraws its prompt and reports a fresh OSC 133;B: a
				// stale row cropped the live area at the wrong line, replaying
				// finished output under the blocks that already showed it (or
				// cutting the prompt off above the cursor). A height change
				// without re-wrapping keeps every row where it was, and the
				// Pane's own block insertion relies on that.
				//
				// A shell whose prompt-start mark said `redraw=0` promised not
				// to draw the prompt again, so the terminal's reflow is the only
				// prompt there will be: dropping the row would leave the flow
				// without a boundary until the next command, which is how a
				// window that came back from the background lost its rows.
				if !s.shellKeepsPromptOnResize() {
					s.promptRowKnown = false
				}
			}
		}
	}
	s.bellMu.Unlock()
}

// shellKeepsPromptOnResize reports that the shell promised not to redraw its
// prompt when the window changes size (OSC 133;A;redraw=0). The terminal then
// reflows the prompt, so the recorded boundary stays the one the shell means.
func (s *terminalFlow) shellKeepsPromptOnResize() bool {
	if s == nil || s.session == nil {
		return false
	}
	return s.session.Snapshot().PromptNoRedraw
}

func (s *terminalFlow) setCols(cols int) {
	if s == nil || cols <= 0 {
		return
	}
	s.bellMu.Lock()
	rows := 0
	if s.bellTerm != nil {
		rows = s.bellSize.Rows
	}
	s.bellMu.Unlock()
	if rows <= 0 {
		rows = 24
	}
	s.setSize(cols, rows)
}

// promptBoundary returns the row where the current shell prompt run starts,
// counted from the replay's first row before the Pane's row cap drops the
// oldest rows above it (see appview.CursorReplayRow). The Pane translates the
// row into its own capped frame, so it names the prompt even after output has
// pushed the replay window down. The row is meaningful only while the mirror
// has the same dimensions as the visible terminal.
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

func (s *terminalFlow) promptBoundaryAfterCommand() bool {
	if s == nil {
		return false
	}
	s.bellMu.Lock()
	defer s.bellMu.Unlock()
	return s.promptAfterCommand
}

// promptBoundaryPending reports whether a command has ended and the shell has
// not drawn its next prompt yet (OSC 133;D seen, no B since).
func (s *terminalFlow) promptBoundaryPending() bool {
	if s == nil {
		return false
	}
	s.bellMu.Lock()
	defer s.bellMu.Unlock()
	return s.promptPending
}

// setPromptTheme records the live terminal scheme the mirror's snapshots are
// resolved with, so a captured prompt row carries the colours the shell drew
// it in. Rows captured earlier keep their own colours; a theme change applies
// to the prompts drawn after it.
func (s *terminalFlow) setPromptTheme(theme *terminal.Theme) {
	if s == nil {
		return
	}
	s.promptThemeMu.Lock()
	s.promptTheme = theme
	s.promptThemeMu.Unlock()
}

func (s *terminalFlow) flowPromptTheme() *terminal.Theme {
	s.promptThemeMu.Lock()
	defer s.promptThemeMu.Unlock()
	return s.promptTheme
}

// notePromptMarks queues the tap's OSC 133;C marks, in order.
func (s *terminalFlow) notePromptMarks(marks []vt.CommandMark) {
	if s == nil || len(marks) == 0 {
		return
	}
	s.promptMu.Lock()
	s.promptMarks = append(s.promptMarks, marks...)
	if len(s.promptMarks) > maxPromptCaptures {
		s.promptMarks = append([]vt.CommandMark(nil), s.promptMarks[len(s.promptMarks)-maxPromptCaptures:]...)
	}
	s.promptMu.Unlock()
}

// notePromptCapture records the rows the shell has drawn for the command whose
// OSC 133;C the mirror just replayed. One entry is recorded per C, even when
// the rows are unavailable, so the queue stays aligned with the tap's marks.
// Called with bellMu held, after the mirror has consumed the marker.
func (s *terminalFlow) notePromptCapture() {
	var rows [][]ui.Span
	if s.promptRowKnown && s.bellTerm != nil {
		if got, ok := appview.PromptRows(s.bellTerm.Snapshot(), s.bellSize, s.promptRow, s.flowPromptTheme()); ok {
			rows = got
		}
	}
	s.promptMu.Lock()
	s.promptCaptures = append(s.promptCaptures, rows)
	if len(s.promptCaptures) > maxPromptCaptures {
		s.promptCaptures = append([][][]ui.Span(nil), s.promptCaptures[len(s.promptCaptures)-maxPromptCaptures:]...)
	}
	s.promptMu.Unlock()
}

// pairPrompts matches queued marks with queued captures in stream order and
// stores the captures per block id. The mark's epoch is the one kept: it was
// read at the C, before a later D in the same read could clear the store.
// Marks that started no block (-1) keep the queues aligned but store nothing.
func (s *terminalFlow) pairPrompts() {
	if s == nil {
		return
	}
	s.promptMu.Lock()
	n := min(len(s.promptMarks), len(s.promptCaptures))
	for i := 0; i < n; i++ {
		mark := s.promptMarks[i]
		rows := s.promptCaptures[i]
		if mark.ID < 0 || len(rows) == 0 {
			continue
		}
		if s.promptRows == nil {
			s.promptRows = make(map[int]promptCapture)
		}
		s.promptRows[mark.ID] = promptCapture{epoch: mark.Epoch, rows: rows}
	}
	s.promptMarks = append([]vt.CommandMark(nil), s.promptMarks[n:]...)
	s.promptCaptures = append([][][]ui.Span(nil), s.promptCaptures[n:]...)
	s.promptMu.Unlock()
}

// promptRowsFor returns the captured prompt rows of a block, or nil when there
// are none (the mirror was not bound at its prompt, or the store was cleared
// since: ids restart at zero, so the epoch must match).
func (s *terminalFlow) promptRowsFor(id int) [][]ui.Span {
	if s == nil {
		return nil
	}
	epoch := s.store.Epoch()
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	capture, ok := s.promptRows[id]
	if !ok || capture.epoch != epoch {
		return nil
	}
	return capture.rows
}

// bindBellTracker attaches the exact-BEL cursor mirror to the visible terminal.
// The visible terminal may already have replayed a Session snapshot, so seed
// the mirror from that snapshot before applying any output chunks that arrived
// while the Surface was being attached.
//
// The mirror keeps the same scrollback as the visible terminal. Its rows then
// count from the same first line as the replay the Pane draws, so a row
// recorded at OSC 133;B is an absolute row of the flow and stays correct when
// the screen scrolls without a new marker. A screen-only mirror made the row
// relative to the visible screen, which the replay's scrollback then shifted.
func (s *terminalFlow) bindBellTracker(term *terminal.Terminal) {
	if s == nil || term == nil {
		return
	}
	cols, rows := term.Size()
	conn := newBellTrackerConn()
	tracker, err := terminal.New(terminal.Options{
		Conn:       conn,
		Scrollback: terminalScrollback,
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
	s.promptAfterCommand = false
	s.commandSincePrompt = false
	// A queued bell's cell counts rows of the replay being replaced; drawing it
	// on the new one would ripple at an unrelated row (a clear rebinds here).
	s.bellQueue = nil
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
	// The backlog's C marks were queued while the mirror was missing; its
	// replay just produced their captures.
	s.pairPrompts()
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
		i, ev := s.bellScanner.NextEvent(data)
		if i < 0 {
			s.bellTerm.Feed(data)
			return
		}
		s.bellTerm.Feed(data[:i+1])
		switch ev {
		case vt.BellEvent:
			snapshot := s.bellTerm.Snapshot()
			col, row, ok := appview.CursorCell(snapshot, s.bellSize)
			if !ok {
				col, row = -1, -1
			}
			s.bellQueue = append(s.bellQueue, appview.Bell{Fired: true, Col: col, Row: row})
		case vt.ClearEvent:
			s.anchorPromptAfterClear()
		}
		data = data[i+1:]
	}
}

// anchorPromptAfterClear re-anchors the prompt row of the live run when the
// screen is cleared without a fresh OSC 133;B. zsh's and bash's clear-screen
// (Ctrl+L) erase the display and repaint the prompt from the home position
// without running precmd, so no B arrives: the recorded boundary then named a
// row above the repainted prompt, and the live area cropped it and the line
// being typed away, with nothing to scroll to. The cursor right after the
// clear is where the shell repaints the prompt, so its row is the new boundary.
// A clear on a running command's behalf (clear(1), a TUI on the main screen)
// lands the live area on the cleared screen the same way. Only a main-screen
// clear counts: a full-screen program clears its alternate screen constantly,
// and those rows name nothing on the main screen.
func (s *terminalFlow) anchorPromptAfterClear() {
	// Only the prompt's own clear matters here: while a command runs, its echo
	// still anchors the live area (tailFrom finds the running line), and a
	// full-screen TUI that redraws on the main screen clears on every frame.
	// Snapshotting the mirror for each of those would cost a replay of the
	// whole scrollback per frame for a boundary the next OSC 133;B replaces.
	if !s.promptRowKnown || s.commandSincePrompt {
		return
	}
	row, alt, ok := appview.CursorReplayRow(s.bellTerm.Snapshot(), s.bellSize)
	if !ok || alt {
		return
	}
	s.promptRow = row
	s.promptSize = s.bellSize
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
		// The cursor is at the end of the echoed input now, before any of the
		// command's output: this is the one moment the real prompt and input
		// are both on the mirror, so capture them for the block.
		s.notePromptCapture()
	case "B":
		row, _, ok := appview.CursorReplayRow(s.bellTerm.Snapshot(), s.bellSize)
		if !ok {
			return
		}
		afterCommand := s.commandSincePrompt
		if afterCommand || !s.promptRowKnown || row < s.promptRow {
			s.promptRow = row
		}
		s.promptRowKnown = true
		s.promptSize = s.bellSize
		s.promptAfterCommand = afterCommand
		s.commandSincePrompt = false
		s.promptPending = false
	case "D":
		// D ends a command, but the next B decides whether a new prompt starts
		// a fresh live run or extends the run of empty prompts.
		_ = args
		s.promptPending = true
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
