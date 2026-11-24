package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// tabBodyPreferences is the ticket's default setting: every Tab event on, the
// body styles at standard speed, and the five switch styles left to each
// case.
func tabBodyPreferences() domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = true
	setting.TabOpen = true
	setting.TabClose = true
	setting.TabSwitch = true
	setting.Speed = domainpreferences.MotionStandard
	return setting
}

// tabBodyWindow builds a two-Tab window below one shell renderer. Both Tabs
// have a real terminal, the last is active, and every body has been shown
// once.
func tabBodyWindow(t *testing.T, setting domainpreferences.Motion) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion) {
	t.Helper()
	return tabBodyWindowN(t, setting, 2)
}

// tabBodyWindowN is tabBodyWindow with n single-Pane Tabs. Showing each body
// once sizes its terminal on that frame, which is the open case; a later
// switch to an already-shown Tab must do no grid math at all.
func tabBodyWindowN(t *testing.T, setting domainpreferences.Motion, n int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion) {
	t.Helper()
	return tabBodyWindowIn(t, motionGraph(t, setting), paneMotionMode{labels: true}, n)
}

// tabBodyLiveWindow is tabBodyWindowIn over a graph whose motion setting the
// test can change while a run plays: the state pulls it every frame.
func tabBodyLiveWindow(t *testing.T, setting *domainpreferences.Motion,
	mode paneMotionMode, n int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion) {
	t.Helper()
	g := newRealTerminalGraph(t, state.WithMotion(func() domainpreferences.Motion { return *setting }))
	return tabBodyWindowIn(t, g, mode, n)
}

// tabBodyWindowIn builds n single-Pane Tabs over g, shows every body once so
// its terminal sizes, and returns the window with its motion runtime.
func tabBodyWindowIn(t *testing.T, g *realTerminalGraph,
	mode paneMotionMode, n int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion) {
	t.Helper()
	for range n {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForRealSurfaces(t, g.state, n)
	view, wm := viewWithPaneMotion(g.state, mode)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, n-1)
	var all []*realTerminalSurface
	for index := range n {
		all = append(all, paneSurfaces(t, g, index)...)
	}
	for index := range n {
		g.state.SelectTab(index)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
	}
	pumpUntil(t, tt, "every Tab body's final grid", func() bool { return gridsSettled(all...) })
	return g, tt, wm
}

// fillTerminal paints a surface's whole screen with an ANSI background so a
// pixel tells two bodies apart (41 red, 44 blue).
func fillTerminal(surface *realTerminalSurface, sgr string) {
	if surface == nil {
		return
	}
	grid := surface.Grid()
	if grid.Cols <= 0 || grid.Rows <= 0 {
		return
	}
	surface.feed(sgr + strings.Repeat(" ", grid.Cols*grid.Rows) + "\x1b[0m")
}

// samplePixel returns the last frame's color at a window point.
func samplePixel(tt *ui.Tester, x, y float32) (r, g, b uint8) {
	img := tt.Image()
	bounds := img.Bounds()
	px := int(x)
	py := int(y)
	if px < bounds.Min.X {
		px = bounds.Min.X
	}
	if py < bounds.Min.Y {
		py = bounds.Min.Y
	}
	if px >= bounds.Max.X {
		px = bounds.Max.X - 1
	}
	if py >= bounds.Max.Y {
		py = bounds.Max.Y - 1
	}
	c := img.RGBAAt(px, py)
	return c.R, c.G, c.B
}

// bodyKey is the element key of the body of Tab id, as the labels show it.
func bodyKey(t *testing.T, g *realTerminalGraph, index int) string {
	t.Helper()
	id, ok := g.state.TabID(index)
	if !ok {
		t.Fatalf("no Tab at index %d", index)
	}
	return bodySpec{kind: bodySurfaceTab, tabID: id}.key()
}

// TestTabSwitchSlidesTheBodyBothWays pulls the live body sideways: the
// outgoing Tab leaves in the direction opposite the target and the incoming
// one enters from the target's side, both at 240 ms, while the final frame is
// the final layout and no animation frame resizes a terminal.
func TestTabSwitchSlidesTheBodyBothWays(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyWindow(t, setting)

	tabA, tabB := bodyKey(t, g, 0), bodyKey(t, g, 1)
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	if _, ok := tt.Find(tabB); !ok {
		t.Fatalf("the active body %q is not built", tabB)
	}
	surfacesA := paneSurfaces(t, g, 0)
	surfacesB := paneSurfaces(t, g, 1)
	all := append(append([]*realTerminalSurface(nil), surfacesA...), surfacesB...)
	calls := make([]int, len(all))
	logs := make([]int, len(all))
	for i, surface := range all {
		calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
	}
	framesBefore := wm.body.animationFrames

	// Last Tab to first: the target is before the current one, so the new
	// body enters from the left and the old leaves to the right.
	g.state.SelectTab(0)
	// The Tester settles an event frame and the phase-in frame at one clock
	// time: the old body is already a copy of the still copy built on the
	// event frame.
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the switch's phase after its frames = %v, want in", wm.body.phase)
	}
	if wm.body.out.tabID != g.mustTabID(t, 1) || !wm.body.out.inert || wm.body.out.ghost != nil {
		t.Fatalf("the switch leaves %+v, want the inert still copy of the old Tab", wm.body.out)
	}
	if wm.body.enterDuration != 240*time.Millisecond || wm.body.exitDuration != 240*time.Millisecond {
		t.Fatalf("switch durations = %s/%s, want 240ms/240ms", wm.body.enterDuration, wm.body.exitDuration)
	}
	if wm.body.enter != (ui.Motion{XPercent: -100, Opacity: 1}) {
		t.Fatalf("the incoming slide = %+v, want from the left opaque", wm.body.enter)
	}
	if wm.body.exit != (ui.Motion{XPercent: 100, Opacity: 1}) {
		t.Fatalf("the outgoing slide = %+v, want to the right opaque", wm.body.exit)
	}
	if _, ok := tt.Find(tabA); !ok {
		t.Fatalf("the incoming body %q is not built on the phase-in frame", tabA)
	}
	if _, ok := tt.Find(tabB); ok {
		t.Fatalf("the old Tab's live body is still built after the switch")
	}

	tt.Advance(80 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the phase mid-switch = %v, want in", wm.body.phase)
	}
	incoming, ok := tt.Find(tabA)
	if !ok {
		t.Fatalf("the incoming body %q is not built mid-switch", tabA)
	}
	// Its left edge is clipped at the body: the incoming body is entering
	// from the left, and only part of its width is on screen yet.
	if incoming.X > root.X+1 {
		t.Fatalf("the incoming body's visible left edge is x=%v, want the body's left %v", incoming.X, root.X)
	}
	if incoming.W <= 0 || incoming.W >= root.W-1 {
		t.Fatalf("the incoming body covers width %v mid-switch, want a growing part of %v", incoming.W, root.W)
	}

	// The switch is pure transform: neither Tab's terminal computes a grid or
	// takes a PTY resize on any frame.
	for i, surface := range all {
		if got := surface.GridForCalls() - calls[i]; got != 0 {
			t.Errorf("surface %d computed %d grids during the switch, want none", i+1, got)
		}
		if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
			t.Errorf("surface %d delivered %d resizes during the switch, want none", i+1, got)
		}
	}
	if wm.body.animationFrames <= framesBefore {
		t.Fatalf("the animated switch asked for no frame")
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the switch did not finish: phase = %v", wm.body.phase)
	}
	if _, ok := tt.Find(tabB); ok {
		t.Fatalf("the old body's copy outlived the switch")
	}
	final, ok := tt.Find(tabA)
	if !ok || !rectNearOK(final, root, 1) {
		t.Fatalf("the final body %+v, want the whole body %+v", final, root)
	}

	// First Tab back to last: the direction reverses.
	g.state.SelectTab(1)
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the returning switch phase = %v, want in", wm.body.phase)
	}
	if wm.body.enter != (ui.Motion{XPercent: 100, Opacity: 1}) {
		t.Fatalf("the returning slide = %+v, want from the right opaque", wm.body.enter)
	}
	if wm.body.exit != (ui.Motion{XPercent: -100, Opacity: 1}) {
		t.Fatalf("the returning outgoing slide = %+v, want to the left opaque", wm.body.exit)
	}
	tt.Advance(80 * time.Millisecond)
	incoming, ok = tt.Find(tabB)
	if !ok || incoming.X <= root.X+1 {
		t.Fatalf("the returning body %+v, want a right-edge copy inside the body %v", incoming, root)
	}
	if incoming.X+incoming.W < root.X+root.W-2 {
		t.Fatalf("the returning body %+v, want its right edge at the body's right", incoming)
	}
	if _, ok := tt.Find(tabA); ok {
		t.Fatalf("the old Tab's live body is still built after the returning switch")
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the returning switch did not finish: phase = %v", wm.body.phase)
	}
}

// TestTabOpenPlaysTheCloseOutAndOpenInSeparately opens a fresh Tab: the old
// body leaves with the close style in 150 ms while the new body enters with
// the open style in 200 ms. This is the close/open pair, not a Tab switch.
func TestTabOpenPlaysTheCloseOutAndOpenInSeparately(t *testing.T) {
	setting := tabBodyPreferences()
	setting.OpenStyle = domainpreferences.MotionPopin
	setting.CloseStyle = domainpreferences.MotionPopin
	g, tt, wm := tabBodyWindow(t, setting)
	oldKey := bodyKey(t, g, 1)
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	surfaces := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
	calls := make([]int, len(surfaces))
	logs := make([]int, len(surfaces))
	for i, surface := range surfaces {
		calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the open's phase after its frames = %v, want in", wm.body.phase)
	}
	if wm.body.enterDuration != 200*time.Millisecond || wm.body.exitDuration != 150*time.Millisecond {
		t.Fatalf("open durations = %s/%s, want 200ms in and an explicit 150ms out",
			wm.body.enterDuration, wm.body.exitDuration)
	}
	if wm.body.enter != (ui.Motion{Scale: motion.PopinStartScale, Opacity: 0}) {
		t.Fatalf("the open entrance = %+v, want the popin", wm.body.enter)
	}
	if wm.body.exit != (ui.Motion{Scale: motion.PopinStartScale, Opacity: 0}) {
		t.Fatalf("the old body's close = %+v, want the popin", wm.body.exit)
	}
	if _, ok := tt.Find(oldKey); ok {
		t.Fatalf("the old Tab's live body is still built on the open's phase-in frame")
	}
	tt.Advance(100 * time.Millisecond)
	newKey := bodyKey(t, g, 2)
	incoming, ok := tt.Find(newKey)
	if !ok {
		t.Fatalf("the new body %q is not built mid-open", newKey)
	}
	if incoming.W >= root.W-1 || incoming.W < root.W*motion.PopinStartScale {
		t.Fatalf("the incoming body width %v at 100 ms, want between the popin %v and the final %v",
			incoming.W, root.W*motion.PopinStartScale, root.W)
	}
	tt.Advance(180 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the open did not finish: phase = %v", wm.body.phase)
	}
	for i, surface := range surfaces {
		if got := surface.GridForCalls() - calls[i]; got != 0 {
			t.Errorf("existing surface %d computed %d grids during the open, want none", i+1, got)
		}
		if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
			t.Errorf("existing surface %d delivered %d resizes during the open, want none", i+1, got)
		}
	}
	fresh := paneSurfaces(t, g, 2)
	// The new Tab's Pane was built at its final grid: one PTY resize, and no
	// grid math on the frames after the first.
	settlePane(t, tt, g, 2)
	for i, surface := range fresh {
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("the new Pane %d delivered %d resizes, want exactly one", i+1, got)
		}
	}
	if _, ok := tt.Find(newKey); !ok {
		t.Fatalf("the final frame does not show the new body")
	}
}

