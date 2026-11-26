package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// Ticket 21c's non-gated frame-budget record. It measures one full motion
// path per event and Interface Style: the change frame, then 16 ms animation
// frames until the runtime rests, then a warp-only reset for the next
// iteration. "frames/event" is the deterministic record (the event's duration
// divided by the cadence); the benchmark's ns/op is this host's wall clock and
// is never asserted. At the standard speed the observed frames/event are:
//
//	pane open 14, pane close 11, pane reorder 34, pane zoom 34,
//	tab open 14, tab close 16, tab switch 16, tab reorder 12.
//
// Run it with:
//
//	go test ./ui/shell -run '^$' -bench 'BenchmarkMotionEventFrames' -benchmem
//
// The fake terminal surfaces keep the measurement on layout, overlays and the
// view runtime; ticket 21c's real-terminal tests own the PTY and grid costs.

type benchMotionWindow struct {
	graph   *workspaceViewGraph
	tt      *ui.Tester
	wm      *workspacePaneMotion
	reveal  *workTabReveal
	current *domainpreferences.Motion
}

func newBenchMotionWindow(b *testing.B, style kit.Style, tabs int) *benchMotionWindow {
	b.Helper()
	current := matrixAllOn()
	service := appsession.NewService(multiFactory{})
	b.Cleanup(service.CloseAll)
	st := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithMotion(func() domainpreferences.Motion { return current }),
	)
	g := &workspaceViewGraph{state: st}
	for range tabs {
		if err := st.NewTab(); err != nil {
			b.Fatalf("NewTab: %v", err)
		}
	}
	g.waitForSurfaces(b, tabs)
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(st, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(styletest.Styled(style, finalFrameTheme(), view), 900, 600)
	tt.SetLocale("en")
	w := &benchMotionWindow{graph: g, tt: tt, wm: wm, reveal: reveal, current: &current}
	w.settle(b)
	return w
}

// settle rests every runtime without asserting wall-clock time.
func (w *benchMotionWindow) settle(b *testing.B) {
	b.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.tt.Frame()
		if motionIdle(w.wm, w.reveal) && !w.tt.FramePending() {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("the benchmark scene did not settle: run %+v body %v strip %+v pending %v",
				w.wm.run, w.wm.body.phase, w.reveal.strip.run, w.tt.FramePending())
		}
		w.tt.Advance(16 * time.Millisecond)
	}
}

// withoutMotion runs a reset with every switch off, so a benchmark measures
// only its own event and not the setup's animation.
func (w *benchMotionWindow) withoutMotion(b *testing.B, reset func()) {
	b.Helper()
	saved := *w.current
	off := saved
	off.Enabled = false
	*w.current = off
	reset()
	w.waitSurfaces(b)
	w.settle(b)
	// A warp frame reports the previous frame's LayoutBounds, so the pane
	// boxes the next event reads are recorded by one more build.
	w.tt.Frame()
	*w.current = saved
}

// waitSurfaces waits until every Pane in every Tab has its Session surface.
// A split opens the Session off the UI thread; an animation planned before it
// lands would move a placeholder with no recorded box, which is not the path
// a user sees.
func (w *benchMotionWindow) waitSurfaces(b *testing.B) {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.graph.state.Flush()
		want, live := 0, 0
		for tab := 0; tab < w.graph.state.TabCount(); tab++ {
			root, _, ok := w.graph.state.TabTree(tab)
			if !ok {
				continue
			}
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				want++
				if info, ok := w.graph.state.PaneInfoAt(tab, id); ok && info.Surface != nil {
					live++
				}
			}
		}
		if want > 0 && live == want {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("the benchmark scene did not get its Pane surfaces: %d of %d", live, want)
		}
		time.Sleep(time.Millisecond)
	}
}

type benchMotionEvent struct {
	name  string
	tabs  int
	setup func(b *testing.B, w *benchMotionWindow) (trigger, reset func())
}

// benchmarkTabID is realTerminalGraph.mustTabID for the fake-surface graph.
func benchmarkTabID(b *testing.B, g *workspaceViewGraph, index int) model.TabID {
	b.Helper()
	id, ok := g.state.TabID(index)
	if !ok {
		b.Fatalf("Tab %d has no ID", index)
	}
	return id
}

