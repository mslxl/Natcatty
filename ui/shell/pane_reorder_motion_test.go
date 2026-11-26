package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// reorderPreferences returns a motion setting with the three Pane parts the
// ticket's per-switch split exposes, popin on both curves, standard speed.
func reorderPreferences(paneClose, paneOpen, paneReorder bool) domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = true
	setting.PaneClose = paneClose
	setting.PaneOpen = paneOpen
	setting.PaneReorder = paneReorder
	setting.CloseStyle = domainpreferences.MotionPopin
	setting.OpenStyle = domainpreferences.MotionPopin
	setting.Speed = domainpreferences.MotionStandard
	return setting
}

// reorderScene is a settled two-Pane Tab with an asymmetric split, so moving
// one Pane changes both Panes' sizes and every part has a real effect. The
// boxes are the view's previous-frame records, exactly what the event starts
// from; the surfaces are in tree order.
type reorderScene struct {
	graph    *realTerminalGraph
	tt       *ui.Tester
	wm       *workspacePaneMotion
	ids      []model.PaneID
	surfaces map[model.PaneID]*realTerminalSurface
	before   map[model.PaneID]motion.PaneRects
}

// newReorderScene builds the settled split. It advances past the open run so
// no earlier animation bookkeeping can be mistaken for this ticket's.
func newReorderScene(t *testing.T, setting domainpreferences.Motion, mode paneMotionMode) *reorderScene {
	t.Helper()
	return newReorderSceneWithMotion(t, func() domainpreferences.Motion { return setting }, mode)
}

// newReorderSceneWithMotion is newReorderScene with a settings getter the
// test can change while a run is in flight.
func newReorderSceneWithMotion(t *testing.T, setting func() domainpreferences.Motion,
	mode paneMotionMode) *reorderScene {
	t.Helper()
	g := newRealTerminalGraph(t, state.WithMotion(setting))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, mode)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := splitAndSettle(t, tt, g, wm)
	root := setAsymmetricSplit(t, g, 0)
	waitForSplitLayout(t, tt, root, surfaces)
	pumpUntil(t, tt, "the asymmetric split's grids", func() bool { return gridsSettled(surfaces...) })
	tt.Frame()

	ids := motionPaneIDsMust(t, g)
	if len(ids) != 2 {
		t.Fatalf("the scene has %d Panes, want 2", len(ids))
	}
	byID := map[model.PaneID]*realTerminalSurface{}
	for _, id := range ids {
		byID[id] = surfaceForPane(t, g, 0, id)
	}
	before := map[model.PaneID]motion.PaneRects{}
	for _, id := range ids {
		rects, ok := wm.rects[id]
		if !ok || !rects.Valid() {
			t.Fatalf("Pane %s has no settled boxes: %+v", id, rects)
		}
		before[id] = rects
	}
	return &reorderScene{graph: g, tt: tt, wm: wm, ids: ids, surfaces: byID, before: before}
}

// resetChangeCounters clears the PTY resize stream so a test can count this
// change alone.
func (s *reorderScene) resetChangeCounters() {
	for _, surface := range s.surfaces {
		surface.ResetResizeLog()
	}
}

// gridCalls snapshots each surface's grid-computation count, so a test can
// separate the change frame's one pin from animation frames' zero.
func (s *reorderScene) gridCalls() map[model.PaneID]int {
	calls := map[model.PaneID]int{}
	for id, surface := range s.surfaces {
		calls[id] = surface.GridForCalls()
	}
	return calls
}

// bodyChanged reports that a Pane's final body differs in size from its
// pre-change body, which is exactly when the contract allows one resize.
func bodyChanged(before, after motion.PaneRects) bool {
	return !rectWithin(before.Body, after.Body, 0.5)
}

// findVisual returns the visual of a Pane with kind.
func findVisual(t *testing.T, run *paneMotionRun, id model.PaneID, kind motion.VisualKind) motion.Visual {
	t.Helper()
	if run == nil {
		t.Fatalf("no run is playing")
	}
	for _, v := range run.anim.Visuals {
		if v.Pane == id && v.Kind == kind {
			return v
		}
	}
	t.Fatalf("Pane %s has no %v visual in %+v", id, kind, run.anim.Visuals)
	return motion.Visual{}
}

// paneRegionExists reports that the frame drew a Pane at the given ordinal,
// which the split tree labels for tests and assistive technology.
func paneRegionExists(tt *ui.Tester, ordinal int) bool {
	_, ok := tt.Find(locales.In("en").PaneRegion(ordinal))
	return ok
}

// TestPaneReorderSameTabMovePlaysTheThreeParts is ticket 18's first
// acceptance: a same-Tab move draws the old position as a close-style ghost
// of the still-live Surface, the new position as an open-style overlay over
// the already-final real Pane, and the sibling making room with the exact
// spring. The final frame is the final layout at final grids, with one PTY
// resize per Pane whose size changed and zero grid math on animation frames.
func TestPaneReorderSameTabMovePlaysTheThreeParts(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	moved, target := scene.ids[1], scene.ids[0]
	tabID, _ := g.state.TabID(0)
	before := scene.before
	scene.resetChangeCounters()
	animationBefore := wm.animationFrames

	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
		t.Fatalf("MovePane in one Tab returned false")
	}
	tt.Frame()
	// The change frame itself may compute the pinned final grids once; no
	// frame after it may.
	changeCalls := scene.gridCalls()

	event, ok := g.state.MotionEvent()
	if !ok || event.Kind != motion.PaneReorder || event.PaneID != moved || event.TargetID != target {
		t.Fatalf("move event = %+v, want PaneReorder of %q beside %q", event, moved, target)
	}
	if event.IsZoom() {
		t.Fatalf("the move event carried zoom fields: %+v", event)
	}
	run := wm.run
	if run == nil || run.kind != motion.PaneReorder || run.zoom {
		t.Fatalf("the move run = %+v, want a non-zoom PaneReorder", run)
	}
	final := run.final
	if _, ok := final[moved]; !ok {
		t.Fatalf("the moved Pane has no final boxes")
	}
	if !bodyChanged(before[moved], final[moved]) || !bodyChanged(before[target], final[target]) {
		t.Fatalf("the scene's move did not change both sizes: before %+v/%+v final %+v/%+v",
			before[moved], before[target], final[moved], final[target])
	}

	exit := findVisual(t, run, moved, motion.Exit)
	if !rectNearOK(exit.From, before[moved].Column, 0.5) ||
		!rectNearOK(exit.Box, before[moved].Column, 0.5) {
		t.Fatalf("the old-position ghost = %+v, want the old column %+v", exit, before[moved].Column)
	}
	if exit.Duration != 150*time.Millisecond {
		t.Fatalf("the close ghost duration = %s, want 150ms", exit.Duration)
	}
	if exit.Style != domainpreferences.MotionPopin {
		t.Fatalf("the ghost style = %v, want the close style", exit.Style)
	}
	enter := findVisual(t, run, moved, motion.Enter)
	if !rectNearOK(enter.Box, final[moved].Column, 0.5) ||
		!rectNearOK(enter.To, final[moved].Column, 0.5) {
		t.Fatalf("the new-position overlay = %+v, want the final column %+v", enter, final[moved].Column)
	}
	if !rectNearOK(enter.Content, before[moved].Column, 0.5) {
		t.Fatalf("the overlay content area = %+v, want the old column %+v", enter.Content, before[moved].Column)
	}
	if enter.Duration != 200*time.Millisecond {
		t.Fatalf("the open overlay duration = %s, want 200ms", enter.Duration)
	}
	sibling := findVisual(t, run, target, motion.Move)
	if !rectNearOK(sibling.From, before[target].Column, 0.5) ||
		!rectNearOK(sibling.To, final[target].Column, 0.5) {
		t.Fatalf("the sibling visual = %+v, want %+v → %+v", sibling, before[target].Column, final[target].Column)
	}
	if sibling.Spring == nil || sibling.Duration <= 0 {
		t.Fatalf("the sibling is not on the reorder spring: %+v", sibling)
	}
	// The sibling shrinks in this scene, so the overlay carries it: MyGo's
	// Match + ScaleContent would grow its final grid back to the old box,
	// enlarging the terminal's text before it settled.
	if sibling.Native || len(run.nativePanes) != 0 {
		t.Fatalf("a shrinking sibling was carried natively: visual %+v nativePanes %+v",
			sibling, run.nativePanes)
	}
	if run.spring == nil || run.spring.Duration != 240*time.Millisecond || run.spring.Bounce != 0.1 {
		t.Fatalf("the run spring = %+v, want 240ms/bounce 0.1", run.spring)
	}

	if got := len(run.anim.Visuals); got != 3 {
		t.Fatalf("the move has %d visuals, want the ghost, the overlay and the sibling: %+v",
			got, run.anim.Visuals)
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(moved)); !ok {
		t.Fatalf("the old-position ghost is not drawn")
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); !ok {
		t.Fatalf("the new-position overlay is not drawn")
	}
	if mask, ok := tt.Find("pane-motion-mask:" + string(moved)); !ok {
		t.Fatalf("the real moved Pane is not masked")
	} else if !rectNearOK(mask, final[moved].Column, 0.5) {
		t.Fatalf("the moved Pane's mask = %+v, want its final column %+v", mask, final[moved].Column)
	}
	if _, ok := tt.Find("pane-motion:" + string(target)); !ok {
		t.Fatalf("the shrinking sibling is not copied by the overlay")
	}
	if mask, ok := tt.Find("pane-motion-mask:" + string(target)); !ok {
		t.Fatalf("the shrinking sibling is not masked")
	} else if !rectNearOK(mask, final[target].Column, 0.5) {
		t.Fatalf("the sibling's mask = %+v, want its final column %+v", mask, final[target].Column)
	}
	if got, want := len(scene.surfaces[moved].ResizeLog()), 1; got != want {
		t.Fatalf("the moved Pane delivered %d resizes, want %d: %+v",
			got, want, scene.surfaces[moved].ResizeLog())
	}
	if got, want := len(scene.surfaces[target].ResizeLog()), 1; got != want {
		t.Fatalf("the sibling delivered %d resizes, want %d: %+v",
			got, want, scene.surfaces[target].ResizeLog())
	}
	if wm.animationFrames == animationBefore {
		t.Fatalf("the move requested no animation frame")
	}
	if scene.surfaces[moved].Closes() != 0 {
		t.Fatalf("the move closed the moved Pane's Surface")
	}
	if got := len(motionPaneIDsMust(t, g)); got != 2 {
		t.Fatalf("the Tab has %d Panes after the move, want the same 2", got)
	}

	// Mid-run: every part is between its ends, every Pane still has its final
	// grid, and no frame computed one or delivered a resize.
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	now := tt.Now()
	exitRect, _ := run.anim.StateAt(exit, now)
	enterRect, _ := run.anim.StateAt(enter, now)
	if exitRect.W >= before[moved].Column.W || exitRect.W <= before[moved].Column.W*motion.PopinStartScale {
		t.Fatalf("the ghost width at mid-run = %v, want inside the popin range of %v",
			exitRect.W, before[moved].Column.W)
	}
	if enterRect.W >= final[moved].Column.W || enterRect.W <= final[moved].Column.W*motion.PopinStartScale {
		t.Fatalf("the overlay width at mid-run = %v, want inside the popin range of %v",
			enterRect.W, final[moved].Column.W)
	}
	if got, ok := tt.Find("pane-close-ghost:" + string(moved)); !ok {
		t.Fatalf("the ghost disappeared mid-run")
	} else if !rectNearOK(got, clipToWindow(exitRect), 0.75) {
		t.Fatalf("the ghost at mid-run = %+v, want %+v", got, clipToWindow(exitRect))
	}
	if got, ok := tt.Find("pane-motion:" + string(moved)); !ok {
		t.Fatalf("the overlay disappeared mid-run")
	} else if !rectNearOK(got, clipToWindow(enterRect), 0.75) {
		t.Fatalf("the overlay at mid-run = %+v, want %+v", got, clipToWindow(enterRect))
	}
	// The shrinking sibling's copy is laid out at its animated box, so its
	// final grid draws at its own cell size instead of scaling up to the old
	// box.
	siblingRect, _ := run.anim.StateAt(sibling, now)
	if got, ok := tt.Find("pane-motion:" + string(target)); !ok {
		t.Fatalf("the sibling overlay disappeared mid-run")
	} else if !rectNearOK(got, clipToWindow(siblingRect), 0.75) {
		t.Fatalf("the sibling overlay at mid-run = %+v, want %+v", got, clipToWindow(siblingRect))
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Fatalf("Pane %s (moved %s, target %s) computed %d grids during animation frames, want none",
				id, moved, target, got)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %s delivered %d resizes over the run, want its one change resize", id, got)
		}
	}

	// Final: the run is gone, the layout and grids are final, and nothing
	// resized again or reflowed.
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the move run did not finish")
	}
	for _, id := range scene.ids {
		if got, ok := wm.rects[id]; !ok || !rectNearOK(got.Column, final[id].Column, 0.5) {
			t.Fatalf("Pane %s final recorded column = %+v, want %+v", id, got.Column, final[id].Column)
		}
		surface := scene.surfaces[id]
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Fatalf("Pane %s final grid = %+v, want its viewport's %+v", id, surface.Grid(), want)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %s delivered %d resizes by the final frame, want 1", id, got)
		}
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
		t.Fatalf("the ghost is still drawn after the run")
	}
	if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
		t.Fatalf("the overlay is still drawn after the run")
	}
	if _, ok := tt.Find("pane-motion:" + string(target)); ok {
		t.Fatalf("the sibling overlay is still drawn after the run")
	}
}