// mustTabID is bodyKey's ID half for tests that need to name a Tab directly.
func (g *realTerminalGraph) mustTabID(t *testing.T, index int) model.TabID {
	t.Helper()
	id, ok := g.state.TabID(index)
	if !ok {
		t.Fatalf("no Tab at index %d", index)
	}
	return id
}

// TestTabSwitchRendersEverySwitchStyle checks all five styles' specified
// X/Y and opacity through the runtime's motion and the live body's geometry:
// the incoming body starts at its own width on the left, its own height
// above, or at its final box for fade.
func TestTabSwitchRendersEverySwitchStyle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		style domainpreferences.MotionSwitchStyle
		enter ui.Motion
		exit  ui.Motion
		axis  byte // 'x', 'y', or 0 for fade
	}{
		{"slide", domainpreferences.MotionSwitchSlide,
			ui.Motion{XPercent: -100, Opacity: 1}, ui.Motion{XPercent: 100, Opacity: 1}, 'x'},
		{"slidevert", domainpreferences.MotionSwitchSlideVert,
			ui.Motion{YPercent: -100, Opacity: 1}, ui.Motion{YPercent: 100, Opacity: 1}, 'y'},
		{"slidefade", domainpreferences.MotionSwitchSlideFade,
			ui.Motion{XPercent: -100}, ui.Motion{XPercent: 100}, 'x'},
		{"slidefadevert", domainpreferences.MotionSwitchSlideFadeVert,
			ui.Motion{YPercent: -100}, ui.Motion{YPercent: 100}, 'y'},
		{"fade", domainpreferences.MotionSwitchFade,
			ui.Motion{}, ui.Motion{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setting := tabBodyPreferences()
			setting.SwitchStyle = tc.style
			g, tt, wm := tabBodyWindow(t, setting)
			tabA := bodyKey(t, g, 0)
			root, ok := tt.Find("work-body-root")
			if !ok {
				t.Fatalf("the body container has no label")
			}
			g.state.SelectTab(0)
			tt.Frame()
			if wm.body.phase != bodyIn {
				t.Fatalf("phase = %v, want in", wm.body.phase)
			}
			if wm.body.enter != tc.enter {
				t.Fatalf("the entrance = %+v, want %+v", wm.body.enter, tc.enter)
			}
			if wm.body.exit != tc.exit {
				t.Fatalf("the exit = %+v, want %+v", wm.body.exit, tc.exit)
			}
			tt.Advance(80 * time.Millisecond)
			incoming, ok := tt.Find(tabA)
			if !ok {
				t.Fatalf("the incoming body has no label mid-switch")
			}
			switch tc.axis {
			case 'x':
				if incoming.X > root.X+2 || incoming.W <= 0 || incoming.W >= root.W-1 {
					t.Fatalf("the incoming body %+v, want a left-edge copy inside the body %+v", incoming, root)
				}
			case 'y':
				if incoming.Y > root.Y+2 || incoming.H <= 0 || incoming.H >= root.H-1 {
					t.Fatalf("the incoming body %+v, want a top-edge copy inside the body %+v", incoming, root)
				}
			default:
				if !rectNearOK(incoming, root, 2) {
					t.Fatalf("the fading body %+v moved, want the final box %+v", incoming, root)
				}
			}
			tt.Advance(400 * time.Millisecond)
			tt.Frame()
			if wm.body.phase != bodyIdle {
				t.Fatalf("the switch did not finish: phase = %v", wm.body.phase)
			}
			final, ok := tt.Find(tabA)
			if !ok || !rectNearOK(final, root, 2) {
				t.Fatalf("the final body %+v, want the whole body %+v", final, root)
			}
		})
	}
}

// TestTabCloseCurrentUsesTheCloseExitAndSwitchEntrance closes the active
// middle Tab, then the last one: the outgoing whole body takes the close
// style with an explicit 150 ms, while the replacement enters with the switch
// style over 240 ms in the replacement's direction.
func TestTabCloseCurrentUsesTheCloseExitAndSwitchEntrance(t *testing.T) {
	setting := tabBodyPreferences()
	setting.CloseStyle = domainpreferences.MotionPopin
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyWindowN(t, setting, 3)
	tabA, tabC := bodyKey(t, g, 0), bodyKey(t, g, 2)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
	// Make the middle Tab active through a normal switch, then let it settle.
	g.state.SelectTab(1)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	closing := paneSurfaces(t, g, 1)
	all := append(append(paneSurfaces(t, g, 0), closing...), paneSurfaces(t, g, 2)...)
	calls := make([]int, len(all))
	logs := make([]int, len(all))
	for i, surface := range all {
		calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
	}

	if !g.state.CloseActiveTab() {
		t.Fatalf("closing the middle Tab failed")
	}
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the close's phase = %v, want in", wm.body.phase)
	}
	if wm.body.out.tabID != ids[1] || wm.body.out.ghost == nil {
		t.Fatalf("the close leaves %+v, want the retained body of %q", wm.body.out, ids[1])
	}
	if wm.body.exitDuration != 150*time.Millisecond {
		t.Fatalf("the close's ExitDuration = %s, want an explicit 150ms", wm.body.exitDuration)
	}
	if wm.body.enterDuration != 240*time.Millisecond {
		t.Fatalf("the replacement's Duration = %s, want the 240ms switch", wm.body.enterDuration)
	}
	if wm.body.enter != (ui.Motion{XPercent: 100, Opacity: 1}) {
		t.Fatalf("the replacement enters with %+v, want from the right opaque", wm.body.enter)
	}
	if wm.body.exit != (ui.Motion{Scale: motion.PopinStartScale, Opacity: 0}) {
		t.Fatalf("the old body leaves with %+v, want the popin close", wm.body.exit)
	}
	if wm.run != nil {
		t.Fatalf("a whole-Tab close started a Pane run")
	}
	if got := closing[0].Closes(); got != 0 {
		t.Fatalf("the closing body's Surface closed %d times before its copy was gone", got)
	}
	tt.Advance(80 * time.Millisecond)
	incoming, ok := tt.Find(tabC)
	if !ok || incoming.X <= root.X+2 || incoming.X+incoming.W < root.X+root.W-2 {
		t.Fatalf("the replacement body %+v, want it entering from the right of %+v", incoming, root)
	}
	for i, surface := range all {
		if got := surface.GridForCalls() - calls[i]; got != 0 {
			t.Errorf("surface %d computed %d grids during the close, want none", i+1, got)
		}
		if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
			t.Errorf("surface %d delivered %d resizes during the close, want none", i+1, got)
		}
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the close did not finish: phase = %v", wm.body.phase)
	}
	if got := closing[0].Closes(); got != 1 {
		t.Fatalf("the closing body's Surface closed %d times, want exactly once", got)
	}
	if _, ok := g.state.TakeTabGhost(ids[1]); ok {
		t.Fatalf("the closed Tab's retained body outlived the run")
	}
	if final, ok := tt.Find(tabC); !ok || !rectNearOK(final, root, 2) {
		t.Fatalf("the replacement's final body %+v, want %+v", final, root)
	}

	// The last Tab now active: its replacement is the left neighbour, so the
	// switch direction reverses.
	nowClosing := paneSurfaces(t, g, 1)
	if !g.state.CloseActiveTab() {
		t.Fatalf("closing the last Tab failed")
	}
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the last close's phase = %v, want in", wm.body.phase)
	}
	if wm.body.out.tabID != ids[2] || wm.body.out.ghost == nil {
		t.Fatalf("the last close leaves %+v, want the retained body of %q", wm.body.out, ids[2])
	}
	if wm.body.enter != (ui.Motion{XPercent: -100, Opacity: 1}) {
		t.Fatalf("the left replacement enters with %+v, want from the left opaque", wm.body.enter)
	}
	tt.Advance(80 * time.Millisecond)
	incoming, ok = tt.Find(tabA)
	if !ok || incoming.X > root.X+2 || incoming.W >= root.W-1 {
		t.Fatalf("the left replacement body %+v, want it entering from the left of %+v", incoming, root)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the last close did not finish: phase = %v", wm.body.phase)
	}
	if got := nowClosing[0].Closes(); got != 1 {
		t.Fatalf("the last close's Surface closed %d times, want exactly once", got)
	}
}

// TestTabCloseLastShowsHomeAfterExits closes the only remaining Tab: the old
// body exits in 150 ms and Home is held unseen until its copy is gone, with
// no animation of Home itself.
func TestTabCloseLastShowsHomeAfterExits(t *testing.T) {
	setting := tabBodyPreferences()
	setting.CloseStyle = domainpreferences.MotionPopin
	g, tt, wm := tabBodyWindow(t, setting)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1)}
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	// Close the background Tab first, leaving only the active body.
	if !g.state.CloseTab(0) {
		t.Fatalf("closing the background Tab failed")
	}
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("a non-current close started a body run: %v", wm.body.phase)
	}
	closing := paneSurfaces(t, g, 0)

	if !g.state.CloseActiveTab() {
		t.Fatalf("closing the last Tab failed")
	}
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the last close's phase = %v, want in", wm.body.phase)
	}
	if !wm.body.afterExits {
		t.Fatalf("Home does not wait for the outgoing body's copies")
	}
	if wm.body.enterSet {
		t.Fatalf("Home was given its own entrance")
	}
	if wm.body.exitDuration != 150*time.Millisecond {
		t.Fatalf("the last close's ExitDuration = %s, want 150ms", wm.body.exitDuration)
	}
	if wm.body.out.ghost == nil || wm.body.out.tabID != ids[1] {
		t.Fatalf("the last close leaves %+v, want the retained closed body", wm.body.out)
	}
	if _, ok := tt.Find("work-body:home"); ok {
		t.Fatalf("Home was visible before the outgoing body's copy was gone")
	}
	if got := closing[0].Closes(); got != 0 {
		t.Fatalf("the closed body's Surface closed %d times before its copy was gone", got)
	}
	tt.Advance(100 * time.Millisecond)
	if _, ok := tt.Find("work-body:home"); ok {
		t.Fatalf("Home appeared at 100 ms, before the 150 ms exit")
	}
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the last close did not finish: phase = %v", wm.body.phase)
	}
	home, ok := tt.Find("work-body:home")
	if !ok || !rectNearOK(home, root, 2) {
		t.Fatalf("Home's box %+v, want the whole body %+v", home, root)
	}
	if got := closing[0].Closes(); got != 1 {
		t.Fatalf("the closed body's Surface closed %d times, want exactly once", got)
	}
	if _, ok := g.state.TakeTabGhost(ids[1]); ok {
		t.Fatalf("Home appeared while the closed Tab's body was still retained")
	}
	// No second Home animation: its box and the runtime's frame requests stop.
	frames := wm.body.animationFrames
	tt.Advance(150 * time.Millisecond)
	tt.Frame()
	again, ok := tt.Find("work-body:home")
	if !ok || !rectNearOK(again, home, 1) {
		t.Fatalf("Home moved after it appeared: %+v then %+v", home, again)
	}
	if wm.body.animationFrames != frames {
		t.Fatalf("Home asked for another body animation after it appeared")
	}
}

