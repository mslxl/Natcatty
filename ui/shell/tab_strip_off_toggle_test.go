package shell

// This file is ticket 20's off-toggle regression. MyGo copies a disappearing
// element from the transition its last build attached; a build with no
// transition is its notion of the element leaving, and a record with no exit
// makes that copy vanish. The strip therefore attaches a transition on every
// build, switches or not, and leaves a warm positive exit record for a switch
// that turns on in the same update as a close. The switch that gates the
// strip's exit can turn off with no state event (a live motion getter), and so
// can the master switch, Reduce Motion or a drag under the pointer. These
// tests toggle them after the strip has recorded and pin the pixels, frames
// and boxes: live Tabs stay, nothing ghosts, the control paths still warp, and
// the warm record is inert until a legitimate close uses it.

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

// liveStripWindow mounts the work strip alone over n Tabs with setting read
// live on every build: the state's getter dereferences setting, so a test can
// turn a switch off with no state event and the next build sees it. It
// mirrors stripWarpWindow's fixture (stepping view, Classic, labeled items).
func liveStripWindow(t *testing.T, setting *domainpreferences.Motion, n, width, height int) (*followGraph, *ui.Tester, *workTabReveal, []model.TabID) {
	t.Helper()
	g := newTabStripGraph(t, setting)
	ids := addStripTabs(t, g.state, n)
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), width, height)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	return g, tt, reveal, ids
}

// liveShellWindow is steppedStripWindow with the setting read live on every
// build, for the cases that need the real shell and the body runtime exposed.
func liveShellWindow(t *testing.T, setting *domainpreferences.Motion, n, width, height int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion, *workTabReveal) {
	t.Helper()
	g := newRealTerminalGraph(t, state.WithMotion(func() domainpreferences.Motion { return *setting }))
	for range n {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, n)
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(steppingStripView(view), width, height)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	return g, tt, wm, reveal
}

// stripBoxesFor reads every live item's box.
func stripBoxesFor(t *testing.T, tt *ui.Tester, ids []model.TabID) []ui.Rect {
	t.Helper()
	boxes := make([]ui.Rect, len(ids))
	for index, id := range ids {
		boxes[index] = stripTravel(t, tt, id)
	}
	return boxes
}

// assertStripBoxes fails when any live item moved from want.
func assertStripBoxes(t *testing.T, tt *ui.Tester, ids []model.TabID, want []ui.Rect, what string) {
	t.Helper()
	for index, id := range ids {
		if got := stripTravel(t, tt, id); !rectNearOK(got, want[index], 1) {
			t.Fatalf("%s moved live Tab %q from %+v to %+v", what, id, want[index], got)
		}
	}
}

// assertStripPixelsStill advances through the window a ghost copy would
// animate over and fails when any frame differs from want over box: the final
// layout matching is not enough, a copy collapsing from the live place must
// not paint at all.
func assertStripPixelsStill(t *testing.T, tt *ui.Tester, want *image.RGBA, box ui.Rect, what string) {
	t.Helper()
	for _, step := range []time.Duration{90 * time.Millisecond, 400 * time.Millisecond} {
		tt.Advance(step)
		got := differingStripPixels(tt.Image(), want, box)
		if got < 0 {
			t.Fatalf("%s: the frame sizes differ", what)
		}
		if got != 0 {
			t.Fatalf("%s painted %d moving pixels after +%s: a ghost copy of a live Tab", what, got, step)
		}
	}
}

// assertStripExitRecord checks the transition a build attaches: an instant
// layout (-1) with a warm positive close exit. Every build attaches one, even
// with the switches off (ticket 20's re-enable contract); the record paints
// nothing by itself, and the removal guard suppresses every disappearance
// except a legitimate animated close.
func assertStripExitRecord(t *testing.T, strip *tabStripMotion, setting domainpreferences.Motion, want time.Duration, what string) {
	t.Helper()
	transition, ok := strip.transition(setting, false)
	if !ok || transition.Duration != -1 || transition.ExitDuration != want {
		t.Fatalf("%s attached %+v (%v), want an instant -1 layout with a %s exit", what, transition, ok, want)
	}
	if transition.Colors || transition.Exit == nil {
		t.Fatalf("%s transition = %+v, want a collapsing exit and no Colors", what, transition)
	}
}

