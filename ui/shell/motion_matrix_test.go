package shell

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// The matrix tests close ticket 21b's toggle and concurrency rows over the
// real views. Earlier tickets own each feature; these tests exercise the
// combinations and the degradation paths, and every one drives an offscreen
// ui.Tester frame.

// matrixAllOn is the ticket 08 default: every event switch on, the master on,
// and the standard speed tier. Each case turns off only what it observes.
func matrixAllOn() domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = true
	setting.PaneOpen = true
	setting.PaneClose = true
	setting.PaneReorder = true
	setting.TabOpen = true
	setting.TabClose = true
	setting.TabSwitch = true
	setting.TabReorder = true
	setting.Speed = domainpreferences.MotionStandard
	return setting
}

// showSettledTab selects a Tab and finishes the whole-body switch, so the
// view's recorded geometry is that Tab's settled layout.
func showSettledTab(t *testing.T, tt *ui.Tester, wm *workspacePaneMotion, s *state.State, index int) {
	t.Helper()
	s.SelectTab(index)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body != nil && wm.body.phase != bodyIdle {
		t.Fatalf("the switch to Tab %d did not settle: phase %v", index, wm.body.phase)
	}
	if wm.run != nil {
		t.Fatalf("selecting Tab %d left a Pane run playing", index)
	}
}

// TestCrossTabPaneDragPlaysOnlyTheTargetOpen is ticket 08's drag row: a Pane
// dropped into another Tab activates the target and plays only the moved
// Pane's open at its final box. The source position is already off screen, so
// it gets no ghost, and the target's other Panes do not make room.
func TestCrossTabPaneDragPlaysOnlyTheTargetOpen(t *testing.T) {
	setting := matrixAllOn()
	setting.OpenStyle = domainpreferences.MotionPopin
	g := motionGraph(t, setting)
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	// Give the target Tab a split first: the moved Pane must enter without
	// making the target's other Pane part of the animation.
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight on the target Tab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 3)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 1)
	sourceTab := g.mustTabID(t, 0)
	targetTab := g.mustTabID(t, 1)
	sourceRoot, _, ok := g.state.TabTree(0)
	if !ok {
		t.Fatalf("no source Tab")
	}
	targetRoot, _, ok := g.state.TabTree(1)
	if !ok {
		t.Fatalf("no target Tab")
	}
	moved := motionPaneIDs(sourceRoot)[0]
	targetPanes := motionPaneIDs(targetRoot)
	if len(targetPanes) != 2 {
		t.Fatalf("the target Tab has %d Panes, want 2", len(targetPanes))
	}
	targetPane, otherTarget := targetPanes[0], targetPanes[1]
	movedSurface := surfaceForPane(t, g, 0, moved)
	targetSurface := surfaceForPane(t, g, 1, targetPane)
	otherSurface := surfaceForPane(t, g, 1, otherTarget)
	otherViewport := otherSurface.Viewport()
	showSettledTab(t, tt, wm, g.state, 0)
	pumpUntil(t, tt, "the source Pane's full grid", func() bool { return gridsSettled(movedSurface) })

	bodyFrames := wm.body.animationFrames
	for _, surface := range []*realTerminalSurface{movedSurface, targetSurface, otherSurface} {
		surface.ResetResizeLog()
	}

	if !g.state.MovePane(sourceTab, moved, targetTab, targetPane, domainworkspace.SideRight) {
		t.Fatalf("the cross-Tab move failed")
	}
	event := mustLatestEvent(t, g.state)
	if event.Kind != motion.PaneOpen || event.TabID != targetTab ||
		event.SourceTabID != sourceTab || event.PaneID != moved || event.TargetID != targetPane {
		t.Fatalf("the cross-Tab drag recorded %+v, want PaneOpen target %q source %q Pane %q beside %q",
			event, targetTab, sourceTab, moved, targetPane)
	}
	tt.Frame()
	if got := g.state.TabCount(); got != 1 {
		t.Fatalf("the source Tab still exists: %d Tabs", got)
	}
	run := wm.run
	if run == nil {
		t.Fatalf("the cross-Tab open did not play")
	}
	if run.kind != motion.PaneOpen || run.tabID != targetTab || run.target != moved || run.native {
		t.Fatalf("run = kind %v tab %q target %q native %v; want a target-Tab overlay open of %q",
			run.kind, run.tabID, run.target, run.native, moved)
	}
	if len(run.anim.Visuals) != 1 {
		t.Fatalf("the cross-Tab open has %d visuals, want only the moved Pane's entrance: %+v",
			len(run.anim.Visuals), run.anim.Visuals)
	}
	entryFinal, ok := run.final[moved]
	if !ok {
		t.Fatalf("the moved Pane has no final boxes: %+v", run.final)
	}
	visual := run.anim.Visuals[0]
	if visual.Kind != motion.Enter || visual.Pane != moved || visual.Style != setting.OpenStyle {
		t.Fatalf("the cross-Tab visual = %+v, want the moved Pane entering with the open style", visual)
	}
	if visual.Box != entryFinal.Column || visual.Content != entryFinal.Column || visual.To != entryFinal.Column {
		t.Fatalf("the cross-Tab visual boxes = box %+v content %+v to %+v, want the final column %+v",
			visual.Box, visual.Content, visual.To, entryFinal.Column)
	}
	if len(run.ghosts) != 0 {
		t.Fatalf("the cross-Tab open ghosted the source position: %+v", run.ghosts)
	}
	if _, ok := run.final[targetPane]; !ok {
		t.Fatalf("the target Pane is missing from the final layout")
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the cross-Tab open moved the whole body: phase %v frames %d -> %d",
			wm.body.phase, bodyFrames, wm.body.animationFrames)
	}
	if _, ok := tt.Find(bodyKey(t, g, 0)); !ok {
		t.Fatalf("the target Tab's body is not built")
	}
	if _, ok := tt.Find(bodySpec{kind: bodySurfaceTab, tabID: sourceTab}.key()); ok {
		t.Fatalf("the removed source Tab's body is still built")
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); !ok {
		t.Fatalf("the moved Pane has no entrance overlay")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(moved)); !ok {
		t.Fatalf("the moved Pane has no final-position mask")
	}
	if _, ok := tt.Find("pane-motion:" + string(targetPane)); ok {
		t.Fatalf("the target Pane moved; a cross-Tab open makes it no room")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(targetPane)); ok {
		t.Fatalf("the target Pane was masked; a cross-Tab open makes it no room")
	}
	for _, visual := range run.anim.Visuals {
		if visual.Pane == otherTarget {
			t.Fatalf("the cross-Tab open animated the target Tab's other Pane: %+v", visual)
		}
	}
	if final, ok := run.final[otherTarget]; !ok || !final.Valid() {
		t.Fatalf("the target Tab's other Pane has no final boxes: %+v", run.final)
	}
	if _, ok := tt.Find("pane-motion:" + string(otherTarget)); ok {
		t.Fatalf("the target Tab's other Pane got an overlay")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(otherTarget)); ok {
		t.Fatalf("the target Tab's other Pane got a mask")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
		t.Fatalf("the cross-Tab open drew a source-position ghost")
	}
	if got := len(movedSurface.ResizeLog()); got != 1 {
		t.Fatalf("the moved Pane delivered %d resizes at the change, want 1: %+v",
			got, movedSurface.ResizeLog())
	}
	if got := len(targetSurface.ResizeLog()); got != 1 {
		t.Fatalf("the target Pane delivered %d resizes at the change, want 1: %+v",
			got, targetSurface.ResizeLog())
	}
	// A Pane whose final box did not change gets no PTY resize at all; one
	// whose box did gets exactly one.
	otherResizes := 0
	if otherSurface.Viewport() != otherViewport {
		otherResizes = 1
	}
	if got := len(otherSurface.ResizeLog()); got != otherResizes {
		t.Fatalf("the target Tab's other Pane delivered %d resizes at the change, want %d: %+v",
			got, otherResizes, otherSurface.ResizeLog())
	}
	movedCalls, targetCalls := movedSurface.GridForCalls(), targetSurface.GridForCalls()
	otherCalls := otherSurface.GridForCalls()

	tt.Advance(80 * time.Millisecond)
	tt.Frame()
	if wm.run != run {
		t.Fatalf("the cross-Tab open was cancelled by an animation frame")
	}
	if got := movedSurface.GridForCalls() - movedCalls; got != 0 {
		t.Errorf("the moved Pane computed %d grids during the run, want none", got)
	}
	if got := targetSurface.GridForCalls() - targetCalls; got != 0 {
		t.Errorf("the target Pane computed %d grids during the run, want none", got)
	}
	if got := otherSurface.GridForCalls() - otherCalls; got != 0 {
		t.Errorf("the target Tab's other Pane computed %d grids during the run, want none", got)
	}
	if got := len(movedSurface.ResizeLog()); got != 1 {
		t.Errorf("the moved Pane delivered %d resizes during the run, want 1", got)
	}
	if got := len(targetSurface.ResizeLog()); got != 1 {
		t.Errorf("the target Pane delivered %d resizes during the run, want 1", got)
	}
	if got := len(otherSurface.ResizeLog()); got != otherResizes {
		t.Errorf("the target Tab's other Pane delivered %d resizes during the run, want %d", got, otherResizes)
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(targetPane)); ok {
		t.Fatalf("the target Pane became masked during the run")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(otherTarget)); ok {
		t.Fatalf("the target Tab's other Pane became masked during the run")
	}

	tt.Advance(220 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the cross-Tab open did not finish")
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
		t.Fatalf("the moved Pane's overlay outlived the run")
	}
	// The target Tab's other Pane may legitimately get no PTY resize at all,
	// so gridsSettled (which reads the transport log) is checked only for the
	// two Panes the drop affects. The other Pane's grid is compared to its
	// own final viewport directly below.
	pumpUntil(t, tt, "the cross-Tab final grids", func() bool {
		return gridsSettled(movedSurface, targetSurface)
	})
	for name, surface := range map[string]*realTerminalSurface{
		"moved":  movedSurface,
		"target": targetSurface,
	} {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Errorf("the %s Pane grid = %+v, want its final viewport's %+v", name, surface.Grid(), want)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("the %s Pane delivered %d resizes over the change, want exactly 1", name, got)
		}
	}
	want, ok := appview.GridFor(otherSurface.Viewport())
	if !ok || otherSurface.Grid() != want {
		t.Errorf("the target Tab's other Pane grid = %+v, want its final viewport's %+v",
			otherSurface.Grid(), want)
	}
	if got := len(otherSurface.ResizeLog()); got != otherResizes {
		t.Errorf("the target Tab's other Pane delivered %d resizes over the change, want %d",
			got, otherResizes)
	}
}