// TestTabCloseOfANonCurrentTabDoesNotMoveTheBody keeps a background close to
// the strip: the body on screen stays live, no copy is played, and the closed
// Tab's Surface is destroyed at once.
func TestTabCloseOfANonCurrentTabDoesNotMoveTheBody(t *testing.T) {
	g, tt, wm := tabBodyWindowN(t, tabBodyPreferences(), 3)
	activeKey := bodyKey(t, g, 2)
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	before := wm.body.animationFrames
	closed := paneSurfaces(t, g, 0)
	if !g.state.CloseTab(0) {
		t.Fatalf("closing the background Tab failed")
	}
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("a background close started a body run: %v", wm.body.phase)
	}
	if wm.body.animationFrames != before {
		t.Fatalf("a background close asked for %d body frames", wm.body.animationFrames-before)
	}
	if wm.body.shown.tabID != g.mustTabID(t, 1) {
		t.Fatalf("the body on screen changed to %q", wm.body.shown.tabID)
	}
	if wm.run != nil {
		t.Fatalf("a background close started a Pane run")
	}
	if got := closed[0].Closes(); got != 1 {
		t.Fatalf("the background Tab's Surface closed %d times, want once at the close", got)
	}
	if _, ok := g.state.TakeTabGhost(g.mustTabID(t, 0)); ok {
		t.Fatalf("a background close retained a body no view drew")
	}
	body, ok := tt.Find(activeKey)
	if !ok || !rectNearOK(body, root, 2) {
		t.Fatalf("the live body %+v, want it unmoved at %+v", body, root)
	}
}

// TestTabCloseOfASplitTabIsOneBody closes an active two-Pane Tab: one
// TabClose, one retained whole body with both Panes, no PaneClose, no Pane
// run, and each Surface destroyed exactly once when the whole-body exit ends.
func TestTabCloseOfASplitTabIsOneBody(t *testing.T) {
	setting := tabBodyPreferences()
	g := motionGraph(t, setting)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 3)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	var all []*realTerminalSurface
	for index := range 2 {
		all = append(all, paneSurfaces(t, g, index)...)
	}
	for index := range 2 {
		g.state.SelectTab(index)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
	}
	pumpUntil(t, tt, "both Tab bodies' final grids", func() bool { return gridsSettled(all...) })
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	closedTab := g.mustTabID(t, 1)
	closedKey := bodyKey(t, g, 1)
	closing := paneSurfaces(t, g, 1)
	if len(closing) != 2 {
		t.Fatalf("the closing Tab has %d Panes, want 2", len(closing))
	}

	if !g.state.CloseActiveTab() {
		t.Fatalf("closing the split Tab failed")
	}
	tt.Frame()
	event := mustLatestEvent(t, g.state)
	if event.Kind != motion.TabClose || event.TabID != closedTab {
		t.Fatalf("the split Tab's close recorded %+v, want one TabClose", event)
	}
	if wm.body.phase != bodyIn {
		t.Fatalf("the split close's phase = %v, want in", wm.body.phase)
	}
	if wm.body.out.key() != closedKey || wm.body.out.ghost == nil {
		t.Fatalf("the split close leaves %+v, want one retained body %q", wm.body.out, closedKey)
	}
	if got := len(wm.body.out.ghost.Panes); got != 2 {
		t.Fatalf("the retained whole body has %d Panes, want 2", got)
	}
	if wm.run != nil {
		t.Fatalf("a split Tab's close started per-Pane runs")
	}
	for i, surface := range closing {
		if got := surface.Closes(); got != 0 {
			t.Fatalf("closing Pane %d's Surface closed %d times before the whole body left", i+1, got)
		}
	}
	tt.Advance(100 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the split close ended before its 150 ms exit")
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the split close did not finish: phase = %v", wm.body.phase)
	}
	if _, ok := tt.Find(closedKey); ok {
		t.Fatalf("the closed split body is still built after its run")
	}
	for i, surface := range closing {
		if got := surface.Closes(); got != 1 {
			t.Fatalf("closing Pane %d's Surface closed %d times, want exactly once", i+1, got)
		}
	}
	if _, ok := g.state.TakeTabGhost(closedTab); ok {
		t.Fatalf("the split Tab's retained body outlived the run")
	}
	replacement := bodyKey(t, g, 0)
	if body, ok := tt.Find(replacement); !ok || !rectNearOK(body, root, 2) {
		t.Fatalf("the replacement body %+v, want the whole body %+v", body, root)
	}
}

// mustLatestEvent is mustEvent without the state package: the shell tests'
// retained event source is the same state.
func mustLatestEvent(t *testing.T, s *state.State) motion.Event {
	t.Helper()
	event, ok := s.MotionEvent()
	if !ok {
		t.Fatalf("no motion event was recorded")
	}
	return event
}

// TestTabBodyFallbacksCloseExactlyOnce covers the retained live body, the
// opaque body-shape used when no Surface was retained, and a Surface whose
// ViewInert has only its blank path. In each, the closed Surface is destroyed
// exactly once.
func TestTabBodyFallbacksCloseExactlyOnce(t *testing.T) {
	t.Run("live retained body", func(t *testing.T) {
		g, tt, wm := tabBodyWindow(t, tabBodyPreferences())
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1)}
		closing := paneSurfaces(t, g, 1)
		if !g.state.CloseActiveTab() {
			t.Fatalf("CloseActiveTab failed")
		}
		tt.Frame()
		if wm.body.phase != bodyIn || wm.body.out.ghost == nil || wm.body.out.blank {
			t.Fatalf("the live fallback was not taken: %+v", wm.body.out)
		}
		if got := closing[0].Closes(); got != 0 {
			t.Fatalf("the retained Surface closed %d times before the copy was gone", got)
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the retained Surface closed %d times, want once", got)
		}
		if _, ok := g.state.TakeTabGhost(ids[1]); ok {
			t.Fatalf("the retained body outlived the run")
		}
	})

	t.Run("blank body shape", func(t *testing.T) {
		g, tt, wm := tabBodyWindow(t, tabBodyPreferences())
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1)}
		tabA := bodyKey(t, g, 0)
		root, _ := tt.Find("work-body-root")
		closing := paneSurfaces(t, g, 1)
		if !g.state.CloseActiveTab() {
			t.Fatalf("CloseActiveTab failed")
		}
		// Release the retained body before the view draws it: this is the
		// last resort where no Surface copy exists. The Surface is destroyed
		// here, not later.
		g.state.ReleaseTabGhosts()
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the released Surface closed %d times, want once", got)
		}
		before := wm.body.animationFrames
		tt.Frame()
		if wm.body.phase != bodyIn {
			t.Fatalf("the blank fallback did not animate: phase = %v", wm.body.phase)
		}
		if !wm.body.out.blank || wm.body.out.ghost != nil || wm.body.out.tabID != ids[1] {
			t.Fatalf("the close leaves %+v, want the opaque body shape of %q", wm.body.out, ids[1])
		}
		if wm.body.animationFrames <= before {
			t.Fatalf("the blank fallback asked for no frame")
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.body.phase != bodyIdle {
			t.Fatalf("the blank fallback did not finish: phase = %v", wm.body.phase)
		}
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the blank fallback closed the Surface again: %d", got)
		}
		if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 2) {
			t.Fatalf("the replacement body %+v, want the whole body %+v", body, root)
		}
	})

	t.Run("surface with no still copy", func(t *testing.T) {
		g, tt, wm := tabBodyWindow(t, tabBodyPreferences())
		closing := paneSurfaces(t, g, 1)
		closing[0].dead = true
		if !g.state.CloseActiveTab() {
			t.Fatalf("CloseActiveTab failed")
		}
		tt.Frame()
		if wm.body.phase != bodyIn || wm.body.out.ghost == nil {
			t.Fatalf("the dead-Surface close did not play the retained body: %+v", wm.body.out)
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.body.phase != bodyIdle {
			t.Fatalf("the dead-Surface close did not finish: phase = %v", wm.body.phase)
		}
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the dead Surface closed %d times, want once", got)
		}
	})
}

