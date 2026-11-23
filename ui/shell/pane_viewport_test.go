package shell

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
	terminalsurface "github.com/mslxl/natcatty/ui/terminal/surface"
)

// TestAsymmetricSplitGivesEachPaneItsOwnGrid is ticket 14's regression: a
// 70/30 split used to give each Pane the sibling's grid, so the wide Pane got
// the narrow grid and vice versa. Each Pane's terminal must now be at the grid
// its own visible box has room for.
func TestAsymmetricSplitGivesEachPaneItsOwnGrid(t *testing.T) {
	for _, tc := range []struct {
		name  string
		split func(*state.State) error
	}{
		{"split-right", (*state.State).SplitRight},
		{"split-down", (*state.State).SplitDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRealTerminalGraph(t)
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			if err := tc.split(g.state); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, g.state, 2)

			tt := ui.NewTester(View(g.state), 900, 600)
			tt.SetLocale("en")
			root := setAsymmetricSplit(t, g, 0)
			surfaces := paneSurfaces(t, g, 0)
			waitForSplitLayout(t, tt, root, surfaces)
			pumpUntil(t, tt, "the final grids", func() bool { return gridsSettled(surfaces...) })

			first, second := surfaces[0], surfaces[1]
			firstGrid, secondGrid := first.Grid(), second.Grid()
			firstBox, secondBox := first.Viewport(), second.Viewport()
			t.Logf("First viewport %+v grid %+v; Second viewport %+v grid %+v",
				firstBox, firstGrid, secondBox, secondGrid)
			if want, ok := appview.GridFor(firstBox); !ok || firstGrid != want {
				t.Fatalf("First grid = %+v, want its own viewport's %+v (viewport %+v)", firstGrid, want, firstBox)
			}
			if want, ok := appview.GridFor(secondBox); !ok || secondGrid != want {
				t.Fatalf("Second grid = %+v, want its own viewport's %+v (viewport %+v)", secondGrid, want, secondBox)
			}
			if root.Direction == domainworkspace.Horizontal {
				if firstGrid.Rows <= secondGrid.Rows {
					t.Fatalf("top Pane rows = %d, bottom Pane rows = %d: the grids are swapped",
						firstGrid.Rows, secondGrid.Rows)
				}
			} else if firstGrid.Cols <= secondGrid.Cols {
				t.Fatalf("left Pane cols = %d, right Pane cols = %d: the grids are swapped",
					firstGrid.Cols, secondGrid.Cols)
			}
			// The PTY heard exactly the grid the Pane shows.
			if last, ok := first.transport.last(); !ok || last != firstGrid {
				t.Fatalf("First PTY size = %+v, want the visible grid %+v", last, firstGrid)
			}
			if last, ok := second.transport.last(); !ok || last != secondGrid {
				t.Fatalf("Second PTY size = %+v, want the visible grid %+v", last, secondGrid)
			}
		})
	}
}

// TestSteadyFramesDoNoGridMathOrResize pins the no-reflow prerequisite: after
// a layout settles, frames that change nothing must not compute a grid or
// deliver a PTY resize.
func TestSteadyFramesDoNoGridMathOrResize(t *testing.T) {
	g := newRealTerminalGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 2)

	tt := ui.NewTester(View(g.state), 900, 600)
	tt.SetLocale("en")
	root := setAsymmetricSplit(t, g, 0)
	surfaces := paneSurfaces(t, g, 0)
	waitForSplitLayout(t, tt, root, surfaces)
	pumpUntil(t, tt, "the final grids", func() bool { return gridsSettled(surfaces...) })

	calls := make([]int, len(surfaces))
	logs := make([]int, len(surfaces))
	for i, surface := range surfaces {
		calls[i] = surface.GridForCalls()
		logs[i] = len(surface.ResizeLog())
	}
	for frame := 0; frame < 5; frame++ {
		tt.Frame()
	}
	for i, surface := range surfaces {
		if got := surface.GridForCalls(); got != calls[i] {
			t.Errorf("Pane %d computed %d grids on steady frames, want none", i+1, got-calls[i])
		}
		if got := len(surface.ResizeLog()); got != logs[i] {
			t.Errorf("Pane %d delivered %d resizes on steady frames, want none", i+1, got-logs[i])
		}
	}
}

