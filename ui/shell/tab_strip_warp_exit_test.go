package shell

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// This file is ticket 20's warp-exit regression: a Tab that disappears on the
// build immediately after a warp must still leave its close copy. A warp
// build has always shown its layout at once (Duration -1), but it must keep
// recording the TabClose exit for MyGo's next frame; MyGo copies an element
// from the transition of its last build, and a negative ExitDuration means the
// copy goes at once. Before this regression a switch warp armed -1/-1, so the
// very next close popped its Tab out with only the surviving siblings moving.

// stripWarpWindow mounts the work Tab strip alone over n Tabs at width×height.
// Every item is labeled and every build asks for the next frame, so
// Advance(17ms) paints exactly one frame and a test can read the event paint
// before a later build could settle or cancel it.
func stripWarpWindow(t *testing.T, setting domainpreferences.Motion, n, width, height int,
	prepare func(t *testing.T, g *followGraph, ids []model.TabID)) (*followGraph, *ui.Tester, *workTabReveal, []model.TabID) {
	t.Helper()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, n)
	if prepare != nil {
		prepare(t, g, ids)
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), width, height)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	return g, tt, reveal, ids
}

// steppedStripWindow is steppedStripGraph with the body runtime exposed, for
// the cases that must prove a non-current close leaves the body idle. It
// mounts the real shell over real terminal surfaces and advances to rest one
// build at a time.
func steppedStripWindow(t *testing.T, setting domainpreferences.Motion, n, width, height int,
	prepare func(t *testing.T, g *realTerminalGraph)) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion, *workTabReveal) {
	t.Helper()
	g := motionGraph(t, setting)
	for range n {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, n)
	if prepare != nil {
		prepare(t, g)
	}
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(steppingStripView(view), width, height)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	return g, tt, wm, reveal
}

// stripWarpViewport finds the strip's viewport label.
func stripWarpViewport(t *testing.T, tt *ui.Tester) ui.Rect {
	t.Helper()
	viewport, ok := tt.Find(locales.In("en").WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	return viewport
}

// stepStripWarp advances exactly one build and asserts it consumed kind. A
// step that builds more than one frame would let a later build replace the
// event paint this file measures.
func stepStripWarp(t *testing.T, s *state.State, tt *ui.Tester, reveal *workTabReveal, kind motion.Kind) {
	t.Helper()
	before := reveal.strip.frames
	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - before; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	stripEvent(t, s, kind)
}

// stepStripBuild advances exactly one build without requiring a motion event,
// for a frame whose warp records none (a row-consistency cancellation, a
// batch close, Reduce Motion or a hidden window).
func stepStripBuild(t *testing.T, tt *ui.Tester, reveal *workTabReveal) {
	t.Helper()
	before := reveal.strip.frames
	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - before; got != 1 {
		t.Fatalf("the step built %d frames, want exactly one", got)
	}
}

// differingStripPixels counts the pixels of box whose colors differ between
// two frames. Image() returns a fresh copy, so both snapshots stay valid.
func differingStripPixels(a, b *image.RGBA, box ui.Rect) int {
	if a.Bounds() != b.Bounds() {
		return -1
	}
	bounds := a.Bounds()
	x0, y0 := max(int(box.X), bounds.Min.X), max(int(box.Y), bounds.Min.Y)
	x1, y1 := min(int(box.X+box.W), bounds.Max.X), min(int(box.Y+box.H), bounds.Max.Y)
	count := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				count++
			}
		}
	}
	return count
}

// tabInkPixels counts the pixels of box that are not the bar's fill. An idle
// Work Tab's title, marker, status dot and control apertures are such ink, so
// a close copy of one is visible even without the selected fill.
func tabInkPixels(tt *ui.Tester, box ui.Rect, bar [3]uint8) int {
	img := tt.Image()
	count := 0
	for y := int(box.Y) + 1; y < int(box.Y+box.H)-1; y++ {
		for x := int(box.X) + 1; x < int(box.X+box.W)-1; x++ {
			c := img.RGBAAt(x, y)
			if diff8(c.R, bar[0]) >= 8 || diff8(c.G, bar[1]) >= 8 || diff8(c.B, bar[2]) >= 8 {
				count++
			}
		}
	}
	return count
}