// TestTabBodyWarpsWithoutMotion checks the master switch, each Tab event's
// switch, Reduce Motion, and a hidden window: each warps to the final layout
// with no body run, no copy, and no animation frames.
func TestTabBodyWarpsWithoutMotion(t *testing.T) {
	assertWarped := func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, wm *workspacePaneMotion,
		before int, activeIndex int) {
		t.Helper()
		if wm.body.phase != bodyIdle {
			t.Fatalf("the warp left the body in phase %v", wm.body.phase)
		}
		if wm.body.animationFrames != before {
			t.Fatalf("the warp asked for %d body frames", wm.body.animationFrames-before)
		}
		if wm.run != nil {
			t.Fatalf("the warp started a Pane run")
		}
		root, ok := tt.Find("work-body-root")
		if !ok {
			t.Fatalf("the body container has no label")
		}
		key := bodyKey(t, g, activeIndex)
		body, ok := tt.Find(key)
		if !ok || !rectNearOK(body, root, 2) {
			t.Fatalf("the warped body %+v, want the final body %+v", body, root)
		}
	}

	t.Run("master off", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.Enabled = false
		g, tt, wm := tabBodyWindow(t, setting)
		before := wm.body.animationFrames
		g.state.SelectTab(0)
		tt.Frame()
		assertWarped(t, g, tt, wm, before, 0)
	})

	t.Run("Tab Open off", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.TabOpen = false
		g, tt, wm := tabBodyWindow(t, setting)
		before := wm.body.animationFrames
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		waitForRealSurfaces(t, g.state, 3)
		settlePane(t, tt, g, 2)
		assertWarped(t, g, tt, wm, before, 2)
	})

	t.Run("Tab Close off", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.TabClose = false
		g, tt, wm := tabBodyWindow(t, setting)
		before := wm.body.animationFrames
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1)}
		closing := paneSurfaces(t, g, 1)
		if !g.state.CloseActiveTab() {
			t.Fatalf("CloseActiveTab failed")
		}
		tt.Frame()
		assertWarped(t, g, tt, wm, before, 0)
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the warped close destroyed its Surface %d times, want once", got)
		}
		if _, ok := g.state.TakeTabGhost(ids[1]); ok {
			t.Fatalf("the warped close retained a body")
		}
	})

	t.Run("Tab Close off keeps Home immediate", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.TabClose = false
		g, tt, wm := tabBodyWindow(t, setting)
		root, _ := tt.Find("work-body-root")
		if !g.state.CloseTab(0) {
			t.Fatalf("closing the background Tab failed")
		}
		tt.Frame()
		before := wm.body.animationFrames
		if !g.state.CloseActiveTab() {
			t.Fatalf("closing the last Tab failed")
		}
		tt.Frame()
		if wm.body.phase != bodyIdle || wm.body.animationFrames != before {
			t.Fatalf("the warped last close played a body run: phase %v, frames +%d",
				wm.body.phase, wm.body.animationFrames-before)
		}
		home, ok := tt.Find("work-body:home")
		if !ok || !rectNearOK(home, root, 2) {
			t.Fatalf("Home %+v, want it at once at %+v with no AfterExits wait", home, root)
		}
	})

	t.Run("Tab Switch off", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.TabSwitch = false
		g, tt, wm := tabBodyWindow(t, setting)
		before := wm.body.animationFrames
		g.state.SelectTab(0)
		tt.Frame()
		assertWarped(t, g, tt, wm, before, 0)
	})

	t.Run("Reduce Motion", func(t *testing.T) {
		g, tt, wm := tabBodyWindow(t, tabBodyPreferences())
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		before := wm.body.animationFrames
		g.state.SelectTab(0)
		tt.Frame()
		assertWarped(t, g, tt, wm, before, 0)
	})

	t.Run("hidden window", func(t *testing.T) {
		g, tt, wm := tabBodyWindow(t, tabBodyPreferences())
		before := wm.body.animationFrames
		tt.SetWindowHidden(true)
		g.state.SelectTab(0)
		tt.Advance(48 * time.Millisecond)
		tt.SetWindowHidden(false)
		tt.Frame()
		assertWarped(t, g, tt, wm, before, 0)
	})

	t.Run("first frame", func(t *testing.T) {
		setting := tabBodyPreferences()
		g := motionGraph(t, setting)
		for range 2 {
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
		}
		waitForRealSurfaces(t, g.state, 2)
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		if wm.body.phase != bodyIdle {
			t.Fatalf("an event first seen on the first frame played: phase %v", wm.body.phase)
		}
		if wm.body.animationFrames != 0 {
			t.Fatalf("the first frame asked for %d body frames", wm.body.animationFrames)
		}
		root, ok := tt.Find("work-body-root")
		if !ok {
			t.Fatalf("the body container has no label")
		}
		body, ok := tt.Find(bodyKey(t, g, 1))
		if !ok || !rectNearOK(body, root, 2) {
			t.Fatalf("the first frame's body %+v, want the final %+v", body, root)
		}
	})
}

// TestTabBodyInterruptionContinuesFromTheShownBody switches back while the
// previous switch is still playing: the latest event wins, starts from the
// body the user saw, and still ends on the final layout without a leak.
func TestTabBodyInterruptionContinuesFromTheShownBody(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyWindow(t, setting)
	tabA, tabB := bodyKey(t, g, 0), bodyKey(t, g, 1)
	root, _ := tt.Find("work-body-root")
	all := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
	calls := make([]int, len(all))
	logs := make([]int, len(all))
	for i, surface := range all {
		calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
	}

	g.state.SelectTab(0)
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the first switch did not reach its phase-in frame")
	}
	// Switch back before the first run ends.
	g.state.SelectTab(1)
	tt.Frame()
	if wm.body.phase != bodyIn {
		t.Fatalf("the interrupting switch phase = %v, want in", wm.body.phase)
	}
	if wm.body.out.tabID != g.mustTabID(t, 0) || !wm.body.out.inert || wm.body.out.ghost != nil {
		t.Fatalf("the interrupting switch leaves %+v, want the still copy of the shown Tab", wm.body.out)
	}
	if wm.body.enter != (ui.Motion{XPercent: 100, Opacity: 1}) {
		t.Fatalf("the interrupting entrance = %+v, want from the right opaque", wm.body.enter)
	}
	tt.Advance(80 * time.Millisecond)
	incoming, ok := tt.Find(tabB)
	if !ok || incoming.X <= root.X+1 || incoming.X+incoming.W < root.X+root.W-2 {
		t.Fatalf("the interrupting body %+v, want it entering from the right of %+v", incoming, root)
	}
	if _, ok := tt.Find(tabA); ok {
		t.Fatalf("the superseded body is still built live after the interruption")
	}
	for i, surface := range all {
		if got := surface.GridForCalls() - calls[i]; got != 0 {
			t.Errorf("surface %d computed %d grids during the interruption, want none", i+1, got)
		}
		if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
			t.Errorf("surface %d delivered %d resizes during the interruption, want none", i+1, got)
		}
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the interruption did not finish: phase = %v", wm.body.phase)
	}
	final, ok := tt.Find(tabB)
	if !ok || !rectNearOK(final, root, 2) {
		t.Fatalf("the interrupted final body %+v, want %+v", final, root)
	}
	if _, ok := g.state.TakeTabGhost(g.mustTabID(t, 0)); ok {
		t.Fatalf("the interruption leaked a retained body")
	}
	for i, surface := range all {
		if got := surface.Closes(); got != 0 {
			t.Fatalf("the interruption closed surface %d %d times", i+1, got)
		}
	}
}

// TestTabSwitchMovesTheOutgoingCopyOppositeTheIncoming paints the two bodies
// different colors and samples the pixels: mid-switch the incoming body owns
// the target's edge and the outgoing copy owns the other, so the whole body
// really is moving as one.
func TestTabSwitchMovesTheOutgoingCopyOppositeTheIncoming(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyWindow(t, setting)
	fillTerminal(paneSurfaces(t, g, 0)[0], "\x1b[44m")
	fillTerminal(paneSurfaces(t, g, 1)[0], "\x1b[41m")
	tt.Advance(120 * time.Millisecond)
	tt.Frame()
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	y := root.Y + root.H/2
	xLeft := root.X + root.W*0.25
	xRight := root.X + root.W*0.75
	if r, _, _ := samplePixel(tt, xRight, y); r < 120 {
		t.Fatalf("the active body is not the red one before the switch: r=%d", r)
	}

	g.state.SelectTab(0) // target is before: the old copy leaves right
	tt.Frame()
	tt.Advance(80 * time.Millisecond)
	lr, _, lb := samplePixel(tt, xLeft, y)
	rr, _, rb := samplePixel(tt, xRight, y)
	if int(lb) <= int(lr)+20 {
		t.Fatalf("the incoming body's left quarter is not the blue one: r=%d b=%d", lr, lb)
	}
	if int(rr) <= int(rb)+40 || rr < 100 {
		t.Fatalf("the outgoing copy's right quarter is not the red one: r=%d b=%d", rr, rb)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the switch did not finish: phase = %v", wm.body.phase)
	}
	for _, x := range []float32{xLeft, xRight} {
		r, _, b := samplePixel(tt, x, y)
		if int(b) <= int(r)+20 {
			t.Fatalf("the final body at x=%v is not the red-free replacement: r=%d b=%d", x, r, b)
		}
	}
}

// TestTabCloseExitEndsAt150msBeforeTheReplacementArrives samples the pixels
// through a slide close under a horizontal switch. The outgoing red body
// still covers a probe near the left at 50 ms; by 100 ms only the correct
// 150 ms exit has carried it past that probe, leaving the background until
// the 240 ms replacement reaches it. A ChildExit that reused the incoming
// 240 ms Duration would still paint the old body at the probe.
func TestTabCloseExitEndsAt150msBeforeTheReplacementArrives(t *testing.T) {
	setting := tabBodyPreferences()
	setting.CloseStyle = domainpreferences.MotionSlide
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	// Three Tabs, the middle one active: its replacement is the right
	// neighbour, so the switch enters from the right while the old body
	// leaves to the left.
	g, tt, wm := tabBodyWindowN(t, setting, 3)
	g.state.SelectTab(1)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	fillTerminal(paneSurfaces(t, g, 1)[0], "\x1b[41m")
	fillTerminal(paneSurfaces(t, g, 2)[0], "\x1b[44m")
	tt.Advance(120 * time.Millisecond)
	tt.Frame()
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	probeX := root.X + 100
	probeY := root.Y + root.H/2
	if r, _, _ := samplePixel(tt, probeX, probeY); r < 120 {
		t.Fatalf("the active body is not the red one before the close: r=%d", r)
	}

	if !g.state.CloseActiveTab() {
		t.Fatalf("CloseActiveTab failed")
	}
	tt.Frame()
	if wm.body.enter != (ui.Motion{XPercent: 100, Opacity: 1}) {
		t.Fatalf("the replacement enters with %+v, want from the right", wm.body.enter)
	}
	if wm.body.exitDuration != 150*time.Millisecond {
		t.Fatalf("the close's ExitDuration = %s, want 150ms", wm.body.exitDuration)
	}
	tt.Advance(50 * time.Millisecond)
	r1, g1, _ := samplePixel(tt, probeX, probeY)
	if int(r1) <= int(g1)+60 {
		t.Fatalf("the closing red body is not over the probe at 50 ms: r=%d g=%d", r1, g1)
	}
	tt.Advance(50 * time.Millisecond)
	r2, g2, _ := samplePixel(tt, probeX, probeY)
	// At 100 ms the 150 ms exit has moved its copy far past the probe. With
	// the incoming 240 ms Duration the red body would still cover it: red
	// and green would differ by about 150, not by less than ten.
	if int(r2) > int(g2)+10 {
		t.Fatalf("the closing body is still over the probe at 100 ms: r=%d g=%d (50 ms: %d,%d)",
			r2, g2, r1, g1)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the close did not finish: phase = %v", wm.body.phase)
	}
	br, _, bb := samplePixel(tt, probeX, probeY)
	if int(bb) <= int(br)+20 {
		t.Fatalf("the replacement is not the final body over the probe: r=%d b=%d", br, bb)
	}
}

// bodyWarpsAtOnce asserts that the frame just built warped the body: the
// runtime is idle on that frame, it asked for no frame, and the active body
// is already the final whole-body box (so MyGo rested its in-flight
// entrance rather than only finishing it later).
func bodyWarpsAtOnce(t *testing.T, g *realTerminalGraph, tt *ui.Tester,
	wm *workspacePaneMotion, framesBefore, activeIndex int, what string) {
	t.Helper()
	if wm.body.phase != bodyIdle {
		t.Fatalf("%s: phase = %v, want idle on the warp frame", what, wm.body.phase)
	}
	if got := wm.body.animationFrames - framesBefore; got != 0 {
		t.Fatalf("%s: the warp asked for %d body frames, want none", what, got)
	}
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("%s: the body container has no label", what)
	}
	body, ok := tt.Find(bodyKey(t, g, activeIndex))
	if !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("%s: the warped body %+v, want the final whole body %+v", what, body, root)
	}
}