// TestCrossTabPaneDragObeysThePaneOpenSwitch checks the one-event-one-switch
// rule on the new route: with PaneOpen off, the drop still activates the
// target and lays the moved Pane out once, but plays nothing.
func TestCrossTabPaneDragObeysThePaneOpenSwitch(t *testing.T) {
	setting := matrixAllOn()
	setting.PaneOpen = false
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
	settlePane(t, tt, g, 1)
	sourceTab := g.mustTabID(t, 0)
	targetTab := g.mustTabID(t, 1)
	sourceRoot, _, _ := g.state.TabTree(0)
	targetRoot, _, _ := g.state.TabTree(1)
	moved := motionPaneIDs(sourceRoot)[0]
	targetPane := motionPaneIDs(targetRoot)[0]
	movedSurface := surfaceForPane(t, g, 0, moved)
	showSettledTab(t, tt, wm, g.state, 0)
	frames := wm.animationFrames
	bodyFrames := wm.body.animationFrames
	movedSurface.ResetResizeLog()

	if !g.state.MovePane(sourceTab, moved, targetTab, targetPane, domainworkspace.SideRight) {
		t.Fatalf("the cross-Tab move failed")
	}
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("PaneOpen off still played a run: %+v", wm.run)
	}
	if wm.animationFrames != frames || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the warped drag asked for frames: pane %d -> %d, body %d -> %d",
			frames, wm.animationFrames, bodyFrames, wm.body.animationFrames)
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
		t.Fatalf("PaneOpen off built an overlay")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
		t.Fatalf("PaneOpen off built a ghost")
	}
	pumpUntil(t, tt, "the warped drag's final grids", func() bool {
		return gridsSettled(movedSurface)
	})
	if got := len(movedSurface.ResizeLog()); got != 1 {
		t.Fatalf("the moved Pane delivered %d resizes at the warp, want 1", got)
	}
	if got := g.state.ActiveTab(); got != 0 {
		t.Fatalf("active Tab after the warped drag = %d, want the target at 0", got)
	}
}

// TestCrossTabPaneDragIntoMagnifiedTargetPlaysNothing drops a Pane onto a
// magnified Tab: it is inserted hidden behind the zoom and plays no open,
// while the target Tab still becomes active with its zoom intact.
func TestCrossTabPaneDragIntoMagnifiedTargetPlaysNothing(t *testing.T) {
	setting := matrixAllOn()
	g := motionGraph(t, setting)
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForRealSurfaces(t, g.state, 3)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 1)
	zoom, ok := g.state.FocusedPaneID(1)
	if !ok {
		t.Fatalf("the split Tab has no focused Pane")
	}
	if !g.state.SetMagnifiedPane(1, zoom) {
		t.Fatalf("SetMagnifiedPane(%q) failed", zoom)
	}
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	sourceTab := g.mustTabID(t, 0)
	targetTab := g.mustTabID(t, 1)
	sourceRoot, _, _ := g.state.TabTree(0)
	moved := motionPaneIDs(sourceRoot)[0]
	movedSurface := surfaceForPane(t, g, 0, moved)
	zoomSurface := surfaceForPane(t, g, 1, zoom)
	showSettledTab(t, tt, wm, g.state, 0)
	before := mustLatestEvent(t, g.state)
	warpBefore := g.state.BodyWarpSeq()
	frames := wm.animationFrames
	bodyFrames := wm.body.animationFrames
	for _, surface := range []*realTerminalSurface{movedSurface, zoomSurface} {
		surface.ResetResizeLog()
	}
	movedCalls, zoomCalls := movedSurface.GridForCalls(), zoomSurface.GridForCalls()

	if !g.state.MovePane(sourceTab, moved, targetTab, zoom, domainworkspace.SideRight) {
		t.Fatalf("the drop on the magnified Tab failed")
	}
	after := mustLatestEvent(t, g.state)
	if after.Seq != before.Seq {
		t.Fatalf("the drop on a magnified Tab recorded a new motion event: %+v", after)
	}
	if got := g.state.BodyWarpSeq(); got == warpBefore {
		t.Fatalf("the drop on a magnified Tab did not raise the body warp signal")
	}
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the hidden insert played a Pane run: %+v", wm.run)
	}
	if wm.animationFrames != frames || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the hidden insert asked for frames: pane %d -> %d, body %d -> %d",
			frames, wm.animationFrames, bodyFrames, wm.body.animationFrames)
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
		t.Fatalf("the hidden insert built an overlay")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
		t.Fatalf("the hidden insert built a ghost")
	}
	active := g.state.ActiveTab()
	if id, ok := g.state.TabID(active); !ok || id != targetTab {
		t.Fatalf("the magnified target is not active: %q", id)
	}
	if got := g.state.MagnifiedPaneID(active); got != zoom {
		t.Fatalf("the magnified target lost its zoom: %q, want %q", got, zoom)
	}
	if got := movedSurface.GridForCalls() - movedCalls; got != 0 {
		t.Errorf("the hidden Pane computed %d grids, want none", got)
	}
	if got := zoomSurface.GridForCalls() - zoomCalls; got != 0 {
		t.Errorf("the zoomed Pane computed %d grids on an unchanged box, want none", got)
	}
	if got := len(movedSurface.ResizeLog()); got != 0 {
		t.Errorf("the hidden Pane delivered %d resizes, want none", got)
	}
	if got := len(zoomSurface.ResizeLog()); got != 0 {
		t.Errorf("the zoomed Pane delivered %d resizes, want none", got)
	}
}

