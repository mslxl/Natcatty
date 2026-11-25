package shell

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// Ticket 21c's hard invariants, as real tests over the real views:
//
//   - one PTY resize per affected Pane per event, and zero additional resizes
//     while an animation plays;
//   - zero GridFor calls on animation frames (no per-frame reflow);
//   - master off, event off, and Reduce Motion warp with zero animation frames
//     and no overlays;
//   - a hidden or unrendered view asks for no frame and warps when shown.
//
// The counters come from the existing real terminal test surface, which logs
// every PTY Resize and every GridFor it computes; no production seam is added.

// invariantWindow is a real-terminal main window whose motion getter can be
// switched off while a test lays out its baseline. A settled baseline reached
// by a warp is the same final layout an animated one ends at, and it keeps the
// invariant test focused on the event under measurement.
type invariantWindow struct {
	graph  *realTerminalGraph
	tt     *ui.Tester
	wm     *workspacePaneMotion
	reveal *workTabReveal
}

// newInvariantWindow builds a styled window with tabs single-Pane Tabs, shows
// every body once with motion off, runs setup (splits, ratios) with motion
// off, then turns the setting on and rests.
func newInvariantWindow(t *testing.T, style kit.Style, setting domainpreferences.Motion,
	tabs int, setup func(t *testing.T, w *invariantWindow)) *invariantWindow {
	t.Helper()
	current := setting
	current.Enabled = false
	g := newRealTerminalGraph(t, state.WithMotion(func() domainpreferences.Motion { return current }))
	for range tabs {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, tabs)
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(styletest.Styled(style, finalFrameTheme(), view), 900, 600)
	tt.SetLocale("en")
	w := &invariantWindow{graph: g, tt: tt, wm: wm, reveal: reveal}
	for index := range tabs {
		g.state.SelectTab(index)
		w.settle(t)
	}
	g.state.SelectTab(tabs - 1)
	w.settle(t)
	if setup != nil {
		setup(t, w)
		w.settle(t)
	}
	current = setting
	w.settle(t)
	return w
}

// settle builds frames until every runtime is idle and every drawn Pane's
// terminal reaches its final grid. It uses the same bounded-loop shape as the
// snapshot tests.
func (w *invariantWindow) settle(t *testing.T) {
	t.Helper()
	w.settleTo(t, true)
}

// settleGrids is settle for a window whose resize logs a test has reset: an
// unchanged Pane has nothing new to deliver, so only the grid has to match.
func (w *invariantWindow) settleGrids(t *testing.T) {
	t.Helper()
	w.settleTo(t, false)
}

func (w *invariantWindow) settleTo(t *testing.T, requireTransport bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.tt.Frame()
		idle := w.wm.run == nil && w.wm.body.phase == bodyIdle && w.reveal.strip.run == nil
		grids := true
		if root, _, ok := w.wm.tabTree(); ok {
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				info, ok := w.wm.paneInfo(id)
				if !ok || info.Surface == nil {
					grids = false
					continue
				}
				if surface, ok := info.Surface.(*realTerminalSurface); ok {
					if !requireTransport {
						want, wantOK := appview.GridFor(surface.Viewport())
						if !wantOK || surface.Grid() != want {
							grids = false
						}
						continue
					}
					if !gridsSettled(surface) {
						grids = false
					}
				} else {
					grids = false
				}
			}
		}
		if idle && grids {
			return
		}
		if time.Now().After(deadline) {
			var detail []string
			root, _, ok := w.wm.tabTree()
			detail = append(detail, "tree="+boolWord(ok))
			if ok {
				for _, id := range domainworkspace.CollectPaneIDs(root) {
					info, ok := w.wm.paneInfo(id)
					if !ok {
						detail = append(detail, string(id)+":missing")
						continue
					}
					if info.Surface == nil {
						detail = append(detail, string(id)+":no-surface pending="+boolWord(info.Pending))
						continue
					}
					surface, ok := info.Surface.(*realTerminalSurface)
					if !ok {
						detail = append(detail, string(id)+":fake")
						continue
					}
					want, wantOK := appview.GridFor(surface.Viewport())
					last, lastOK := surface.transport.last()
					detail = append(detail, string(id)+" viewport="+fmt.Sprint(surface.Viewport())+
						" grid="+fmt.Sprint(surface.Grid())+" want="+fmt.Sprint(want)+"/"+boolWord(wantOK)+
						" last="+fmt.Sprint(last)+"/"+boolWord(lastOK))
				}
			}
			t.Fatalf("the invariant scene did not settle: run %+v body %v strip %+v active=%d %s",
				w.wm.run, w.wm.body.phase, w.reveal.strip.run, w.graph.state.ActiveTab(), strings.Join(detail, " | "))
		}
		w.tt.Advance(24 * time.Millisecond)
	}
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// split lays out a two-Pane split on the active Tab and rests. The caller runs
// it while motion is off, so the split arrives at the same final layout a warp
// would produce.
func (w *invariantWindow) split(t *testing.T) {
	t.Helper()
	if err := w.graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	w.settle(t)
}