// TestTabStripCloseAfterSwitchWarpPaintsTheExitCopy is ticket 20's warp-exit
// regression, the exact repro: four fitting 1600-DIP Classic Tabs, two switch
// warps, then closing the current Tab on the very next build. The close must
// run 180 ms with warp false and paint the closed Tab's collapsing copy on its
// event frame; the final layout must equal the settled one.
func TestTabStripCloseAfterSwitchWarpPaintsTheExitCopy(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g, tt, reveal, ids := stripWarpWindow(t, setting, 4, 1600, 480, nil)
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the four-Tab strip overflows (MaxX=%v); the case needs a fitting strip", reveal.scroll.MaxX)
	}
	viewport := stripWarpViewport(t, tt)

	g.state.SelectTab(0)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.run != nil || !reveal.strip.warp {
		t.Fatalf("the first switch left run=%+v warp=%v, want a warp", reveal.strip.run, reveal.strip.warp)
	}
	if !reveal.strip.exit {
		t.Fatalf("the switch warp did not arm the TabClose exit record")
	}
	g.state.SelectTab(3)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.run != nil || !reveal.strip.warp {
		t.Fatalf("the second switch left run=%+v warp=%v, want a warp", reveal.strip.run, reveal.strip.warp)
	}
	if !reveal.strip.exit {
		t.Fatalf("the second switch warp did not arm the TabClose exit record")
	}

	closed := ids[3]
	box := stripTravel(t, tt, closed)
	bar := stripBarFill(tt, viewport, box)
	pre := tt.Image()
	preInk := tabInkPixels(tt, box, bar)
	if preInk < 60 {
		t.Fatalf("the current Tab has only %d ink pixels before the close; the case cannot see its copy", preInk)
	}

	preFrames := reveal.strip.animationFrames
	if !g.state.CloseTab(3) {
		t.Fatalf("CloseTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose ||
		reveal.strip.run.duration != 180*time.Millisecond {
		t.Fatalf("the close run = %+v, want a 180ms TabClose", reveal.strip.run)
	}
	if reveal.strip.warp {
		t.Fatalf("the close event painted a warp instead of its run")
	}
	if got := reveal.strip.animationFrames - preFrames; got != 1 {
		t.Fatalf("the close event asked for %d strip frames, want one", got)
	}
	event := tt.Image()
	if eventInk := tabInkPixels(tt, box, bar); eventInk < preInk/2 {
		t.Fatalf("the closed Tab paints %d of its %d ink pixels on the event frame: the exit copy is missing",
			eventInk, preInk)
	}
	diffPre := differingStripPixels(event, pre, box)
	if diffPre < 0 {
		t.Fatalf("the pre-close and event frames differ in size")
	}

	tt.Advance(180*time.Millisecond + 17*time.Millisecond)
	final := tt.Image()
	diffSettled := differingStripPixels(final, event, box)
	if diffSettled <= diffPre {
		t.Fatalf("the event frame differs from the pre-close frame in %d pixels and from the settled layout in %d; the copy must start at the Tab's old place",
			diffPre, diffSettled)
	}
	tt.Advance(400 * time.Millisecond)
	settled := tt.Image()
	if got := differingStripPixels(settled, final, box); got != 0 {
		t.Fatalf("the strip moved %d pixels after its run ended; the final frame is not the settled layout", got)
	}
}

// TestTabStripCloseAfterSwitchWarpPaintsTheExitCopyInTheShell is the same
// repro over the real shell and real terminal surfaces at 1600×600: the strip
// runtime is the window's own, and the body's Ticket-19 transition must not
// change the strip's exit record.
func TestTabStripCloseAfterSwitchWarpPaintsTheExitCopyInTheShell(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g, tt, reveal := steppedStripGraph(t, setting, 4, 1600, 600, nil)
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the four-Tab strip overflows (MaxX=%v); the case needs a fitting strip", reveal.scroll.MaxX)
	}
	viewport := stripWarpViewport(t, tt)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}

	g.state.SelectTab(0)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	g.state.SelectTab(3)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.run != nil || !reveal.strip.warp || !reveal.strip.exit {
		t.Fatalf("the switch warps left run=%+v warp=%v exit=%v, want a warp with the close record",
			reveal.strip.run, reveal.strip.warp, reveal.strip.exit)
	}

	closed := ids[3]
	box := stripTravel(t, tt, closed)
	bar := stripBarFill(tt, viewport, box)
	pre := tt.Image()
	preFrames := reveal.strip.animationFrames
	if !g.state.CloseTab(3) {
		t.Fatalf("CloseTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose ||
		reveal.strip.run.duration != 180*time.Millisecond || reveal.strip.warp {
		t.Fatalf("the shell close = run %+v warp=%v, want a 180ms TabClose run", reveal.strip.run, reveal.strip.warp)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 1 {
		t.Fatalf("the shell close event asked for %d strip frames, want one", got)
	}
	event := tt.Image()
	if got := countTabFill(tt, box, bar); got < int(box.W/2) {
		t.Fatalf("the shell's closed Tab paints only %d fill pixels of its %v box on the event frame", got, box.W)
	}
	diffPre := differingStripPixels(event, pre, box)
	tt.Advance(180*time.Millisecond + 17*time.Millisecond)
	final := tt.Image()
	if got := countTabFill(tt, box, bar); got != 0 {
		t.Fatalf("the shell's closed Tab still paints %d fill pixels after its run", got)
	}
	if diffSettled := differingStripPixels(final, event, box); diffSettled <= diffPre {
		t.Fatalf("the shell's event frame differs from the pre-close frame in %d pixels and from the settled layout in %d",
			diffPre, diffSettled)
	}
	tt.Advance(400 * time.Millisecond)
	if got := differingStripPixels(tt.Image(), final, box); got != 0 {
		t.Fatalf("the shell strip moved %d pixels after its run ended", got)
	}
}

// TestTabStripNonCurrentCloseAfterSwitchWarpPaintsTheExit closes a background
// Tab on the build right after a switch warp. Its idle-styled exit copy must
// paint over its old place while the survivors still make room over 180 ms,
// and the body must stay idle: a non-current close is strip-only.
func TestTabStripNonCurrentCloseAfterSwitchWarpPaintsTheExit(t *testing.T) {
	setting := tabStripPreferences()
	// The switch itself must not animate the body: the assertion below is that
	// the non-current close is strip-only, not that a switch under test is off.
	setting.TabSwitch = false
	g, tt, wm, reveal := steppedStripWindow(t, setting, 4, 1600, 600, nil)
	viewport := stripWarpViewport(t, tt)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}

	g.state.SelectTab(0)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	g.state.SelectTab(2)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if wm.body.phase != bodyIdle {
		t.Fatalf("the warped switches moved the body: phase %v", wm.body.phase)
	}
	if !reveal.strip.exit {
		t.Fatalf("the switch warp did not arm the close record")
	}

	closed := ids[1]
	box := stripTravel(t, tt, closed)
	nextBefore := stripTravel(t, tt, ids[2])
	bar := stripBarFill(tt, viewport, box)
	pre := tt.Image()
	preInk := tabInkPixels(tt, box, bar)
	if preInk < 60 {
		t.Fatalf("the closing background Tab has only %d ink pixels; the case cannot see its copy", preInk)
	}
	bodyFrames := wm.body.animationFrames

	if !g.state.CloseTab(1) {
		t.Fatalf("CloseTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose || reveal.strip.warp {
		t.Fatalf("the background close = run %+v warp=%v, want a TabClose run", reveal.strip.run, reveal.strip.warp)
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the background close moved the body: phase %v, +%d frames",
			wm.body.phase, wm.body.animationFrames-bodyFrames)
	}
	event := tt.Image()
	if eventInk := tabInkPixels(tt, box, bar); eventInk < preInk*3/4 {
		t.Fatalf("the background Tab paints %d of its %d ink pixels on the event frame: the exit copy is missing",
			eventInk, preInk)
	}
	if diffPre := differingStripPixels(event, pre, box); diffPre >= preInk/2 {
		t.Fatalf("the event frame differs from the pre-close frame in %d of the box's %d ink pixels; the copy is not at its old place",
			diffPre, preInk)
	}
	if start := stripTravel(t, tt, ids[2]); !rectNearOK(start, nextBefore, 1) {
		t.Fatalf("the next Tab starts at %+v, want its old place %+v", start, nextBefore)
	}
	tt.Advance(90 * time.Millisecond)
	mid := stripTravel(t, tt, ids[2])
	if mid.X >= nextBefore.X-1 || mid.X <= box.X+1 {
		t.Fatalf("the next Tab is at x=%v mid-run, want between the closed Tab's %v and its old %v",
			mid.X, box.X, nextBefore.X)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, ids[2]); abs32(final.X-box.X) > 1 {
		t.Fatalf("the next Tab ends at x=%v, want the closed Tab's place %v", final.X, box.X)
	}
}

// TestTabStripCloseAfterCancelledRunWarpPaintsTheExit cancels a running strip
// reorder with a row change that records no strip event, then closes the
// current Tab on the remounted warp build's very next frame. The close copy
// must animate: the cancellation warp has to record the TabClose exit too.
func TestTabStripCloseAfterCancelledRunWarpPaintsTheExit(t *testing.T) {
	setting := tabStripPreferences()
	g, tt, reveal, ids := stripWarpWindow(t, setting, 4, 1600, 480, nil)
	viewport := stripWarpViewport(t, tt)

	if !g.state.MoveTab(ids[0], ids[3], true) {
		t.Fatalf("MoveTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabReorder)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
		t.Fatalf("the reorder did not start a run: %+v", reveal.strip.run)
	}
	tt.Advance(30 * time.Millisecond)
	if reveal.strip.run == nil {
		t.Fatalf("the reorder ended before the cancellation")
	}
	// A rename records no strip event; the row-consistency rule ends the run
	// and warps on the next build.
	if !g.state.RenameTab(0, "cancelled-reorder") {
		t.Fatalf("RenameTab failed")
	}
	generation := reveal.strip.generation
	stepStripBuild(t, tt, reveal)
	if reveal.strip.run != nil || !reveal.strip.warp {
		t.Fatalf("the renamed row left run=%+v warp=%v, want the run cancelled with a warp",
			reveal.strip.run, reveal.strip.warp)
	}
	if reveal.strip.generation == generation {
		t.Fatalf("the cancelled run did not remount the row; the case needs the warp's fresh generation")
	}
	if !reveal.strip.exit {
		t.Fatalf("the cancellation warp did not arm the TabClose exit record")
	}

	active := g.state.ActiveTab()
	closed := followTabID(t, g.state, active)
	box := stripTravel(t, tt, closed)
	bar := stripBarFill(tt, viewport, box)
	preInk := tabInkPixels(tt, box, bar)
	if preInk < 60 {
		t.Fatalf("the current Tab has only %d ink pixels before the close; the case cannot see its copy", preInk)
	}
	if !g.state.CloseTab(active) {
		t.Fatalf("CloseTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose || reveal.strip.warp {
		t.Fatalf("the close after the cancelled run = run %+v warp=%v, want a TabClose run",
			reveal.strip.run, reveal.strip.warp)
	}
	event := tt.Image()
	if eventInk := tabInkPixels(tt, box, bar); eventInk < preInk/2 {
		t.Fatalf("the close after the cancelled run paints %d of the box's %d ink pixels on its event frame",
			eventInk, preInk)
	}
	tt.Advance(180*time.Millisecond + 17*time.Millisecond)
	tt.Advance(400 * time.Millisecond)
	final := tt.Image()
	// The strip fixture's Tabs share one title, so a survivor can refill the
	// closed box pixel-for-pixel; measure the whole strip, where the survivor's
	// old place is empty once the layout settles.
	if got := differingStripPixels(final, event, viewport); got == 0 {
		t.Fatalf("the strip did not change from the cancelled-run close's event frame after its run")
	}
	if _, ok := tt.Find(tabStripBoxLabel(closed)); ok {
		t.Fatalf("the closed Tab is still labeled after its run")
	}
	tt.Advance(400 * time.Millisecond)
	if got := differingStripPixels(tt.Image(), final, viewport); got != 0 {
		t.Fatalf("the strip moved %d pixels after the cancelled-run close settled", got)
	}
}

// TestTabStripCloseAfterOpenWarpPaintsTheExit opens a Tab on a remounted row
// whose empty content box makes the open's prediction warp, then closes that
// same Tab on the next build. The open warp records the close exit, so the
// closed Tab collapses over the strip's duration instead of popping out.
func TestTabStripCloseAfterOpenWarpPaintsTheExit(t *testing.T) {
	setting := tabStripPreferences()
	g, tt, reveal, ids := stripWarpWindow(t, setting, 2, 1600, 480,
		func(t *testing.T, g *followGraph, _ []model.TabID) {
			if !g.state.RenameTab(0, "a") || !g.state.RenameTab(1, "b") {
				t.Fatalf("RenameTab failed")
			}
		})
	viewport := stripWarpViewport(t, tt)

	if !g.state.MoveTab(ids[0], ids[1], true) {
		t.Fatalf("MoveTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabReorder)
	g.state.SelectTab(1)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.generation == 0 {
		t.Fatalf("the switch did not remount the row; the case needs the empty generation")
	}
	if reveal.boxes.Viewport.W <= 0 || reveal.boxes.Content.W > 0 {
		t.Fatalf("the remounted row's boxes = %+v; the case needs a usable viewport and an empty content",
			reveal.boxes)
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	opened := followTabID(t, g.state, 2)
	stepStripWarp(t, g.state, tt, reveal, motion.TabOpen)
	if reveal.strip.run != nil || !reveal.strip.warp {
		t.Fatalf("the remounted row's open = run %+v warp=%v, want a warp", reveal.strip.run, reveal.strip.warp)
	}
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the widened fixture overflows (MaxX=%v); the close case needs a fitting row", reveal.scroll.MaxX)
	}
	if !reveal.strip.exit {
		t.Fatalf("the open warp did not arm the TabClose exit record")
	}

	box := stripTravel(t, tt, opened)
	bar := stripBarFill(tt, viewport, box)
	preInk := tabInkPixels(tt, box, bar)
	if preInk < 60 {
		t.Fatalf("the opened Tab has only %d ink pixels before the close; the case cannot see its copy", preInk)
	}
	if !g.state.CloseTab(2) {
		t.Fatalf("CloseTab failed")
	}
	stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose || reveal.strip.warp {
		t.Fatalf("the close after the open warp = run %+v warp=%v, want a TabClose run",
			reveal.strip.run, reveal.strip.warp)
	}
	event := tt.Image()
	if eventInk := tabInkPixels(tt, box, bar); eventInk < preInk/2 {
		t.Fatalf("the close after the open warp paints %d of the box's %d ink pixels on its event frame", eventInk, preInk)
	}
	tt.Advance(180*time.Millisecond + 17*time.Millisecond)
	tt.Advance(400 * time.Millisecond)
	final := tt.Image()
	if got := differingStripPixels(final, event, box); got == 0 {
		t.Fatalf("the close copy after the open warp left the box unchanged")
	}
	tt.Advance(400 * time.Millisecond)
	if got := differingStripPixels(tt.Image(), final, box); got != 0 {
		t.Fatalf("the strip moved %d pixels after the open-warp close settled", got)
	}
}

// TestTabStripOverflowOpenWarpKeepsTheExitRecord drives the other open-warp
// trigger at the runtime seam: a reveal over an overflowing strip warps the
// open. Its build must still record the TabClose exit for a Tab that
// disappears on the next build, even though the overflow itself moves no
// layout on this frame.
func TestTabStripOverflowOpenWarpKeepsTheExitRecord(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 2)
	p, tt := newTabStripProbe(t, g.state)
	p.signals.revealActive, p.signals.overflow = true, true
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabOpen)
	if p.strip.run != nil || !p.strip.warp {
		t.Fatalf("the overflowing reveal left run=%+v warp=%v, want a warp", p.strip.run, p.strip.warp)
	}
	if !p.strip.exit {
		t.Fatalf("the overflow open warp did not arm the TabClose exit record")
	}
	transition, ok := p.strip.transition(setting, false)
	if !ok || transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
		t.Fatalf("the overflow open warp transition = %+v (%v), want an instant layout with the 180ms exit",
			transition, ok)
	}
	if tt.FramePending() {
		t.Fatalf("the overflow open warp's armed exit record asked for frames")
	}

	// The next build closes a Tab: the record above is what MyGo copies, so
	// the close must run rather than warp.
	p.signals.revealActive, p.signals.overflow = false, false
	if !g.state.CloseTab(0) {
		t.Fatalf("CloseTab failed")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabClose)
	if p.strip.run == nil || p.strip.run.kind != motion.TabClose || p.strip.warp {
		t.Fatalf("the close after the overflow open warp = run %+v warp=%v, want a TabClose run",
			p.strip.run, p.strip.warp)
	}
}

// TestTabStripWarpExitNegativeControls pins the warps that must not animate a
// close copy even when a later build disappears an item: a batch close after a
// warp, a close with its switch off, and Reduce Motion or a hidden window.
func TestTabStripWarpExitNegativeControls(t *testing.T) {
	message := locales.In("en")

	t.Run("batch close after a switch warp", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, reveal, _ := stripWarpWindow(t, setting, 5, 1600, 480, nil)
		viewport := stripWarpViewport(t, tt)
		g.state.SelectTab(0)
		stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
		if !reveal.strip.exit {
			t.Fatalf("the switch warp did not arm the close record")
		}
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("the batch close after a warp = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the batch close after a warp asked for %d strip frames", got)
		}
		if reveal.strip.generation == generation {
			t.Fatalf("the batch close did not drop the armed exits with a fresh generation")
		}
		event := tt.Image()
		tt.Advance(400 * time.Millisecond)
		if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
			t.Fatalf("the batch close after a warp painted %d moving pixels; the final layout must show at once", got)
		}
	})

	t.Run("batch close after a switch warp stays instant in the shell", func(t *testing.T) {
		setting := tabStripPreferences()
		setting.TabSwitch = false
		g, tt, wm, reveal := steppedStripWindow(t, setting, 5, 1600, 600, nil)
		viewport := stripWarpViewport(t, tt)
		g.state.SelectTab(0)
		stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
		if !reveal.canInstant() {
			t.Fatalf("the body is not idle; the case needs the window-wide instant path")
		}
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("the shell batch close after a warp = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the shell batch close after a warp asked for %d strip frames", got)
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the shell batch close remounted the row instead of resting it (%d to %d)",
				generation, reveal.strip.generation)
		}
		if wm.body.phase != bodyIdle {
			t.Fatalf("the shell batch close moved the body: phase %v", wm.body.phase)
		}
		event := tt.Image()
		tt.Advance(400 * time.Millisecond)
		if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
			t.Fatalf("the shell batch close painted %d moving pixels; the final layout must show at once", got)
		}
	})

	t.Run("TabClose off after a switch warp", func(t *testing.T) {
		setting := tabStripPreferences()
		setting.TabClose = false
		g, tt, reveal, _ := stripWarpWindow(t, setting, 4, 1600, 480, nil)
		viewport := stripWarpViewport(t, tt)
		g.state.SelectTab(0)
		stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
		g.state.SelectTab(3)
		stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
		if reveal.strip.exit {
			t.Fatalf("Tab Close off armed an exit on the switch warp")
		}
		if transition, ok := reveal.strip.transition(setting, false); !ok ||
			transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("Tab Close off warp transition = %+v (%v), want an instant layout with the warm 180ms exit",
				transition, ok)
		}
		frames := reveal.strip.animationFrames
		if !g.state.CloseTab(3) {
			t.Fatalf("CloseTab failed")
		}
		stepStripBuild(t, tt, reveal)
		stripEvent(t, g.state, motion.TabClose)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("Tab Close off after a warp = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("Tab Close off after a warp asked for %d strip frames", got)
		}
		event := tt.Image()
		tt.Advance(400 * time.Millisecond)
		if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
			t.Fatalf("Tab Close off after a warp painted %d moving pixels", got)
		}
		if _, ok := tt.Find(message.WorkTabsLabel()); !ok {
			t.Fatalf("the strip disappeared")
		}
	})

	t.Run("Reduce Motion after a switch warp", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, reveal, ids := stripWarpWindow(t, setting, 4, 1600, 480, nil)
		viewport := stripWarpViewport(t, tt)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		g.state.SelectTab(0)
		stepStripBuild(t, tt, reveal)
		stripEvent(t, g.state, motion.TabSwitch)
		g.state.SelectTab(3)
		stepStripBuild(t, tt, reveal)
		stripEvent(t, g.state, motion.TabSwitch)
		if reveal.strip.exit {
			t.Fatalf("Reduce Motion armed an exit on the switch warp")
		}
		if transition, ok := reveal.strip.transition(setting, false); !ok ||
			transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the Reduce Motion warp transition = %+v (%v), want an instant layout with the warm 180ms exit",
				transition, ok)
		}
		box := stripTravel(t, tt, ids[3])
		bar := stripBarFill(tt, viewport, box)
		frames := reveal.strip.animationFrames
		if !g.state.CloseTab(3) {
			t.Fatalf("CloseTab failed")
		}
		stepStripBuild(t, tt, reveal)
		stripEvent(t, g.state, motion.TabClose)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("Reduce Motion after a warp = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("Reduce Motion after a warp asked for %d strip frames", got)
		}
		if got := countTabFill(tt, box, bar); got != 0 {
			t.Fatalf("Reduce Motion after a warp painted %d copy pixels", got)
		}
		event := tt.Image()
		tt.Advance(400 * time.Millisecond)
		if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
			t.Fatalf("Reduce Motion after a warp painted %d moving pixels", got)
		}
	})

	t.Run("hidden window after a switch warp", func(t *testing.T) {
		// A hidden Tester holds its frames, so the runtime probe drives the
		// event stream; its transition record is the copy MyGo would leave on
		// the next visible frame.
		setting := tabStripPreferences()
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 4)
		p, tt := newTabStripProbe(t, g.state)
		tt.SetWindowHidden(true)

		g.state.SelectTab(0)
		tt.Frame()
		stripEvent(t, g.state, motion.TabSwitch)
		g.state.SelectTab(3)
		tt.Frame()
		stripEvent(t, g.state, motion.TabSwitch)
		if p.strip.exit {
			t.Fatalf("a hidden window armed an exit on the switch warp")
		}
		if transition, ok := p.strip.transition(setting, false); !ok ||
			transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the hidden window's warp transition = %+v (%v), want an instant layout with the warm 180ms exit",
				transition, ok)
		}
		frames := p.strip.animationFrames
		if !g.state.CloseTab(3) {
			t.Fatalf("CloseTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabClose)
		if p.strip.run != nil || p.strip.animate || !p.strip.warp {
			t.Fatalf("a hidden window after a warp = run %+v animate=%v warp=%v, want a warp alone",
				p.strip.run, p.strip.animate, p.strip.warp)
		}
		if got := p.strip.animationFrames - frames; got != 0 {
			t.Fatalf("a hidden window after a warp asked for %d strip frames", got)
		}
	})
}