// TestStalePaneEventWarpsInsteadOfPlaying closes the target Pane's tree
// entries before the view renders. The state records no newer motion event
// (DetachPane and DetachTab are layout changes without motion), so the view
// must notice the event's Pane is gone and warp rather than animate a stale
// target.
func TestStalePaneEventWarpsInsteadOfPlaying(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		g := motionGraph(t, matrixAllOn())
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForRealSurfaces(t, g.state, 1)
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		surfaces := settlePane(t, tt, g, 0)
		before := mustLatestEvent(t, g.state)
		frames := wm.animationFrames
		surfaces[0].ResetResizeLog()

		if err := g.state.SplitRight(); err != nil {
			t.Fatalf("SplitRight: %v", err)
		}
		newPane, ok := g.state.FocusedPaneID(0)
		if !ok {
			t.Fatalf("the split Pane has no focus")
		}
		// The new Pane leaves the main window before the view draws the open.
		if !g.state.DetachPane(0, newPane) {
			t.Fatalf("DetachPane(%q) failed", newPane)
		}
		if event := mustLatestEvent(t, g.state); event.Kind != motion.PaneOpen ||
			event.PaneID != newPane || event.Seq == before.Seq {
			t.Fatalf("the stale event = %+v, want the detached Pane's open", event)
		}
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the view played an open of a Pane that left the tree: %+v", wm.run)
		}
		if wm.animationFrames != frames {
			t.Fatalf("the stale open asked for %d frames", wm.animationFrames-frames)
		}
		if _, ok := tt.Find("pane-motion:" + string(newPane)); ok {
			t.Fatalf("the stale open built an overlay")
		}
		if _, ok := tt.Find("pane-close-ghost:" + string(newPane)); ok {
			t.Fatalf("the stale open built a ghost")
		}
		if got := len(surfaces[0].ResizeLog()); got != 0 {
			t.Fatalf("the surviving Pane resized at the stale warp, want none: %+v",
				surfaces[0].ResizeLog())
		}
	})

	t.Run("reorder", func(t *testing.T) {
		scene := newReorderScene(t, matrixAllOn(), paneMotionMode{labels: true})
		before := mustLatestEvent(t, scene.graph.state)
		frames := scene.wm.animationFrames
		if !scene.graph.state.MovePane(scene.graph.mustTabID(t, 0), scene.ids[0],
			scene.graph.mustTabID(t, 0), scene.ids[1], domainworkspace.SideRight) {
			t.Fatalf("the same-Tab move failed")
		}
		if event := mustLatestEvent(t, scene.graph.state); event.Kind != motion.PaneReorder || event.Seq == before.Seq {
			t.Fatalf("the move recorded %+v, want a PaneReorder", event)
		}
		if !scene.graph.state.DetachPane(0, scene.ids[0]) {
			t.Fatalf("DetachPane(%q) failed", scene.ids[0])
		}
		tt := scene.tt
		tt.Frame()
		if scene.wm.run != nil {
			t.Fatalf("the view played a move of a Pane that left the tree: %+v", scene.wm.run)
		}
		if scene.wm.animationFrames != frames {
			t.Fatalf("the stale move asked for %d frames", scene.wm.animationFrames-frames)
		}
		if _, ok := tt.Find("pane-close-ghost:" + string(scene.ids[0])); ok {
			t.Fatalf("the stale move built an old-position ghost")
		}
		if _, ok := tt.Find("pane-motion:" + string(scene.ids[0])); ok {
			t.Fatalf("the stale move built a new-position overlay")
		}
	})

	t.Run("close", func(t *testing.T) {
		g := motionGraph(t, matrixAllOn())
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForRealSurfaces(t, g.state, 1)
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		settlePane(t, tt, g, 0)
		surfaces := startPaneOpen(t, tt, g)
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the split did not settle before the close")
		}
		survivor := motionPaneIDsMust(t, g)[0]
		closed, _ := g.state.FocusedPaneID(0)
		frames := wm.animationFrames
		for _, surface := range surfaces {
			surface.ResetResizeLog()
		}
		if !g.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane failed")
		}
		// The survivor leaves with the Tab before the view draws the close;
		// the close now names neither a live Pane nor a box to reveal.
		if !g.state.DetachPane(0, survivor) {
			t.Fatalf("DetachPane(%q) failed", survivor)
		}
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the view played a close with no target left: %+v", wm.run)
		}
		if wm.animationFrames != frames {
			t.Fatalf("the stale close asked for %d frames", wm.animationFrames-frames)
		}
		for _, id := range []model.PaneID{closed, survivor} {
			if _, ok := tt.Find("pane-motion:" + string(id)); ok {
				t.Fatalf("the stale close built Pane %q's overlay", id)
			}
			if _, ok := tt.Find("pane-close-ghost:" + string(id)); ok {
				t.Fatalf("the stale close built Pane %q's ghost", id)
			}
		}
	})

	t.Run("cross-tab open", func(t *testing.T) {
		g := motionGraph(t, matrixAllOn())
		for range 2 {
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
		}
		waitForRealSurfaces(t, g.state, 2)
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		settlePane(t, tt, g, 1)
		showSettledTab(t, tt, wm, g.state, 0)
		sourceTab := g.mustTabID(t, 0)
		targetTab := g.mustTabID(t, 1)
		sourceRoot, _, ok := g.state.TabTree(0)
		if !ok {
			t.Fatalf("no source Tab")
		}
		targetRoot, _, ok := g.state.TabTree(1)
		if !ok {
			t.Fatalf("no target Tab")
		}
		moved := motionPaneIDs(sourceRoot)[0]
		targetPane := motionPaneIDs(targetRoot)[0]
		frames := wm.animationFrames
		if !g.state.MovePane(sourceTab, moved, targetTab, targetPane, domainworkspace.SideRight) {
			t.Fatalf("the cross-Tab move failed")
		}
		// The moved Pane leaves the target Tab before the view draws its open;
		// the source Tab is already gone, so the drop names no live Pane.
		if !g.state.DetachPane(0, moved) {
			t.Fatalf("DetachPane(%q) failed", moved)
		}
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the view played an open of a Pane that left the target: %+v", wm.run)
		}
		if wm.animationFrames != frames {
			t.Fatalf("the stale cross-Tab open asked for %d frames", wm.animationFrames-frames)
		}
		if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
			t.Fatalf("the stale cross-Tab open built an overlay")
		}
		if _, ok := tt.Find("pane-motion-mask:" + string(moved)); ok {
			t.Fatalf("the stale cross-Tab open built a mask")
		}
		if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
			t.Fatalf("the stale cross-Tab open built a ghost")
		}
	})

	t.Run("cross-tab stale target", func(t *testing.T) {
		g := motionGraph(t, matrixAllOn())
		for range 2 {
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
		}
		// The target Tab is a split, so the drop names one target Pane the
		// move will replace.
		if err := g.state.SplitRight(); err != nil {
			t.Fatalf("SplitRight on the target Tab: %v", err)
		}
		waitForRealSurfaces(t, g.state, 3)
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		settlePane(t, tt, g, 1)
		showSettledTab(t, tt, wm, g.state, 0)
		sourceTab := g.mustTabID(t, 0)
		targetTab := g.mustTabID(t, 1)
		sourceRoot, _, ok := g.state.TabTree(0)
		if !ok {
			t.Fatalf("no source Tab")
		}
		targetRoot, _, ok := g.state.TabTree(1)
		if !ok {
			t.Fatalf("no target Tab")
		}
		moved := motionPaneIDs(sourceRoot)[0]
		targetPane := motionPaneIDs(targetRoot)[0]
		movedSurface := surfaceForPane(t, g, 0, moved)
		frames := wm.animationFrames
		if !g.state.MovePane(sourceTab, moved, targetTab, targetPane, domainworkspace.SideRight) {
			t.Fatalf("the cross-Tab move failed")
		}
		drop := mustLatestEvent(t, g.state)
		if drop.Kind != motion.PaneOpen || drop.PaneID != moved || drop.TargetID != targetPane {
			t.Fatalf("the cross-Tab move recorded %+v, want the open of %q beside %q",
				drop, moved, targetPane)
		}
		// The target Pane the drop landed beside leaves the target Tab before
		// the view draws the open. DetachPane records no motion event, so the
		// view still consumes the PaneOpen that names it.
		if !g.state.DetachPane(0, targetPane) {
			t.Fatalf("DetachPane(%q) failed", targetPane)
		}
		if event := mustLatestEvent(t, g.state); event.Seq != drop.Seq ||
			event.Kind != motion.PaneOpen || event.PaneID != moved || event.TargetID != targetPane {
			t.Fatalf("the stale event = %+v, want the cross-Tab open of %q beside %q",
				event, moved, targetPane)
		}
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the view played a cross-Tab open whose target Pane left: %+v", wm.run)
		}
		if wm.animationFrames != frames {
			t.Fatalf("the stale cross-Tab target asked for %d frames", wm.animationFrames-frames)
		}
		if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
			t.Fatalf("the stale cross-Tab target built an overlay")
		}
		if _, ok := tt.Find("pane-motion-mask:" + string(moved)); ok {
			t.Fatalf("the stale cross-Tab target built a mask")
		}
		if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
			t.Fatalf("the stale cross-Tab target built a ghost")
		}
		// The warped frame is the final layout: the target Tab holds the moved
		// Pane and the target split's other Pane, never the detached target.
		root, _, ok := g.state.TabTree(0)
		if !ok || root == nil {
			t.Fatalf("the target Tab left after the warp")
		}
		ids := motionPaneIDs(root)
		if len(ids) != 2 {
			t.Fatalf("the warped target Tab holds %v, want the moved Pane and the target's other Pane", ids)
		}
		if !domainworkspace.ContainsPane(root, moved) {
			t.Fatalf("the warped target Tab lost the moved Pane %q", moved)
		}
		if domainworkspace.ContainsPane(root, targetPane) {
			t.Fatalf("the detached target Pane %q is still in the target Tab", targetPane)
		}
		pumpUntil(t, tt, "the stale target's final grid", func() bool { return gridsSettled(movedSurface) })
		if want, ok := appview.GridFor(movedSurface.Viewport()); !ok || movedSurface.Grid() != want {
			t.Fatalf("the moved Pane grid = %+v, want its final viewport's %+v",
				movedSurface.Grid(), want)
		}
	})
}