// surfaces lists every live Pane surface in tree order across the state's
// Tabs. It is the baseline set a close or a batch may remove surfaces from.
func (w *invariantWindow) surfaces(t *testing.T) map[*realTerminalSurface]appview.Size {
	t.Helper()
	out := map[*realTerminalSurface]appview.Size{}
	for index := 0; index < w.graph.state.TabCount(); index++ {
		root, _, ok := w.graph.state.TabTree(index)
		if !ok {
			continue
		}
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			info, ok := w.graph.state.PaneInfoAt(index, id)
			if !ok || info.Surface == nil {
				continue
			}
			surface, ok := info.Surface.(*realTerminalSurface)
			if !ok {
				t.Fatalf("Pane %q surface = %T, want the real terminal surface", id, info.Surface)
			}
			out[surface] = surface.Grid()
		}
	}
	return out
}

// motionFrameCounts is the three runtimes' requested-animation-frame counts.
type motionFrameCounts struct {
	pane, body, strip int
}

func framesOf(wm *workspacePaneMotion, reveal *workTabReveal) motionFrameCounts {
	counts := motionFrameCounts{pane: wm.animationFrames}
	if wm.body != nil {
		counts.body = wm.body.animationFrames
	}
	if reveal != nil {
		counts.strip = reveal.strip.animationFrames
	}
	return counts
}

func (c motionFrameCounts) minus(before motionFrameCounts) motionFrameCounts {
	return motionFrameCounts{pane: c.pane - before.pane, body: c.body - before.body, strip: c.strip - before.strip}
}

// motionIdle reports that every runtime of this window is at rest.
func motionIdle(wm *workspacePaneMotion, reveal *workTabReveal) bool {
	if wm.run != nil {
		return false
	}
	if wm.body != nil && wm.body.phase != bodyIdle {
		return false
	}
	return reveal == nil || reveal.strip.run == nil
}

// invariantEvent is one of the ticket's seven events. setup prepares the
// baseline with motion off; trigger drives the event with motion on. The
// want* flags name the runtime that must animate (the one-switch rule), so a
// test that silently warps cannot pass by asking for no frames at all.
type invariantEvent struct {
	name    string
	kind    motion.Kind
	setup   func(t *testing.T, w *invariantWindow)
	trigger func(t *testing.T, w *invariantWindow)
	tabs    int
	want    motionFrameCounts // 1 per runtime that must animate
}