// TestPaneReorderGatesEachPartWithItsOwnSwitch turns one switch off at a
// time: only that part warps, while the other two still play and the final
// frame is the final layout. This per-part split is unique to PaneReorder.
func TestPaneReorderGatesEachPartWithItsOwnSwitch(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		closeOn, openOn, reorderOn    bool
		wantGhost, wantEnter, wantSib bool
	}{
		{"all-on", true, true, true, true, true, true},
		{"pane-close-off", false, true, true, false, true, true},
		{"pane-open-off", true, false, true, true, false, true},
		{"pane-reorder-off", true, true, false, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setting := reorderPreferences(tc.closeOn, tc.openOn, tc.reorderOn)
			scene := newReorderScene(t, setting, paneMotionMode{labels: true})
			g, tt, wm := scene.graph, scene.tt, scene.wm
			moved, target := scene.ids[1], scene.ids[0]
			tabID, _ := g.state.TabID(0)
			scene.resetChangeCounters()

			if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
				t.Fatalf("MovePane returned false")
			}
			tt.Frame()
			run := wm.run
			if run == nil {
				t.Fatalf("the move did not start a run with the remaining parts on")
			}
			_, hasGhost := findVisualOK(run, moved, motion.Exit)
			_, hasEnter := findVisualOK(run, moved, motion.Enter)
			_, hasSibling := findVisualOK(run, target, motion.Move)
			if hasGhost != tc.wantGhost || hasEnter != tc.wantEnter || hasSibling != tc.wantSib {
				t.Fatalf("parts ghost/enter/sibling = %v/%v/%v, want %v/%v/%v (visuals %+v)",
					hasGhost, hasEnter, hasSibling, tc.wantGhost, tc.wantEnter, tc.wantSib,
					run.anim.Visuals)
			}
			if _, ok := run.ghosts[moved]; ok != tc.wantGhost {
				t.Fatalf("the run's old-position ghost presence = %v, want %v", ok, tc.wantGhost)
			}
			if len(run.nativePanes) != 0 {
				// The scene's sibling shrinks, so no reorder sibling may take
				// the native ScaleContent route.
				t.Fatalf("a shrinking sibling was carried natively: %+v", run.nativePanes)
			}
			if got, want := run.spring != nil, tc.wantSib; got != want {
				t.Fatalf("run spring presence = %v, want %v", got, want)
			}
			if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok != tc.wantGhost {
				t.Fatalf("drawn ghost presence = %v, want %v", ok, tc.wantGhost)
			}
			if _, ok := tt.Find("pane-motion:" + string(moved)); ok != tc.wantEnter {
				t.Fatalf("drawn overlay presence = %v, want %v", ok, tc.wantEnter)
			}
			if _, ok := tt.Find("pane-motion:" + string(target)); ok != tc.wantSib {
				t.Fatalf("drawn sibling overlay presence = %v, want %v", ok, tc.wantSib)
			}

			tt.Advance(2 * time.Second)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("the partial run did not finish")
			}
			for _, id := range scene.ids {
				end, ok := wm.rects[id]
				if !ok {
					t.Fatalf("Pane %s has no final boxes", id)
				}
				if !rectNearOK(end.Column, run.final[id].Column, 0.5) {
					t.Fatalf("Pane %s final column = %+v, want %+v", id, end.Column, run.final[id].Column)
				}
			}
			if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
				t.Fatalf("the ghost outlived the run")
			}
			if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
				t.Fatalf("the overlay outlived the run")
			}
		})
	}
}

// findVisualOK is findVisual without failing when the part is absent.
func findVisualOK(run *paneMotionRun, id model.PaneID, kind motion.VisualKind) (motion.Visual, bool) {
	if run == nil {
		return motion.Visual{}, false
	}
	for _, v := range run.anim.Visuals {
		if v.Pane == id && v.Kind == kind {
			return v, true
		}
	}
	return motion.Visual{}, false
}

// TestPaneReorderSiblingFallsBackToTheOverlay forces the route used when
// MyGo's Match + ScaleContent cannot cross the changed tree: the sibling is
// an opaque copy over a mask at its final column, moving from its old box to
// the final one, with the same spring and no per-frame reflow.
func TestPaneReorderSiblingFallsBackToTheOverlay(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true),
		paneMotionMode{overlayReorder: true, labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	moved, target := scene.ids[1], scene.ids[0]
	tabID, _ := g.state.TabID(0)
	scene.resetChangeCounters()

	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
		t.Fatalf("MovePane returned false")
	}
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("the move did not start a run")
	}
	sibling := findVisual(t, run, target, motion.Move)
	if sibling.Native || len(run.nativePanes) != 0 {
		t.Fatalf("the forced overlay route still carried the sibling natively: %+v %+v",
			sibling, run.nativePanes)
	}
	if sibling.Spring == nil || run.spring == nil {
		t.Fatalf("the overlay sibling is not on the reorder spring: %+v", sibling)
	}
	if _, ok := tt.Find("pane-motion:" + string(target)); !ok {
		t.Fatalf("the sibling overlay is not drawn")
	}
	if mask, ok := tt.Find("pane-motion-mask:" + string(target)); !ok {
		t.Fatalf("the real sibling is not masked")
	} else if !rectNearOK(mask, run.final[target].Column, 0.5) {
		t.Fatalf("the sibling mask = %+v, want its final column %+v", mask, run.final[target].Column)
	}
	changeCalls := scene.gridCalls()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	rect, _ := run.anim.StateAt(sibling, tt.Now())
	if rect.W <= run.final[target].Column.W || rect.W >= sibling.From.W {
		t.Fatalf("the overlay sibling at mid-run = %+v, want between %+v and %+v",
			rect, run.final[target].Column, sibling.From)
	}
	if got, ok := tt.Find("pane-motion:" + string(target)); !ok {
		t.Fatalf("the sibling overlay disappeared mid-run")
	} else if !rectNearOK(got, clipToWindow(rect), 0.75) {
		t.Fatalf("the sibling overlay at mid-run = %+v, want %+v", got, clipToWindow(rect))
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during the overlay spring, want none", id, got)
		}
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the overlay spring did not finish")
	}
	if _, ok := tt.Find("pane-motion:" + string(target)); ok {
		t.Fatalf("the sibling overlay outlived the run")
	}
}