// TestStaleTabEventWarpsInsteadOfPlaying is the Tab half of the stale-target
// row: a Tab switch recorded before the frame names a Tab that has already
// left the window. The body runtime consumes the sequence and builds the real
// active body; it starts no run toward the missing Tab and retains no ghost.
func TestStaleTabEventWarpsInsteadOfPlaying(t *testing.T) {
	g, tt, wm := tabBodyWindow(t, matrixAllOn())
	shown := g.mustTabID(t, 0)
	showSettledTab(t, tt, wm, g.state, 0)
	before := mustLatestEvent(t, g.state)
	frames := wm.body.animationFrames
	stale := g.mustTabID(t, 1)
	g.state.SelectTab(1)
	if event := mustLatestEvent(t, g.state); event.Kind != motion.TabSwitch ||
		event.TabID != stale || event.Seq == before.Seq {
		t.Fatalf("the stale event = %+v, want a new TabSwitch to %q", event, stale)
	}
	// The target leaves the main window before the view draws the switch.
	if !g.state.DetachTab(1) {
		t.Fatalf("DetachTab(%q) failed", stale)
	}
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the body played a switch to a Tab that left the window: phase %v", wm.body.phase)
	}
	if wm.body.animationFrames != frames {
		t.Fatalf("the stale switch asked for %d body frames", wm.body.animationFrames-frames)
	}
	if wm.run != nil {
		t.Fatalf("the stale switch started a Pane run")
	}
	if _, ok := tt.Find("work-body:" + string(stale)); ok {
		t.Fatalf("the stale switch built the missing Tab's body")
	}
	if _, ok := g.state.TakeTabGhost(stale); ok {
		t.Fatalf("the stale switch retained a ghost for the missing Tab")
	}
	active := g.state.ActiveTab()
	if id, ok := g.state.TabID(active); !ok || id != shown {
		t.Fatalf("the body after the stale switch shows %q, want the shown Tab %q", id, shown)
	}
	root, ok := tt.Find("work-body-root")
	body, bodyOK := tt.Find(bodyKey(t, g, active))
	if !ok || !bodyOK || !rectNearOK(body, root, 2) {
		t.Fatalf("the stale switch did not build the final body %q: %+v vs %+v",
			shown, body, root)
	}
}