func invariantEvents() []invariantEvent {
	one := func(kind motion.Kind) motionFrameCounts {
		switch kind {
		case motion.TabOpen, motion.TabClose:
			return motionFrameCounts{body: 1, strip: 1}
		case motion.TabSwitch:
			return motionFrameCounts{body: 1}
		case motion.TabReorder:
			return motionFrameCounts{strip: 1}
		default:
			return motionFrameCounts{pane: 1}
		}
	}
	events := []invariantEvent{
		{
			name: "pane-open", kind: motion.PaneOpen, tabs: 1,
			trigger: func(t *testing.T, w *invariantWindow) {
				if err := w.graph.state.SplitRight(); err != nil {
					t.Fatalf("SplitRight: %v", err)
				}
			},
		},
		{
			name: "pane-close", kind: motion.PaneClose, tabs: 1,
			setup: func(t *testing.T, w *invariantWindow) { w.split(t) },
			trigger: func(t *testing.T, w *invariantWindow) {
				if !w.graph.state.CloseFocusedPane() {
					t.Fatalf("CloseFocusedPane returned false")
				}
			},
		},
		{
			name: "pane-reorder", kind: motion.PaneReorder, tabs: 1,
			setup: func(t *testing.T, w *invariantWindow) {
				w.split(t)
				root, _, ok := w.graph.state.TabTree(0)
				if !ok {
					t.Fatalf("the split Tab is missing")
				}
				if !w.graph.state.SetSplitRatio(0, root.SplitID, 70) {
					t.Fatalf("SetSplitRatio failed")
				}
			},
			trigger: func(t *testing.T, w *invariantWindow) {
				ids := motionPaneIDsMust(t, w.graph)
				tabID, _ := w.graph.state.TabID(0)
				if !w.graph.state.MovePane(tabID, ids[1], tabID, ids[0], domainworkspace.SideLeft) {
					t.Fatalf("MovePane in one Tab returned false")
				}
			},
		},
		{
			name: "pane-zoom", kind: motion.PaneReorder, tabs: 1,
			setup: func(t *testing.T, w *invariantWindow) { w.split(t) },
			trigger: func(t *testing.T, w *invariantWindow) {
				if !w.graph.state.TogglePaneZoom() {
					t.Fatalf("TogglePaneZoom returned false")
				}
			},
		},
		{
			name: "tab-open", kind: motion.TabOpen, tabs: 1,
			trigger: func(t *testing.T, w *invariantWindow) {
				if err := w.graph.state.NewTab(); err != nil {
					t.Fatalf("NewTab: %v", err)
				}
			},
		},
		{
			name: "tab-close", kind: motion.TabClose, tabs: 2,
			trigger: func(t *testing.T, w *invariantWindow) {
				if !w.graph.state.CloseActiveTab() {
					t.Fatalf("CloseActiveTab returned false")
				}
			},
		},
		{
			name: "tab-switch", kind: motion.TabSwitch, tabs: 2,
			trigger: func(t *testing.T, w *invariantWindow) {
				w.graph.state.SelectTab(0)
			},
		},
		{
			name: "tab-reorder", kind: motion.TabReorder, tabs: 3,
			trigger: func(t *testing.T, w *invariantWindow) {
				first := w.graph.mustTabID(t, 0)
				last := w.graph.mustTabID(t, 2)
				if !w.graph.state.MoveTab(last, first, false) {
					t.Fatalf("MoveTab returned false")
				}
			},
		},
	}
	for index := range events {
		events[index].want = one(events[index].kind)
	}
	return events
}

// TestMotionOneResizePerAffectedPaneAndNoReflow is the no-reflow gate over
// every event and both Interface Styles. At the change each Pane may deliver
// one resize and compute one grid; every frame after it must deliver and
// compute none. At the final frame an affected Pane has exactly one resize,
// an unchanged Pane none, a new Pane exactly its one creation resize, and a
// removed Pane none.
func TestMotionOneResizePerAffectedPaneAndNoReflow(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		style := kit.CurrentStyle()
		for _, event := range invariantEvents() {
			event := event
			t.Run(event.name, func(t *testing.T) {
				w := newInvariantWindow(t, style, matrixAllOn(), event.tabs, event.setup)
				assertMotionEventInvariants(t, w, event)
			})
		}
	})
}