// TestPaneReorderZoomInKeepsTheSplitUntilCovered is ticket 18's zoom-in
// contract: the split stays built and at its split grids behind an opaque
// copy that springs from the Pane's slot to the full window; the zoomed
// Pane's grid is the final full-window one (one resize), the sibling keeps
// its split grid (none), and the tree switches to the single Pane only at
// the end.
func TestPaneReorderZoomInKeepsTheSplitUntilCovered(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	zoomed, sibling := scene.ids[0], scene.ids[1]
	region := wm.region
	before := scene.before
	scene.resetChangeCounters()
	animationBefore := wm.animationFrames

	if !g.state.SetMagnifiedPane(0, zoomed) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", zoomed)
	}
	tt.Frame()
	event, _ := g.state.MotionEvent()
	if event.Kind != motion.PaneReorder || event.FromMagnified != "" || event.ToMagnified != zoomed {
		t.Fatalf("zoom-in event = %+v, want %q from no zoom", event, zoomed)
	}
	run := wm.run
	if run == nil || !run.zoom || run.render != zoomRenderSplit {
		t.Fatalf("the zoom-in run = %+v, want a split-rendering zoom", run)
	}
	if wm.effectiveMagnified != "" {
		t.Fatalf("the zoom switched to the single Pane before the overlay covered it")
	}
	if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) {
		t.Fatalf("the split is not behind the zoom overlay")
	}
	final := run.final[zoomed]
	if !rectNearOK(final.Slot, region, 0.5) || !rectNearOK(final.Column, region, 8) {
		t.Fatalf("the zoomed Pane's final full-window boxes = %+v, want the region %+v", final, region)
	}
	visual := findVisual(t, run, zoomed, motion.Move)
	if !rectNearOK(visual.From, before[zoomed].Column, 0.5) {
		t.Fatalf("the zoom copy starts at %+v, want the old slot %+v", visual.From, before[zoomed].Column)
	}
	if !rectNearOK(visual.To, final.Column, 0.5) || !visual.NoMask {
		t.Fatalf("the zoom copy = %+v, want the full column and no mask", visual)
	}
	if visual.Spring == nil || run.spring == nil ||
		run.spring.Duration != 240*time.Millisecond || run.spring.Bounce != 0.1 {
		t.Fatalf("the zoom spring = %+v / run %+v, want 240ms/bounce 0.1", visual.Spring, run.spring)
	}
	if _, ok := tt.Find("pane-motion:" + string(zoomed)); !ok {
		t.Fatalf("the zoom copy is not drawn")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(zoomed)); ok {
		t.Fatalf("the zoom copy masked the split it is supposed to reveal")
	}
	zoomSurface, siblingSurface := scene.surfaces[zoomed], scene.surfaces[sibling]
	if got := zoomSurface.Viewport(); got.W < region.W*0.9 || got.H < region.H*0.8 {
		t.Fatalf("the zoomed Pane's viewport = %+v, want the full window's region %+v", got, region)
	}
	if !gridsSettled(zoomSurface) {
		t.Fatalf("the zoomed Pane's terminal is not at its full-window grid")
	}
	if got := len(zoomSurface.ResizeLog()); got != 1 {
		t.Fatalf("the zoomed Pane delivered %d resizes, want 1: %+v", got, zoomSurface.ResizeLog())
	}
	if got := len(siblingSurface.ResizeLog()); got != 0 {
		t.Fatalf("the hidden sibling was resized %d times, want none: %+v", got, siblingSurface.ResizeLog())
	}
	if wm.animationFrames == animationBefore {
		t.Fatalf("the zoom requested no animation frame")
	}
	changeCalls := scene.gridCalls()

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	now := tt.Now()
	rect, _ := run.anim.StateAt(visual, now)
	if rect.W <= visual.From.W || rect.W >= final.Column.W {
		t.Fatalf("the zoom copy width at mid-run = %v, want between the slot %v and the window %v",
			rect.W, visual.From.W, final.Column.W)
	}
	if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) {
		t.Fatalf("the split disappeared before the overlay covered it")
	}
	if got, ok := tt.Find("pane-motion:" + string(zoomed)); !ok {
		t.Fatalf("the zoom copy disappeared mid-run")
	} else if !rectNearOK(got, clipToWindow(rect), 0.75) {
		t.Fatalf("the zoom copy at mid-run = %+v, want %+v", got, clipToWindow(rect))
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during the zoom, want none", id, got)
		}
	}
	if got := len(zoomSurface.ResizeLog()); got != 1 {
		t.Fatalf("the zoomed Pane delivered %d resizes over the run, want 1", got)
	}
	if got := len(siblingSurface.ResizeLog()); got != 0 {
		t.Fatalf("the sibling delivered %d resizes over the run, want none", got)
	}

	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the zoom-in run did not finish")
	}
	if wm.effectiveMagnified != zoomed {
		t.Fatalf("the finished zoom shows effective Pane %q, want %q", wm.effectiveMagnified, zoomed)
	}
	if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
		t.Fatalf("the final zoom layout is not the single Pane")
	}
	if _, ok := tt.Find("pane-motion:" + string(zoomed)); ok {
		t.Fatalf("the zoom copy outlived the run")
	}
	want, ok := appview.GridFor(zoomSurface.Viewport())
	if !ok || zoomSurface.Grid() != want {
		t.Fatalf("the zoomed Pane's final grid = %+v, want its viewport's %+v", zoomSurface.Grid(), want)
	}
	if got := len(zoomSurface.ResizeLog()); got != 1 {
		t.Fatalf("the zoomed Pane delivered %d resizes by the final frame, want 1", got)
	}
}

// TestPaneReorderZoomOutShrinksOverTheRevealedSplit is the reverse: the
// split is laid out at its final boxes immediately, the copy shrinks from
// the full window to the Pane's slot, and the Pane's grid returns to its
// split size once.
func TestPaneReorderZoomOutShrinksOverTheRevealedSplit(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	zoomed, sibling := scene.ids[0], scene.ids[1]
	if !g.state.SetMagnifiedPane(0, zoomed) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", zoomed)
	}
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the initial zoom did not settle")
	}
	full, ok := wm.rects[zoomed]
	if !ok || !rectNearOK(full.Column, wm.region, 8) {
		t.Fatalf("the magnified Pane's boxes = %+v, want the full region %+v", full, wm.region)
	}
	scene.resetChangeCounters()

	if !g.state.SetMagnifiedPane(0, "") {
		t.Fatalf("clearing the zoom returned false")
	}
	tt.Frame()
	event, _ := g.state.MotionEvent()
	if event.Kind != motion.PaneReorder || event.FromMagnified != zoomed || event.ToMagnified != "" {
		t.Fatalf("zoom-out event = %+v, want %q back to the split", event, zoomed)
	}
	run := wm.run
	if run == nil || !run.zoom || run.render != zoomRenderState {
		t.Fatalf("the zoom-out run = %+v, want the state's split layout", run)
	}
	final := run.final[zoomed]
	visual := findVisual(t, run, zoomed, motion.Move)
	if !rectNearOK(visual.From, full.Column, 0.5) {
		t.Fatalf("the shrinking copy starts at %+v, want the full window %+v", visual.From, full.Column)
	}
	if !rectNearOK(visual.To, final.Column, 0.5) || !visual.NoMask {
		t.Fatalf("the shrinking copy = %+v, want the split column %+v and no mask", visual, final.Column)
	}
	if visual.Spring == nil || run.spring == nil {
		t.Fatalf("the zoom out is not on the reorder spring")
	}
	if wm.effectiveMagnified != "" {
		t.Fatalf("the zoom-out layout is not the split")
	}
	if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) {
		t.Fatalf("the split was not revealed behind the shrinking copy")
	}
	zoomSurface, siblingSurface := scene.surfaces[zoomed], scene.surfaces[sibling]
	if got := len(zoomSurface.ResizeLog()); got != 1 {
		t.Fatalf("the shrinking Pane delivered %d resizes, want 1: %+v", got, zoomSurface.ResizeLog())
	}
	if got := len(siblingSurface.ResizeLog()); got != 0 {
		t.Fatalf("the revealed sibling delivered %d resizes, want none", got)
	}
	changeCalls := scene.gridCalls()

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	rect, _ := run.anim.StateAt(visual, tt.Now())
	if rect.W >= visual.From.W || rect.W <= final.Column.W {
		t.Fatalf("the shrinking copy width at mid-run = %v, want between %v and %v",
			rect.W, final.Column.W, visual.From.W)
	}
	if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) {
		t.Fatalf("the split vanished mid-shrink")
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during the zoom out, want none", id, got)
		}
	}

	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the zoom-out run did not finish")
	}
	want, ok := appview.GridFor(zoomSurface.Viewport())
	if !ok || zoomSurface.Grid() != want {
		t.Fatalf("the restored Pane's grid = %+v, want its split viewport's %+v", zoomSurface.Grid(), want)
	}
	if got := len(zoomSurface.ResizeLog()); got != 1 {
		t.Fatalf("the restored Pane delivered %d resizes by the final frame, want 1", got)
	}
}