// TestWindowResizeCancelsPaneMotion reflows the window mid-open. The resize
// is outside any motion event: the run ends on that frame, each Pane's own
// layout delivers its one resize, and later frames do no grid math.
func TestWindowResizeCancelsPaneMotion(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 2)
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("the split did not start a Pane run")
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if wm.run != run {
		t.Fatalf("the Pane run ended before the resize")
	}
	surfaces := paneSurfaces(t, g, 0)
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	frames := wm.animationFrames
	bodyFrames := wm.body.animationFrames
	moved, _ := g.state.FocusedPaneID(0)
	oldViewport := surfaces[1].Viewport()

	tt.SetSize(720, 500)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the run survived the resize: %+v", wm.run)
	}
	if wm.animationFrames != frames {
		t.Fatalf("the resize frame asked for %d animation frames", wm.animationFrames-frames)
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("the resize frame moved the body: phase %v frames %d -> %d",
			wm.body.phase, bodyFrames, wm.body.animationFrames)
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
		t.Fatalf("the resize frame kept the overlay")
	}
	pumpUntil(t, tt, "the resized final grids", func() bool { return gridsSettled(surfaces...) })
	for i, surface := range surfaces {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Fatalf("Pane %d grid after the resize = %+v, want its viewport's %+v", i+1, surface.Grid(), want)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %d delivered %d resizes at the resize, want 1: %+v",
				i+1, got, surface.ResizeLog())
		}
	}
	if surfaces[1].Viewport() == oldViewport {
		t.Fatalf("the new Pane kept its pre-resize viewport %+v", oldViewport)
	}
	calls := make([]int, len(surfaces))
	logs := make([]int, len(surfaces))
	for i, surface := range surfaces {
		calls[i], logs[i] = surface.GridForCalls(), len(surface.ResizeLog())
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	for i, surface := range surfaces {
		if got := surface.GridForCalls() - calls[i]; got != 0 {
			t.Errorf("Pane %d computed %d grids on post-resize frames, want none", i+1, got)
		}
		if got := len(surface.ResizeLog()) - logs[i]; got != 0 {
			t.Errorf("Pane %d delivered %d resizes on post-resize frames, want none", i+1, got)
		}
	}
}

// TestWindowResizeCancelsTabBodyMotion reflows the window mid-switch. The
// whole body warps to the incoming Tab and the run asks no further frame.
func TestWindowResizeCancelsTabBodyMotion(t *testing.T) {
	g, tt, wm := tabBodyWindow(t, matrixAllOn())
	tabA := bodyKey(t, g, 0)
	tabB := bodyKey(t, g, 1)
	g.state.SelectTab(0)
	tt.Frame()
	if wm.body.phase == bodyIdle {
		t.Fatalf("the switch did not start a body run")
	}
	frames := wm.body.animationFrames
	if wm.body.out.tabID == "" || !wm.body.out.inert {
		t.Fatalf("the switch is not painting the outgoing still body: %+v", wm.body.out)
	}
	surfacesA := paneSurfaces(t, g, 0)
	for _, surface := range surfacesA {
		surface.ResetResizeLog()
	}

	tt.SetSize(720, 500)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the body run survived the resize: phase %v", wm.body.phase)
	}
	if wm.body.animationFrames != frames {
		t.Fatalf("the resize frame asked for %d body frames", wm.body.animationFrames-frames)
	}
	if wm.run != nil {
		t.Fatalf("the resize started a Pane run")
	}
	if _, ok := tt.Find(tabA); !ok {
		t.Fatalf("the final body %q is not built", tabA)
	}
	if _, ok := tt.Find(tabB); ok {
		t.Fatalf("the outgoing body %q outlived the resize warp", tabB)
	}
	pumpUntil(t, tt, "the resized Tab body's grids", func() bool { return gridsSettled(surfacesA...) })
	for i, surface := range surfacesA {
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %d delivered %d resizes at the resize, want 1: %+v",
				i+1, got, surface.ResizeLog())
		}
	}
}

// TestTerminalOutputDuringMotionDoesNotCancelIt streams normal output and a
// Lens flow rebuild into an animating Pane. The run keeps its start and asks
// no grid math, the output is visible during it, and the final frame is the
// final layout with the latest output.
func TestTerminalOutputDuringMotionDoesNotCancelIt(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 2)
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("the split did not start a run")
	}
	surfaces := paneSurfaces(t, g, 0)
	surfaces[0].ResetResizeLog()
	surfaces[1].ResetResizeLog()
	calls := []int{surfaces[0].GridForCalls(), surfaces[1].GridForCalls()}

	// A prompt line and a Lens command/result pair arrive mid-run, exactly
	// the asynchronous output that rebuilds the terminal flow.
	surfaces[0].feed("\x1b]1337;NetcattyExec;powershell;Qzpcd29yaw;ZWNobyBvbmU\x07")
	surfaces[0].feed("\x1b]133;C\x07motion-output\r\n\x1b]133;D;0\x07")
	pumpUntil(t, tt, "the mid-run Lens output", func() bool { return tt.HasText("motion-output") })
	if wm.run != run {
		t.Fatalf("terminal output cancelled or restarted the run: %+v", wm.run)
	}
	if got := surfaces[0].GridForCalls() - calls[0]; got != 0 {
		t.Errorf("the output drove %d GridFor calls, want none", got)
	}
	if got := surfaces[1].GridForCalls() - calls[1]; got != 0 {
		t.Errorf("the output drove %d GridFor calls in the new Pane, want none", got)
	}
	if got := len(surfaces[0].ResizeLog()); got != 0 {
		t.Errorf("the output delivered %d resizes, want none", got)
	}
	if got := len(surfaces[1].ResizeLog()); got != 0 {
		t.Errorf("the new Pane delivered %d resizes during the run, want none", got)
	}

	tt.Advance(220 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the run did not finish after its duration")
	}
	if !tt.HasText("motion-output") {
		t.Fatalf("the mid-run output was lost by the final frame")
	}
	tt.Advance(40 * time.Millisecond)
	tt.Frame()
	if got := surfaces[0].GridForCalls() - calls[0]; got != 0 {
		t.Errorf("the frame after the run drove %d GridFor calls, want none", got)
	}
	if got := len(surfaces[0].ResizeLog()); got != 0 {
		t.Errorf("the frame after the run delivered %d resizes, want none", got)
	}
}

// TestSurfaceDeathMidCloseFallsBackToABlankGhost kills the retained surface
// while its close ghost is on screen. The ghost degrades to the blank last
// resort without cancelling the run, closing twice, or leaking a handle.
func TestSurfaceDeathMidCloseFallsBackToABlankGhost(t *testing.T) {
	const ghostText = "doomed-live-text"
	g := motionGraph(t, matrixAllOn())
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	splitAndSettle(t, tt, g, wm)
	closedID, _ := g.state.FocusedPaneID(0)
	closedSurface := surfaceForPane(t, g, 0, closedID)
	var survivor *realTerminalSurface
	for _, id := range motionPaneIDsMust(t, g) {
		if id != closedID {
			survivor = surfaceForPane(t, g, 0, id)
		}
	}
	closedSurface.feed(ghostText + "\r\n")
	pumpUntil(t, tt, "the closing Pane's content", func() bool { return tt.HasText(ghostText) })

	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane failed")
	}
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("the close did not start a run")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); !ok {
		t.Fatalf("the close built no ghost")
	}
	if !tt.HasText(ghostText) {
		t.Fatalf("the live ghost does not draw its terminal content")
	}
	tt.Advance(40 * time.Millisecond)
	tt.Frame()
	if wm.run != run {
		t.Fatalf("the ghost frame ended the run")
	}
	// The Surface dies mid-run: ViewInert now takes the blank route.
	closedSurface.dead = true
	tt.Frame()
	if wm.run != run {
		t.Fatalf("the dead surface cancelled the run")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); !ok {
		t.Fatalf("the blank ghost is not built")
	}
	if tt.HasText(ghostText) {
		t.Fatalf("the dead ghost still shows its old terminal content")
	}
	tt.Advance(220 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the close did not finish")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
		t.Fatalf("the blank ghost outlived the run")
	}
	if got := closedSurface.Closes(); got != 1 {
		t.Fatalf("the dead surface closed %d times, want exactly once", got)
	}
	tt.Advance(40 * time.Millisecond)
	tt.Frame()
	if got := closedSurface.Closes(); got != 1 {
		t.Fatalf("the dead surface closed again after the run: %d", got)
	}
	pumpUntil(t, tt, "the survivor's final grid", func() bool { return gridsSettled(survivor) })
	if _, ok := tt.Find("pane-motion:" + string(closedID)); ok {
		t.Fatalf("the dead Pane has a moving copy after the run")
	}
}