// TestTabBodyLiveSwitchOffWarpsMidRun is the verifier's case: the Tab switch
// is turned off through the live getter while the body is mid-entrance. The
// frame that notices must already paint the final whole-body box at
// {0 36 900 564}, and MyGo must stop building the entrance and the outgoing
// copy on that frame, not keep moving them for the rest of the duration.
func TestTabBodyLiveSwitchOffWarpsMidRun(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
	tabA := bodyKey(t, g, 0)
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	if !rectNearOK(root, ui.Rect{X: 0, Y: 36, W: 900, H: 564}, 0.5) {
		t.Fatalf("the whole body is %+v, want {0 36 900 564}", root)
	}

	// Paint the two Tabs blue and red, so the pixels tell the final body from
	// the outgoing copy.
	fillTerminal(paneSurfaces(t, g, 0)[0], "\x1b[44m")
	fillTerminal(paneSurfaces(t, g, 1)[0], "\x1b[41m")
	tt.Advance(120 * time.Millisecond)
	tt.Frame()
	y := root.Y + root.H/2
	xLeft := root.X + root.W*0.25
	xRight := root.X + root.W*0.75

	// The last Tab is active; switching to the first slides the new body in
	// from the left over 240 ms.
	g.state.SelectTab(0)
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the switch's phase at 60 ms = %v, want in", wm.body.phase)
	}
	incoming, ok := tt.Find(tabA)
	if !ok || incoming.W <= 0 || incoming.W >= root.W-1 {
		t.Fatalf("the body at 60 ms = %+v, want it mid-entrance inside %+v", incoming, root)
	}
	// The outgoing red copy still owns the right quarter mid-run.
	if lr, _, lb := samplePixel(tt, xLeft, y); int(lb) <= int(lr)+20 {
		t.Fatalf("the incoming body's left quarter is not blue: r=%d b=%d", lr, lb)
	}
	if rr, _, rb := samplePixel(tt, xRight, y); int(rr) <= int(rb)+40 || rr < 100 {
		t.Fatalf("the outgoing copy's right quarter is not red: r=%d b=%d", rr, rb)
	}

	framesBefore := wm.body.animationFrames
	all := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
	calls := make([]int, len(all))
	logs := make([]int, len(all))
	for i, surface := range all {
		calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
	}
	setting.TabSwitch = false
	tt.Frame()
	bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "the live switch-off")
	for i, surface := range all {
		if got := surface.GridForCalls() - calls[i]; got != 0 {
			t.Errorf("surface %d computed %d grids on the warp frame, want none", i+1, got)
		}
		if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
			t.Errorf("surface %d delivered %d resizes on the warp frame, want none", i+1, got)
		}
	}
	if body, ok := tt.Find(tabA); !ok || body.W != root.W {
		t.Fatalf("the warped body width = %v, want the final %v", body.W, root.W)
	}
	// The warp frame shows only the blue replacement: the red exit copy is
	// gone, not still sliding under it.
	for _, x := range []float32{xLeft, xRight} {
		r, _, b := samplePixel(tt, x, y)
		if int(b) <= int(r)+20 {
			t.Fatalf("the warp frame at x=%v still shows the outgoing copy: r=%d b=%d", x, r, b)
		}
	}

	// Ten more refresh frames must show the same final box: the entrance is
	// over, not merely finished at the app level.
	tt.Advance(100 * time.Millisecond)
	if wm.body.phase != bodyIdle {
		t.Fatalf("the switch-off warped into phase %v", wm.body.phase)
	}
	if got := wm.body.animationFrames - framesBefore; got != 0 {
		t.Fatalf("the switch-off asked for %d body frames after the warp", got)
	}
	if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body 100 ms after the warp = %+v, want the final %+v", body, root)
	}
	for _, x := range []float32{xLeft, xRight} {
		r, _, b := samplePixel(tt, x, y)
		if int(b) <= int(r)+20 {
			t.Fatalf("100 ms after the warp x=%v still shows the outgoing copy: r=%d b=%d", x, r, b)
		}
	}
}

// TestTabBodyLiveOffMidRunWarpsImmediately switches the master switch or the
// run's own event off mid-run: each warp is final on its frame and leaves no
// retained body and no second close.
func TestTabBodyLiveOffMidRunWarpsImmediately(t *testing.T) {
	t.Run("master off during a switch", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		tabA := bodyKey(t, g, 0)
		root, _ := tt.Find("work-body-root")
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("phase at 60 ms = %v, want in", wm.body.phase)
		}
		if body, ok := tt.Find(tabA); !ok || body.W >= root.W-1 {
			t.Fatalf("the body is not mid-entrance: %+v of %+v", body, root)
		}
		framesBefore := wm.body.animationFrames
		setting.Enabled = false
		tt.Frame()
		bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "master off")
	})

	t.Run("Tab Open off during an open", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.OpenStyle = domainpreferences.MotionPopin
		setting.CloseStyle = domainpreferences.MotionPopin
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		waitForRealSurfaces(t, g.state, 3)
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("the open's phase at 60 ms = %v, want in", wm.body.phase)
		}
		root, _ := tt.Find("work-body-root")
		if body, ok := tt.Find(bodyKey(t, g, 2)); !ok || body.W >= root.W-1 {
			t.Fatalf("the opening body is not mid-entrance: %+v of %+v", body, root)
		}
		framesBefore := wm.body.animationFrames
		setting.TabOpen = false
		tt.Frame()
		bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 2, "Tab Open off")
	})

	t.Run("Tab Close off during a close", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.CloseStyle = domainpreferences.MotionPopin
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1)}
		root, _ := tt.Find("work-body-root")
		closing := paneSurfaces(t, g, 1)
		if !g.state.CloseActiveTab() {
			t.Fatalf("CloseActiveTab failed")
		}
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("the close's phase at 60 ms = %v, want in", wm.body.phase)
		}
		if body, ok := tt.Find(bodyKey(t, g, 0)); !ok || body.W >= root.W-1 {
			t.Fatalf("the replacement is not mid-entrance at 60 ms: %+v of %+v", body, root)
		}
		framesBefore := wm.body.animationFrames
		all := append(paneSurfaces(t, g, 0), closing...)
		calls := make([]int, len(all))
		logs := make([]int, len(all))
		for i, surface := range all {
			calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
		}
		setting.TabClose = false
		tt.Frame()
		bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "Tab Close off")
		for i, surface := range all {
			if got := surface.GridForCalls() - calls[i]; got != 0 {
				t.Errorf("surface %d computed %d grids on the close's warp frame, want none", i+1, got)
			}
			if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
				t.Errorf("surface %d delivered %d resizes on the close's warp frame, want none", i+1, got)
			}
		}
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the warped close destroyed its Surface %d times, want exactly once", got)
		}
		if _, ok := g.state.TakeTabGhost(ids[1]); ok {
			t.Fatalf("the warped close retained a body")
		}
		tt.Advance(400 * time.Millisecond)
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the warped close destroyed its Surface %d times after 400 ms, want once", got)
		}
	})

	t.Run("Reduce Motion on during a switch", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		tabA := bodyKey(t, g, 0)
		root, _ := tt.Find("work-body-root")
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("phase at 60 ms = %v, want in", wm.body.phase)
		}
		if body, ok := tt.Find(tabA); !ok || body.W >= root.W-1 {
			t.Fatalf("the body is not mid-entrance: %+v of %+v", body, root)
		}
		framesBefore := wm.body.animationFrames
		// SetPreferences settles the frame that turns Reduce Motion on.
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "Reduce Motion on")
	})

	t.Run("Tab Close off during the last close", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.CloseStyle = domainpreferences.MotionPopin
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 1)
		root, _ := tt.Find("work-body-root")
		closedID := g.mustTabID(t, 0)
		closing := paneSurfaces(t, g, 0)
		if !g.state.CloseActiveTab() {
			t.Fatalf("CloseActiveTab failed")
		}
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("the last close's phase at 60 ms = %v, want in", wm.body.phase)
		}
		if _, ok := tt.Find("work-body:home"); ok {
			t.Fatalf("Home appeared before the outgoing copy was gone")
		}
		framesBefore := wm.body.animationFrames
		setting.TabClose = false
		tt.Frame()
		if wm.body.phase != bodyIdle {
			t.Fatalf("the last close's warp left phase %v", wm.body.phase)
		}
		if got := wm.body.animationFrames - framesBefore; got != 0 {
			t.Fatalf("the last close's warp asked for %d body frames", got)
		}
		home, ok := tt.Find("work-body:home")
		if !ok || !rectNearOK(home, root, 0.5) {
			t.Fatalf("Home after the warp %+v, want the whole body %+v", home, root)
		}
		if got := closing[0].Closes(); got != 1 {
			t.Fatalf("the warped last close destroyed its Surface %d times, want once", got)
		}
		if _, ok := g.state.TakeTabGhost(closedID); ok {
			t.Fatalf("the warped last close retained a body")
		}
		tt.Advance(150 * time.Millisecond)
		if again, ok := tt.Find("work-body:home"); !ok || !rectNearOK(again, home, 0.5) {
			t.Fatalf("Home moved after the warp: %+v then %+v", home, again)
		}
	})
}

// TestTabBodyLiveNormalRunAndEventTimeWarpAreUnchanged is the control: with
// the switch left on the body still animates at 60 ms, and switching it off
// before the event still warps on the event frame.
func TestTabBodyLiveNormalRunAndEventTimeWarpAreUnchanged(t *testing.T) {
	t.Run("normal switch keeps animating", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		tabA := bodyKey(t, g, 0)
		root, _ := tt.Find("work-body-root")
		framesBefore := wm.body.animationFrames
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("phase at 60 ms = %v, want in", wm.body.phase)
		}
		if body, ok := tt.Find(tabA); !ok || body.W >= root.W-1 {
			t.Fatalf("the body at 60 ms = %+v, want it still moving inside %+v", body, root)
		}
		if wm.body.animationFrames <= framesBefore {
			t.Fatalf("the normal switch asked for no frame")
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.body.phase != bodyIdle {
			t.Fatalf("the normal switch did not finish: phase %v", wm.body.phase)
		}
		if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
			t.Fatalf("the final body %+v, want %+v", body, root)
		}
	})

	t.Run("switch off before the event warps", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		framesBefore := wm.body.animationFrames
		setting.TabSwitch = false
		g.state.SelectTab(0)
		tt.Frame()
		bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "event-time switch-off")
	})

	t.Run("another event after a mid-run switch-off warps", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("phase at 60 ms = %v, want in", wm.body.phase)
		}
		// The switch goes off and the user picks the other Tab before the
		// view sees either: the new event must still paint the final body and
		// reclaim the copy the first run left moving.
		framesBefore := wm.body.animationFrames
		setting.TabSwitch = false
		g.state.SelectTab(1)
		tt.Frame()
		bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 1, "a post-switch-off event")
	})
}