// TestPaneReorderDirectMagnificationGrowsBOverA covers A→B: B's copy grows
// from B's slot over A, which stays rendered full and keeps its grid; there
// is no unzoom-then-zoom and only B resizes.
func TestPaneReorderDirectMagnificationGrowsBOverA(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	a, b := scene.ids[0], scene.ids[1]
	region := wm.region
	if !g.state.SetMagnifiedPane(0, a) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", a)
	}
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the initial zoom did not settle")
	}
	fullA := wm.rects[a].Column
	if !rectNearOK(fullA, region, 8) {
		t.Fatalf("the old magnified Pane's boxes = %+v, want the full region %+v", fullA, region)
	}
	slotB, ok := wm.rects[b]
	if !ok || !slotB.Valid() {
		t.Fatalf("Pane %q has no split boxes", b)
	}
	scene.resetChangeCounters()

	if !g.state.SetMagnifiedPane(0, b) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", b)
	}
	tt.Frame()
	event, _ := g.state.MotionEvent()
	if event.FromMagnified != a || event.ToMagnified != b || event.PaneID != b {
		t.Fatalf("A→B event = %+v, want %q over %q", event, b, a)
	}
	run := wm.run
	if run == nil || !run.zoom || run.render != zoomRenderPane || run.renderPane != a {
		t.Fatalf("the A→B run = %+v, want the old magnified Pane %q behind B", run, a)
	}
	visual := findVisual(t, run, b, motion.Move)
	if !rectNearOK(visual.From, slotB.Column, 0.5) {
		t.Fatalf("B's copy starts at %+v, want its old slot %+v", visual.From, slotB.Column)
	}
	final := run.final[b]
	if !rectNearOK(visual.To, final.Column, 0.5) || !rectNearOK(final.Column, region, 8) {
		t.Fatalf("B's copy target = %+v / final %+v, want the full region %+v", visual.To, final, region)
	}
	if wm.effectiveMagnified != a {
		t.Fatalf("the A→B layout shows Pane %q, want the old magnified %q as the backdrop",
			wm.effectiveMagnified, a)
	}
	if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
		t.Fatalf("the A→B frame is not rendering the single old Pane")
	}
	if _, ok := tt.Find("pane-motion:" + string(b)); !ok {
		t.Fatalf("B's overlay is not drawn")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(b)); ok {
		t.Fatalf("B's overlay masked the Pane it covers")
	}
	aSurface, bSurface := scene.surfaces[a], scene.surfaces[b]
	if got := len(aSurface.ResizeLog()); got != 0 {
		t.Fatalf("A was resized %d times in A→B, want none", got)
	}
	if got := len(bSurface.ResizeLog()); got != 1 {
		t.Fatalf("B delivered %d resizes, want 1: %+v", got, bSurface.ResizeLog())
	}
	// A received no resize at all (its log was cleared with the counters), so
	// compare its terminal grid to what its viewport still fits.
	if got := aSurface.Grid(); got != mustGrid(t, aSurface) {
		t.Fatalf("A's grid changed in A→B: %+v viewport %+v want %+v", got, aSurface.Viewport(), mustGrid(t, aSurface))
	}
	changeCalls := scene.gridCalls()

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	rect, _ := run.anim.StateAt(visual, tt.Now())
	if rect.W <= slotB.Column.W || rect.W >= final.Column.W {
		t.Fatalf("B's copy width at mid-run = %v, want between %v and %v",
			rect.W, slotB.Column.W, final.Column.W)
	}
	if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
		t.Fatalf("the A→B backdrop switched before the copy covered it")
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during A→B, want none", id, got)
		}
	}

	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the A→B run did not finish")
	}
	if wm.effectiveMagnified != b {
		t.Fatalf("the finished A→B shows Pane %q, want %q", wm.effectiveMagnified, b)
	}
	if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
		t.Fatalf("the final A→B layout is not the single new Pane")
	}
	if got := len(aSurface.ResizeLog()); got != 0 {
		t.Fatalf("A delivered %d resizes over A→B, want none", got)
	}
	want, ok := appview.GridFor(bSurface.Viewport())
	if !ok || bSurface.Grid() != want {
		t.Fatalf("B's final grid = %+v, want its viewport's %+v", bSurface.Grid(), want)
	}
	if got := len(bSurface.ResizeLog()); got != 1 {
		t.Fatalf("B delivered %d resizes by the final frame, want 1", got)
	}
}

// mustGrid is the grid the surface's viewport fits.
func mustGrid(t *testing.T, surface *realTerminalSurface) appview.Size {
	t.Helper()
	grid, ok := appview.GridFor(surface.Viewport())
	if !ok {
		t.Fatalf("the viewport %+v fits no grid", surface.Viewport())
	}
	return grid
}

// TestPaneReorderSplitFromMagnifiedIsOneContinuousOpen covers the magnified
// split: the old full-window Pane's open starts from the window (not its
// LayoutBounds), the new Pane enters with the open style, the split is
// already behind at final boxes, and each affected Pane resizes once.
func TestPaneReorderSplitFromMagnifiedIsOneContinuousOpen(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	old, sibling := scene.ids[0], scene.ids[1]
	region := wm.region
	if !g.state.SetMagnifiedPane(0, old) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", old)
	}
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the initial zoom did not settle")
	}
	fullOld := wm.rects[old].Column
	if fullOld.W <= wm.region.W*0.9 {
		t.Fatalf("the magnified Pane's boxes = %+v, want the full window, not its split LayoutBounds", fullOld)
	}
	beforeEvent, _ := g.state.MotionEvent()
	scene.resetChangeCounters()

	if err := g.state.SplitFocusedPane(domainworkspace.SideRight); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	event, _ := g.state.MotionEvent()
	if event.Kind != motion.PaneOpen || event.FromMagnified != old {
		t.Fatalf("magnified split event = %+v, want the PaneOpen of the old %q", event, old)
	}
	if event.Seq != beforeEvent.Seq+2 {
		t.Fatalf("the magnified split advanced the sequence by %d events, want 2",
			event.Seq-beforeEvent.Seq)
	}
	newPane, ok := g.state.FocusedPaneID(0)
	if !ok || newPane == old || newPane == sibling {
		t.Fatalf("the split focused %q, want a new Pane", newPane)
	}
	run := wm.run
	if run == nil || run.kind != motion.PaneOpen || run.zoom {
		t.Fatalf("the magnified split run = %+v, want one Pane-open run", run)
	}
	oldVisual := findVisual(t, run, old, motion.Move)
	if !rectNearOK(oldVisual.From, fullOld, 0.5) {
		t.Fatalf("the old Pane starts at %+v, want the full window %+v, not its LayoutBounds",
			oldVisual.From, fullOld)
	}
	if final := run.final[old]; !rectNearOK(oldVisual.To, final.Column, 0.5) ||
		oldVisual.To.W >= region.W*0.9 {
		t.Fatalf("the old Pane targets %+v, want its split column %+v", oldVisual.To, run.final[old].Column)
	}
	enter := findVisual(t, run, newPane, motion.Enter)
	if run.anim.Duration != 200*time.Millisecond || enter.Style != domainpreferences.MotionPopin ||
		enter.To == (ui.Rect{}) {
		t.Fatalf("the new Pane's open = %+v over %s, want the 200ms popin open at its final box",
			enter, run.anim.Duration)
	}
	if enter.Native {
		t.Fatalf("a Pane created during the split was expected to use the motion overlay")
	}
	if wm.effectiveMagnified != "" {
		t.Fatalf("the split still renders the old Pane alone")
	}
	if _, ok := tt.Find("pane-motion:" + string(newPane)); !ok {
		t.Fatalf("the new Pane's open overlay is not drawn")
	}
	waitForRealSurfaces(t, g.state, 3)
	tt.Frame()
	newSurface, ok := scene.surfaces[newPane]
	if !ok {
		newSurface = surfaceForPane(t, g, 0, newPane)
		scene.surfaces[newPane] = newSurface
	}
	pumpUntil(t, tt, "the new Pane's first grid", func() bool { return gridsSettled(newSurface) })
	oldSurface, siblingSurface := scene.surfaces[old], scene.surfaces[sibling]
	if got := len(oldSurface.ResizeLog()); got != 1 {
		t.Fatalf("the old full-window Pane delivered %d resizes, want 1: %+v", got, oldSurface.ResizeLog())
	}
	if got := len(siblingSurface.ResizeLog()); got != 0 {
		t.Fatalf("the revealing sibling delivered %d resizes, want none", got)
	}
	if got := len(newSurface.ResizeLog()); got != 1 {
		t.Fatalf("the new Pane delivered %d creation resizes, want 1: %+v", got, newSurface.ResizeLog())
	}
	for _, ordinal := range []int{1, 2, 3} {
		if !paneRegionExists(tt, ordinal) {
			t.Fatalf("the split behind the open is missing Pane %d", ordinal)
		}
	}
	changeCalls := scene.gridCalls()

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	rect, _ := run.anim.StateAt(oldVisual, tt.Now())
	if rect.W <= oldVisual.To.W || rect.W >= fullOld.W {
		t.Fatalf("the old Pane width at mid-open = %v, want between %v and the window %v",
			rect.W, oldVisual.To.W, fullOld.W)
	}
	if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) || !paneRegionExists(tt, 3) {
		t.Fatalf("the split vanished during the magnified open")
	}
	for id, surface := range scene.surfaces {
		if id == newPane {
			continue // the new Pane's first frame is its own creation resize
		}
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during the magnified open, want none", id, got)
		}
	}

	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the magnified open did not finish")
	}
	if wm.effectiveMagnified != "" {
		t.Fatalf("the finished split shows the old Pane magnified")
	}
	if got := len(oldSurface.ResizeLog()); got != 1 {
		t.Fatalf("the old Pane delivered %d resizes by the final frame, want 1", got)
	}
	want, ok := appview.GridFor(oldSurface.Viewport())
	if !ok || oldSurface.Grid() != want {
		t.Fatalf("the old Pane's final grid = %+v, want its split viewport's %+v",
			oldSurface.Grid(), want)
	}
	wantNew, ok := appview.GridFor(newSurface.Viewport())
	if !ok || newSurface.Grid() != wantNew {
		t.Fatalf("the new Pane's final grid = %+v, want its split viewport's %+v",
			newSurface.Grid(), wantNew)
	}
	if got := len(newSurface.ResizeLog()); got != 1 {
		t.Fatalf("the new Pane delivered %d resizes by the final frame, want 1", got)
	}
}