// TestTabClosePlaysWholeBodyWithPaneCloseOff keeps the Pane switch out of a
// Tab close: with PaneClose off and TabSwitch off, closing the last Pane still
// plays the whole-body exit and the replacement entrance under TabClose.
func TestTabClosePlaysWholeBodyWithPaneCloseOff(t *testing.T) {
	setting := matrixAllOn()
	setting.PaneClose = false
	setting.TabSwitch = false
	setting.CloseStyle = domainpreferences.MotionPopin
	setting.SwitchStyle = domainpreferences.MotionSwitchSlide
	g, tt, wm := tabBodyWindow(t, setting)
	tabA := bodyKey(t, g, 0)
	tabB := bodyKey(t, g, 1)
	closedTab := g.mustTabID(t, 1)
	closedPane, ok := g.state.FocusedPaneID(1)
	if !ok {
		t.Fatalf("the closing Tab has no focused Pane")
	}
	closing := paneSurfaces(t, g, 1)
	if len(closing) != 1 {
		t.Fatalf("the closing Tab has %d Panes, want 1", len(closing))
	}
	frames := wm.animationFrames
	bodyFrames := wm.body.animationFrames

	if !g.state.CloseFocusedPane() {
		t.Fatalf("closing the last Pane failed")
	}
	event := mustLatestEvent(t, g.state)
	if event.Kind != motion.TabClose || event.TabID != closedTab {
		t.Fatalf("the last-Pane close recorded %+v, want the TabClose of %q", event, closedTab)
	}
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("PaneClose off still started a Pane run: %+v", wm.run)
	}
	if wm.animationFrames != frames {
		t.Fatalf("the Tab close asked for %d Pane frames", wm.animationFrames-frames)
	}
	if wm.body.phase != bodyIn || wm.body.animationFrames == bodyFrames {
		t.Fatalf("the whole-body close did not play: phase %v frames %d -> %d",
			wm.body.phase, bodyFrames, wm.body.animationFrames)
	}
	if wm.body.event.Kind != motion.TabClose {
		t.Fatalf("the body run is playing %v, want TabClose", wm.body.event.Kind)
	}
	if wm.body.out.tabID != closedTab || wm.body.out.ghost == nil {
		t.Fatalf("the close leaves %+v, want the retained body of %q", wm.body.out, closedTab)
	}
	if !wm.body.enterSet {
		t.Fatalf("the replacement body has no entrance")
	}
	if wm.body.enterDuration == 0 || wm.body.exitDuration == 0 {
		t.Fatalf("the close lost its durations: %s/%s", wm.body.enterDuration, wm.body.exitDuration)
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedPane)); ok {
		t.Fatalf("the Tab close built a per-Pane ghost")
	}
	tt.Advance(80 * time.Millisecond)
	if _, ok := tt.Find(tabA); !ok {
		t.Fatalf("the replacement body %q is not entering", tabA)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.body.phase != bodyIdle {
		t.Fatalf("the Tab close did not finish: phase %v", wm.body.phase)
	}
	if _, ok := tt.Find(tabB); ok {
		t.Fatalf("the closed body %q outlived the run", tabB)
	}
	if got := closing[0].Closes(); got != 1 {
		t.Fatalf("the closed body's surface closed %d times, want once", got)
	}
}

// TestTabCloseOffLeavesPaneClosesOutOfTheBody closes a split Tab with
// TabClose off but PaneClose on: the Tab close is final on its frame and never
// fans out into per-Pane close runs.
func TestTabCloseOffLeavesPaneClosesOutOfTheBody(t *testing.T) {
	setting := matrixAllOn()
	setting.TabClose = false
	g, tt, wm := tabBodyWindow(t, setting)
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForRealSurfaces(t, g.state, 3)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the split run did not finish")
	}
	active := g.state.ActiveTab()
	if active != 1 {
		t.Fatalf("the split Tab is not active: %d", active)
	}
	closedRoot, _, ok := g.state.TabTree(active)
	if !ok {
		t.Fatalf("the split Tab is missing")
	}
	paneIDs := motionPaneIDs(closedRoot)
	if len(paneIDs) != 2 {
		t.Fatalf("the split Tab has %d Panes, want 2", len(paneIDs))
	}
	closedSurfaces := []*realTerminalSurface{
		surfaceForPane(t, g, active, paneIDs[0]),
		surfaceForPane(t, g, active, paneIDs[1]),
	}
	frames := wm.animationFrames
	bodyFrames := wm.body.animationFrames
	closedTab := g.mustTabID(t, active)

	if !g.state.CloseTab(active) {
		t.Fatalf("CloseTab(%d) failed", active)
	}
	event := mustLatestEvent(t, g.state)
	if event.Kind != motion.TabClose || event.TabID != closedTab {
		t.Fatalf("the close recorded %+v, want TabClose of %q", event, closedTab)
	}
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("TabClose off played a Pane run: %+v", wm.run)
	}
	if wm.animationFrames != frames {
		t.Fatalf("TabClose off asked for %d Pane frames", wm.animationFrames-frames)
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("TabClose off moved the body: phase %v frames %d -> %d",
			wm.body.phase, bodyFrames, wm.body.animationFrames)
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(paneIDs[0])); ok {
		t.Fatalf("the Tab close built a per-Pane ghost")
	}
	for i, surface := range closedSurfaces {
		if got := surface.Closes(); got != 1 {
			t.Fatalf("the closed Tab's Pane %d surface closed %d times, want once", i+1, got)
		}
	}
	if _, ok := tt.Find(bodyKey(t, g, 0)); !ok {
		t.Fatalf("the replacement body is not built")
	}
}

// TestTabReorderLeavesPaneMotionAlone moves the strip while a Pane open is
// playing. A TabReorder owns no body or Pane visual, so the run keeps its
// start and never restarts.
func TestTabReorderLeavesPaneMotionAlone(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForRealSurfaces(t, g.state, 2)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 1)
	tabA := g.mustTabID(t, 0)
	tabB := g.mustTabID(t, 1)
	showSettledTab(t, tt, wm, g.state, 0)
	pumpUntil(t, tt, "the source Pane's grid", func() bool { return gridsSettled(paneSurfaces(t, g, 0)...) })
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 3)
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("the split did not start a run")
	}
	bodyFrames := wm.body.animationFrames
	frames := wm.animationFrames

	if !g.state.MoveTab(tabA, tabB, true) {
		t.Fatalf("MoveTab failed")
	}
	event := mustLatestEvent(t, g.state)
	if event.Kind != motion.TabReorder {
		t.Fatalf("MoveTab recorded %+v, want TabReorder", event)
	}
	tt.Frame()
	if wm.run != run {
		t.Fatalf("a TabReorder cancelled or restarted the Pane run: %+v", wm.run)
	}
	if wm.run.seq != run.seq || wm.run.anim.Started != run.anim.Started {
		t.Fatalf("the Pane run restarted: seq %d started %v, want %d/%v",
			wm.run.seq, wm.run.anim.Started, run.seq, run.anim.Started)
	}
	if wm.animationFrames <= frames {
		t.Fatalf("the Pane run asked for no frame after the reorder")
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFrames {
		t.Fatalf("a TabReorder moved the body: phase %v frames %d -> %d",
			wm.body.phase, bodyFrames, wm.body.animationFrames)
	}
	tt.Advance(220 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the Pane run did not finish after the reorder")
	}
}

