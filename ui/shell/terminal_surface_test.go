package shell

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	lens "github.com/mslxl/natcatty/application/lens/lens"
	lensmodel "github.com/mslxl/natcatty/application/lens/model"
	appsession "github.com/mslxl/natcatty/application/session"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
	terminaleffects "github.com/mslxl/natcatty/ui/terminal/effects"
)

// recordingTerminalTransport is the fake transport under the test-only real
// terminal surface. MyGo's terminal tells a sized Conn its screen size from a
// goroutine, so the log is the PTY resize stream the no-reflow contract counts
// (see ADR 0026): one entry per delivered size change, coalesced by the
// terminal when several requests arrive before one is delivered.
type recordingTerminalTransport struct {
	mu      sync.Mutex
	resizes []appview.Size
	writes  []byte
	output  chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newRecordingTerminalTransport() *recordingTerminalTransport {
	return &recordingTerminalTransport{
		output: make(chan []byte, 8),
		closed: make(chan struct{}),
	}
}

func (t *recordingTerminalTransport) Read(p []byte) (int, error) {
	select {
	case data := <-t.output:
		return copy(p, data), nil
	case <-t.closed:
		return 0, io.EOF
	}
}

// feed sends screen output to the terminal, so a test can put content on the
// terminal the ghost draws. It is the fake PTY's program talking.
func (t *recordingTerminalTransport) feed(data []byte) {
	select {
	case t.output <- append([]byte(nil), data...):
	case <-t.closed:
	}
}

// isClosed reports that the terminal closed this transport.
func (t *recordingTerminalTransport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// Write records what the terminal sends the program, so a test can prove a
// keystroke reached this Pane's terminal during a motion run.
func (t *recordingTerminalTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.writes = append(t.writes, p...)
	t.mu.Unlock()
	return len(p), nil
}

func (t *recordingTerminalTransport) Resize(cols, rows int) error {
	t.mu.Lock()
	t.resizes = append(t.resizes, appview.Size{Cols: cols, Rows: rows})
	t.mu.Unlock()
	return nil
}

func (t *recordingTerminalTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *recordingTerminalTransport) log() []appview.Size {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]appview.Size(nil), t.resizes...)
}

func (t *recordingTerminalTransport) last() (appview.Size, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.resizes) == 0 {
		return appview.Size{}, false
	}
	return t.resizes[len(t.resizes)-1], true
}

func (t *recordingTerminalTransport) reset() {
	t.mu.Lock()
	t.resizes = nil
	t.writes = nil
	t.mu.Unlock()
}

// input is what this Pane's terminal has sent the program.
func (t *recordingTerminalTransport) input() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.writes...)
}

// realTerminalSurface is the test-only real terminal surface: a real MyGo
// terminal backed by a fake transport that records resize calls. It draws the
// terminal through appview.Pane, the same self-drawn grid the production
// Session surface uses, and exposes the current grid and resize log so the
// motion tickets can assert GridFor/Resize counts. It is not production code.
type realTerminalSurface struct {
	term      *terminal.Terminal
	transport *recordingTerminalTransport
	store     *lensmodel.Store
	engine    *lens.Engine
	scroll    ui.ScrollState
	follow    appview.Follow
	ripple    terminaleffects.Ripple

	// viewport is this Pane's own room for the live screen, reported by its
	// own Pane; grid caches the grid computed from it so a steady frame does
	// no grid math.
	viewport     appview.Viewport
	grid         appview.Size
	gridViewport appview.Viewport
	gridCellW    float32
	gridCellH    float32
	gridKnown    bool
	gridCalls    int
	// pinViewport is the final room the motion runtime pushed at a layout
	// change. While viewportPinned is set, the previous-frame report from the
	// Pane's layout is ignored until it names the pinned room.
	pinViewport    appview.Viewport
	viewportPinned bool
	// focused and focusPending mirror the production Surface's one-shot focus
	// request: the frame a Pane gains focus asks for the keyboard, later frames
	// keep it while it is here. liveFocused is what the terminal's own input
	// box reported after the frame, so a test can assert the keyboard owner.
	focused      bool
	focusPending bool
	liveFocused  bool

	// closeMu guards closeCount, the number of Close calls. A ghost's retained
	// surface must be closed exactly once, however many release paths run.
	closeMu    sync.Mutex
	closeCount int
	// dead makes ViewInert take the blank last-resort route, as a Surface that
	// cannot draw a still copy would.
	dead bool
}

