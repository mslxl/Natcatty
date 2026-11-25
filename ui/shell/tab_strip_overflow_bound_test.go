package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// stripOnlySteppingView renders only the work Tab strip. It asks for the next
// frame only while step is set, so a test can let Tester.Frame settle a single
// build (the event paint) and then read the box that build painted through
// Find. A Tester.Advance would build only when a frame was already requested,
// and a settling Frame would hide a live defect in later same-clock builds.
func stripOnlySteppingView(s *state.State, reveal *workTabReveal, step *bool) func(*ui.Context) {
	return func(c *ui.Context) {
		workTabs(c, s, locales.In(c.Locale()), c.Theme(), reveal)
		if *step {
			c.Invalidate()
		}
	}
}

// TestPendingRowWidthBoundCoversLaidOutRows proves the Pending Connection
// bound against the row pendingTab really lays out, in both Interface Styles:
// the bound covers the row's padding, gap, loader icon, text and the left
// margin it takes in the strip, with only the documented safety above it.
func TestPendingRowWidthBoundCoversLaidOutRows(t *testing.T) {
	labels := []string{"", "a", "alpha", "a-very-long-host-name.example.com"}
	for _, style := range []struct {
		name string
		s    kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			kit.SetStyle(style.s)
			t.Cleanup(func() { kit.SetStyle(kit.Classic) })
			m := locales.In("en")
			var bounds, actuals []float32
			tt := ui.NewTester(func(c *ui.Context) {
				bounds, actuals = bounds[:0], actuals[:0]
				kit.TabStrip(c, kit.TabWork, kit.TabStripOptions{Height: 36}, func() {
					for _, label := range labels {
						row := pendingTab(c, m, c.Theme(), label)
						actuals = append(actuals, row.LayoutBounds().W)
						bounds = append(bounds, pendingRowWidthBound(c, m, label))
					}
				})
			}, 1600, 120)
			tt.SetLocale("en")
			tt.Frame()
			tt.Frame()
			for index, label := range labels {
				if actuals[index] <= 0 {
					t.Fatalf("pending row %q has no laid-out width", label)
				}
				if bounds[index] < actuals[index] {
					t.Fatalf("pending row %q: bound %v is below its laid-out %v",
						label, bounds[index], actuals[index])
				}
				// The bound adds the row's 4-DIP left margin (outside its own
				// LayoutBounds) and 8 DIPs of safety.
				if slack := bounds[index] - actuals[index]; slack > 12+1 {
					t.Fatalf("pending row %q: bound %v is %v above its laid-out %v, want at most the margin and safety",
						label, bounds[index], slack, actuals[index])
				}
			}
			if bounds[len(bounds)-1] <= bounds[1] {
				t.Fatalf("the long label's bound %v is not wider than the short row's %v",
					bounds[len(bounds)-1], bounds[1])
			}
		})
	}
}