// TestPaneReorderWarpPathsSkipTheOverlay pins the three whole-run warp paths:
// master off, ReduceMotion, and the zoom's own switch off each land on the
// final layout with no overlay, no spring, and no animation frame.
func TestPaneReorderWarpPathsSkipTheOverlay(t *testing.T) {
	cases := []struct {
		name    string
		setting domainpreferences.Motion
		reduce  bool
	}{
		{name: "master off", setting: func() domainpreferences.Motion {
			s := reorderPreferences(true, true, true)
			s.Enabled = false
			return s
		}()},
		{name: "pane reorder off", setting: reorderPreferences(true, true, false)},
		{name: "reduce motion", setting: reorderPreferences(true, true, true), reduce: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scene := newReorderScene(t, tc.setting, paneMotionMode{labels: true})
			g, tt, wm := scene.graph, scene.tt, scene.wm
			zoomed := scene.ids[0]
			if tc.reduce {
				tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			}
			beforeFrames := wm.animationFrames
			scene.resetChangeCounters()

			if !g.state.SetMagnifiedPane(0, zoomed) {
				t.Fatalf("SetMagnifiedPane(%q) returned false", zoomed)
			}
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("%s started a run: %+v", tc.name, wm.run)
			}
			if wm.animationFrames != beforeFrames {
				t.Fatalf("%s requested %d animation frames, want none",
					tc.name, wm.animationFrames-beforeFrames)
			}
			if _, ok := tt.Find("pane-motion:" + string(zoomed)); ok {
				t.Fatalf("%s drew the zoom overlay", tc.name)
			}
			if _, ok := tt.Find("pane-motion-mask:" + string(zoomed)); ok {
				t.Fatalf("%s drew the zoom mask", tc.name)
			}
			if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
				t.Fatalf("%s did not land on the single-Pane layout", tc.name)
			}
			if wm.effectiveMagnified != zoomed {
				t.Fatalf("%s shows Pane %q, want %q", tc.name, wm.effectiveMagnified, zoomed)
			}
			if got := len(scene.surfaces[zoomed].ResizeLog()); got != 1 {
				t.Fatalf("%s delivered %d resizes to the zoomed Pane, want 1", tc.name, got)
			}
			if got := len(scene.surfaces[scene.ids[1]].ResizeLog()); got != 0 {
				t.Fatalf("%s delivered %d resizes to the hidden Pane, want none", tc.name, got)
			}
			afterFrames := wm.animationFrames
			tt.Advance(300 * time.Millisecond)
			tt.Frame()
			if wm.run != nil || wm.animationFrames != afterFrames {
				t.Fatalf("%s started an animation after the warp", tc.name)
			}
		})
	}
}

// TestPaneReorderReverseZoomKeepsTheCurrentRectAndVelocity interrupts a zoom
// in flight: the zoom out starts from the rect and velocity the copy had at
// that instant and retargets immediately, rather than restarting from the
// old slot or from rest.
func TestPaneReorderReverseZoomKeepsTheCurrentRectAndVelocity(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	zoomed := scene.ids[0]
	if !g.state.SetMagnifiedPane(0, zoomed) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", zoomed)
	}
	tt.Frame()
	first := wm.run
	if first == nil {
		t.Fatalf("the zoom-in started no run")
	}
	firstVisual := findVisual(t, first, zoomed, motion.Move)
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	now := tt.Now()
	current, opacity := first.anim.StateAt(firstVisual, now)
	velocity := first.anim.VelocityAt(firstVisual, now)

	if !g.state.SetMagnifiedPane(0, "") {
		t.Fatalf("clearing the zoom returned false")
	}
	tt.Frame()
	event, _ := g.state.MotionEvent()
	if event.FromMagnified != zoomed || event.ToMagnified != "" {
		t.Fatalf("reverse event = %+v, want %q back to the split", event, zoomed)
	}
	run := wm.run
	if run == nil || !run.zoom || run.render != zoomRenderState {
		t.Fatalf("the reverse zoom run = %+v", run)
	}
	visual := findVisual(t, run, zoomed, motion.Move)
	if !rectNearOK(visual.From, current, 0.01) {
		t.Fatalf("the reversed copy starts at %+v, want the interrupted %+v", visual.From, current)
	}
	if !rectNearOK(visual.Velocity, velocity, 0.01) {
		t.Fatalf("the reversed copy's velocity = %+v, want the interrupted %+v",
			visual.Velocity, velocity)
	}
	if visual.To.W >= visual.From.W {
		t.Fatalf("the reversed copy still targets the full window: %+v", visual)
	}
	if _, got := run.anim.StateAt(visual, now); abs32(got-opacity) > 0.001 {
		t.Fatalf("the reversed copy restarted opacity at %v, want %v", got, opacity)
	}
	tt.Advance(4 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the reversed zoom did not finish")
	}
	if wm.effectiveMagnified != "" {
		t.Fatalf("the finished reverse zoom still shows the single Pane")
	}
	if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) {
		t.Fatalf("the final reverse layout is not the split")
	}
}

// TestPaneReorderReverseMoveKeepsTheSiblingVelocity reverses a move in
// flight. The old-position ghost and the sibling spring both start from what
// was on screen, and the spring keeps its velocity so the sibling does not
// jump or stop dead at the interruption.
func TestPaneReorderReverseMoveKeepsTheSiblingVelocity(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	moved, target := scene.ids[1], scene.ids[0]
	tabID, _ := g.state.TabID(0)
	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
		t.Fatalf("the first MovePane returned false")
	}
	tt.Frame()
	first := wm.run
	if first == nil {
		t.Fatalf("the first move started no run")
	}
	firstSibling := findVisual(t, first, target, motion.Move)
	firstEnter := findVisual(t, first, moved, motion.Enter)
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	now := tt.Now()
	currentSibling, _ := first.anim.StateAt(firstSibling, now)
	siblingVelocity := first.anim.VelocityAt(firstSibling, now)
	currentMoved, movedOpacity := first.anim.StateAt(firstEnter, now)

	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideRight) {
		t.Fatalf("the reversing MovePane returned false")
	}
	tt.Frame()
	event, _ := g.state.MotionEvent()
	if event.Kind != motion.PaneReorder || event.Seq == first.seq {
		t.Fatalf("reverse move event = %+v, want a newer PaneReorder", event)
	}
	run := wm.run
	if run == nil || run.zoom || run.seq != event.Seq {
		t.Fatalf("the reverse move run = %+v, want the newest move", run)
	}
	sibling := findVisual(t, run, target, motion.Move)
	if !rectNearOK(sibling.From, currentSibling, 0.01) {
		t.Fatalf("the reversed sibling starts at %+v, want the interrupted %+v",
			sibling.From, currentSibling)
	}
	if !rectNearOK(sibling.Velocity, siblingVelocity, 0.01) {
		t.Fatalf("the reversed sibling velocity = %+v, want the interrupted %+v",
			sibling.Velocity, siblingVelocity)
	}
	if sibling.Spring == nil || sibling.Spring.Duration != 240*time.Millisecond ||
		sibling.Spring.Bounce != 0.1 {
		t.Fatalf("the reversed sibling spring = %+v, want 240ms/bounce 0.1", sibling.Spring)
	}
	ghost := findVisual(t, run, moved, motion.Exit)
	if !rectNearOK(ghost.From, currentMoved, 0.01) {
		t.Fatalf("the reversed old-position ghost starts at %+v, want the interrupted %+v",
			ghost.From, currentMoved)
	}
	if abs32(ghost.FromOpacity-movedOpacity) > 0.001 {
		t.Fatalf("the reversed ghost opacity = %v, want %v", ghost.FromOpacity, movedOpacity)
	}
	if ghost.Duration != 150*time.Millisecond {
		t.Fatalf("the reversed close ghost duration = %s, want the full 150ms from the current value",
			ghost.Duration)
	}
	enter := findVisual(t, run, moved, motion.Enter)
	if !enter.Interrupted || enter.Duration != 200*time.Millisecond {
		t.Fatalf("the reversed open overlay = %+v, want the interrupted full 200ms open", enter)
	}
	if !rectNearOK(enter.From, currentMoved, 0.01) {
		t.Fatalf("the reversed open overlay starts at %+v, want the visible %+v",
			enter.From, currentMoved)
	}
	if abs32(enter.FromOpacity-movedOpacity) > 0.001 {
		t.Fatalf("the reversed open overlay opacity = %v, want the visible %v",
			enter.FromOpacity, movedOpacity)
	}
	if rect, opacity := run.anim.StateAt(enter, now); !rectNearOK(rect, currentMoved, 0.01) ||
		abs32(opacity-movedOpacity) > 0.001 {
		t.Fatalf("the reversed open overlay's first frame = %+v/%v, want %+v/%v",
			rect, opacity, currentMoved, movedOpacity)
	}
	tt.Advance(4 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the reversed move did not finish")
	}
	if len(motionPaneIDsMust(t, g)) != 2 {
		t.Fatalf("the reversed move lost a Pane")
	}
	want, ok := appview.GridFor(scene.surfaces[target].Viewport())
	if !ok || scene.surfaces[target].Grid() != want {
		t.Fatalf("the sibling's final grid = %+v, want its viewport's %+v",
			scene.surfaces[target].Grid(), want)
	}
}