// assertMotionEventInvariants drives one event and checks the resize, grid and
// frame invariants around it.
func assertMotionEventInvariants(t *testing.T, w *invariantWindow, event invariantEvent) {
	t.Helper()
	baseline := w.surfaces(t)
	initialGrid := make(map[*realTerminalSurface]appview.Size, len(baseline))
	initialCalls := make(map[*realTerminalSurface]int, len(baseline))
	for surface := range baseline {
		initialGrid[surface] = surface.Grid()
		initialCalls[surface] = surface.GridForCalls()
		surface.ResetResizeLog()
	}
	before := framesOf(w.wm, w.reveal)

	event.trigger(t, w)
	w.tt.Frame()
	// A new Split or Tab creates its Pane asynchronously; wait until the view
	// has built every Pane of its final tree so the change frame's grid work is
	// fully accounted for.
	pumpUntil(t, w.tt, "the event's Panes", func() bool {
		root, _, ok := w.wm.tabTree()
		if !ok {
			return w.graph.state.TabCount() == 0
		}
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			info, ok := w.wm.paneInfo(id)
			if !ok || info.Surface == nil {
				return false
			}
		}
		return true
	})
	w.tt.Frame()
	// The change's one PTY size reaches the fake program asynchronously. Wait
	// for it before counting animation frames, so a delivery that belongs to
	// the change is never charged to an animation frame. A Pane whose grid did
	// not change has nothing to deliver.
	pumpUntil(t, w.tt, "the "+event.name+" change PTY sizes", func() bool {
		for surface := range w.surfaces(t) {
			if grid, existed := initialGrid[surface]; existed && surface.Grid() == grid {
				continue
			}
			if last, ok := surface.transport.last(); !ok || last != surface.Grid() {
				return false
			}
		}
		return true
	})

	changeLogs := map[*realTerminalSurface]int{}
	changeCalls := map[*realTerminalSurface]int{}
	// At the change, a Pane resized and computed its grid exactly once if it
	// is new or if its final grid differs from the one it had before the
	// event, and not at all otherwise. The change frame is where the one
	// reflow belongs; an unchanged Pane must not recompute its grid either.
	for surface := range w.surfaces(t) {
		want := 0
		if grid, existed := initialGrid[surface]; !existed || surface.Grid() != grid {
			want = 1
		}
		if got := len(surface.ResizeLog()); got != want {
			t.Fatalf("a Pane delivered %d resizes at the %s change, want %d: %+v",
				got, event.name, want, surface.ResizeLog())
		}
		if got := surface.GridForCalls() - initialCalls[surface]; got != want {
			t.Fatalf("a Pane computed %d grids at the %s change, want %d (one for a new or grid-changing Pane, none otherwise)",
				got, event.name, want)
		}
		changeLogs[surface] = len(surface.ResizeLog())
		changeCalls[surface] = surface.GridForCalls()
	}

	// Every animation frame must leave the PTY size and the grid computation
	// untouched. Sample at the test's frame cadence while any runtime moves.
	samples, deadline := 0, time.Now().Add(5*time.Second)
	for !motionIdle(w.wm, w.reveal) {
		if time.Now().After(deadline) {
			t.Fatalf("the %s run did not end", event.name)
		}
		w.tt.Advance(24 * time.Millisecond)
		w.tt.Frame()
		samples++
		if samples > 1000 {
			t.Fatalf("the %s run asked for %d frames; it is not converging", event.name, samples)
		}
		for surface, logs := range changeLogs {
			if got := len(surface.ResizeLog()); got != logs {
				t.Fatalf("Pane delivered %d resizes during %s animation frames, want %d: %+v",
					got, event.name, logs, surface.ResizeLog())
			}
			if got := surface.GridForCalls(); got != changeCalls[surface] {
				t.Fatalf("Pane computed %d grids during %s animation frames, want %d",
					got, event.name, changeCalls[surface])
			}
		}
	}
	if samples == 0 {
		t.Fatalf("the %s event asked for no animation frame despite its switch being on", event.name)
	}

	// The runtime that owns the event animated; the other switches stayed out.
	frames := framesOf(w.wm, w.reveal).minus(before)
	if event.want.pane == 1 && frames.pane == 0 {
		t.Fatalf("the %s event played no Pane frames", event.name)
	}
	if event.want.body == 1 && frames.body == 0 {
		t.Fatalf("the %s event played no body frames", event.name)
	}
	if event.want.strip == 1 && frames.strip == 0 {
		t.Fatalf("the %s event played no strip frames", event.name)
	}
	if event.want.pane == 0 && frames.pane != 0 {
		t.Fatalf("the %s event touched the Pane runtime: %+v", event.name, frames)
	}
	if event.want.body == 0 && frames.body != 0 {
		t.Fatalf("the %s event touched the body runtime: %+v", event.name, frames)
	}
	if event.want.strip == 0 && frames.strip != 0 {
		t.Fatalf("the %s event touched the strip runtime: %+v", event.name, frames)
	}

	w.settleGrids(t)
	final := w.surfaces(t)
	for surface := range final {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Fatalf("a final Pane grid = %+v, want its final viewport's %+v", surface.Grid(), want)
		}
		// A resize log was reset before the event: only a Pane that delivered
		// its one change size has a last entry, and it must agree with the grid.
		if logged := surface.ResizeLog(); len(logged) > 0 {
			if last := logged[len(logged)-1]; last != surface.Grid() {
				t.Fatalf("a final PTY size = %+v; want the final grid %+v", last, surface.Grid())
			}
		}
	}
	for surface := range final {
		if _, existed := initialGrid[surface]; existed {
			want := 0
			if surface.Grid() != initialGrid[surface] {
				want = 1
			}
			if got := len(surface.ResizeLog()); got != want {
				t.Fatalf("a %s Pane delivered %d resizes, want %d (grid %+v -> %+v)",
					event.name, got, want, initialGrid[surface], surface.Grid())
			}
			continue
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("a Pane created by %s delivered %d resizes, want exactly its one creation resize",
				event.name, got)
		}
	}
	for surface := range baseline {
		if _, live := final[surface]; live {
			continue
		}
		// A removed Pane is already leaving; nothing may resize it or
		// recompute its grid again.
		if got := len(surface.ResizeLog()); got != 0 {
			t.Fatalf("a Pane removed by %s delivered %d resizes, want none: %+v",
				event.name, got, surface.ResizeLog())
		}
		if got := surface.GridForCalls() - initialCalls[surface]; got != 0 {
			t.Fatalf("a Pane removed by %s computed %d grids, want none", event.name, got)
		}
	}
}