func benchMotionEvents() []benchMotionEvent {
	split := func(b *testing.B, w *benchMotionWindow) (func(), func()) {
		b.Helper()
		w.withoutMotion(b, func() {
			if err := w.graph.state.SplitRight(); err != nil {
				b.Fatalf("SplitRight: %v", err)
			}
		})
		return func() { _ = w.graph.state.CloseFocusedPane() }, func() {
			if err := w.graph.state.SplitRight(); err != nil {
				b.Fatalf("SplitRight: %v", err)
			}
		}
	}
	return []benchMotionEvent{
		{
			name: "pane-open", tabs: 1,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				return func() {
					if err := w.graph.state.SplitRight(); err != nil {
						b.Fatalf("SplitRight: %v", err)
					}
				}, func() { _ = w.graph.state.CloseFocusedPane() }
			},
		},
		{
			name: "pane-close", tabs: 1,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				return split(b, w)
			},
		},
		{
			name: "pane-reorder", tabs: 1,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				w.withoutMotion(b, func() {
					if err := w.graph.state.SplitRight(); err != nil {
						b.Fatalf("SplitRight: %v", err)
					}
					root, _, ok := w.graph.state.TabTree(0)
					if !ok {
						b.Fatalf("the split Tab is missing")
					}
					if !w.graph.state.SetSplitRatio(0, root.SplitID, 70) {
						b.Fatalf("SetSplitRatio failed")
					}
				})
				root, _, ok := w.graph.state.TabTree(0)
				if !ok {
					b.Fatalf("the split Tab is missing")
				}
				ids := domainworkspace.CollectPaneIDs(root)
				tabID, _ := w.graph.state.TabID(0)
				return func() {
						_ = w.graph.state.MovePane(tabID, ids[1], tabID, ids[0], domainworkspace.SideLeft)
					}, func() {
						_ = w.graph.state.MovePane(tabID, ids[1], tabID, ids[0], domainworkspace.SideRight)
					}
			},
		},
		{
			name: "pane-zoom", tabs: 1,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				w.withoutMotion(b, func() {
					if err := w.graph.state.SplitRight(); err != nil {
						b.Fatalf("SplitRight: %v", err)
					}
				})
				return func() { _ = w.graph.state.TogglePaneZoom() }, func() { _ = w.graph.state.TogglePaneZoom() }
			},
		},
		{
			name: "tab-open", tabs: 1,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				return func() {
					if err := w.graph.state.NewTab(); err != nil {
						b.Fatalf("NewTab: %v", err)
					}
				}, func() { _ = w.graph.state.CloseActiveTab() }
			},
		},
		{
			name: "tab-close", tabs: 2,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				return func() { _ = w.graph.state.CloseActiveTab() }, func() {
					if err := w.graph.state.NewTab(); err != nil {
						b.Fatalf("NewTab: %v", err)
					}
				}
			},
		},
		{
			name: "tab-switch", tabs: 2,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				return func() { w.graph.state.SelectTab(0) }, func() { w.graph.state.SelectTab(1) }
			},
		},
		{
			name: "tab-reorder", tabs: 3,
			setup: func(b *testing.B, w *benchMotionWindow) (func(), func()) {
				first := benchmarkTabID(b, w.graph, 0)
				middle := benchmarkTabID(b, w.graph, 1)
				last := benchmarkTabID(b, w.graph, 2)
				return func() { _ = w.graph.state.MoveTab(last, first, false) },
					func() { _ = w.graph.state.MoveTab(last, middle, true) }
			},
		},
	}
}

// BenchmarkMotionEventFrames is the record above: one sub-benchmark per event
// and Interface Style.
func BenchmarkMotionEventFrames(b *testing.B) {
	for _, style := range styletest.Styles() {
		style := style
		b.Run(style.ID(), func(b *testing.B) {
			kit.SetStyle(style)
			defer kit.SetStyle(nil)
			for _, event := range benchMotionEvents() {
				event := event
				b.Run(event.name, func(b *testing.B) {
					benchmarkOneMotionEvent(b, style, event)
				})
			}
		})
	}
}

func benchmarkOneMotionEvent(b *testing.B, style kit.Style, event benchMotionEvent) {
	b.Helper()
	w := newBenchMotionWindow(b, style, event.tabs)
	trigger, reset := event.setup(b, w)
	// One unmeasured pass primes the style's text, material and overlay
	// caches, as the app's first motion after launch would.
	play := func() int {
		trigger()
		w.waitSurfaces(b)
		w.tt.Frame() // the change frame; it freezes the final layout and grid
		frames := 0
		// Advance runs one frame per display tick; Frame afterwards would
		// settle up to 20 more at a frozen clock, which is a test harness
		// artifact, not a frame of the path being measured.
		for !motionIdle(w.wm, w.reveal) {
			w.tt.Advance(16 * time.Millisecond)
			frames++
			if frames > 1000 {
				b.Fatalf("the %s run asked for %d frames; it is not converging", event.name, frames)
			}
		}
		return frames
	}
	if warm := play(); warm == 0 {
		latest, _ := w.graph.state.MotionEvent()
		b.Fatalf("the %s event played no animation frame: run %+v body %v strip %+v event %+v",
			event.name, w.wm.run, w.wm.body.phase, w.reveal.strip.run, latest)
	}
	w.withoutMotion(b, reset)
	b.ResetTimer()

	totalFrames := 0
	for range b.N {
		frames := play()
		if frames == 0 {
			b.Fatalf("the %s event played no animation frame", event.name)
		}
		totalFrames += frames
		w.withoutMotion(b, reset)
	}
	b.StopTimer()
	if totalFrames > 0 {
		b.ReportMetric(float64(totalFrames)/float64(b.N), "frames/event")
	}
}