// TestFollowBarCloseAfterSwitchWarpPaintsTheExit drives the mini strip through
// the same sequence. Follow's Position-only transition has its own warp shape;
// it must record the positive exit too, so the closing mini Tab slides out
// instead of vanishing on the build after a switch.
func TestFollowBarCloseAfterSwitchWarpPaintsTheExit(t *testing.T) {
	graph := newFollowGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.waitForSurfaces(t, 3)
	titles := []string{"follow-warp-a", "follow-warp-b", "follow-warp-c"}
	tt := followStripFrame(t, graph, titles...)

	graph.state.SelectTab(0)
	tt.Frame()
	stripEvent(t, graph.state, motion.TabSwitch)
	graph.state.SelectTab(2)
	tt.Frame()
	stripEvent(t, graph.state, motion.TabSwitch)

	box := followStripRect(t, tt, titles[2])
	active := followFill(tt, box)
	idle := followFill(tt, followStripRect(t, tt, titles[0]))
	if active == idle {
		t.Fatalf("the active and idle mini fills are both %v; the theme cannot show a selection", active)
	}
	before := tt.Image()

	if !graph.state.CloseTab(2) {
		t.Fatalf("CloseTab failed")
	}
	tt.Frame()
	stripEvent(t, graph.state, motion.TabClose)
	if got := followFill(tt, box); got != active {
		t.Fatalf("the closing mini Tab's copy = %v on the build after a switch warp, want the selected %v", got, active)
	}
	if diff := differingStripPixels(tt.Image(), before, box); diff < 0 {
		t.Fatalf("the mini strip snapshots differ in size")
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if got := followFill(tt, box); got == active {
		t.Fatalf("the closing mini Tab's copy still paints %v after its run", got)
	}
}

// TestTabStripWarpExitScalesWithSpeed pins the exit record's duration at the
// slow and fast tiers after a switch warp (270 ms and 108 ms), and proves the
// live copy lasts those durations.
func TestTabStripWarpExitScalesWithSpeed(t *testing.T) {
	cases := []struct {
		name  string
		speed domainpreferences.MotionSpeed
		want  time.Duration
	}{
		{"slow", domainpreferences.MotionSlow, 270 * time.Millisecond},
		{"fast", domainpreferences.MotionFast, 108 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := tabStripPreferences()
			setting.Speed = tc.speed
			g, tt, reveal, ids := stripWarpWindow(t, setting, 3, 1600, 480, nil)
			viewport := stripWarpViewport(t, tt)
			g.state.SelectTab(0)
			stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
			g.state.SelectTab(2)
			stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
			if transition, ok := reveal.strip.transition(setting, false); !ok ||
				transition.Duration != -1 || transition.ExitDuration != tc.want {
				t.Fatalf("the warp exit record = %+v (%v), want an instant layout with a %s exit", transition, ok, tc.want)
			}

			box := stripTravel(t, tt, ids[2])
			bar := stripBarFill(tt, viewport, box)
			preInk := tabInkPixels(tt, box, bar)
			if preInk < 60 {
				t.Fatalf("the %s current Tab has only %d ink pixels before the close", tc.name, preInk)
			}
			if !g.state.CloseTab(2) {
				t.Fatalf("CloseTab failed")
			}
			stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
			if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose ||
				reveal.strip.run.duration != tc.want {
				t.Fatalf("the %s close run = %+v, want %s", tc.name, reveal.strip.run, tc.want)
			}
			start := tabInkPixels(tt, box, bar)
			if start < preInk/2 {
				t.Fatalf("the %s close paints %d of the box's %d ink pixels on its event frame: the exit copy is missing",
					tc.name, start, preInk)
			}
			tt.Advance(tc.want / 2)
			mid := tabInkPixels(tt, box, bar)
			if mid <= 0 || mid >= start {
				t.Fatalf("the %s exit copy paints %d pixels halfway through %s, want fewer than %d and more than 0",
					tc.name, mid, tc.want, start)
			}
			tt.Advance(tc.want/2 + 17*time.Millisecond)
			final := tabInkPixels(tt, box, bar)
			if final >= mid {
				t.Fatalf("the %s exit copy still paints %d pixels after %s, want fewer than halfway's %d",
					tc.name, final, tc.want, mid)
			}
			settled := tt.Image()
			tt.Advance(400 * time.Millisecond)
			if got := differingStripPixels(tt.Image(), settled, box); got != 0 {
				t.Fatalf("the %s strip moved %d pixels after its exit settled", tc.name, got)
			}
		})
	}
}