// TestTabBodyWarpLeavesASimultaneousPaneAnimationAlone turns Tab Switch off
// on the same frame a native Pane open starts. The body is already final on
// that frame, but the Pane's own switch is still on, so the warp must not rest
// MyGo's transition for it: the one-switch rule holds.
func TestTabBodyWarpLeavesASimultaneousPaneAnimationAlone(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	setting.OpenStyle = domainpreferences.MotionPopin
	g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{native: true, labels: true}, 2)
	tabA := bodyKey(t, g, 0)
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	target, _ := g.state.FocusedPaneID(0)
	regionBefore := wm.region
	g.state.SelectTab(0)
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the switch's phase at 60 ms = %v, want in", wm.body.phase)
	}
	framesBefore := wm.body.animationFrames

	// The same frame: a native Pane open starts and the Tab switch goes off.
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	setting.TabSwitch = false
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the Tab switch's run did not end: phase %v", wm.body.phase)
	}
	if got := wm.body.animationFrames - framesBefore; got != 0 {
		t.Fatalf("the body-only warp asked for %d body frames, want none", got)
	}
	// The body is final on the warp frame: the in-flight entrance was dropped,
	// not left to MyGo to finish.
	if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the warped body %+v, want the final whole body %+v", body, root)
	}
	// The Pane's region and previous boxes were recorded untransformed, so the
	// open computes its final layout from them.
	if !rectNearOK(wm.region, regionBefore, 0.5) {
		t.Fatalf("the region mid-switch = %+v, want the untransformed %+v",
			wm.region, regionBefore)
	}
	if got := wm.rects[target]; got.Column.X < regionBefore.X-0.5 {
		t.Fatalf("the target's boxes mid-switch = %+v, want them inside %+v",
			got.Column, regionBefore)
	}
	if wm.run == nil || wm.run.kind != motion.PaneOpen {
		t.Fatalf("the simultaneous Pane open did not start: run %+v", wm.run)
	}
	target = wm.run.target
	tt.Advance(60 * time.Millisecond)
	if wm.run == nil {
		t.Fatalf("the Pane open was warped by the Tab body's warp")
	}
	label, ok := tt.Find(locales.In("en").PaneRegion(1))
	if !ok {
		t.Fatalf("the first Pane has no region label")
	}
	box := wm.rects[target].Column
	// ScaleContent leaves the label off the Pane's final layout box while the
	// native transition plays; an instant frame would rest it at the box
	// exactly.
	if abs32(label.W-box.W) <= 1 {
		t.Fatalf("the simultaneous Pane open was rested: label width %v, final box %v",
			label.W, box.W)
	}
	if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body drifted after the warp: %+v, want %+v", body, root)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the simultaneous Pane open did not finish")
	}
	if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the final body %+v, want %+v", body, root)
	}
}

// TestTabBodyOpenAndCloseStyles checks the whole body's use of the open and
// close styles. slide and gnomed must say opacity 1 explicitly, and fade
// keeps its geometry.
func TestTabBodyOpenAndCloseStyles(t *testing.T) {
	for _, tc := range []struct {
		style domainpreferences.MotionStyle
		want  ui.Motion
	}{
		{domainpreferences.MotionPopin, ui.Motion{Scale: motion.PopinStartScale, Opacity: 0}},
		{domainpreferences.MotionSlide, ui.Motion{XPercent: -100, Opacity: 1}},
		{domainpreferences.MotionGnomed, ui.Motion{YPercent: 100, Collapse: true, Opacity: 1}},
		{domainpreferences.MotionFade, ui.Motion{Opacity: 0}},
	} {
		if got := bodyOpenMotion(tc.style); got != tc.want {
			t.Errorf("bodyOpenMotion(%v) = %+v, want %+v", tc.style, got, tc.want)
		}
		if got := bodyCloseMotion(tc.style); got != tc.want {
			t.Errorf("bodyCloseMotion(%v) = %+v, want %+v", tc.style, got, tc.want)
		}
	}
}

// tabBodySplitScene builds two Tabs and gives the first two settled Panes,
// then leaves the second Tab active so a SelectTab(0) under test is the
// animated slide switch. It returns the first Tab's Pane IDs in tree order
// and the boxes they recorded at rest.
func tabBodySplitScene(t *testing.T, setting *domainpreferences.Motion,
	mode paneMotionMode) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion,
	[]model.PaneID, map[model.PaneID]motion.PaneRects) {
	t.Helper()
	g, tt, wm := tabBodyLiveWindow(t, setting, mode, 2)
	g.state.SelectTab(0)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 3)
	pumpUntil(t, tt, "the split's final grids", func() bool { return gridsSettled(paneSurfaces(t, g, 0)...) })
	tt.Frame()
	root, _, ok := g.state.TabTree(0)
	if !ok || root == nil {
		t.Fatalf("Tab 0 has no tree")
	}
	ids := domainworkspace.CollectPaneIDs(root)
	if len(ids) != 2 {
		t.Fatalf("Tab 0 has %d Panes, want 2", len(ids))
	}
	before := map[model.PaneID]motion.PaneRects{}
	for _, id := range ids {
		rects, ok := wm.rects[id]
		if !ok || !rects.Valid() {
			t.Fatalf("Pane %s has no settled boxes: %+v", id, rects)
		}
		before[id] = rects
	}
	g.state.SelectTab(1)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	return g, tt, wm, ids, before
}

// slideToTabZero starts the slide switch to Tab 0 and asserts it is midway
// through its entrance with every recorded Pane box and the region still in
// the untransformed final layout. It returns the first Tab's body label key.
func slideToTabZero(t *testing.T, g *realTerminalGraph, tt *ui.Tester,
	wm *workspacePaneMotion, ids []model.PaneID, before map[model.PaneID]motion.PaneRects,
	root, region ui.Rect) string {
	t.Helper()
	tabA := bodyKey(t, g, 0)
	g.state.SelectTab(0)
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the switch's phase at 60 ms = %v, want in", wm.body.phase)
	}
	if body, ok := tt.Find(tabA); !ok || body.W >= root.W-1 {
		t.Fatalf("the body at 60 ms = %+v, want it mid-entrance inside %+v", body, root)
	}
	if !rectNearOK(wm.region, region, 0.5) {
		t.Fatalf("the region mid-switch = %+v, want the untransformed %+v", wm.region, region)
	}
	for _, id := range ids {
		if got := wm.rects[id]; !rectNearOK(got.Column, before[id].Column, 0.5) ||
			!rectNearOK(got.Slot, before[id].Slot, 0.5) {
			t.Fatalf("Pane %s's boxes mid-switch = %+v, want the untransformed %+v",
				id, got, before[id])
		}
	}
	return tabA
}

// surfaceCounters is a frame's grid and resize counts per surface.
type surfaceCounters struct {
	grid map[*realTerminalSurface]int
	log  map[*realTerminalSurface]int
}

func snapshotSurfaceCounters(surfaces []*realTerminalSurface) surfaceCounters {
	c := surfaceCounters{grid: map[*realTerminalSurface]int{}, log: map[*realTerminalSurface]int{}}
	for _, surface := range surfaces {
		c.grid[surface] = surface.GridForCalls()
		c.log[surface] = len(surface.ResizeLog())
	}
	return c
}

// assertNoSurfaceMath checks that no animation frame computed a grid or
// delivered a resize.
func assertNoSurfaceMath(t *testing.T, surfaces []*realTerminalSurface, c surfaceCounters, what string) {
	t.Helper()
	for _, surface := range surfaces {
		if got := surface.GridForCalls() - c.grid[surface]; got != 0 {
			t.Errorf("%s: surface %p computed %d grids on animation frames, want none", what, surface, got)
		}
		if got := len(surface.ResizeLog()) - c.log[surface]; got != 0 {
			t.Errorf("%s: surface %p delivered %d resizes on animation frames, want none", what, surface, got)
		}
	}
}