// gateWindow is a fast fake-surface main window for the switch gates. The
// terminals do not need to paint; the assertions are about requested frames
// and built overlays.
type gateWindow struct {
	graph  *workspaceViewGraph
	tt     *ui.Tester
	wm     *workspacePaneMotion
	reveal *workTabReveal
}

func newGateWindow(t *testing.T, setting domainpreferences.Motion, tabs int, reduce bool) *gateWindow {
	t.Helper()
	g := fakeMotionGraph(t, setting)
	for range tabs {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	g.waitForSurfaces(t, tabs)
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	opts := ui.TesterOptions{}
	if reduce {
		opts.Preferences = ui.Preferences{ReduceMotion: true}
	}
	tt := ui.NewTesterWith(view, 900, 600, opts)
	tt.SetLocale("en")
	tt.Frame()
	for index := range tabs {
		g.state.SelectTab(index)
		tt.Frame()
	}
	g.state.SelectTab(tabs - 1)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	return &gateWindow{graph: g, tt: tt, wm: wm, reveal: reveal}
}

func (w *gateWindow) split(t *testing.T) {
	t.Helper()
	if err := w.graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	w.tt.Frame()
	w.graph.waitForSurfaces(t, 2)
	w.tt.Frame()
	w.tt.Advance(400 * time.Millisecond)
	w.tt.Frame()
}

func (w *gateWindow) paneCount(t *testing.T) int {
	t.Helper()
	root, _, ok := w.graph.state.TabTree(w.graph.state.ActiveTab())
	if !ok {
		return 0
	}
	return len(domainworkspace.CollectPaneIDs(root))
}

// tabIDs lists every Tab's ID in strip order.
func (w *gateWindow) tabIDs(t *testing.T) []model.TabID {
	t.Helper()
	ids := make([]model.TabID, 0, w.graph.state.TabCount())
	for index := 0; index < w.graph.state.TabCount(); index++ {
		id, ok := w.graph.state.TabID(index)
		if !ok {
			t.Fatalf("Tab %d has no ID", index)
		}
		ids = append(ids, id)
	}
	return ids
}

// gateEvent is one event under the three warp gates.
type gateEvent struct {
	name     string
	off      func(setting *domainpreferences.Motion)
	tabs     int
	baseline func(t *testing.T, w *gateWindow)
	trigger  func(t *testing.T, w *gateWindow)
	want     func(t *testing.T, w *gateWindow)
}

func gateEvents() []gateEvent {
	return []gateEvent{
		{
			name: "pane-open", tabs: 1,
			off: func(setting *domainpreferences.Motion) { setting.PaneOpen = false },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if err := w.graph.state.SplitRight(); err != nil {
					t.Fatalf("SplitRight: %v", err)
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.paneCount(t); got != 2 {
					t.Fatalf("Panes = %d, want 2", got)
				}
			},
		},
		{
			name: "pane-close", tabs: 1,
			off:      func(setting *domainpreferences.Motion) { setting.PaneClose = false },
			baseline: func(t *testing.T, w *gateWindow) { w.split(t) },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if !w.graph.state.CloseFocusedPane() {
					t.Fatalf("CloseFocusedPane returned false")
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.paneCount(t); got != 1 {
					t.Fatalf("Panes = %d, want 1", got)
				}
			},
		},
		{
			name: "pane-reorder", tabs: 1,
			// A same-Tab move is one event with three parts, so its "event off"
			// gate turns all three off. Ticket 21b's per-part gating tests
			// already cover each switch alone.
			off: func(setting *domainpreferences.Motion) {
				setting.PaneOpen = false
				setting.PaneClose = false
				setting.PaneReorder = false
			},
			baseline: func(t *testing.T, w *gateWindow) { w.split(t) },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				root, _, ok := w.graph.state.TabTree(0)
				if !ok {
					t.Fatalf("the split Tab is missing")
				}
				ids := domainworkspace.CollectPaneIDs(root)
				tabID, _ := w.graph.state.TabID(0)
				if !w.graph.state.MovePane(tabID, ids[1], tabID, ids[0], domainworkspace.SideLeft) {
					t.Fatalf("MovePane returned false")
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.paneCount(t); got != 2 {
					t.Fatalf("Panes = %d, want 2", got)
				}
			},
		},
		{
			// A zoom is a PaneReorder event without a tree move.
			name: "pane-zoom", tabs: 1,
			off:      func(setting *domainpreferences.Motion) { setting.PaneReorder = false },
			baseline: func(t *testing.T, w *gateWindow) { w.split(t) },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if !w.graph.state.TogglePaneZoom() {
					t.Fatalf("TogglePaneZoom returned false")
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if w.graph.state.MagnifiedPaneID(w.graph.state.ActiveTab()) == "" {
					t.Fatalf("the zoom did not reach the state")
				}
			},
		},
		{
			name: "tab-open", tabs: 1,
			off: func(setting *domainpreferences.Motion) { setting.TabOpen = false },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if err := w.graph.state.NewTab(); err != nil {
					t.Fatalf("NewTab: %v", err)
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.graph.state.TabCount(); got != 2 {
					t.Fatalf("Tabs = %d, want 2", got)
				}
			},
		},
		{
			name: "tab-close", tabs: 2,
			off: func(setting *domainpreferences.Motion) { setting.TabClose = false },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if !w.graph.state.CloseActiveTab() {
					t.Fatalf("CloseActiveTab returned false")
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.graph.state.TabCount(); got != 1 {
					t.Fatalf("Tabs = %d, want 1", got)
				}
			},
		},
		{
			name: "tab-switch", tabs: 2,
			off:     func(setting *domainpreferences.Motion) { setting.TabSwitch = false },
			trigger: func(t *testing.T, w *gateWindow) { t.Helper(); w.graph.state.SelectTab(0) },
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.graph.state.ActiveTab(); got != 0 {
					t.Fatalf("active Tab = %d, want 0", got)
				}
			},
		},
		{
			name: "tab-reorder", tabs: 3,
			off: func(setting *domainpreferences.Motion) { setting.TabReorder = false },
			trigger: func(t *testing.T, w *gateWindow) {
				t.Helper()
				ids := w.tabIDs(t)
				if !w.graph.state.MoveTab(ids[2], ids[0], false) {
					t.Fatalf("MoveTab returned false")
				}
				want := []model.TabID{ids[2], ids[0], ids[1]}
				got := w.tabIDs(t)
				for index := range want {
					if got[index] != want[index] {
						t.Fatalf("tab order after the move = %v, want %v", got, want)
					}
				}
			},
			want: func(t *testing.T, w *gateWindow) {
				t.Helper()
				if got := w.graph.state.TabCount(); got != 3 {
					t.Fatalf("Tabs = %d, want 3", got)
				}
			},
		},
	}
}