// TestTabStripOffToggleAfterSwitchWarpKeepsLiveTabs is the confirmed repro
// strip-only: three Classic Tabs at 1600×480, a switch warp that arms the
// close exit, then Tab Close off through the live getter with no event. The
// off build must attach the instant layout with a warm close exit: no ghost
// copy, no frames, no box movement. Turning Tab Close back on must animate a
// close again.
func TestTabStripOffToggleAfterSwitchWarpKeepsLiveTabs(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)
	assertStripExitRecord(t, &reveal.strip, setting, 180*time.Millisecond, "the settled idle build")
	viewport := stripWarpViewport(t, tt)

	g.state.SelectTab(0)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.run != nil || !reveal.strip.warp || !reveal.strip.exit {
		t.Fatalf("the switch warp left run=%+v warp=%v exit=%v, want a warp with the close record",
			reveal.strip.run, reveal.strip.warp, reveal.strip.exit)
	}

	setting.TabClose = false
	pre := tt.Image()
	boxes := stripBoxesFor(t, tt, ids)
	frames := reveal.strip.animationFrames
	stepStripBuild(t, tt, reveal)
	if reveal.strip.run != nil || reveal.strip.animate || reveal.strip.warp {
		t.Fatalf("the Tab Close off build = run %+v animate=%v warp=%v, want a resting build",
			reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
	}
	if got := reveal.strip.animationFrames - frames; got != 0 {
		t.Fatalf("the Tab Close off build asked for %d strip frames", got)
	}
	if transition, ok := reveal.strip.transition(setting, false); !ok ||
		transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
		t.Fatalf("the Tab Close off build attached %+v (%v), want the warm 180ms exit record", transition, ok)
	}
	if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
		t.Fatalf("the Tab Close off build painted %d pixels over the live strip: a ghost copy", got)
	}
	assertStripBoxes(t, tt, ids, boxes, "the Tab Close off build")
	assertStripPixelsStill(t, tt, pre, viewport, "the Tab Close off build")
	assertStripBoxes(t, tt, ids, boxes, "the Tab Close off build's later frames")

	// The switch coming back on re-arms the idle exit; a close on the next
	// event paints its 180 ms copy again.
	setting.TabClose = true
	stepStripBuild(t, tt, reveal)
	if transition, ok := reveal.strip.transition(setting, false); !ok ||
		transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
		t.Fatalf("Tab Close on again attached %+v (%v), want the idle 180ms exit record", transition, ok)
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
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose ||
		reveal.strip.run.duration != 180*time.Millisecond || reveal.strip.warp {
		t.Fatalf("the close after the switch came back = run %+v warp=%v, want a 180ms TabClose",
			reveal.strip.run, reveal.strip.warp)
	}
	if got := tabInkPixels(tt, box, bar); got < preInk/2 {
		t.Fatalf("the close after the switch came back paints %d of its %d ink pixels on the event frame",
			got, preInk)
	}
	tt.Advance(180*time.Millisecond + 17*time.Millisecond)
	tt.Advance(400 * time.Millisecond)
	if _, ok := tt.Find(tabStripBoxLabel(closed)); ok {
		t.Fatalf("the closed Tab is still labeled after its run")
	}
}

// TestTabStripOffToggleAsksNoFrames mounts without the stepping view, so the
// Tester can tell whether MyGo still asks for frames after the off build. A
// ghost copy would keep asking; the instant record asks nothing.
func TestTabStripOffToggleAsksNoFrames(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 3)
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(stripOnlyView(g.state, reveal), 1600, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	viewport := stripWarpViewport(t, tt)

	g.state.SelectTab(0)
	tt.Frame()
	if reveal.strip.run != nil || !reveal.strip.warp || !reveal.strip.exit {
		t.Fatalf("the switch warp left run=%+v warp=%v exit=%v, want a warp with the close record",
			reveal.strip.run, reveal.strip.warp, reveal.strip.exit)
	}
	setting.TabClose = false
	builds := reveal.strip.frames
	tt.Frame()
	if got := reveal.strip.frames - builds; got != 1 {
		t.Fatalf("the off frame built %d times, want one", got)
	}
	if tt.FramePending() {
		t.Fatalf("the off build still asks for frames: MyGo is animating a ghost copy of a live Tab")
	}
	pre := tt.Image()
	tt.Advance(90 * time.Millisecond)
	if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
		t.Fatalf("the off build painted %d moving pixels at +90ms", got)
	}
	tt.Advance(400 * time.Millisecond)
	if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
		t.Fatalf("the off build painted %d moving pixels at +490ms", got)
	}
}