// TestPaneReorderSwitchOffMidRunWarpsOnlyThatPart changes a switch while a
// move is already playing. The other parts keep their visuals and the run;
// the disabled part disappears immediately. In particular, turning PaneClose
// off must not release the still-live moving Pane's surface.
func TestPaneReorderSwitchOffMidRunWarpsOnlyThatPart(t *testing.T) {
	cases := []struct {
		name string
		off  func(*domainpreferences.Motion)
	}{
		{"pane close off", func(s *domainpreferences.Motion) { s.PaneClose = false }},
		{"pane open off", func(s *domainpreferences.Motion) { s.PaneOpen = false }},
		{"pane reorder off", func(s *domainpreferences.Motion) { s.PaneReorder = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := reorderPreferences(true, true, true)
			scene := newReorderSceneWithMotion(t,
				func() domainpreferences.Motion { return setting },
				paneMotionMode{labels: true})
			g, tt, wm := scene.graph, scene.tt, scene.wm
			moved, target := scene.ids[1], scene.ids[0]
			tabID, _ := g.state.TabID(0)
			if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
				t.Fatalf("the move returned false")
			}
			tt.Frame()
			run := wm.run
			if run == nil {
				t.Fatalf("the move started no run")
			}
			if _, ok := findVisualOK(run, moved, motion.Exit); !ok {
				t.Fatalf("the starting run has no old-position ghost")
			}
			if _, ok := findVisualOK(run, moved, motion.Enter); !ok {
				t.Fatalf("the starting run has no new-position overlay")
			}
			if _, ok := findVisualOK(run, target, motion.Move); !ok {
				t.Fatalf("the starting run has no sibling spring")
			}
			tt.Advance(40 * time.Millisecond)
			tt.Frame()

			tc.off(&setting)
			tt.Frame()
			if wm.run == nil {
				t.Fatalf("turning %s off ended the other parts too", tc.name)
			}
			_, hasExit := findVisualOK(run, moved, motion.Exit)
			_, hasEnter := findVisualOK(run, moved, motion.Enter)
			_, hasMove := findVisualOK(run, target, motion.Move)
			switch tc.name {
			case "pane close off":
				if hasExit {
					t.Fatalf("the old-position ghost survived PaneClose off")
				}
				if !hasEnter || !hasMove {
					t.Fatalf("PaneClose off warped the other parts: enter %v sibling %v", hasEnter, hasMove)
				}
				if _, ok := run.ghosts[moved]; ok {
					t.Fatalf("the ghost map still holds the moving Pane")
				}
				if _, ok := tt.Find("pane-close-ghost:" + string(moved)); ok {
					t.Fatalf("the old-position ghost is still drawn")
				}
			case "pane open off":
				if hasEnter {
					t.Fatalf("the new-position overlay survived PaneOpen off")
				}
				if !hasExit || !hasMove {
					t.Fatalf("PaneOpen off warped the other parts: ghost %v sibling %v", hasExit, hasMove)
				}
				if _, ok := tt.Find("pane-motion:" + string(moved)); ok {
					t.Fatalf("the new-position overlay is still drawn")
				}
			case "pane reorder off":
				if hasMove {
					t.Fatalf("the sibling spring survived PaneReorder off")
				}
				if !hasExit || !hasEnter {
					t.Fatalf("PaneReorder off warped the other parts: ghost %v enter %v", hasExit, hasEnter)
				}
				if run.spring != nil || len(run.nativePanes) != 0 {
					t.Fatalf("PaneReorder off kept the spring %+v or natives %+v",
						run.spring, run.nativePanes)
				}
			}
			if closes := scene.surfaces[moved].Closes(); closes != 0 {
				t.Fatalf("%s closed the live moving Pane %d times", tc.name, closes)
			}

			tt.Advance(2 * time.Second)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("the partially warped move did not finish")
			}
			if closes := scene.surfaces[moved].Closes(); closes != 0 {
				t.Fatalf("%s closed the moved Pane by the end %d times", tc.name, closes)
			}
			if len(motionPaneIDsMust(t, g)) != 2 {
				t.Fatalf("%s lost a Pane", tc.name)
			}
			if !paneRegionExists(tt, 1) || !paneRegionExists(tt, 2) {
				t.Fatalf("%s did not land on the final split", tc.name)
			}
		})
	}
}

// paintedPaneRegion returns the box the real Pane id paints this frame. The
// split renderer labels each real Pane with its tree ordinal; a native route
// moves the real element, so its label is the painted box rather than the
// layout box.
func paintedPaneRegion(t *testing.T, tt *ui.Tester, g *realTerminalGraph, id model.PaneID) ui.Rect {
	t.Helper()
	ids := motionPaneIDsMust(t, g)
	for index, candidate := range ids {
		if candidate != id {
			continue
		}
		rect, ok := tt.Find(locales.In("en").PaneRegion(index + 1))
		if !ok {
			t.Fatalf("Pane %q has no PaneRegion(%d) label", id, index+1)
		}
		return rect
	}
	t.Fatalf("Pane %q is not in the tree %v", id, ids)
	return ui.Rect{}
}

// signOf returns -1, 0 or 1 for the sign of v.
func signOf(v float32) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// TestPaneReorderNativeSiblingKeepsVelocityOnReverse measures the production
// native sibling route across an interruption, not just its bookkeeping. The
// scene moves the larger Pane, so its sibling grows into the room it takes;
// a growing sibling keeps MyGo's Match + ScaleContent route. The reverse move
// recreates the split, so the sibling continues through the overlay, whose
// spring must keep the measured velocity: it carries on in the pre-reverse
// direction at the pre-reverse speed for a few milliseconds, then heads back
// to the new target. A sibling re-keyed at the change would start from rest
// and move the opposite way immediately.
func TestPaneReorderNativeSiblingKeepsVelocityOnReverse(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	moved, target := scene.ids[0], scene.ids[1]
	tabID, _ := g.state.TabID(0)

	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideRight) {
		t.Fatalf("the first MovePane returned false")
	}
	tt.Frame()
	first := wm.run
	if first == nil {
		t.Fatalf("the first move started no run")
	}
	firstSibling := findVisual(t, first, target, motion.Move)
	if !firstSibling.Native {
		t.Fatalf("production did not carry the growing sibling on the native route: %+v", firstSibling)
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	now := tt.Now()
	preBook, _ := first.anim.StateAt(firstSibling, now)
	preVelocity := first.anim.VelocityAt(firstSibling, now)
	// The sibling grows from the right slot into the left one, so its flight
	// left edge is moving left.
	if preVelocity.X >= 0 {
		t.Fatalf("the sibling's flight left edge is not moving left: velocity %+v", preVelocity)
	}

	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
		t.Fatalf("the reversing MovePane returned false")
	}
	tt.Frame()
	run := wm.run
	if run == nil || run.zoom || run.seq == first.seq {
		t.Fatalf("the reverse move run = %+v, want the newest move", run)
	}
	sibling := findVisual(t, run, target, motion.Move)
	// The recreated split makes the sibling a new element, so MyGo's native
	// channel cannot be retargeted; the moving Pane continues through the
	// overlay, whose spring carries the measured velocity. The first move
	// above still used the native route.
	if sibling.Native || run.nativePanes[target] {
		t.Fatalf("the interrupted sibling kept the native route, which starts its match from rest: %+v",
			sibling)
	}
	if !rectNearOK(sibling.From, preBook, 0.01) {
		t.Fatalf("the reversed sibling starts at %+v, want the interrupted %+v",
			sibling.From, preBook)
	}
	if !rectNearOK(sibling.Velocity, preVelocity, 0.01) {
		t.Fatalf("the reversed sibling bookkeeping velocity = %+v, want %+v",
			sibling.Velocity, preVelocity)
	}
	paintedSibling := func() ui.Rect {
		t.Helper()
		rect, ok := tt.Find("pane-motion:" + string(target))
		if !ok {
			t.Fatalf("the interrupted sibling's overlay is not drawn")
		}
		return rect
	}
	atChange := paintedSibling()
	if !rectNearOK(atChange, clipToWindow(preBook), 0.75) {
		t.Fatalf("the interrupted sibling's overlay starts at %+v, want the visible %+v",
			atChange, clipToWindow(preBook))
	}

	// Four milliseconds later the painted sibling must still move the way it
	// was moving before the reverse, at a speed the old spring explains. A
	// fresh channel restarts at rest and moves the other way at once.
	tt.Advance(4 * time.Millisecond)
	tt.Frame()
	at4 := paintedSibling()
	seconds := float32(4*time.Millisecond) / float32(time.Second)
	leftDelta := at4.X - atChange.X
	wantDelta := preVelocity.X * seconds
	if signOf(leftDelta) != signOf(preVelocity.X) {
		t.Fatalf("the interrupted sibling at +4ms moved %v, want the pre-reverse direction of %v DIP/s (want about %v)",
			leftDelta, preVelocity.X, wantDelta)
	}
	if abs32(leftDelta) < abs32(wantDelta)*0.25 {
		t.Fatalf("the interrupted sibling at +4ms moved %v, want about %v from the kept velocity",
			leftDelta, wantDelta)
	}
	if book, _ := run.anim.StateAt(sibling, tt.Now()); !rectNearOK(at4, clipToWindow(book), 0.75) {
		t.Fatalf("the interrupted sibling's painted box at +4ms = %+v, want the spring's %+v",
			at4, clipToWindow(book))
	}

	// Later it heads for the new target and the run settles there.
	wantEnd := run.final[target].Column
	tt.Advance(120 * time.Millisecond)
	tt.Frame()
	later := paintedSibling()
	if abs32(later.X-wantEnd.X) >= abs32(at4.X-wantEnd.X) {
		t.Fatalf("the sibling did not head back to the new target: left edge at +120ms %v, at +4ms %v, target %+v",
			later.X, at4.X, wantEnd)
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the reversed move did not finish")
	}
	if _, ok := tt.Find("pane-motion:" + string(target)); ok {
		t.Fatalf("a stale sibling overlay remains after the run")
	}
	end := paintedPaneRegion(t, tt, g, target)
	if !rectNearOK(end, wantEnd, 0.75) {
		t.Fatalf("the sibling's final painted box = %+v, want %+v", end, wantEnd)
	}
}