// TestTabStripOpenBoundDistrustsStaleBoxes pins the three reasons the open
// decision refuses the previous boxes: the window width that produced them is
// not this frame's, the row has no usable content box, or it has no viewport.
// The last case also proves a fitting open is not warped when the width does
// match, so the guard does not stop every open.
func TestTabStripOpenBoundDistrustsStaleBoxes(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 2)
	if !g.state.RenameTab(0, "a") || !g.state.RenameTab(1, "b") {
		t.Fatalf("RenameTab failed")
	}
	reveal := newWorkTabReveal()

	type check struct {
		name             string
		boxes            kit.TabStripBoxes
		boxesWidth       float32
		want, wantResult bool
	}
	// Two one-glyph Tabs bound to 164 each, plus the insertion strips and the
	// add control: 384 in Classic, comfortably inside a 462-DIP viewport.
	checks := []check{
		{
			name:       "current width and fitting content",
			boxes:      kit.TabStripBoxes{Viewport: ui.Rect{W: 462.18}, Content: ui.Rect{W: 352}},
			boxesWidth: 800,
			wantResult: false,
		},
		{
			name:       "boxes from another window width",
			boxes:      kit.TabStripBoxes{Viewport: ui.Rect{W: 1262.18}, Content: ui.Rect{W: 352}},
			boxesWidth: 1600,
			want:       true,
		},
		{
			name:       "no content box",
			boxes:      kit.TabStripBoxes{Viewport: ui.Rect{W: 462.18}},
			boxesWidth: 800,
			want:       true,
		},
		{
			name:       "no viewport",
			boxes:      kit.TabStripBoxes{Content: ui.Rect{W: 352}},
			boxesWidth: 800,
			want:       true,
		},
		{
			name:       "unknown box width",
			boxes:      kit.TabStripBoxes{Viewport: ui.Rect{W: 462.18}, Content: ui.Rect{W: 352}},
			boxesWidth: 0,
			want:       true,
		},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			reveal.boxes, reveal.boxesWidth = tc.boxes, tc.boxesWidth
			var got bool
			tt := ui.NewTester(func(c *ui.Context) {
				got = reveal.openWillOverflow(c, g.state, locales.In(c.Locale()), nil)
			}, 800, 480)
			tt.SetLocale("en")
			tt.Frame()
			if got != tc.want {
				t.Fatalf("openWillOverflow = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTabStripResizeThenOpenWarpsOnTheEventPaint is the sequential-resize
// regression: the window resizes on one build (no motion event), and a Tab
// opens on the next. The boxes then describe the pre-resize layout and are
// wider than the strip this frame lays out; trusting them starts an enter
// whose reveal scrolls the strip, and the opened Tab paints collapsed at w=0.
// The decision must see that the box width is stale and warp on the event
// paint itself.
func TestTabStripResizeThenOpenWarpsOnTheEventPaint(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 2)
	if !g.state.RenameTab(0, "a") || !g.state.RenameTab(1, "b") {
		t.Fatalf("RenameTab failed")
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	step := false
	builds := 0
	base := stripOnlySteppingView(g.state, reveal, &step)
	type paint struct {
		running, warp bool
		frames        int
	}
	var paints []paint
	view := func(c *ui.Context) {
		builds++
		base(c)
		got := paint{warp: reveal.strip.warp, frames: reveal.strip.animationFrames}
		if reveal.strip.run != nil {
			got.running = true
		}
		paints = append(paints, got)
	}
	tt := ui.NewTester(view, 700, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the two-Tab strip overflows at 700 (MaxX=%v); the case needs it fitting", reveal.scroll.MaxX)
	}
	if got := reveal.boxesWidth; got != 700 {
		t.Fatalf("the recorded box width = %v, want the 700 build's", got)
	}

	// The window resizes on one build. The boxes this build wrote still
	// describe the old 700-wide layout, and a new Tab does not arrive yet.
	builds = 0
	tt.SetSize(400, 480)
	if builds != 1 {
		t.Fatalf("the resize built %d frames, want the single resize paint", builds)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the resize started a strip run: %+v", reveal.strip.run)
	}
	if have, want := reveal.boxes.Viewport.W, float32(700); abs32(have-want) > 1 {
		t.Fatalf("the resize paint's boxes describe a %v viewport, want the old %v", have, want)
	}

	preFrames, preGeneration := reveal.strip.animationFrames, reveal.strip.generation
	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	opened := followTabID(t, g.state, 2)
	paints = nil
	tt.Frame()
	if len(paints) == 0 {
		t.Fatalf("the sequential-resize open built no paint")
	}
	if paints[0].running {
		t.Fatalf("the sequential-resize open started a strip run on its event paint")
	}
	if !paints[0].warp {
		t.Fatalf("the sequential-resize open did not warp the strip on its event paint")
	}
	if got := paints[0].frames - preFrames; got != 0 {
		t.Fatalf("the sequential-resize open asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the sequential-resize open left a run after its event paint: %+v", reveal.strip.run)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the sequential-resize open remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
		t.Fatalf("the reveal did not scroll the overflowing strip on the event paint (X=%v MaxX=%v)",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	eventOpened := stripTravel(t, tt, opened)
	if eventOpened.W <= 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	viewport, _ := tt.Find(locales.In("en").WorkTabsLabel())
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}

	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripDetachThenOpenWarpsOnTheEventPaint is the leaving-record
// regression: a Tab's width is recorded for a wide title, the title narrows,
// the Tab is detached without a motion event, and the next open's row is
// bounded from the state. A forecast subtracting the leaked record for content
// the boxes already exclude calls the open fitting, starts the enter, and the
// opened Tab paints at w=0 under the reveal's scroll jump.
func TestTabStripDetachThenOpenWarpsOnTheEventPaint(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 2)
	if !g.state.RenameTab(0, maxWidthTabTitle) || !g.state.RenameTab(1, "a") {
		t.Fatalf("RenameTab failed")
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	step := false
	tt := ui.NewTester(stripOnlySteppingView(g.state, reveal, &step), 400, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()

	// The wide Tab narrows and leaves on the following builds. Neither is a
	// motion event, so the strip keeps its width record while the row no
	// longer draws the Tab.
	detached := followTabID(t, g.state, 0)
	if !g.state.RenameTab(0, "s") {
		t.Fatalf("RenameTab failed")
	}
	tt.Frame()
	if !g.state.DetachTab(0) {
		t.Fatalf("DetachTab failed")
	}
	tt.Frame()
	if g.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after the detach, want 1", g.state.TabCount())
	}
	if _, leaked := reveal.strip.widths[detached]; !leaked {
		t.Fatalf("the detach left no width record behind; the case needs the stale record")
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if !g.state.RenameTab(1, maxWidthTabTitle) {
		t.Fatalf("RenameTab failed")
	}
	opened := followTabID(t, g.state, 1)
	preFrames, preGeneration := reveal.strip.animationFrames, reveal.strip.generation
	tt.Frame()
	if reveal.strip.run != nil {
		t.Fatalf("the detached-record open started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the detached-record open asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the detached-record open remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
		t.Fatalf("the reveal did not scroll the overflowing strip on the event paint (X=%v MaxX=%v)",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	eventOpened := stripTravel(t, tt, opened)
	if eventOpened.W <= 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	viewport, _ := tt.Find(locales.In("en").WorkTabsLabel())
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}

	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripStyleChangeThenOpenWarpsOnTheEventPaint changes the Interface
// Style and opens a Tab on one build. Every width changes with the style's
// fonts, without a new title or marker, so a bound built from earlier widths
// or from a recorded content signature calls the open fitting and the opened
// Tab paints collapsed under the reveal's scroll jump.
func TestTabStripStyleChangeThenOpenWarpsOnTheEventPaint(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 8)
	for index := range 8 {
		if !g.state.RenameTab(index, maxWidthTabTitle) {
			t.Fatalf("RenameTab(%d) failed", index)
		}
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	step := false
	tt := ui.NewTester(stripOnlySteppingView(g.state, reveal, &step), 2300, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the eight max-width Tabs already overflow (MaxX=%v); the case needs the first overflow",
			reveal.scroll.MaxX)
	}

	kit.SetStyle(kit.MacOS)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if !g.state.RenameTab(8, maxWidthTabTitle) {
		t.Fatalf("RenameTab failed")
	}
	opened := followTabID(t, g.state, 8)
	preFrames, preGeneration := reveal.strip.animationFrames, reveal.strip.generation
	tt.Frame()
	if reveal.strip.run != nil {
		t.Fatalf("the style-change open started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if !reveal.strip.warp {
		t.Fatalf("the style-change open did not warp the strip")
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the style-change open asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the style-change open remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the macOS row does not overflow (MaxX=%v); the case needs the post-build overflow",
			reveal.scroll.MaxX)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	eventOpened := stripTravel(t, tt, opened)
	if eventOpened.W <= 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	viewport, _ := tt.Find(locales.In("en").WorkTabsLabel())
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}

	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripResizeCancelsLiveReorder resizes the window while a reorder is
// mid-run. The row reflows and the strip's scroll offset clamps; a run left
// playing would slide every item under that jump. The resize must end the run
// and paint the final layout, with no further movement.
func TestTabStripResizeCancelsLiveReorder(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 6)
	reveal := newWorkTabReveal()
	reveal.labels = true
	step := false
	tt := ui.NewTester(stripOnlySteppingView(g.state, reveal, &step), 450, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the six-Tab strip does not overflow (MaxX=%v); the case needs the strip scrolled",
			reveal.scroll.MaxX)
	}
	watched := ids[5]

	if !g.state.MoveTab(ids[0], ids[5], true) {
		t.Fatalf("MoveTab failed")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabReorder)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
		t.Fatalf("the reorder did not start a run: %+v", reveal.strip.run)
	}
	tt.Advance(90 * time.Millisecond)
	if reveal.strip.run == nil {
		t.Fatalf("the reorder ended before the resize; the case needs it mid-run")
	}
	preFrames, preGeneration := reveal.strip.animationFrames, reveal.strip.generation
	preScroll := reveal.scroll.X

	// The resize's one paint must cancel the run and show the final layout.
	tt.SetSize(600, 480)
	if reveal.strip.run != nil {
		t.Fatalf("the resize left the reorder running: %+v", reveal.strip.run)
	}
	if reveal.strip.generation == preGeneration {
		t.Fatalf("the resize did not drop the in-flight reorder; the case needs the row remount")
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the resize paint asked for %d strip frames, want none", got)
	}
	if reveal.scroll.X > reveal.scroll.MaxX+0.5 {
		t.Fatalf("the resize left the scroll past its maximum (X=%v MaxX=%v)",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	if preScroll <= eventMax+0.5 {
		t.Fatalf("the resize did not clamp the scroll (old X=%v, new MaxX=%v); the case needs the clamp",
			preScroll, eventMax)
	}
	if eventScroll > eventMax+0.5 {
		t.Fatalf("the resize left the scroll past its clamped maximum (X=%v MaxX=%v)", eventScroll, eventMax)
	}
	eventWatched := stripTravel(t, tt, watched)

	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, watched); !rectNearOK(eventWatched, next, 1) {
		t.Fatalf("the watched Tab moved from %+v to %+v after the resize paint, want the final layout at once",
			eventWatched, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, watched); !rectNearOK(eventWatched, final, 1) {
		t.Fatalf("the watched Tab settled at %+v from the resize paint's %+v", final, eventWatched)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the resize paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripBoundsFitThePrimaryCases pins the over-warp boundary the ticket
// cares about: the bounded row must fit where an open has always animated, and
// overflow where the primary narrow-window cases must warp.
func TestTabStripBoundsFitThePrimaryCases(t *testing.T) {
	setting := tabStripPreferences()
	for _, tc := range []struct {
		name     string
		tabs     int
		title    string
		width    int
		overflow bool
	}{
		{name: "800 one small Tab and a fitting open", tabs: 1, title: "a", width: 800, overflow: false},
		{name: "1600 default", tabs: 1, width: 1600, overflow: false},
		{name: "720 many Tabs", tabs: 6, width: 720, overflow: true},
		// A 720-wide window leaves the shell's strip a 382-DIP viewport; the
		// strip-only view is the viewport itself, so pin that width.
		{name: "narrow sixteen-char duplicate", tabs: 1, title: "wide-overflow-16", width: 382, overflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newTabStripGraph(t, &setting)
			addStripTabs(t, g.state, tc.tabs)
			if tc.title != "" && !g.state.RenameTab(0, tc.title) {
				t.Fatalf("RenameTab failed")
			}
			reveal := newWorkTabReveal()
			var got bool
			var bound float32
			tt := ui.NewTester(func(c *ui.Context) {
				// Read the decision before the build, as workTabs does.
				got = reveal.openWillOverflow(c, g.state, locales.In(c.Locale()), nil)
				bound = reveal.contentWidthBound(c, g.state, locales.In(c.Locale()), nil)
				workTabs(c, g.state, locales.In(c.Locale()), c.Theme(), reveal)
			}, tc.width, 480)
			tt.SetLocale("en")
			tt.Frame()
			tt.Frame()
			if err := g.state.NewTab(); err != nil {
				t.Fatalf("NewTab: %v", err)
			}
			tt.Frame()
			if got != tc.overflow {
				t.Fatalf("openWillOverflow = %v, want %v (bound %v, viewport %v)",
					got, tc.overflow, bound, reveal.boxes.Viewport.W)
			}
		})
	}
}