// TestTabStripOffToggleKeepsLiveTabsInTheShell is the same repro over the
// real shell at 1600×600 with a live motion getter. Tab Switch is off, so the
// body stays idle and the assertion is the strip's alone plus an untouched
// body.
func TestTabStripOffToggleKeepsLiveTabsInTheShell(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	setting.TabSwitch = false
	g, tt, wm, reveal := liveShellWindow(t, &setting, 3, 1600, 600)
	assertStripExitRecord(t, &reveal.strip, setting, 180*time.Millisecond, "the settled shell idle build")
	viewport := stripWarpViewport(t, tt)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}

	g.state.SelectTab(0)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.run != nil || !reveal.strip.warp || !reveal.strip.exit {
		t.Fatalf("the switch warp left run=%+v warp=%v exit=%v, want a warp with the close record",
			reveal.strip.run, reveal.strip.warp, reveal.strip.exit)
	}
	if wm.body.phase != bodyIdle {
		t.Fatalf("the switched-off body moved: phase %v", wm.body.phase)
	}

	setting.TabClose = false
	pre := tt.Image()
	boxes := stripBoxesFor(t, tt, ids)
	stripFrames, bodyFrames := reveal.strip.animationFrames, wm.body.animationFrames
	stepStripBuild(t, tt, reveal)
	if reveal.strip.run != nil || reveal.strip.animate || reveal.strip.warp {
		t.Fatalf("the shell off build = run %+v animate=%v warp=%v, want a resting build",
			reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
	}
	if got := reveal.strip.animationFrames - stripFrames; got != 0 {
		t.Fatalf("the shell off build asked for %d strip frames", got)
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the shell off build moved the body: phase %v, +%d frames",
			wm.body.phase, wm.body.animationFrames-bodyFrames)
	}
	if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
		t.Fatalf("the shell off build painted %d pixels over the live strip: a ghost copy", got)
	}
	assertStripBoxes(t, tt, ids, boxes, "the shell off build")
	assertStripPixelsStill(t, tt, pre, viewport, "the shell off build")
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the shell's later frames moved the body: phase %v, +%d frames",
			wm.body.phase, wm.body.animationFrames-bodyFrames)
	}
	assertStripBoxes(t, tt, ids, boxes, "the shell off build's later frames")
}

// TestTabStripIdleOffToggleKeepsLiveTabs covers the pre-existing path the
// warp arm later exposed: an ordinary idle build, then Tab Close or the
// master switch off with no event. The master subtest repeats it in the
// shell, where the body must stay idle.
func TestTabStripIdleOffToggleKeepsLiveTabs(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })

	t.Run("Tab Close off after idle", func(t *testing.T) {
		setting := tabStripPreferences()
		_, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		pre := tt.Image()
		boxes := stripBoxesFor(t, tt, ids)
		frames := reveal.strip.animationFrames

		setting.TabClose = false
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate || reveal.strip.warp {
			t.Fatalf("the idle off build = run %+v animate=%v warp=%v, want a resting build",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the idle off build asked for %d strip frames", got)
		}
		if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
			t.Fatalf("the idle off build painted %d pixels over the live strip: a ghost copy", got)
		}
		assertStripBoxes(t, tt, ids, boxes, "the idle off build")
		assertStripPixelsStill(t, tt, pre, viewport, "the idle off build")
	})

	t.Run("master off after idle in the shell", func(t *testing.T) {
		setting := tabStripPreferences()
		setting.TabSwitch = false
		g, tt, wm, reveal := liveShellWindow(t, &setting, 3, 1600, 600)
		viewport := stripWarpViewport(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
		pre := tt.Image()
		boxes := stripBoxesFor(t, tt, ids)
		stripFrames, bodyFrames := reveal.strip.animationFrames, wm.body.animationFrames

		setting.Enabled = false
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate || reveal.strip.warp {
			t.Fatalf("the master off build = run %+v animate=%v warp=%v, want a resting build",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - stripFrames; got != 0 {
			t.Fatalf("the master off build asked for %d strip frames", got)
		}
		if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
			t.Fatalf("the master off build moved the body: phase %v, +%d frames",
				wm.body.phase, wm.body.animationFrames-bodyFrames)
		}
		if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
			t.Fatalf("the master off build painted %d pixels over the live strip: a ghost copy", got)
		}
		assertStripBoxes(t, tt, ids, boxes, "the master off build")
		assertStripPixelsStill(t, tt, pre, viewport, "the master off build")
	})
}