// TestPaneReorderDirectReverseRetargetsTheOutgoingCopy interrupts a settled
// A→B magnification with B→A at early, mid and late progress. The outgoing B
// copy must exist at the next frame, start from B's visible rect, opacity and
// velocity, shrink toward B's remembered split slot over A, and leave the
// final frame as A alone at full window. Warping straight to A is the defect.
func TestPaneReorderDirectReverseRetargetsTheOutgoingCopy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
	}{
		{"early", 16 * time.Millisecond},
		{"mid", 60 * time.Millisecond},
		{"late", 150 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
			g, tt, wm := scene.graph, scene.tt, scene.wm
			a, b := scene.ids[0], scene.ids[1]
			if !g.state.SetMagnifiedPane(0, a) {
				t.Fatalf("SetMagnifiedPane(%q) returned false", a)
			}
			tt.Frame()
			tt.Advance(2 * time.Second)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("the initial zoom did not settle")
			}
			homeB, ok := wm.rects[b]
			if !ok || !homeB.Valid() {
				t.Fatalf("Pane %q has no remembered split boxes: %+v", b, homeB)
			}
			if !rectNearOK(homeB.Column, scene.before[b].Column, 0.5) {
				t.Fatalf("Pane %q's remembered boxes = %+v, want its pre-zoom split %+v",
					b, homeB.Column, scene.before[b].Column)
			}

			if !g.state.SetMagnifiedPane(0, b) {
				t.Fatalf("SetMagnifiedPane(%q) returned false", b)
			}
			tt.Frame()
			first := wm.run
			if first == nil || !first.zoom || first.zoomPane != b {
				t.Fatalf("the A→B run = %+v, want B's zoom", first)
			}
			firstVisual := findVisual(t, first, b, motion.Move)
			tt.Advance(tc.delay)
			tt.Frame()
			now := tt.Now()
			current, opacity := first.anim.StateAt(firstVisual, now)
			velocity := first.anim.VelocityAt(firstVisual, now)
			if painted, ok := tt.Find("pane-motion:" + string(b)); !ok {
				t.Fatalf("the A→B copy is not drawn at %s", tc.name)
			} else if !rectNearOK(painted, clipToWindow(current), 0.75) {
				t.Fatalf("the A→B copy at %s = %+v, want %+v", tc.name, painted, clipToWindow(current))
			}

			if !g.state.SetMagnifiedPane(0, a) {
				t.Fatalf("the reversing SetMagnifiedPane(%q) returned false", a)
			}
			tt.Frame()
			event, _ := g.state.MotionEvent()
			if event.FromMagnified != b || event.ToMagnified != a {
				t.Fatalf("reverse event = %+v, want %q over %q", event, a, b)
			}
			run := wm.run
			if run == nil || !run.zoom {
				t.Fatalf("the B→A reversal warped instead of retargeting: run %+v", run)
			}
			if run.render != zoomRenderState {
				t.Fatalf("the B→A reversal renders %v, want the new final A layout", run.render)
			}
			out := findVisual(t, run, b, motion.Move)
			if !rectNearOK(out.From, current, 0.01) {
				t.Fatalf("the outgoing B copy starts at %+v, want the interrupted %+v", out.From, current)
			}
			if abs32(out.FromOpacity-opacity) > 0.001 {
				t.Fatalf("the outgoing B copy opacity = %v, want the interrupted %v",
					out.FromOpacity, opacity)
			}
			if !rectNearOK(out.Velocity, velocity, 0.01) {
				t.Fatalf("the outgoing B copy velocity = %+v, want the interrupted %+v",
					out.Velocity, velocity)
			}
			if !rectNearOK(out.To, homeB.Column, 0.5) {
				t.Fatalf("the outgoing B copy targets %+v, want B's remembered slot %+v",
					out.To, homeB.Column)
			}
			if !out.NoMask {
				t.Fatalf("the outgoing B copy masks A's full-window layout")
			}
			endA, ok := run.final[a]
			if !ok || !rectNearOK(endA.Column, wm.region, 8) {
				t.Fatalf("A's final box = %+v, want the full region %+v", endA, wm.region)
			}
			if wm.effectiveMagnified != a {
				t.Fatalf("the reversal shows Pane %q behind the copy, want A %q",
					wm.effectiveMagnified, a)
			}
			if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
				t.Fatalf("the reversal does not render A alone at full window")
			}
			if painted, ok := tt.Find("pane-motion:" + string(b)); !ok {
				t.Fatalf("the outgoing B copy is not drawn at the reversal")
			} else if !rectNearOK(painted, clipToWindow(current), 0.75) {
				t.Fatalf("the outgoing B copy at the reversal = %+v, want the visible %+v",
					painted, clipToWindow(current))
			}

			// Four milliseconds later it keeps the old direction: the kept
			// velocity carries it away from B's slot before the spring turns
			// it around.
			tt.Advance(4 * time.Millisecond)
			tt.Frame()
			at4, _ := run.anim.StateAt(out, tt.Now())
			for _, pair := range [][2]float32{
				{at4.X - out.From.X, out.Velocity.X},
				{at4.W - out.From.W, out.Velocity.W},
			} {
				if pair[1] == 0 {
					continue
				}
				if signOf(pair[0]) != signOf(pair[1]) {
					t.Fatalf("the outgoing B copy at +4ms moved %v against its kept velocity %v",
						pair[0], pair[1])
				}
			}
			if painted, ok := tt.Find("pane-motion:" + string(b)); !ok {
				t.Fatalf("the outgoing B copy disappeared at +4ms")
			} else if !rectNearOK(painted, clipToWindow(at4), 0.75) {
				t.Fatalf("the outgoing B copy at +4ms = %+v, want %+v", painted, clipToWindow(at4))
			}

			// Later it heads to B's slot, and the run settles on A alone.
			tt.Advance(120 * time.Millisecond)
			tt.Frame()
			later, _ := run.anim.StateAt(out, tt.Now())
			if abs32(later.X+later.W/2-(homeB.Column.X+homeB.Column.W/2)) >=
				abs32(at4.X+at4.W/2-(homeB.Column.X+homeB.Column.W/2)) {
				t.Fatalf("the outgoing B copy did not head back: center at +120ms %v, at +4ms %v, slot %+v",
					later, at4, homeB.Column)
			}
			tt.Advance(2 * time.Second)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("the reversed zoom did not finish")
			}
			if wm.effectiveMagnified != a {
				t.Fatalf("the finished reversal shows Pane %q, want %q", wm.effectiveMagnified, a)
			}
			if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
				t.Fatalf("the final reversal layout is not A full-window")
			}
			if _, ok := tt.Find("pane-motion:" + string(b)); ok {
				t.Fatalf("a stale B overlay remains after the reversal")
			}
			if wm.rects[a].Column.W < wm.region.W*0.9 {
				t.Fatalf("A's final boxes = %+v, want the full window %+v", wm.rects[a].Column, wm.region)
			}
		})
	}
}

// resizeSince is the PTY size stream a surface delivered after mark.
func resizeSince(surface *realTerminalSurface, mark int) []appview.Size {
	return surface.ResizeLog()[mark:]
}

// viewportMatches reports that a surface's current room is want, within the
// sub-DIP layout rounding sameViewport allows.
func viewportMatches(surface *realTerminalSurface, want appview.Viewport) bool {
	v := surface.Viewport()
	return sameViewport(v.W, v.H, want)
}

// TestPaneReorderDirectReversePinsTheOutgoingHomeGrid is the stale-pin
// regression: a direct A→B magnification reversed mid-run must return the
// outgoing B's surface to its slot grid once and record the same home boxes.
// The next A→B then derives its pin from a truthful home pair and pins exactly
// once to the full grid. Before the fix the reversal left B's viewport full
// while wm.rects[B] named the split home, so the next magnification computed a
// widening delta and pinned an out-of-window 185-column viewport.
func TestPaneReorderDirectReversePinsTheOutgoingHomeGrid(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	a, b := scene.ids[0], scene.ids[1]
	region := wm.region
	bSurface := scene.surfaces[b]

	if !g.state.SetMagnifiedPane(0, a) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", a)
	}
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the initial zoom did not settle")
	}
	homeB, ok := wm.rects[b]
	if !ok || !homeB.Valid() {
		t.Fatalf("Pane %q has no remembered split boxes: %+v", b, homeB)
	}
	homeGrid := bSurface.Grid()
	homeView := bSurface.Viewport()
	if !viewportMatches(bSurface, homeView) {
		t.Fatalf("B's settled split viewport = %+v, want %+v", bSurface.Viewport(), homeView)
	}

	mark := len(bSurface.ResizeLog())
	if !g.state.SetMagnifiedPane(0, b) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", b)
	}
	tt.Frame()
	first := wm.run
	if first == nil || !first.zoom || first.zoomPane != b {
		t.Fatalf("the A→B run = %+v, want B's zoom", first)
	}
	fullGrid := bSurface.Grid()
	fullView := bSurface.Viewport()
	if got := resizeSince(bSurface, mark); len(got) != 1 || got[0] != fullGrid {
		t.Fatalf("A→B delivered %+v, want one resize to %+v", got, fullGrid)
	}
	firstVisual := findVisual(t, first, b, motion.Move)
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	now := tt.Now()
	current, opacity := first.anim.StateAt(firstVisual, now)

	if !g.state.SetMagnifiedPane(0, a) {
		t.Fatalf("the reversing SetMagnifiedPane(%q) returned false", a)
	}
	tt.Frame()
	run := wm.run
	if run == nil || !run.zoom || run.render != zoomRenderState {
		t.Fatalf("the B→A reversal = %+v, want the new final A layout", run)
	}
	out := findVisual(t, run, b, motion.Move)
	if !rectNearOK(out.From, current, 0.01) || abs32(out.FromOpacity-opacity) > 0.001 {
		t.Fatalf("the outgoing B copy starts at %+v/%v, want the visible %+v/%v",
			out.From, out.FromOpacity, current, opacity)
	}
	if !rectNearOK(out.To, homeB.Column, 0.5) {
		t.Fatalf("the outgoing B copy targets %+v, want B's remembered slot %+v", out.To, homeB.Column)
	}
	// The outgoing Pane is zooming out to its slot grid once, at the change.
	resizes := resizeSince(bSurface, mark)
	if len(resizes) != 2 || resizes[1] != homeGrid {
		t.Fatalf("the reversal delivered %v resizes since A→B began, want [full home] %+v/%+v",
			resizes, fullGrid, homeGrid)
	}
	if !viewportMatches(bSurface, homeView) {
		t.Fatalf("B's viewport after the reversal = %+v, want its pre-zoom room %+v",
			bSurface.Viewport(), homeView)
	}
	if got := wm.rects[b]; !rectNearOK(got.Body, homeB.Body, 0.5) ||
		!rectNearOK(got.Column, homeB.Column, 0.5) {
		t.Fatalf("B's recorded boxes after the reversal = %+v, want the home pair %+v", got, homeB)
	}

	// The outgoing copy animates home and the run settles on A alone.
	tt.Advance(120 * time.Millisecond)
	tt.Frame()
	if got := resizeSince(bSurface, mark); len(got) != 2 {
		t.Fatalf("the reversal's animation frames added resizes: %v", got)
	}
	later, _ := run.anim.StateAt(out, tt.Now())
	if abs32(later.X+later.W/2-(homeB.Column.X+homeB.Column.W/2)) >=
		abs32(current.X+current.W/2-(homeB.Column.X+homeB.Column.W/2)) {
		t.Fatalf("the outgoing B copy did not head home: center %v -> %v, slot %v",
			current, later, homeB.Column)
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the reversed zoom did not finish")
	}
	if wm.effectiveMagnified != a {
		t.Fatalf("the settled reversal shows Pane %q, want %q", wm.effectiveMagnified, a)
	}
	if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
		t.Fatalf("the final reversal layout is not A full-window")
	}
	if _, ok := tt.Find("pane-motion:" + string(b)); ok {
		t.Fatalf("a stale B overlay remains after the reversal")
	}
	if !rectNearOK(wm.rects[a].Column, region, 8) {
		t.Fatalf("A's final boxes = %+v, want the full region %+v", wm.rects[a].Column, region)
	}

	// The second A→B starts from the truthful home pair, so it pins exactly
	// once to the full grid and never an out-of-window viewport.
	beforeSecond := len(bSurface.ResizeLog())
	if !g.state.SetMagnifiedPane(0, b) {
		t.Fatalf("the second SetMagnifiedPane(%q) returned false", b)
	}
	tt.Frame()
	second := wm.run
	if second == nil || !second.zoom || second.zoomPane != b {
		t.Fatalf("the second A→B run = %+v, want B's zoom", second)
	}
	secondFull := second.final[b]
	if got := resizeSince(bSurface, beforeSecond); len(got) != 1 || got[0] != fullGrid {
		t.Fatalf("the second A→B delivered %+v, want exactly one resize to the first's full grid %+v",
			got, fullGrid)
	}
	if got := bSurface.Viewport(); got.W > region.W+0.5 || got.H > region.H+0.5 {
		t.Fatalf("the second A→B pinned the out-of-window viewport %+v (region %+v)", got, region)
	}
	if !viewportMatches(bSurface, fullView) {
		t.Fatalf("the second A→B pinned %+v, want the full room %+v", bSurface.Viewport(), fullView)
	}

	calls := scene.gridCalls()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if got := resizeSince(bSurface, beforeSecond); len(got) != 1 {
		t.Fatalf("animation frames added resizes: %v", got)
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - calls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during the second A→B, want none", id, got)
		}
	}

	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the second A→B did not finish")
	}
	if wm.effectiveMagnified != b {
		t.Fatalf("the settled second A→B shows Pane %q, want %q", wm.effectiveMagnified, b)
	}
	if got := resizeSince(bSurface, beforeSecond); len(got) != 1 {
		t.Fatalf("the second A→B delivered %v resizes by its end, want one", got)
	}
	if got := bSurface.Grid(); got != fullGrid {
		t.Fatalf("B's final grid = %+v, want %+v", got, fullGrid)
	}
	if !viewportMatches(bSurface, fullView) {
		t.Fatalf("B's final viewport = %+v, want the full room %+v",
			bSurface.Viewport(), fullView)
	}
	if !rectNearOK(wm.rects[b].Body, secondFull.Body, 0.5) {
		t.Fatalf("B's final record = %+v, want the full body %+v", wm.rects[b].Body, secondFull.Body)
	}
	for i, size := range resizeSince(bSurface, mark) {
		if size != homeGrid && size != fullGrid {
			t.Fatalf("B delivered the out-of-window grid %+v at resize %d (home %+v, full %+v)",
				size, i, homeGrid, fullGrid)
		}
	}
	if got := resizeSince(bSurface, mark); len(got) != 3 {
		t.Fatalf("B delivered %v resizes across the chain, want exactly three (full, home, full)", got)
	}
}