func newRealTerminalSurface() (*realTerminalSurface, error) {
	transport := newRecordingTerminalTransport()
	term, err := terminal.New(terminal.Options{Conn: transport, Scrollback: -1})
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	engine := lens.NewEngine()
	return &realTerminalSurface{
		term:      term,
		transport: transport,
		store:     lensmodel.NewStore(engine),
		engine:    engine,
	}, nil
}

// View renders the terminal the way the application does: the grid comes from
// this Pane's own viewport, never from a sibling's.
func (s *realTerminalSurface) View(c *ui.Context) ui.Element {
	return s.view(c, false)
}

// ViewFocused is the focused Pane's render: the frame the Pane gains focus
// asks the terminal's input box for the keyboard, and later frames keep it
// while the Pane is focused, as the production Surface does.
func (s *realTerminalSurface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	if focused && !s.focused {
		s.focusPending = true
	}
	s.focused = focused
	return s.view(c, focused)
}

func (s *realTerminalSurface) view(c *ui.Context, focused bool) ui.Element {
	if grid, ok := s.gridForViewport(); ok {
		if cols, rows := s.term.Size(); cols != grid.Cols || rows != grid.Rows {
			s.term.Resize(grid.Cols, grid.Rows)
		}
	}
	focus := s.focusPending
	s.focusPending = false
	cols, rows := s.term.Size()
	return ui.Column(c).Fill().MinHeight(0).Children(func() {
		appview.Pane(
			c,
			s.store,
			s.engine,
			s.term,
			appview.Size{Cols: cols, Rows: rows},
			nil,
			&s.scroll,
			&s.follow,
			&s.ripple,
			appview.Bell{},
			appview.Tail{},
			appview.Cursor{},
			nil,
			appview.Hooks{
				Key:       s,
				Viewport:  s.noteViewport,
				Focus:     focus,
				KeepFocus: focused,
				Focused:   func(ok bool) { s.liveFocused = ok },
			},
			nil,
		)
	})
}

// LiveFocused reports whether this Pane's terminal input box held the
// keyboard after the last frame.
func (s *realTerminalSurface) LiveFocused() bool { return s.liveFocused }

func (s *realTerminalSurface) Close() {
	s.closeMu.Lock()
	s.closeCount++
	s.closeMu.Unlock()
	if s.term != nil {
		_ = s.term.Close()
	}
	_ = s.transport.Close()
}

// Closes is how many times this surface was closed. A retained ghost surface
// must close once, whether the run ends, warps, or is superseded.
func (s *realTerminalSurface) Closes() int {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeCount
}

// feed puts text on this Pane's terminal screen, as its program would.
func (s *realTerminalSurface) feed(text string) { s.transport.feed([]byte(text)) }

// inertSurfaceKey identifies a still copy's live container apart from the real
// Pane's.
type inertSurfaceKey struct{ surface *realTerminalSurface }

// ViewInert draws a still copy of this Pane for a motion overlay: it does no
// grid math, delivers no PTY resize, reports no viewport, and never asks for
// focus. The real Pane keeps the live View.
func (s *realTerminalSurface) ViewInert(c *ui.Context) ui.Element {
	if s.term == nil || s.dead {
		// The blank last resort: the ghost's own opaque background remains; no
		// live terminal content is drawn.
		return ui.Column(c).Fill()
	}
	cols, rows := s.term.Size()
	var scroll ui.ScrollState
	var follow appview.Follow
	var ripple terminaleffects.Ripple
	return ui.Column(c).Fill().MinHeight(0).Children(func() {
		appview.Pane(c, s.store, s.engine, s.term, appview.Size{Cols: cols, Rows: rows},
			nil, &scroll, &follow, &ripple, appview.Bell{}, appview.Tail{}, appview.Cursor{},
			nil, appview.Hooks{Key: inertSurfaceKey{s}}, nil)
	})
}