// TestClosingOnePaneResizesTheSurvivorOnce is ticket 14's close invariant:
// the survivor expands to the whole Tab and receives exactly one PTY size
// change, then nothing more.
func TestClosingOnePaneResizesTheSurvivorOnce(t *testing.T) {
	g := newRealTerminalGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 2)

	tt := ui.NewTester(View(g.state), 900, 600)
	tt.SetLocale("en")
	root := setAsymmetricSplit(t, g, 0)
	surfaces := paneSurfaces(t, g, 0)
	waitForSplitLayout(t, tt, root, surfaces)
	pumpUntil(t, tt, "the final split grids", func() bool { return gridsSettled(surfaces...) })

	survivor := surfaces[0]
	oldBox := survivor.Viewport()
	survivor.ResetResizeLog()

	// The split focuses the new (second) Pane, so closing it leaves First.
	if !g.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	pumpUntil(t, tt, "the survivor's expanded viewport", func() bool {
		return survivor.Viewport().W > oldBox.W+1
	})
	pumpUntil(t, tt, "the survivor's final grid", func() bool { return gridsSettled(survivor) })

	log := survivor.ResizeLog()
	if len(log) != 1 {
		t.Fatalf("survivor resizes = %+v, want exactly one to the final grid", log)
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || log[0] != want {
		t.Fatalf("survivor resize = %+v, want the final grid %+v", log[0], want)
	}
	// The frames after the one resize must not deliver another.
	for frame := 0; frame < 5; frame++ {
		tt.Frame()
	}
	if got := survivor.ResizeLog(); len(got) != 1 {
		t.Fatalf("survivor resizes after steady frames = %+v, want the one resize", got)
	}
}

// TestFontChangeRecomputesTheGrid proves the cached grid follows the measured
// cell: a terminal font change must reach the PTY even though the Pane's
// viewport did not change.
func TestFontChangeRecomputesTheGrid(t *testing.T) {
	g := newRealTerminalGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)

	tt := ui.NewTester(View(g.state), 900, 600)
	tt.SetLocale("en")
	surfaces := paneSurfaces(t, g, 0)
	pumpUntil(t, tt, "the initial grid", func() bool { return gridsSettled(surfaces...) })

	surface := surfaces[0]
	before := surface.Grid()
	calls := surface.GridForCalls()
	t.Cleanup(func() { terminalsurface.SetTerminalFont("", 0) })
	terminalsurface.SetTerminalFont("monospace", 20)
	pumpUntil(t, tt, "the grid to follow the font", func() bool {
		return surface.Grid() != before && surface.GridForCalls() > calls
	})

	want, ok := appview.GridFor(surface.Viewport())
	if !ok || surface.Grid() != want {
		t.Fatalf("grid after the font change = %+v, want %+v", surface.Grid(), want)
	}
	if last, ok := surface.transport.last(); !ok || last != want {
		t.Fatalf("PTY size after the font change = %+v, want %+v", last, want)
	}
}

// recordingSessionTransport is a Session transport that records the PTY size
// the production Session surface's terminal delivers. It is the production
// path's resize log.
type recordingSessionTransport struct {
	mu      sync.Mutex
	resizes []appview.Size
	output  chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newRecordingSessionTransport() *recordingSessionTransport {
	return &recordingSessionTransport{
		output: make(chan []byte, 8),
		closed: make(chan struct{}),
	}
}

func (t *recordingSessionTransport) Read(p []byte) (int, error) {
	select {
	case data := <-t.output:
		return copy(p, data), nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *recordingSessionTransport) Write(p []byte) (int, error) { return len(p), nil }

func (t *recordingSessionTransport) Resize(cols, rows int) error {
	t.mu.Lock()
	t.resizes = append(t.resizes, appview.Size{Cols: cols, Rows: rows})
	t.mu.Unlock()
	return nil
}

func (t *recordingSessionTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *recordingSessionTransport) last() (appview.Size, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.resizes) == 0 {
		return appview.Size{}, false
	}
	return t.resizes[len(t.resizes)-1], true
}

func (t *recordingSessionTransport) log() []appview.Size {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]appview.Size(nil), t.resizes...)
}