// TestPaneReorderDirectReverseMidRunKeepsTheGridTruthful reverses B→A only 16
// ms into the reversal, so the next A→B interrupts the home-bound copy. The
// reversal has already returned B's surface and recorded boxes to the same
// home pair; the interrupting zoom then pins once to the full grid instead of
// widening an out-of-window viewport, and the latest event wins.
func TestPaneReorderDirectReverseMidRunKeepsTheGridTruthful(t *testing.T) {
	scene := newReorderScene(t, reorderPreferences(true, true, true), paneMotionMode{labels: true})
	g, tt, wm := scene.graph, scene.tt, scene.wm
	a, b := scene.ids[0], scene.ids[1]
	region := wm.region
	bSurface := scene.surfaces[b]

	if !g.state.SetMagnifiedPane(0, a) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", a)
	}
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the initial zoom did not settle")
	}
	homeB, ok := wm.rects[b]
	if !ok || !homeB.Valid() {
		t.Fatalf("Pane %q has no remembered split boxes: %+v", b, homeB)
	}
	homeGrid := bSurface.Grid()
	homeView := bSurface.Viewport()

	mark := len(bSurface.ResizeLog())
	if !g.state.SetMagnifiedPane(0, b) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", b)
	}
	tt.Frame()
	first := wm.run
	if first == nil || !first.zoom || first.zoomPane != b {
		t.Fatalf("the A→B run = %+v, want B's zoom", first)
	}
	fullGrid := bSurface.Grid()
	fullView := bSurface.Viewport()
	if got := resizeSince(bSurface, mark); len(got) != 1 || got[0] != fullGrid {
		t.Fatalf("A→B delivered %+v, want one resize to %+v", got, fullGrid)
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()

	if !g.state.SetMagnifiedPane(0, a) {
		t.Fatalf("the reversing SetMagnifiedPane(%q) returned false", a)
	}
	tt.Frame()
	rev := wm.run
	if rev == nil || !rev.zoom || rev.render != zoomRenderState {
		t.Fatalf("the B→A reversal = %+v, want the new final A layout", rev)
	}
	out := findVisual(t, rev, b, motion.Move)
	if !rectNearOK(out.To, homeB.Column, 0.5) {
		t.Fatalf("the outgoing B copy targets %+v, want B's remembered slot %+v", out.To, homeB.Column)
	}
	resizes := resizeSince(bSurface, mark)
	if len(resizes) != 2 || resizes[1] != homeGrid {
		t.Fatalf("the reversal delivered %v resizes since A→B began, want [full home] %+v/%+v",
			resizes, fullGrid, homeGrid)
	}
	if !viewportMatches(bSurface, homeView) {
		t.Fatalf("B's viewport after the reversal = %+v, want its pre-zoom room %+v",
			bSurface.Viewport(), homeView)
	}
	if got := wm.rects[b]; !rectNearOK(got.Body, homeB.Body, 0.5) ||
		!rectNearOK(got.Column, homeB.Column, 0.5) {
		t.Fatalf("B's recorded boxes after the reversal = %+v, want the home pair %+v", got, homeB)
	}

	// The third event interrupts the home-bound copy 16 ms in.
	tt.Advance(16 * time.Millisecond)
	tt.Frame()
	if got := resizeSince(bSurface, mark); len(got) != 2 {
		t.Fatalf("the reversal's animation frame added resizes: %v", got)
	}
	now := tt.Now()
	current, _ := rev.anim.StateAt(out, now)
	velocity := rev.anim.VelocityAt(out, now)

	beforeThird := len(bSurface.ResizeLog())
	if !g.state.SetMagnifiedPane(0, b) {
		t.Fatalf("the third SetMagnifiedPane(%q) returned false", b)
	}
	tt.Frame()
	third := wm.run
	if third == nil || !third.zoom || third.zoomPane != b {
		t.Fatalf("the third A→B run = %+v, want B's zoom", third)
	}
	if third.render != zoomRenderPane || third.renderPane != a {
		t.Fatalf("the interrupting A→B backdrop = %v/%q, want A %q",
			third.render, third.renderPane, a)
	}
	visual := findVisual(t, third, b, motion.Move)
	if !rectNearOK(visual.From, current, 0.01) {
		t.Fatalf("the third A→B copy starts at %+v, want the interrupted %+v", visual.From, current)
	}
	if !rectNearOK(visual.Velocity, velocity, 0.01) {
		t.Fatalf("the third A→B copy velocity = %+v, want the interrupted %+v",
			visual.Velocity, velocity)
	}
	thirdFull := third.final[b]
	if got := resizeSince(bSurface, beforeThird); len(got) != 1 || got[0] != fullGrid {
		t.Fatalf("the third A→B delivered %+v, want exactly one resize to the first's full grid %+v",
			got, fullGrid)
	}
	if got := bSurface.Viewport(); got.W > region.W+0.5 || got.H > region.H+0.5 {
		t.Fatalf("the third A→B pinned the out-of-window viewport %+v (region %+v)", got, region)
	}
	if !viewportMatches(bSurface, fullView) {
		t.Fatalf("the third A→B pinned %+v, want the full room %+v", bSurface.Viewport(), fullView)
	}

	calls := scene.gridCalls()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if got := resizeSince(bSurface, beforeThird); len(got) != 1 {
		t.Fatalf("animation frames added resizes: %v", got)
	}
	for id, surface := range scene.surfaces {
		if got := surface.GridForCalls() - calls[id]; got != 0 {
			t.Fatalf("Pane %s computed %d grids during the third A→B, want none", id, got)
		}
	}

	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the third A→B did not finish")
	}
	if wm.effectiveMagnified != b {
		t.Fatalf("the settled third A→B shows Pane %q, want %q", wm.effectiveMagnified, b)
	}
	if !paneRegionExists(tt, 1) || paneRegionExists(tt, 2) {
		t.Fatalf("the final frame is not B full-window")
	}
	if _, ok := tt.Find("pane-motion:" + string(b)); ok {
		t.Fatalf("a stale B overlay remains after the third A→B")
	}
	if got := resizeSince(bSurface, beforeThird); len(got) != 1 {
		t.Fatalf("the third A→B delivered %v resizes by its end, want one", got)
	}
	if got := bSurface.Grid(); got != fullGrid {
		t.Fatalf("B's final grid = %+v, want %+v", got, fullGrid)
	}
	if !viewportMatches(bSurface, fullView) {
		t.Fatalf("B's final viewport = %+v, want the full room %+v",
			bSurface.Viewport(), fullView)
	}
	if !rectNearOK(wm.rects[b].Body, thirdFull.Body, 0.5) {
		t.Fatalf("B's final record = %+v, want the full body %+v", wm.rects[b].Body, thirdFull.Body)
	}
	for i, size := range resizeSince(bSurface, mark) {
		if size != homeGrid && size != fullGrid {
			t.Fatalf("B delivered the out-of-window grid %+v at resize %d (home %+v, full %+v)",
				size, i, homeGrid, fullGrid)
		}
	}
	if got := resizeSince(bSurface, mark); len(got) != 3 {
		t.Fatalf("B delivered %v resizes across the chain, want exactly three (full, home, full)", got)
	}
}