// TestMotionGatesWarpWithNoFramesOrOverlays is the switch gate: with the
// master off, the event's own switch off, or Reduce Motion, the change reaches
// its final layout on the frame it is seen, asks for no animation frame in any
// runtime, and builds no moving copy, mask or ghost.
func TestMotionGatesWarpWithNoFramesOrOverlays(t *testing.T) {
	gates := []struct {
		name   string
		reduce bool
		apply  func(t *testing.T, setting *domainpreferences.Motion, event gateEvent)
	}{
		{name: "master-off", apply: func(t *testing.T, setting *domainpreferences.Motion, _ gateEvent) {
			setting.Enabled = false
		}},
		{name: "event-off", apply: func(t *testing.T, setting *domainpreferences.Motion, event gateEvent) {
			event.off(setting)
		}},
		{name: "reduce-motion", reduce: true, apply: func(*testing.T, *domainpreferences.Motion, gateEvent) {}},
	}
	for _, gate := range gates {
		for _, event := range gateEvents() {
			gate, event := gate, event
			t.Run(gate.name+"/"+event.name, func(t *testing.T) {
				setting := matrixAllOn()
				gate.apply(t, &setting, event)
				w := newGateWindow(t, setting, event.tabs, gate.reduce)
				if event.baseline != nil {
					event.baseline(t, w)
				}
				before := framesOf(w.wm, w.reveal)
				event.trigger(t, w)
				w.tt.Frame()
				w.tt.Advance(400 * time.Millisecond)
				w.tt.Frame()

				if !motionIdle(w.wm, w.reveal) {
					t.Fatalf("%s/%s played a run: pane %+v body %v strip %+v",
						gate.name, event.name, w.wm.run, w.wm.body.phase, w.reveal.strip.run)
				}
				if got := framesOf(w.wm, w.reveal).minus(before); got != (motionFrameCounts{}) {
					t.Fatalf("%s/%s asked for animation frames: %+v", gate.name, event.name, got)
				}
				if w.tt.FramePending() {
					t.Fatalf("%s/%s still asks for frames after its final frame", gate.name, event.name)
				}
				for _, text := range w.tt.Texts() {
					if strings.HasPrefix(text, "pane-motion:") || strings.HasPrefix(text, "pane-motion-mask:") ||
						strings.HasPrefix(text, "pane-close-ghost:") {
						t.Fatalf("%s/%s built the overlay %q", gate.name, event.name, text)
					}
				}
				event.want(t, w)
				latest, ok := w.graph.state.MotionEvent()
				if !ok {
					t.Fatalf("%s/%s recorded no event", gate.name, event.name)
				}
				if w.wm.controller.Unseen(latest.Seq) {
					t.Fatalf("%s/%s left the Pane runtime behind at %d", gate.name, event.name, latest.Seq)
				}
				if w.reveal.strip.lastSeq != latest.Seq {
					t.Fatalf("%s/%s left the strip at %d, want %d", gate.name, event.name, w.reveal.strip.lastSeq, latest.Seq)
				}
			})
		}
	}
}