// TestTabStripMasterOffAfterWarpKeepsLiveTabs turns the master switch off on
// the build after a switch warp, strip-only: the warp's armed exit must not
// replay, and the live Tabs must stay.
func TestTabStripMasterOffAfterWarpKeepsLiveTabs(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)
	viewport := stripWarpViewport(t, tt)

	g.state.SelectTab(0)
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if !reveal.strip.exit {
		t.Fatalf("the switch warp did not arm the close exit")
	}
	setting.Enabled = false
	pre := tt.Image()
	boxes := stripBoxesFor(t, tt, ids)
	frames := reveal.strip.animationFrames
	stepStripBuild(t, tt, reveal)
	if reveal.strip.run != nil || reveal.strip.animate {
		t.Fatalf("the master off build = run %+v animate=%v, want neither", reveal.strip.run, reveal.strip.animate)
	}
	if got := reveal.strip.animationFrames - frames; got != 0 {
		t.Fatalf("the master off build asked for %d strip frames", got)
	}
	if transition, ok := reveal.strip.transition(setting, false); !ok ||
		transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
		t.Fatalf("the master off build attached %+v (%v), want the warm 180ms exit record", transition, ok)
	}
	if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
		t.Fatalf("the master off build painted %d pixels over the live strip: a ghost copy", got)
	}
	assertStripBoxes(t, tt, ids, boxes, "the master off build")
	assertStripPixelsStill(t, tt, pre, viewport, "the master off build")
}

// TestTabStripOffToggleAfterRunKeepsLiveTabs covers the run paths: with Tab
// Close off, an open or reorder run still animates, and its natural end must
// not leave live Tabs to ghost. A run cut mid-flight (the event switch off, a
// row change) already warps; its following off builds must stay still too.
func TestTabStripOffToggleAfterRunKeepsLiveTabs(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })

	t.Run("open natural end", func(t *testing.T) {
		setting := tabStripPreferences()
		setting.TabClose = false
		g, tt, reveal, _ := liveStripWindow(t, &setting, 2, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		stepStripWarp(t, g.state, tt, reveal, motion.TabOpen)
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabOpen {
			t.Fatalf("the open did not start a run: %+v", reveal.strip.run)
		}
		tt.Advance(200 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the open run outlived its 180ms: %+v", reveal.strip.run)
		}
		pre := tt.Image()
		frames := reveal.strip.animationFrames
		assertStripPixelsStill(t, tt, pre, viewport, "the open run's natural end")
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the open run's natural end asked for %d strip frames", got)
		}
	})

	t.Run("reorder natural end", func(t *testing.T) {
		setting := tabStripPreferences()
		setting.TabClose = false
		g, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		stepStripWarp(t, g.state, tt, reveal, motion.TabReorder)
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
			t.Fatalf("the reorder did not start a run: %+v", reveal.strip.run)
		}
		tt.Advance(200 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the reorder run outlived its 180ms: %+v", reveal.strip.run)
		}
		pre := tt.Image()
		frames := reveal.strip.animationFrames
		assertStripPixelsStill(t, tt, pre, viewport, "the reorder run's natural end")
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the reorder run's natural end asked for %d strip frames", got)
		}
	})

	t.Run("open run cut", func(t *testing.T) {
		setting := tabStripPreferences()
		setting.TabClose = false
		g, tt, reveal, _ := liveStripWindow(t, &setting, 2, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		stepStripWarp(t, g.state, tt, reveal, motion.TabOpen)
		tt.Advance(90 * time.Millisecond)
		if reveal.strip.run == nil {
			t.Fatalf("the open ended before the cut")
		}
		generation := reveal.strip.generation
		setting.TabOpen = false
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || !reveal.strip.warp || reveal.strip.generation == generation {
			t.Fatalf("the cut left run=%+v warp=%v generation %d, want a remounting warp",
				reveal.strip.run, reveal.strip.warp, reveal.strip.generation)
		}
		pre := tt.Image()
		frames := reveal.strip.animationFrames
		assertStripPixelsStill(t, tt, pre, viewport, "the open run's cut")
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the open run's cut or its later builds asked for %d strip frames", got)
		}
	})

	t.Run("close run cut", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, reveal, _ := liveStripWindow(t, &setting, 3, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		if !g.state.CloseTab(0) {
			t.Fatalf("CloseTab failed")
		}
		stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
		tt.Advance(90 * time.Millisecond)
		if reveal.strip.run == nil {
			t.Fatalf("the close ended before the cut")
		}
		generation := reveal.strip.generation
		setting.TabClose = false
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || !reveal.strip.warp || reveal.strip.generation == generation {
			t.Fatalf("the close cut left run=%+v warp=%v generation %d, want a remounting warp",
				reveal.strip.run, reveal.strip.warp, reveal.strip.generation)
		}
		pre := tt.Image()
		frames := reveal.strip.animationFrames
		assertStripPixelsStill(t, tt, pre, viewport, "the close run's cut")
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the close run's cut or its later builds asked for %d strip frames", got)
		}
	})
}