// TestTabBodyPaneEventDuringSwitchUsesTheUntransformedLayout is the verifier's
// second defect: a Pane event on a Tab body switch records Pane rectangles from
// the body's animated transform. The event must animate from the true old
// boxes, end at the final layout and grids, and keep the one-resize, zero
// animation-frame-grid-math contract.
func TestTabBodyPaneEventDuringSwitchUsesTheUntransformedLayout(t *testing.T) {
	t.Run("split", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
		root, _ := tt.Find("work-body-root")
		regionBefore := wm.region
		target, ok := g.state.FocusedPaneID(0)
		if !ok {
			t.Fatalf("Tab 0 has no focused Pane")
		}
		before := map[model.PaneID]motion.PaneRects{target: wm.rects[target]}
		surfaces := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
		for _, surface := range surfaces {
			surface.ResetResizeLog()
		}
		tabA := slideToTabZero(t, g, tt, wm, []model.PaneID{target}, before, root, regionBefore)

		if err := g.state.SplitRight(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		// The body is final on the event frame, so the Pane geometry below is
		// read in the final body's coordinates.
		if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
			t.Fatalf("the body on the split frame %+v, want the final whole body %+v", body, root)
		}
		if !rectNearOK(wm.region, regionBefore, 0.5) {
			t.Fatalf("the region after the split = %+v, want %+v", wm.region, regionBefore)
		}
		run := wm.run
		if run == nil || run.kind != motion.PaneOpen {
			t.Fatalf("the split did not start a Pane-open run: %+v", wm.run)
		}
		if run.target != target {
			t.Fatalf("the run's target = %q, want the split Pane %q", run.target, target)
		}
		move := findVisual(t, run, target, motion.Move)
		if !rectNearOK(move.From, before[target].Column, 0.5) {
			t.Fatalf("the split starts from %+v, want the untransformed old column %+v",
				move.From, before[target].Column)
		}
		finalTarget, ok := run.final[target]
		if !ok {
			t.Fatalf("the target has no final boxes")
		}
		var fresh model.PaneID
		for id := range run.final {
			if id != target {
				fresh = id
			}
		}
		finalFresh, ok := run.final[fresh]
		if !ok || fresh == "" {
			t.Fatalf("the split Pane has no final boxes")
		}
		if finalTarget.Slot.X < regionBefore.X-0.5 || finalFresh.Slot.X < regionBefore.X-0.5 {
			t.Fatalf("the final slots are shifted: %+v / %+v in region %+v",
				finalTarget.Slot, finalFresh.Slot, regionBefore)
		}
		if abs32(finalTarget.Slot.W-regionBefore.W/2) > 1 ||
			abs32(finalFresh.Slot.X-(regionBefore.X+regionBefore.W/2)) > 1 {
			t.Fatalf("the final split slots %+v / %+v do not halve the region %+v",
				finalTarget.Slot, finalFresh.Slot, regionBefore)
		}
		if finalTarget.Column.W >= before[target].Column.W {
			t.Fatalf("the target's final column %+v is not narrower than its old %+v",
				finalTarget.Column, before[target].Column)
		}
		final := map[model.PaneID]motion.PaneRects{target: finalTarget, fresh: finalFresh}

		waitForRealSurfaces(t, g.state, 3)
		tt.Frame()
		all := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
		counters := snapshotSurfaceCounters(all)
		targetSurface := surfaceForPane(t, g, 0, target)
		freshSurface := surfaceForPane(t, g, 0, fresh)
		if got := len(targetSurface.ResizeLog()); got != 1 {
			t.Fatalf("the target delivered %d resizes at the split, want 1: %+v",
				got, targetSurface.ResizeLog())
		}
		if got := len(freshSurface.ResizeLog()); got != 1 {
			t.Fatalf("the split Pane delivered %d resizes at the split, want 1: %+v",
				got, freshSurface.ResizeLog())
		}
		for _, surface := range paneSurfaces(t, g, 1) {
			if got := len(surface.ResizeLog()); got != 0 {
				t.Fatalf("the other Tab's Pane delivered %d resizes, want none", got)
			}
		}

		tt.Advance(60 * time.Millisecond)
		tt.Frame()
		if wm.run == nil {
			t.Fatalf("the split run ended before its duration")
		}
		assertNoSurfaceMath(t, all, counters, "the split during a switch")
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the split run did not finish")
		}
		for id, want := range final {
			if got := wm.rects[id]; !rectNearOK(got.Column, want.Column, 0.5) ||
				!rectNearOK(got.Slot, want.Slot, 0.5) || !rectNearOK(got.Body, want.Body, 0.5) {
				t.Fatalf("Pane %s settled at %+v, want the run's final %+v", id, got, want)
			}
		}
		if !gridsSettled(paneSurfaces(t, g, 0)...) {
			t.Fatalf("the split's Panes are not at their final grids")
		}
		if !rectNearOK(wm.region, regionBefore, 0.5) {
			t.Fatalf("the settled region = %+v, want %+v", wm.region, regionBefore)
		}
	})

	t.Run("close", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm, ids, before := tabBodySplitScene(t, &setting, paneMotionMode{labels: true})
		root, _ := tt.Find("work-body-root")
		regionBefore := wm.region
		closed, ok := g.state.FocusedPaneID(0)
		if !ok {
			t.Fatalf("Tab 0 has no focused Pane")
		}
		survivor := ids[0]
		if survivor == closed {
			survivor = ids[1]
		}
		closedSurface := surfaceForPane(t, g, 0, closed)
		survivorSurface := surfaceForPane(t, g, 0, survivor)
		all := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
		for _, surface := range all {
			surface.ResetResizeLog()
		}
		tabA := slideToTabZero(t, g, tt, wm, ids, before, root, regionBefore)

		if !g.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane returned false")
		}
		tt.Frame()
		if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
			t.Fatalf("the body on the close frame %+v, want the final whole body %+v", body, root)
		}
		run := wm.run
		if run == nil || run.kind != motion.PaneClose || !run.native {
			t.Fatalf("the close did not start a native Pane-close run: %+v", wm.run)
		}
		if run.target != survivor {
			t.Fatalf("the close's target = %q, want the survivor %q", run.target, survivor)
		}
		ghost, ok := run.ghosts[closed]
		if !ok {
			t.Fatalf("the close has no ghost for the closed Pane")
		}
		if got := ghost.rects.Column; !rectNearOK(got, before[closed].Column, 0.5) {
			t.Fatalf("the ghost starts at %+v, want the untransformed old column %+v",
				got, before[closed].Column)
		}
		ghostVisual := findVisual(t, run, closed, motion.Exit)
		if !rectNearOK(ghostVisual.From, before[closed].Column, 0.5) {
			t.Fatalf("the ghost visual starts at %+v, want the untransformed %+v",
				ghostVisual.From, before[closed].Column)
		}
		survivorVisual := findVisual(t, run, survivor, motion.Move)
		if !rectNearOK(survivorVisual.From, before[survivor].Column, 0.5) {
			t.Fatalf("the survivor starts at %+v, want its untransformed old column %+v",
				survivorVisual.From, before[survivor].Column)
		}
		final, ok := run.final[survivor]
		if !ok {
			t.Fatalf("the survivor has no final boxes")
		}
		if !rectNearOK(final.Slot, regionBefore, 1) {
			t.Fatalf("the survivor's final slot %+v does not fill the region %+v",
				final.Slot, regionBefore)
		}
		if _, ok := run.final[closed]; ok {
			t.Fatalf("the closed Pane is still in the final layout")
		}

		waitForRealSurfaces(t, g.state, 2)
		tt.Frame()
		counters := snapshotSurfaceCounters(all)
		if got := len(survivorSurface.ResizeLog()); got != 1 {
			t.Fatalf("the survivor delivered %d resizes at the close, want 1: %+v",
				got, survivorSurface.ResizeLog())
		}
		if got := len(closedSurface.ResizeLog()); got != 0 {
			t.Fatalf("the closed Pane delivered %d resizes at the close, want none", got)
		}
		for _, surface := range paneSurfaces(t, g, 1) {
			if got := len(surface.ResizeLog()); got != 0 {
				t.Fatalf("the other Tab's Pane delivered %d resizes, want none", got)
			}
		}

		tt.Advance(60 * time.Millisecond)
		tt.Frame()
		if wm.run == nil {
			t.Fatalf("the close run ended before its duration")
		}
		// The native survivor is not rested by the body warp: its box is still
		// on its way to the final column.
		if label, ok := tt.Find(locales.In("en").PaneRegion(1)); !ok ||
			abs32(label.W-final.Column.W) <= 1 {
			t.Fatalf("the survivor's label at 60 ms = %+v, want it still moving toward %+v",
				label, final.Column)
		}
		assertNoSurfaceMath(t, all, counters, "the close during a switch")
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the close run did not finish")
		}
		if got := closedSurface.Closes(); got != 1 {
			t.Fatalf("the closed Pane's surface was destroyed %d times, want exactly once", got)
		}
		if got := wm.rects[survivor]; !rectNearOK(got.Column, final.Column, 0.5) ||
			!rectNearOK(got.Slot, final.Slot, 0.5) || !rectNearOK(got.Body, final.Body, 0.5) {
			t.Fatalf("the survivor settled at %+v, want the run's final %+v", got, final)
		}
		if !gridsSettled(paneSurfaces(t, g, 0)...) {
			t.Fatalf("the survivor is not at its final grid")
		}
		if !rectNearOK(wm.region, regionBefore, 0.5) {
			t.Fatalf("the settled region = %+v, want %+v", wm.region, regionBefore)
		}
	})

	t.Run("move", func(t *testing.T) {
		setting := tabBodyPreferences()
		setting.SwitchStyle = domainpreferences.MotionSwitchSlide
		g, tt, wm, ids, before := tabBodySplitScene(t, &setting, paneMotionMode{labels: true})
		root, _ := tt.Find("work-body-root")
		regionBefore := wm.region
		moved, target := ids[1], ids[0]
		all := append(paneSurfaces(t, g, 0), paneSurfaces(t, g, 1)...)
		tabA := slideToTabZero(t, g, tt, wm, ids, before, root, regionBefore)

		tabID, _ := g.state.TabID(0)
		if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
			t.Fatalf("MovePane in one Tab returned false")
		}
		tt.Frame()
		if body, ok := tt.Find(tabA); !ok || !rectNearOK(body, root, 0.5) {
			t.Fatalf("the body on the move frame %+v, want the final whole body %+v", body, root)
		}
		run := wm.run
		if run == nil || run.kind != motion.PaneReorder || run.zoom {
			t.Fatalf("the move did not start a same-Tab reorder run: %+v", wm.run)
		}
		exit := findVisual(t, run, moved, motion.Exit)
		if !rectNearOK(exit.From, before[moved].Column, 0.5) {
			t.Fatalf("the old-position ghost starts at %+v, want the untransformed %+v",
				exit.From, before[moved].Column)
		}
		enter := findVisual(t, run, moved, motion.Enter)
		if !rectNearOK(enter.Content, before[moved].Column, 0.5) {
			t.Fatalf("the new-position overlay's content = %+v, want the untransformed %+v",
				enter.Content, before[moved].Column)
		}
		sibling := findVisual(t, run, target, motion.Move)
		if !rectNearOK(sibling.From, before[target].Column, 0.5) {
			t.Fatalf("the sibling starts at %+v, want its untransformed old column %+v",
				sibling.From, before[target].Column)
		}
		finalMoved, okMoved := run.final[moved]
		finalTarget, okTarget := run.final[target]
		if !okMoved || !okTarget {
			t.Fatalf("the move has no final boxes")
		}
		if !rectNearOK(sibling.To, finalTarget.Column, 0.5) {
			t.Fatalf("the sibling heads for %+v, want %+v", sibling.To, finalTarget.Column)
		}
		if abs32(finalMoved.Slot.X-regionBefore.X) > 1 ||
			abs32(finalTarget.Slot.X-(regionBefore.X+regionBefore.W/2)) > 1 {
			t.Fatalf("the final move slots %+v / %+v are shifted in region %+v",
				finalMoved.Slot, finalTarget.Slot, regionBefore)
		}

		tt.Frame()
		counters := snapshotSurfaceCounters(all)
		// The Panes swap places without changing size; their final grids are
		// pinned once and no animation frame does grid math.
		resizesBefore := map[*realTerminalSurface]int{}
		for _, surface := range paneSurfaces(t, g, 0) {
			resizesBefore[surface] = len(surface.ResizeLog())
		}
		tt.Advance(60 * time.Millisecond)
		tt.Frame()
		if wm.run == nil {
			t.Fatalf("the move run ended before its duration")
		}
		if !sibling.Native || !run.nativePanes[target] {
			t.Fatalf("the sibling lost its native route to the warp: %+v", sibling)
		}
		flight, _ := run.anim.StateAt(sibling, tt.Now())
		if flight.X <= sibling.From.X+1 || abs32(flight.X-sibling.To.X) <= 0.5 {
			t.Fatalf("the native sibling at 60 ms = %+v, want it in flight %+v → %+v",
				flight, sibling.From, sibling.To)
		}
		assertNoSurfaceMath(t, all, counters, "the move during a switch")
		tt.Advance(2 * time.Second)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the move run did not finish")
		}
		for id, want := range map[model.PaneID]motion.PaneRects{moved: finalMoved, target: finalTarget} {
			if got := wm.rects[id]; !rectNearOK(got.Column, want.Column, 0.5) ||
				!rectNearOK(got.Slot, want.Slot, 0.5) || !rectNearOK(got.Body, want.Body, 0.5) {
				t.Fatalf("Pane %s settled at %+v, want the run's final %+v", id, got, want)
			}
		}
		for _, surface := range paneSurfaces(t, g, 0) {
			if got := len(surface.ResizeLog()) - resizesBefore[surface]; got > 1 {
				t.Fatalf("a moved Pane delivered %d resizes, want at most one", got)
			}
		}
		if !gridsSettled(paneSurfaces(t, g, 0)...) {
			t.Fatalf("the moved Panes are not at their final grids")
		}
		if !rectNearOK(wm.region, regionBefore, 0.5) {
			t.Fatalf("the settled region = %+v, want %+v", wm.region, regionBefore)
		}
	})
}