// TestMotionHiddenViewsAskNoFramesAndWarpOnShow covers the visibility gate for
// the main window and its Pane-only siblings: a hidden view's event is not
// replayed; the show frame warps, asks for no extra animation frame, and
// catches the sequence up. Detach, Topdown, the mini strip and Quake have
// their own unrendered tests; this one pins the shared Pane and Tab-body
// paths.
func TestMotionHiddenViewsAskNoFramesAndWarpOnShow(t *testing.T) {
	t.Run("pane", func(t *testing.T) {
		cases := []struct {
			name  string
			mount func(*workspaceViewGraph) (func(*ui.Context), *workspacePaneMotion)
		}{
			{"main", func(g *workspaceViewGraph) (func(*ui.Context), *workspacePaneMotion) {
				return viewWithPaneMotion(g.state, paneMotionMode{labels: true})
			}},
			{"follow", func(g *workspaceViewGraph) (func(*ui.Context), *workspacePaneMotion) {
				return followWithPaneMotion(g.state)
			}},
			{"topdown", func(g *workspaceViewGraph) (func(*ui.Context), *workspacePaneMotion) {
				return topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
			}},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				g := fakeMotionGraph(t, matrixAllOn())
				if err := g.state.NewTab(); err != nil {
					t.Fatalf("NewTab: %v", err)
				}
				g.waitForSurfaces(t, 1)
				view, wm := tc.mount(g)
				tt := ui.NewTester(view, 900, 600)
				tt.SetLocale("en")
				tt.Frame()
				tt.Frame()
				tt.SetWindowHidden(true)
				tt.Frame()
				frames := wm.animationFrames
				if err := g.state.SplitRight(); err != nil {
					t.Fatalf("SplitRight: %v", err)
				}
				g.waitForSurfaces(t, 2)
				tt.SetWindowHidden(false)
				tt.Frame()
				assertPaneMotionWarped(t, tc.name, wm, frames)
				root, _, ok := g.state.TabTree(0)
				if !ok || len(domainworkspace.CollectPaneIDs(root)) != 2 {
					t.Fatalf("%s did not draw the final two-Pane layout", tc.name)
				}
			})
		}
	})

	t.Run("tab-body", func(t *testing.T) {
		g := fakeMotionGraph(t, matrixAllOn())
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		g.waitForSurfaces(t, 1)
		reveal := newWorkTabReveal()
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		tt.Frame()
		tt.Frame()
		tt.SetWindowHidden(true)
		tt.Frame()
		paneFrames := wm.animationFrames
		bodyFrames := wm.body.animationFrames
		stripFrames := reveal.strip.animationFrames
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		g.waitForSurfaces(t, 2)
		tt.SetWindowHidden(false)
		tt.Frame()
		if wm.body.phase != bodyIdle || reveal.strip.run != nil {
			t.Fatalf("the hidden Tab open replayed: body %v strip %+v", wm.body.phase, reveal.strip.run)
		}
		if got := wm.animationFrames; got != paneFrames {
			t.Fatalf("the hidden Tab open asked for %d Pane frames, want none", got-paneFrames)
		}
		if got := wm.body.animationFrames; got != bodyFrames {
			t.Fatalf("the hidden Tab open asked for %d body frames, want none", got-bodyFrames)
		}
		if got := reveal.strip.animationFrames; got != stripFrames {
			t.Fatalf("the hidden Tab open asked for %d strip frames, want none", got-stripFrames)
		}
		tabID, _ := g.state.TabID(g.state.ActiveTab())
		if _, ok := tt.Find(bodySpec{kind: bodySurfaceTab, tabID: tabID}.key()); !ok {
			t.Fatalf("the show frame did not draw the final Tab body")
		}
		if event, ok := g.state.MotionEvent(); !ok || wm.controller.Unseen(event.Seq) || reveal.strip.lastSeq != event.Seq {
			t.Fatalf("the show frame did not catch both runtimes up")
		}
	})
}