// TestTopdownViewWarpsPaneMotionOnResize checks the resize row on a second
// view: Topdown's own Pane runtime owns its controller, so it cuts its own
// run and lets each Pane's layout deliver the one post-resize size.
func TestTopdownViewWarpsPaneMotionOnResize(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := startPaneOpen(t, tt, g)
	run := wm.run
	if run == nil {
		t.Fatalf("the Topdown split did not start a run")
	}
	frames := wm.animationFrames
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}

	tt.SetSize(720, 500)
	if wm.run != nil {
		t.Fatalf("the Topdown run survived the resize: %+v", wm.run)
	}
	if wm.animationFrames != frames {
		t.Fatalf("the Topdown resize frame asked for %d animation frames", wm.animationFrames-frames)
	}
	for _, id := range motionPaneIDsMust(t, g) {
		if _, ok := tt.Find("pane-motion:" + string(id)); ok {
			t.Fatalf("the Topdown resize kept Pane %q's overlay", id)
		}
	}
	pumpUntil(t, tt, "the Topdown resized grids", func() bool { return gridsSettled(surfaces...) })
	for i, surface := range surfaces {
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Topdown Pane %d delivered %d resizes at the resize, want 1: %+v",
				i+1, got, surface.ResizeLog())
		}
	}
}

// TestFollowViewPaneOpenResizesEachPaneOnce keeps the no-reflow invariant in
// Follow: a lone Follow Pane hides its header, so a split that appears must
// pin the old Pane at the body it will lay out. Otherwise the pin is one
// header off and the end of the run resizes the PTY a second time.
func TestFollowViewPaneOpenResizesEachPaneOnce(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := followWithPaneMotion(g.state)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	for _, surface := range settlePane(t, tt, g, 0) {
		surface.ResetResizeLog()
	}
	surfaces := startPaneOpen(t, tt, g)
	if wm.run == nil {
		t.Fatalf("the Follow split did not start a run")
	}
	// The change pins both Panes at their final grids: the old Pane shrinks
	// with exactly one resize and the new Pane was created at its final size.
	for i, surface := range surfaces {
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Follow Pane %d delivered %d resizes at the change, want 1: %+v",
				i+1, got, surface.ResizeLog())
		}
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the Follow open did not finish")
	}
	pumpUntil(t, tt, "the Follow split's final grids", func() bool { return gridsSettled(surfaces...) })
	for i, surface := range surfaces {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Errorf("Follow Pane %d grid = %+v, want its final viewport's %+v",
				i+1, surface.Grid(), want)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("Follow Pane %d delivered %d resizes over the open, want exactly 1: %+v",
				i+1, got, surface.ResizeLog())
		}
	}
}

// TestFollowViewWarpsPaneMotionOnResize checks the resize row in the Follow
// view too: it owns its controller and recorded boxes, and the shared runtime
// cuts the run on the new size with no further animation frames.
func TestFollowViewWarpsPaneMotionOnResize(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := followWithPaneMotion(g.state)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := startPaneOpen(t, tt, g)
	if wm.run == nil {
		t.Fatalf("the Follow split did not start a run")
	}
	frames := wm.animationFrames
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}

	tt.SetSize(720, 500)
	if wm.run != nil {
		t.Fatalf("the Follow run survived the resize: %+v", wm.run)
	}
	if wm.animationFrames != frames {
		t.Fatalf("the Follow resize frame asked for %d animation frames", wm.animationFrames-frames)
	}
	for _, id := range motionPaneIDsMust(t, g) {
		if _, ok := tt.Find("pane-motion:" + string(id)); ok {
			t.Fatalf("the Follow resize kept Pane %q's overlay", id)
		}
	}
	pumpUntil(t, tt, "the Follow resized grids", func() bool { return gridsSettled(surfaces...) })
	for i, surface := range surfaces {
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Follow Pane %d delivered %d resizes at the resize, want 1: %+v",
				i+1, got, surface.ResizeLog())
		}
	}
}

// TestFollowViewPlaysItsOwnCrossTabOpen keeps the drag row view-local: Follow
// owns its own controller and recorded boxes, so it plays the moved Pane's
// entrance itself even though the main window may be showing another route.
func TestFollowViewPlaysItsOwnCrossTabOpen(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForRealSurfaces(t, g.state, 2)
	view, wm := followWithPaneMotion(g.state)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	tt.Frame()
	settlePane(t, tt, g, 1)
	sourceTab := g.mustTabID(t, 0)
	targetTab := g.mustTabID(t, 1)
	sourceRoot, _, _ := g.state.TabTree(0)
	targetRoot, _, _ := g.state.TabTree(1)
	moved := motionPaneIDs(sourceRoot)[0]
	targetPane := motionPaneIDs(targetRoot)[0]
	g.state.SelectTab(0)
	tt.Frame()
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the switch started a Follow Pane run")
	}

	if !g.state.MovePane(sourceTab, moved, targetTab, targetPane, domainworkspace.SideRight) {
		t.Fatalf("the cross-Tab move failed")
	}
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("Follow did not play the cross-Tab open")
	}
	if run.kind != motion.PaneOpen || run.tabID != targetTab || run.target != moved {
		t.Fatalf("Follow's run = kind %v tab %q target %q, want the target Tab's open of %q",
			run.kind, run.tabID, run.target, moved)
	}
	if len(run.anim.Visuals) != 1 || run.anim.Visuals[0].Kind != motion.Enter ||
		run.anim.Visuals[0].Pane != moved {
		t.Fatalf("Follow's visuals = %+v, want only the moved Pane's entrance", run.anim.Visuals)
	}
	if len(run.ghosts) != 0 {
		t.Fatalf("Follow ghosted the source position: %+v", run.ghosts)
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); !ok {
		t.Fatalf("Follow built no entrance overlay")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
		t.Fatalf("Follow built a source-position ghost")
	}
}

// endingSessionFactory records each Session's fake transport, so a test can
// end one Session while the Tab's other Panes stay live.
type endingSessionFactory struct {
	mu         sync.Mutex
	transports []*fakeTransport
}

func (f *endingSessionFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	transport := newFakeTransport()
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

// transport returns the transport of the index-th Session opened, or nil.
func (f *endingSessionFactory) transport(index int) *fakeTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.transports) {
		return nil
	}
	return f.transports[index]
}

// newEndingSessionFollowGraph builds real Sessions whose ended Local Pane
// stays on screen (the Local exit setting is off), with the graph and the
// transport factory a collapse test needs.
func newEndingSessionFollowGraph(t *testing.T, setting domainpreferences.Motion) (*realTerminalGraph, *endingSessionFactory) {
	t.Helper()
	factory := &endingSessionFactory{}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	surfaces := &realTerminalSurfaceFactory{}
	st := state.NewState(
		state.WithSessions(service, surfaces.open),
		state.WithMotion(func() domainpreferences.Motion { return setting }),
		state.WithLocalAutoCloseOnExit(func() bool { return false }),
	)
	t.Cleanup(func() {
		for _, surface := range surfaces.all() {
			surface.Close()
		}
	})
	return &realTerminalGraph{state: st, factory: surfaces}, factory
}