// TestTabStripDraggingOffKeepsLiveTabs covers the pointer path: a drag under
// the pointer keeps its layout instant, but the dragged item and its siblings
// still attach the instant layout with the warm close exit. The seam pins the
// dragging shape; a real press-and-move proves no live Tab ghosts and no boxes
// move.
func TestTabStripDraggingOffKeepsLiveTabs(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	_, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)

	setting.TabClose = false
	stepStripBuild(t, tt, reveal)
	for _, dragging := range []bool{false, true} {
		transition, ok := reveal.strip.transition(setting, dragging)
		if !ok || transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the off build while dragging=%v attached %+v (%v), want the warm 180ms exit record",
				dragging, transition, ok)
		}
	}

	pre := tt.Image()
	boxes := stripBoxesFor(t, tt, ids)
	frames := reveal.strip.animationFrames
	dragged, far := stripTravel(t, tt, ids[2]), stripTravel(t, tt, ids[0])
	x, y := dragged.X+dragged.W/2, dragged.Y+dragged.H/2
	tt.Press(x, y)
	tt.Move(x+12, y)
	if reveal.strip.run != nil {
		t.Fatalf("the drag started a strip run: %+v", reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - frames; got != 0 {
		t.Fatalf("the drag asked for %d strip frames", got)
	}
	assertStripBoxes(t, tt, ids, boxes, "the drag")
	if got := differingStripPixels(tt.Image(), pre, far); got != 0 {
		t.Fatalf("the drag painted %d pixels over a live Tab away from the pointer: a ghost copy", got)
	}
	tt.Release(x+12, y)
}

// TestFollowBarOffToggleKeepsLiveTabs is Follow's position-only half of the
// same rule. The mini strip has no runtime probe, so the seam pins the shape
// (Position alone, no Size, instant layout with a warm exit) and the mounted
// mini strip pins the pixels and frames: the master switch going off on the
// build after a switch must not slide live mini Tabs out.
func TestFollowBarOffToggleKeepsLiveTabs(t *testing.T) {
	setting := tabStripPreferences()
	off := setting
	off.Enabled = false
	positionOnly := tabStripMotion{positionOnly: true}
	transition, ok := positionOnly.transition(off, false)
	if !ok || transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
		t.Fatalf("Follow's off build attached %+v (%v), want the warm 180ms exit record", transition, ok)
	}
	if !transition.Position || transition.Size {
		t.Fatalf("Follow's off transition = Position:%v Size:%v, want Position alone", transition.Position, transition.Size)
	}
	if transition.Exit == nil || transition.Exit.XPercent >= 0 {
		t.Fatalf("Follow's off exit = %+v, want the position-only leftward exit", transition.Exit)
	}

	graph := newTabStripGraph(t, &setting)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.waitForSurfaces(t, 3)
	titles := []string{"follow-off-a", "follow-off-b", "follow-off-c"}
	tt := followStripFrame(t, graph, titles...)
	graph.state.SelectTab(0)
	tt.Frame()
	pre := tt.Image()
	boxes := []ui.Rect{
		followStripRect(t, tt, titles[0]),
		followStripRect(t, tt, titles[1]),
		followStripRect(t, tt, titles[2]),
	}

	setting.Enabled = false
	tt.Frame()
	if tt.FramePending() {
		t.Fatalf("the mini strip's off build still asks for frames: a ghost copy of a live Tab")
	}
	all := ui.Rect{X: 0, Y: 0, W: 600, H: 120}
	if got := differingStripPixels(tt.Image(), pre, all); got != 0 {
		t.Fatalf("the mini strip's off build painted %d pixels over the live strip", got)
	}
	for index, title := range titles {
		if got := followStripRect(t, tt, title); !rectNearOK(got, boxes[index], 1) {
			t.Fatalf("the mini strip's off build moved %q from %+v to %+v", title, boxes[index], got)
		}
	}
	assertStripPixelsStill(t, tt, pre, all, "the mini strip's off build")
	for index, title := range titles {
		if got := followStripRect(t, tt, title); !rectNearOK(got, boxes[index], 1) {
			t.Fatalf("the mini strip's later off frames moved %q from %+v to %+v", title, boxes[index], got)
		}
	}
}