// recordingSessionFactory gives each Session a transport that records the
// production surface's PTY resizes, in Session creation order.
type recordingSessionFactory struct {
	mu         sync.Mutex
	transports []*recordingSessionTransport
}

func (f *recordingSessionFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	transport := newRecordingSessionTransport()
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

func (f *recordingSessionFactory) all() []*recordingSessionTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*recordingSessionTransport(nil), f.transports...)
}

// TestProductionSessionSurfaceSizesEachPaneFromItsOwnBox runs the real shell
// with the production Session surface. The 70% Pane's PTY must get the wider
// (or taller) grid; the old package-level viewport swapped them.
func TestProductionSessionSurfaceSizesEachPaneFromItsOwnBox(t *testing.T) {
	for _, tc := range []struct {
		name  string
		split func(*state.State) error
	}{
		{"split-right", (*state.State).SplitRight},
		{"split-down", (*state.State).SplitDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := &recordingSessionFactory{}
			service := appsession.NewService(factory)
			t.Cleanup(service.CloseAll)
			st := state.NewState(state.WithSessions(service, func(session *appsession.Session) (state.Surface, error) {
				return sessionui.New(session, sessionui.WithRedraw(func() {}))
			}))

			if err := st.NewTab(); err != nil {
				t.Fatal(err)
			}
			if err := tc.split(st); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, st, 2)

			tt := ui.NewTester(View(st), 900, 600)
			tt.SetLocale("en")
			root, _, ok := st.TabTree(0)
			if !ok || root == nil || root.IsPane() {
				t.Fatalf("Tab 0 is not split")
			}
			if !st.SetSplitRatio(0, root.SplitID, 70) {
				t.Fatalf("SetSplitRatio(%s, 70) failed", root.SplitID)
			}
			pumpUntil(t, tt, "both Panes' PTY sizes", func() bool {
				transports := factory.all()
				if len(transports) < 2 {
					return false
				}
				for _, transport := range transports[:2] {
					if _, ok := transport.last(); !ok {
						return false
					}
				}
				first, _ := transports[0].last()
				second, _ := transports[1].last()
				if root.Direction == domainworkspace.Horizontal {
					return first.Rows > second.Rows+1
				}
				return first.Cols > second.Cols+1
			})

			transports := factory.all()
			first, _ := transports[0].last()
			second, _ := transports[1].last()
			t.Logf("First Pane PTY %+v; Second Pane PTY %+v", first, second)
			if root.Direction == domainworkspace.Horizontal {
				if first.Rows <= second.Rows {
					t.Fatalf("top Pane rows = %d, bottom Pane rows = %d: the grids are swapped", first.Rows, second.Rows)
				}
				if diff := first.Cols - second.Cols; diff < -1 || diff > 1 {
					t.Fatalf("top Pane cols = %d, bottom Pane cols = %d, want equal widths", first.Cols, second.Cols)
				}
			} else {
				if first.Cols <= second.Cols {
					t.Fatalf("left Pane cols = %d, right Pane cols = %d: the grids are swapped", first.Cols, second.Cols)
				}
				if diff := first.Rows - second.Rows; diff < -1 || diff > 1 {
					t.Fatalf("left Pane rows = %d, right Pane rows = %d, want equal heights", first.Rows, second.Rows)
				}
			}
			// A settled layout must not deliver another PTY size.
			logs := []int{len(transports[0].log()), len(transports[1].log())}
			for frame := 0; frame < 5; frame++ {
				tt.Frame()
			}
			for i, transport := range transports[:2] {
				if got := len(transport.log()); got != logs[i] {
					t.Errorf("Pane %d delivered %d resizes on steady frames, want none", i+1, got-logs[i])
				}
			}
		})
	}
}