// noteViewport records the room this Pane's live screen was laid out in. The
// reported box is the previous frame's layout, so after a motion event it
// still names the old room for a frame; a pinned final room wins until the
// layout catches up.
func (s *realTerminalSurface) noteViewport(w, h float32) {
	if w <= 0 || h <= 0 {
		return
	}
	if s.viewportPinned {
		if sameViewport(w, h, s.pinViewport) {
			// The report is the pinned room within layout rounding; keep the
			// pinned values so the arrival frame does no grid math.
			s.viewportPinned = false
		}
		return
	}
	if sameViewport(w, h, s.viewport) {
		// Layout rounding alone does not move the room; keep the recorded
		// values so the cached grid stays valid.
		return
	}
	s.viewport = appview.Viewport{W: w, H: h}
}

// MotionViewport reports the room this Pane's live screen currently gets.
func (s *realTerminalSurface) MotionViewport() (w, h float32, ok bool) {
	if s.viewport.W <= 0 || s.viewport.H <= 0 {
		return 0, 0, false
	}
	return s.viewport.W, s.viewport.H, true
}

// PinViewport sets the final room and grid at a layout change, so the PTY
// receives at most one size and animation frames do no grid math.
func (s *realTerminalSurface) PinViewport(w, h float32) {
	if s.term == nil || w <= 0 || h <= 0 {
		return
	}
	s.viewport = appview.Viewport{W: w, H: h}
	s.pinViewport = s.viewport
	s.viewportPinned = true
	if grid, ok := s.gridForViewport(); ok {
		if cols, rows := s.term.Size(); cols != grid.Cols || rows != grid.Rows {
			s.term.Resize(grid.Cols, grid.Rows)
		}
	}
}

// ReleaseViewport lets the Pane's own layout size the terminal again.
func (s *realTerminalSurface) ReleaseViewport() { s.viewportPinned = false }

// sameViewport reports that a reported box is the pinned one, allowing for
// sub-DIP layout rounding.
func sameViewport(w, h float32, want appview.Viewport) bool {
	const epsilon = 0.5
	abs := func(v float32) float32 {
		if v < 0 {
			return -v
		}
		return v
	}
	return abs(w-want.W) <= epsilon && abs(h-want.H) <= epsilon
}

// gridForViewport is the grid this Pane's terminal fits, recomputed only when
// its own viewport or the measured cell changed.
func (s *realTerminalSurface) gridForViewport() (appview.Size, bool) {
	v := s.viewport
	if v.W <= 0 || v.H <= 0 {
		return appview.Size{}, false
	}
	cellW, cellH := appview.CellSize()
	if s.gridKnown && v == s.gridViewport && cellW == s.gridCellW && cellH == s.gridCellH {
		return s.grid, true
	}
	s.gridCalls++
	grid, ok := appview.GridFor(v)
	if !ok {
		return appview.Size{}, false
	}
	s.grid, s.gridViewport = grid, v
	s.gridCellW, s.gridCellH, s.gridKnown = cellW, cellH, true
	return grid, true
}

// Grid is the grid the terminal is at now.
func (s *realTerminalSurface) Grid() appview.Size {
	cols, rows := s.term.Size()
	return appview.Size{Cols: cols, Rows: rows}
}

// Viewport is the room this Pane's live screen was last laid out in.
func (s *realTerminalSurface) Viewport() appview.Viewport { return s.viewport }

// GridForCalls is how many times this surface actually computed a grid. A
// steady frame must not add to it.
func (s *realTerminalSurface) GridForCalls() int { return s.gridCalls }

// ResizeLog is the PTY size stream MyGo's terminal delivered to the fake
// transport.
func (s *realTerminalSurface) ResizeLog() []appview.Size { return s.transport.log() }

// ResetResizeLog drops the log so a test can count one layout change alone.
func (s *realTerminalSurface) ResetResizeLog() { s.transport.reset() }

// realTerminalSurfaceFactory builds one real terminal surface per Pane.
type realTerminalSurfaceFactory struct {
	mu       sync.Mutex
	surfaces []*realTerminalSurface
}