// TestTabStripOffToggleControls pins the negative controls around the warm
// record: a close under the still-off switch paints no copy and warps at once,
// Reduce Motion still suppresses its copy after a build with a warm record,
// and a batch close still warps.
func TestTabStripOffToggleControls(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })

	t.Run("close with the switch still off paints no copy", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, reveal, _ := liveStripWindow(t, &setting, 3, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		setting.TabClose = false
		stepStripBuild(t, tt, reveal)
		if transition, ok := reveal.strip.transition(setting, false); !ok ||
			transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the off build attached %+v (%v), want the warm 180ms exit record", transition, ok)
		}

		active := g.state.ActiveTab()
		closed := followTabID(t, g.state, active)
		box := stripTravel(t, tt, closed)
		bar := stripBarFill(tt, viewport, box)
		frames := reveal.strip.animationFrames
		if !g.state.CloseTab(active) {
			t.Fatalf("CloseTab failed")
		}
		stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("the off close = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the off close asked for %d strip frames", got)
		}
		if got := countTabFill(tt, box, bar); got != 0 {
			t.Fatalf("the off close painted %d copy pixels", got)
		}
		pre := tt.Image()
		assertStripPixelsStill(t, tt, pre, viewport, "the off close")
	})

	t.Run("detach on the same build as the switch off", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		g.state.SelectTab(0)
		stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
		if !reveal.strip.exit {
			t.Fatalf("the switch warp did not arm the close exit")
		}

		// A detach records no motion event: the row shrinking is the only
		// signal, and the removed Tab is not built, so it cannot refresh its
		// record itself.
		setting.TabClose = false
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames
		if !g.state.DetachTab(1) {
			t.Fatalf("DetachTab failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("the same-build detach = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if reveal.strip.generation == generation {
			t.Fatalf("the same-build detach did not drop the armed record with a fresh generation")
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the same-build detach asked for %d strip frames", got)
		}
		if _, ok := tt.Find(tabStripBoxLabel(ids[1])); ok {
			t.Fatalf("the detached Tab is still labeled")
		}
		kept := []model.TabID{ids[0], ids[2]}
		boxes := stripBoxesFor(t, tt, kept)
		pre := tt.Image()
		assertStripPixelsStill(t, tt, pre, viewport, "the same-build detach")
		assertStripBoxes(t, tt, kept, boxes, "the same-build detach")
	})

	t.Run("Reduce Motion after a warm-record build", func(t *testing.T) {
		setting := tabStripPreferences()
		_, tt, reveal, ids := liveStripWindow(t, &setting, 3, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		assertStripExitRecord(t, &reveal.strip, setting, 180*time.Millisecond, "the settled idle build")
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		if transition, ok := reveal.strip.transition(setting, false); !ok ||
			transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("Reduce Motion attached %+v (%v), want the warm 180ms exit record the removal guard suppresses",
				transition, ok)
		}
		pre := tt.Image()
		boxes := stripBoxesFor(t, tt, ids)
		frames := reveal.strip.animationFrames
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate {
			t.Fatalf("Reduce Motion = run %+v animate=%v, want neither", reveal.strip.run, reveal.strip.animate)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("Reduce Motion asked for %d strip frames", got)
		}
		if got := differingStripPixels(tt.Image(), pre, viewport); got != 0 {
			t.Fatalf("Reduce Motion painted %d moving pixels", got)
		}
		assertStripBoxes(t, tt, ids, boxes, "Reduce Motion")
		assertStripPixelsStill(t, tt, pre, viewport, "Reduce Motion")
		if _, ok := tt.Find(locales.In("en").WorkTabsLabel()); !ok {
			t.Fatalf("the strip disappeared")
		}
	})

	t.Run("batch close still warps", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, reveal, _ := liveStripWindow(t, &setting, 5, 1600, 480)
		viewport := stripWarpViewport(t, tt)
		frames := reveal.strip.animationFrames
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
			t.Fatalf("the batch close = run %+v animate=%v warp=%v, want a warp alone",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the batch close asked for %d strip frames", got)
		}
		pre := tt.Image()
		assertStripPixelsStill(t, tt, pre, viewport, "the batch close")
	})
}