// slideSwitchToFirst starts a slide switch from the last Tab to the first and
// stops 60 ms into the entrance. It returns the body key of the incoming Tab
// and the body container's final box.
func slideSwitchToFirst(t *testing.T, g *realTerminalGraph, tt *ui.Tester,
	wm *workspacePaneMotion) (string, ui.Rect) {
	t.Helper()
	root, ok := tt.Find("work-body-root")
	if !ok {
		t.Fatalf("the body container has no label")
	}
	incoming := bodyKey(t, g, 0)
	g.state.SelectTab(0)
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	if wm.body.phase != bodyIn {
		t.Fatalf("the switch's phase at 60 ms = %v, want in", wm.body.phase)
	}
	if body, ok := tt.Find(incoming); !ok || body.W >= root.W-1 {
		t.Fatalf("the incoming body at 60 ms = %+v, want it mid-entrance inside %+v", body, root)
	}
	return incoming, root
}

// TestBatchTabCloseMidSwitchWarpsTheBody is D1: a batch close during an
// in-flight Tab switch must paint the final layout on that frame instead of
// letting the running entrance continue. Closing several Tabs records no
// per-Tab event, so the state raises the dedicated body warp signal the
// runtime consumes: no per-Tab body or Pane animation starts.
func TestBatchTabCloseMidSwitchWarpsTheBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(*state.State) bool
	}{
		{"CloseTabsRight several", func(s *state.State) bool { return s.CloseTabsRight(0) }},
		{"CloseOtherTabs", func(s *state.State) bool { return s.CloseOtherTabs(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setting := tabBodyPreferences()
			setting.SwitchStyle = domainpreferences.MotionSwitchSlide
			g, tt, wm := tabBodyWindowN(t, setting, 3)
			ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
			incoming, root := slideSwitchToFirst(t, g, tt, wm)
			if !rectNearOK(root, ui.Rect{X: 0, Y: 36, W: 900, H: 564}, 0.5) {
				t.Fatalf("the whole body is %+v, want the final {X:0 Y:36 W:900 H:564}", root)
			}
			var closed []*realTerminalSurface
			for index := 1; index < 3; index++ {
				closed = append(closed, paneSurfaces(t, g, index)...)
			}
			outKey := wm.body.out.key()
			framesBefore := wm.body.animationFrames
			warpBefore := g.state.BodyWarpSeq()
			if !tc.close(g.state) {
				t.Fatalf("%s failed", tc.name)
			}
			if got := g.state.BodyWarpSeq(); got != warpBefore+1 {
				t.Fatalf("the batch close raised the body warp seq to %d, want %d", got, warpBefore+1)
			}

			tt.Frame()
			bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, tc.name)
			if wm.run != nil {
				t.Fatalf("%s started a Pane run: %+v", tc.name, wm.run)
			}
			if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
				t.Fatalf("%s: the final body %+v, want the whole body %+v", tc.name, body, root)
			}
			if _, ok := tt.Find(outKey); ok {
				t.Fatalf("%s still builds the outgoing body %q", tc.name, outKey)
			}
			if g.state.TabCount() != 1 || g.state.ActiveTab() != 0 {
				t.Fatalf("%s left %d Tabs with active %d, want 1 and 0",
					tc.name, g.state.TabCount(), g.state.ActiveTab())
			}
			if id, _ := g.state.TabID(0); id != ids[0] {
				t.Fatalf("%s kept %q, want %q", tc.name, id, ids[0])
			}
			for i, surface := range closed {
				if got := surface.Closes(); got != 1 {
					t.Fatalf("%s destroyed surface %d %d times, want once", tc.name, i+1, got)
				}
			}
			for _, id := range ids[1:] {
				if _, ok := g.state.TakeTabGhost(id); ok {
					t.Fatalf("%s retained a body ghost for %q", tc.name, id)
				}
			}

			// The warp is final: no later frame asks for a body frame or moves
			// the body again.
			tt.Advance(400 * time.Millisecond)
			tt.Frame()
			if wm.body.phase != bodyIdle {
				t.Fatalf("%s warped into phase %v", tc.name, wm.body.phase)
			}
			if got := wm.body.animationFrames - framesBefore; got != 0 {
				t.Fatalf("%s asked for %d body frames after the warp", tc.name, got)
			}
			if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
				t.Fatalf("%s: the body 400 ms later %+v, want %+v", tc.name, body, root)
			}
		})
	}
}

// TestBatchTabCloseAtRestKeepsWarping is the control: with no run in flight
// the body warp signal is a no-op and the kept body simply appears at its
// final layout, with no frame, ghost or Pane run.
func TestBatchTabCloseAtRestKeepsWarping(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyWindowN(t, setting, 3)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
	var closed []*realTerminalSurface
	for index := 1; index < 3; index++ {
		closed = append(closed, paneSurfaces(t, g, index)...)
	}
	framesBefore := wm.body.animationFrames
	if !g.state.CloseOtherTabs(0) {
		t.Fatalf("CloseOtherTabs failed")
	}
	tt.Frame()
	bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "the batch close at rest")
	if wm.run != nil {
		t.Fatalf("a batch close at rest started a Pane run")
	}
	if g.state.TabCount() != 1 || g.state.ActiveTab() != 0 {
		t.Fatalf("the batch close left %d Tabs with active %d, want 1 and 0",
			g.state.TabCount(), g.state.ActiveTab())
	}
	if id, _ := g.state.TabID(0); id != ids[0] {
		t.Fatalf("the batch close kept %q, want %q", id, ids[0])
	}
	for i, surface := range closed {
		if got := surface.Closes(); got != 1 {
			t.Fatalf("the batch close destroyed surface %d %d times, want once", i+1, got)
		}
	}
	for _, id := range ids[1:] {
		if _, ok := g.state.TakeTabGhost(id); ok {
			t.Fatalf("the batch close retained a body ghost for %q", id)
		}
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if got := wm.body.animationFrames - framesBefore; got != 0 {
		t.Fatalf("the batch close at rest asked for %d body frames", got)
	}
}

// TestBatchTabCloseMidSwitchLeavesAPaneAnimationAlone is D1's same-frame Pane
// case: a native Pane transition starts on the frame of the batch close. The
// body must already be final, but the warp must not rest the Pane's MyGo
// transition (the one-switch rule), so it warps by generation only.
func TestBatchTabCloseMidSwitchLeavesAPaneAnimationAlone(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{native: true, labels: true}, 2)
	incoming, root := slideSwitchToFirst(t, g, tt, wm)
	closed := paneSurfaces(t, g, 1)
	framesBefore := wm.body.animationFrames

	// The same frame: a native Pane open starts on the kept Tab and the batch
	// close removes the other Tab without recording a motion event.
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	if !g.state.CloseOtherTabs(0) {
		t.Fatalf("CloseOtherTabs failed")
	}
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the batch close's body phase = %v, want idle", wm.body.phase)
	}
	if got := wm.body.animationFrames - framesBefore; got != 0 {
		t.Fatalf("the batch close asked for %d body frames, want none", got)
	}
	if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body on the batch close frame %+v, want the final whole body %+v", body, root)
	}
	if wm.run == nil || wm.run.kind != motion.PaneOpen || !wm.run.native {
		t.Fatalf("the simultaneous native Pane open did not start: run %+v", wm.run)
	}
	target := wm.run.target
	tt.Advance(60 * time.Millisecond)
	if wm.run == nil {
		t.Fatalf("the Pane open was rested by the body warp")
	}
	label, ok := tt.Find(locales.In("en").PaneRegion(1))
	if !ok {
		t.Fatalf("the first Pane has no region label")
	}
	box := wm.rects[target].Column
	// ScaleContent leaves the label off the Pane's final layout box while the
	// native transition plays; an instant frame would rest it at the box.
	if abs32(label.W-box.W) <= 1 {
		t.Fatalf("the simultaneous Pane open was rested: label width %v, final box %v",
			label.W, box.W)
	}
	if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body drifted during the Pane open: %+v, want %+v", body, root)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the simultaneous Pane open did not finish")
	}
	if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the final body %+v, want %+v", body, root)
	}
	if got := closed[0].Closes(); got != 1 {
		t.Fatalf("the batch close destroyed the other Tab's surface %d times, want once", got)
	}
}

// TestNonCurrentTabCloseMidSwitchWarpsTheBody is D2: closing a Tab that is not
// the body on screen while a switch plays must make the body final on that
// frame, not stop the runtime and leave MyGo painting the entrance. With the
// body idle, a later live switch-off must find it already final and stay put.
func TestNonCurrentTabCloseMidSwitchWarpsTheBody(t *testing.T) {
	setting := tabBodyPreferences()
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyLiveWindow(t, &setting, paneMotionMode{labels: true}, 2)
	incoming, root := slideSwitchToFirst(t, g, tt, wm)
	closedID := g.mustTabID(t, 1)
	closed := paneSurfaces(t, g, 1)
	framesBefore := wm.body.animationFrames
	if !g.state.CloseTab(1) {
		t.Fatalf("closing the non-current Tab failed")
	}
	tt.Frame()
	bodyWarpsAtOnce(t, g, tt, wm, framesBefore, 0, "the non-current close mid-switch")
	if wm.run != nil {
		t.Fatalf("a non-current close started a Pane run: %+v", wm.run)
	}
	if g.state.TabCount() != 1 || g.state.ActiveTab() != 0 {
		t.Fatalf("the non-current close left %d Tabs with active %d, want 1 and 0",
			g.state.TabCount(), g.state.ActiveTab())
	}
	if got := closed[0].Closes(); got != 1 {
		t.Fatalf("the closed background Surface closed %d times, want once", got)
	}
	if _, ok := g.state.TakeTabGhost(closedID); ok {
		t.Fatalf("a non-current close retained a body ghost")
	}
	if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body after the non-current close %+v, want %+v", body, root)
	}

	// The run is over and the body final, so a live switch-off has nothing to
	// correct. It must not ask for a frame or move the body.
	setting.TabSwitch = false
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the live switch-off left phase %v", wm.body.phase)
	}
	if got := wm.body.animationFrames - framesBefore; got != 0 {
		t.Fatalf("the live switch-off asked for %d body frames, want none", got)
	}
	if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body on the live switch-off frame %+v, want %+v", body, root)
	}
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if body, ok := tt.Find(incoming); !ok || !rectNearOK(body, root, 0.5) {
		t.Fatalf("the body 100 ms after the switch-off %+v, want %+v", body, root)
	}
}