// splitAndSettleFollow opens a second Pane in a Follow view and lets the open
// finish. It returns the surfaces in tree order.
func splitAndSettleFollow(t *testing.T, tt *ui.Tester, g *realTerminalGraph, wm *workspacePaneMotion) []*realTerminalSurface {
	t.Helper()
	surfaces := startPaneOpen(t, tt, g)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the Follow split did not settle")
	}
	pumpUntil(t, tt, "the split grids", func() bool { return gridsSettled(surfaces...) })
	return surfaces
}

// TestFollowViewLoneHeaderCollapseResizesOnce is the lone-status collapse
// row: when a split collapses to one Pane, the final body must keep the
// header row exactly when the lone Pane still draws one. A status header
// (Pending, or a focused Pane with an unresolved Session status) stays; a
// settled running Pane's header goes. Either way the pinned grid is the real
// final body, so the survivor gets one PTY resize, not two.
func TestFollowViewLoneHeaderCollapseResizesOnce(t *testing.T) {
	t.Run("status header stays", func(t *testing.T) {
		g, transports := newEndingSessionFollowGraph(t, matrixAllOn())
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForRealSurfaces(t, g.state, 1)
		view, wm := followWithPaneMotion(g.state)
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		settlePane(t, tt, g, 0)
		surfaces := splitAndSettleFollow(t, tt, g, wm)
		ids := motionPaneIDsMust(t, g)
		if len(ids) != 2 {
			t.Fatalf("the Tab has %d Panes, want 2", len(ids))
		}
		live, exitedPane := ids[0], ids[1]
		liveSurface, exitedSurface := surfaces[0], surfaces[1]
		// The split's Session ends on its own. Auto-close is off, so its Pane
		// stays as an exit screen with a status header.
		transport := transports.transport(1)
		if transport == nil {
			t.Fatalf("the split Session has no transport")
		}
		transport.end()
		pumpUntil(t, tt, "the sibling Session's exit", func() bool {
			snapshot, ok := g.state.TabSnapshot(0)
			return ok && snapshot.Status == appsession.StatusExited
		})
		if !g.state.FocusPaneID(0, live) {
			t.Fatalf("focusing the live Pane failed")
		}
		tt.Frame()
		liveSurface.ResetResizeLog()
		exitedSurface.ResetResizeLog()

		if !g.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane failed")
		}
		event := mustLatestEvent(t, g.state)
		if event.Kind != motion.PaneClose || event.PaneID != live || event.TargetID != exitedPane {
			t.Fatalf("the close recorded %+v, want PaneClose %q target %q",
				event, live, exitedPane)
		}
		tt.Frame()
		if wm.run == nil {
			t.Fatalf("the Follow close did not play")
		}
		if got := len(exitedSurface.ResizeLog()); got != 1 {
			t.Fatalf("the surviving status Pane delivered %d resizes at the change, want 1: %+v",
				got, exitedSurface.ResizeLog())
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the Follow close did not finish")
		}
		pumpUntil(t, tt, "the survivor's final grid", func() bool { return gridsSettled(exitedSurface) })
		if want, ok := appview.GridFor(exitedSurface.Viewport()); !ok || exitedSurface.Grid() != want {
			t.Fatalf("the survivor grid = %+v, want its final viewport's %+v",
				exitedSurface.Grid(), want)
		}
		if got := len(exitedSurface.ResizeLog()); got != 1 {
			t.Fatalf("the surviving status Pane delivered %d resizes over the close, want exactly 1: %+v",
				got, exitedSurface.ResizeLog())
		}
		if !tt.HasText(locales.In("en").SessionExitedWithCode(6)) {
			t.Fatalf("the survivor does not show its status header: %v", tt.Texts())
		}
	})

	t.Run("settled header hidden", func(t *testing.T) {
		g := motionGraph(t, matrixAllOn())
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForRealSurfaces(t, g.state, 1)
		view, wm := followWithPaneMotion(g.state)
		tt := ui.NewTester(view, 900, 600)
		tt.SetLocale("en")
		settlePane(t, tt, g, 0)
		surfaces := splitAndSettleFollow(t, tt, g, wm)
		ids := motionPaneIDsMust(t, g)
		if len(ids) != 2 {
			t.Fatalf("the Tab has %d Panes, want 2", len(ids))
		}
		closed, survivor := ids[1], ids[0]
		survivorSurface := surfaces[0]
		survivorSurface.ResetResizeLog()

		if !g.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane failed")
		}
		event := mustLatestEvent(t, g.state)
		if event.Kind != motion.PaneClose || event.PaneID != closed || event.TargetID != survivor {
			t.Fatalf("the close recorded %+v, want PaneClose %q target %q",
				event, closed, survivor)
		}
		tt.Frame()
		if wm.run == nil {
			t.Fatalf("the Follow close did not play")
		}
		if got := len(survivorSurface.ResizeLog()); got != 1 {
			t.Fatalf("the settled survivor delivered %d resizes at the change, want 1: %+v",
				got, survivorSurface.ResizeLog())
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the Follow close did not finish")
		}
		pumpUntil(t, tt, "the settled survivor's final grid", func() bool {
			return gridsSettled(survivorSurface)
		})
		if want, ok := appview.GridFor(survivorSurface.Viewport()); !ok || survivorSurface.Grid() != want {
			t.Fatalf("the settled survivor grid = %+v, want its final viewport's %+v",
				survivorSurface.Grid(), want)
		}
		if got := len(survivorSurface.ResizeLog()); got != 1 {
			t.Fatalf("the settled survivor delivered %d resizes over the close, want exactly 1: %+v",
				got, survivorSurface.ResizeLog())
		}
	})
}

// TestRestoredLayoutFirstFrameWarpsWithoutReplay draws a workspace whose
// Tabs, split and Pane opens all happened before the first frame, as a
// restore does. Every runtime warps: no Body or Pane event is replayed, no
// overlay is built, and no animation frame is requested.
func TestRestoredLayoutFirstFrameWarpsWithoutReplay(t *testing.T) {
	g := motionGraph(t, matrixAllOn())
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForRealSurfaces(t, g.state, 3)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	// The Tester's construction builds the first frame, so the state's events
	// are seen there; this Frame is the settled frame the assertions read.
	tt.Frame()
	surfaces := paneSurfaces(t, g, 1)
	if wm.run != nil {
		t.Fatalf("the first frame replayed a Pane event: %+v", wm.run)
	}
	if wm.animationFrames != 0 {
		t.Fatalf("the first frame asked for %d Pane frames", wm.animationFrames)
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != 0 {
		t.Fatalf("the first frame replayed a Tab event: phase %v frames %d",
			wm.body.phase, wm.body.animationFrames)
	}
	for _, id := range motionPaneIDsMust(t, g) {
		if _, ok := tt.Find("pane-motion:" + string(id)); ok {
			t.Fatalf("the first frame built Pane %q's overlay", id)
		}
		if _, ok := tt.Find("pane-close-ghost:" + string(id)); ok {
			t.Fatalf("the first frame built Pane %q's ghost", id)
		}
	}
	for i, surface := range surfaces {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Fatalf("restored Pane %d grid = %+v, want its final viewport's %+v",
				i+1, surface.Grid(), want)
		}
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the restored layout started a run on a later frame")
	}
	if wm.body.animationFrames != 0 {
		t.Fatalf("the restored layout asked for body frames on later frames")
	}
}