func (f *realTerminalSurfaceFactory) open(*appsession.Session) (state.Surface, error) {
	surface, err := newRealTerminalSurface()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.surfaces = append(f.surfaces, surface)
	f.mu.Unlock()
	return surface, nil
}

func (f *realTerminalSurfaceFactory) all() []*realTerminalSurface {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*realTerminalSurface(nil), f.surfaces...)
}

// sessionTransportFactory gives each Session a transport of its own. The
// terminal in these tests is not wired to it: the surface owns a real terminal
// over its recording transport, which is the seam under test.
type sessionTransportFactory struct{}

func (sessionTransportFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newFakeTransport(), nil
}

type realTerminalGraph struct {
	state   *state.State
	factory *realTerminalSurfaceFactory
}

func newRealTerminalGraph(t *testing.T, options ...state.Option) *realTerminalGraph {
	t.Helper()
	service := appsession.NewService(sessionTransportFactory{})
	t.Cleanup(service.CloseAll)
	factory := &realTerminalSurfaceFactory{}
	options = append([]state.Option{state.WithSessions(service, factory.open)}, options...)
	st := state.NewState(options...)
	t.Cleanup(func() {
		for _, surface := range factory.all() {
			surface.Close()
		}
	})
	return &realTerminalGraph{state: st, factory: factory}
}

// pumpUntil builds frames until ready reports true. Every pass checks the
// real condition; the sleep is the loop's cadence, not a synchronization
// assumption.
func pumpUntil(t *testing.T, tt *ui.Tester, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// gridsSettled reports that every surface's terminal is at the grid its own
// viewport has room for and that the fake transport has delivered that size.
func gridsSettled(surfaces ...*realTerminalSurface) bool {
	for _, surface := range surfaces {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			return false
		}
		last, ok := surface.transport.last()
		if !ok || last != want {
			return false
		}
	}
	return true
}

// paneSurfaces returns the real terminal surfaces of the Tab in tree order.
func paneSurfaces(t *testing.T, g *realTerminalGraph, tabIndex int) []*realTerminalSurface {
	t.Helper()
	root, _, ok := g.state.TabTree(tabIndex)
	if !ok {
		t.Fatalf("no Tab at index %d", tabIndex)
	}
	ids := domainworkspace.CollectPaneIDs(root)
	surfaces := make([]*realTerminalSurface, 0, len(ids))
	for _, id := range ids {
		info, ok := g.state.PaneInfoAt(tabIndex, id)
		if !ok || info.Surface == nil {
			t.Fatalf("Pane %s has no surface", id)
		}
		surface, ok := info.Surface.(*realTerminalSurface)
		if !ok {
			t.Fatalf("Pane %s surface = %T, want the real terminal surface", id, info.Surface)
		}
		surfaces = append(surfaces, surface)
	}
	return surfaces
}

// setAsymmetricSplit sets the Tab's only split to a 70/30 ratio, First heavy.
func setAsymmetricSplit(t *testing.T, g *realTerminalGraph, tabIndex int) *domainworkspace.Node {
	t.Helper()
	root, _, ok := g.state.TabTree(tabIndex)
	if !ok || root == nil || root.IsPane() {
		t.Fatalf("Tab %d is not split: %+v", tabIndex, root)
	}
	if !g.state.SetSplitRatio(tabIndex, root.SplitID, 70) {
		t.Fatalf("SetSplitRatio(%s, 70) failed", root.SplitID)
	}
	return root
}

// waitForSplitLayout pumps frames until each surface's viewport reflects the
// asymmetric split: the 70% Pane must be visibly larger in the split's
// direction than the 30% Pane.
func waitForSplitLayout(t *testing.T, tt *ui.Tester, root *domainworkspace.Node, surfaces []*realTerminalSurface) {
	t.Helper()
	if len(surfaces) != 2 {
		t.Fatalf("split has %d surfaces, want 2", len(surfaces))
	}
	first, second := surfaces[0], surfaces[1]
	pumpUntil(t, tt, "the asymmetric split viewports", func() bool {
		a, b := first.Viewport(), second.Viewport()
		if root.Direction == domainworkspace.Horizontal {
			return a.H > b.H+1
		}
		return a.W > b.W+1
	})
}
