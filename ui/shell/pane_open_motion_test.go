package shell

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// viewWithPaneMotion is the tests' renderer: it exposes the window's motion
// runtime so a test can read the run it planned, and selects the test-only
// routes (native old-Pane transition, overlay labels).
func viewWithPaneMotion(s *state.State, mode paneMotionMode) (func(*ui.Context), *workspacePaneMotion) {
	wm := newWorkspacePaneMotion(s, mode)
	return shellView(state.RenderOwnerMain, s, wm), wm
}

// paneMotionPreferences returns a motion setting with the given switches and
// style at standard speed.
func paneMotionPreferences(enabled, paneOpen bool, style domainpreferences.MotionStyle) domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = enabled
	setting.PaneOpen = paneOpen
	setting.OpenStyle = style
	setting.Speed = domainpreferences.MotionStandard
	return setting
}

// motionGraph builds a graph whose state reports setting for motion.
func motionGraph(t *testing.T, setting domainpreferences.Motion) *realTerminalGraph {
	t.Helper()
	return newRealTerminalGraph(t, state.WithMotion(func() domainpreferences.Motion { return setting }))
}

// settlePane waits until every surface's terminal is at its own viewport's
// grid.
func settlePane(t *testing.T, tt *ui.Tester, g *realTerminalGraph, tabIndex int) []*realTerminalSurface {
	t.Helper()
	surfaces := paneSurfaces(t, g, tabIndex)
	pumpUntil(t, tt, "the settled grids", func() bool { return gridsSettled(surfaces...) })
	return surfaces
}

// startPaneOpen splits the focused Pane right and lets the view consume the
// event. It returns the two surfaces in tree order.
func startPaneOpen(t *testing.T, tt *ui.Tester, g *realTerminalGraph) []*realTerminalSurface {
	t.Helper()
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 2)
	tt.Frame()
	return paneSurfaces(t, g, 0)
}

// TestPaneOpenShrinksTheOldPaneAndEntersTheNewOne is the ticket's first
// acceptance: the split shows the old Pane shrinking and the new Pane
// entering, and the final frame is the final layout at the final grids.
func TestPaneOpenShrinksTheOldPaneAndEntersTheNewOne(t *testing.T) {
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	surfaces := settlePane(t, tt, g, 0)
	old := surfaces[0]
	oldID, _ := g.state.FocusedPaneID(0)
	oldBody, ok := wm.rects[oldID]
	if !ok || !oldBody.Valid() {
		t.Fatalf("the old Pane has no recorded boxes: %+v", oldBody)
	}
	oldGrid := old.Grid()
	old.ResetResizeLog()

	surfaces = startPaneOpen(t, tt, g)
	newID, _ := g.state.FocusedPaneID(0)
	run := wm.run
	if run == nil {
		t.Fatalf("the split did not start a Pane-open run")
	}
	if run.target != oldID || run.tabID == "" {
		t.Fatalf("run target = %q, want the split Pane %q", run.target, oldID)
	}
	if got := run.anim.Duration; got != 200*time.Millisecond {
		t.Fatalf("run duration = %s, want 200ms at standard speed", got)
	}
	oldVisual, ok := run.anim.VisualFor(oldID)
	if !ok {
		t.Fatalf("the old Pane has no visual in %+v", run.anim.Visuals)
	}
	if oldVisual.From != oldBody.Column {
		t.Fatalf("the old visual starts at %+v, want its previous whole Pane %+v", oldVisual.From, oldBody.Column)
	}
	newVisual, ok := run.anim.VisualFor(newID)
	if !ok || newVisual.Kind != motion.Enter {
		t.Fatalf("the new Pane has no entry visual: %+v", run.anim.Visuals)
	}
	if _, ok := tt.Find("pane-motion:" + string(newID)); !ok {
		t.Fatalf("the overlay of the new Pane is not built")
	}
	if got := len(old.ResizeLog()); got != 1 {
		t.Fatalf("the old Pane delivered %d resizes at the change, want 1", got)
	}
	if got := len(surfaces[1].ResizeLog()); got != 1 {
		t.Fatalf("the new Pane delivered %d resizes at the change, want 1", got)
	}
	changeCalls := []int{old.GridForCalls(), surfaces[1].GridForCalls()}
	changeLogs := []int{len(old.ResizeLog()), len(surfaces[1].ResizeLog())}
	changeViewports := []appview.Viewport{old.Viewport(), surfaces[1].Viewport()}

	tt.Advance(80 * time.Millisecond)
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("mid-run progress = %v, %v, want inside (0,1)", k, done)
	}
	rect, opacity := run.anim.State(oldVisual, k)
	if rect.W >= oldBody.Column.W || rect.W < oldVisual.To.W {
		t.Fatalf("the old Pane width at k=%v is %v, want between the final %v and the old %v",
			k, rect.W, oldVisual.To.W, oldBody.Column.W)
	}
	if opacity != 1 {
		t.Fatalf("the old Pane opacity at k=%v = %v, want 1", k, opacity)
	}
	entry, entryOpacity := run.anim.State(newVisual, k)
	if entry.W <= 0 || entryOpacity < 0 || entryOpacity > 1 {
		t.Fatalf("the new Pane entry at k=%v = %+v, opacity %v", k, entry, entryOpacity)
	}
	if got := old.GridForCalls() - changeCalls[0]; got != 0 {
		t.Errorf("the old Pane computed %d grids during animation frames, want none", got)
	}
	if got := surfaces[1].GridForCalls() - changeCalls[1]; got != 0 {
		t.Errorf("the new Pane computed %d grids during animation frames, want none", got)
	}
	if got := len(old.ResizeLog()) - changeLogs[0]; got != 0 {
		t.Errorf("the old Pane delivered %d resizes during animation frames, want none", got)
	}
	if got := len(surfaces[1].ResizeLog()) - changeLogs[1]; got != 0 {
		t.Errorf("the new Pane delivered %d resizes during animation frames, want none", got)
	}

	tt.Advance(200 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the run is still playing after its duration")
	}
	if _, ok := tt.Find("pane-motion:" + string(newID)); ok {
		t.Fatalf("the overlay is still built after the run")
	}
	for i, surface := range surfaces {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Errorf("Pane %d grid = %+v, want its final viewport's %+v", i+1, surface.Grid(), want)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("Pane %d delivered %d resizes over the run, want exactly 1 (change viewport %+v, final %+v, log %+v)",
				i+1, got, changeViewports[i], surface.Viewport(), surface.ResizeLog())
		}
	}
	if old.Grid() == oldGrid {
		t.Fatalf("the old Pane kept its pre-split grid %+v", oldGrid)
	}
	logID, _ := g.state.FocusedPaneID(0)
	if logID != newID {
		t.Fatalf("focus after the split = %q, want %q", logID, newID)
	}
}

// TestPaneOpenStylesRenderTheirGeometryAndOpacity checks all four open
// styles at one mid-run frame: the popin's fixed 87%, the slide's one-Pane
// offset, gnomed's bottom-anchored height, fade's fixed geometry.
func TestPaneOpenStylesRenderTheirGeometryAndOpacity(t *testing.T) {
	const eps = 0.5
	for _, style := range []domainpreferences.MotionStyle{
		domainpreferences.MotionPopin,
		domainpreferences.MotionSlide,
		domainpreferences.MotionGnomed,
		domainpreferences.MotionFade,
	} {
		t.Run(string(style), func(t *testing.T) {
			g := motionGraph(t, paneMotionPreferences(true, true, style))
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, g.state, 1)
			view, wm := viewWithPaneMotion(g.state, paneMotionMode{})
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			settlePane(t, tt, g, 0)

			surfaces := startPaneOpen(t, tt, g)
			run := wm.run
			if run == nil {
				t.Fatalf("style %s did not start a run", style)
			}
			newID, _ := g.state.FocusedPaneID(0)
			visual, ok := run.anim.VisualFor(newID)
			if !ok {
				t.Fatalf("style %s has no new-Pane visual", style)
			}
			final := visual.To
			tt.Advance(60 * time.Millisecond)
			k, done := run.anim.Progress(tt.Now())
			if done || k <= 0 || k >= 1 {
				t.Fatalf("style %s progress = %v, %v", style, k, done)
			}
			rect, opacity := run.anim.State(visual, k)
			switch style {
			case domainpreferences.MotionPopin:
				if want := final.W * (0.87 + 0.13*k); abs32(rect.W-want) > eps {
					t.Fatalf("popin width = %v, want %v (87%% at k=0)", rect.W, want)
				}
				if abs32(rect.X+rect.W/2-(final.X+final.W/2)) > eps {
					t.Fatalf("popin is not centered: %+v vs %+v", rect, final)
				}
				if abs32(opacity-k) > 0.01 {
					t.Fatalf("popin opacity = %v, want %v", opacity, k)
				}
			case domainpreferences.MotionSlide:
				if rect.W != final.W || rect.H != final.H {
					t.Fatalf("slide size = %vx%v, want the final %vx%v", rect.W, rect.H, final.W, final.H)
				}
				// The new Pane is the right half, so it slides in from the
				// content's right edge.
				if abs32(rect.X-(final.X+final.W*(1-k))) > eps {
					t.Fatalf("slide X = %v, want %v", rect.X, final.X+final.W*(1-k))
				}
				if opacity != 1 {
					t.Fatalf("slide opacity = %v, want an explicit 1", opacity)
				}
			case domainpreferences.MotionGnomed:
				if rect.W != final.W || abs32(rect.H-final.H*k) > eps {
					t.Fatalf("gnomed = %+v, want width %v and height %v", rect, final.W, final.H*k)
				}
				if abs32(rect.Y+rect.H-(final.Y+final.H)) > eps {
					t.Fatalf("gnomed bottom = %v, want %v", rect.Y+rect.H, final.Y+final.H)
				}
				if opacity != 1 {
					t.Fatalf("gnomed opacity = %v, want an explicit 1", opacity)
				}
			case domainpreferences.MotionFade:
				if rect != final {
					t.Fatalf("fade geometry = %+v, want %+v", rect, final)
				}
				if abs32(opacity-k) > 0.01 {
					t.Fatalf("fade opacity = %v, want %v", opacity, k)
				}
			}
			tt.Advance(200 * time.Millisecond)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("style %s did not finish", style)
			}
			for i, surface := range surfaces {
				want, ok := appview.GridFor(surface.Viewport())
				if !ok || surface.Grid() != want {
					t.Errorf("style %s Pane %d grid = %+v, want %+v", style, i+1, surface.Grid(), want)
				}
			}
		})
	}
}

// TestPaneOpenWarpsWhenMotionIsOff covers the three switch-offs: the master
// switch, the PaneOpen switch, and the desktop's Reduce Motion. Each warps to
// the final layout with no run, no overlay, no animation frame, and still
// exactly one PTY resize per affected Pane.
func TestPaneOpenWarpsWhenMotionIsOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting domainpreferences.Motion
		reduce  bool
	}{
		{"master-off", paneMotionPreferences(false, true, domainpreferences.MotionPopin), false},
		{"pane-open-off", paneMotionPreferences(true, false, domainpreferences.MotionPopin), false},
		{"reduce-motion", paneMotionPreferences(true, true, domainpreferences.MotionPopin), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := motionGraph(t, tc.setting)
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, g.state, 1)
			view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			if tc.reduce {
				tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			}
			surfaces := settlePane(t, tt, g, 0)
			for _, surface := range surfaces {
				surface.ResetResizeLog()
			}

			surfaces = startPaneOpen(t, tt, g)
			if wm.run != nil {
				t.Fatalf("a run started with motion off")
			}
			if wm.animationFrames != 0 {
				t.Fatalf("the warp asked for %d animation frames", wm.animationFrames)
			}
			newID, _ := g.state.FocusedPaneID(0)
			if _, ok := tt.Find("pane-motion:" + string(newID)); ok {
				t.Fatalf("the warp built an overlay")
			}
			pumpUntil(t, tt, "the warped final grids", func() bool { return gridsSettled(surfaces...) })
			for i, surface := range surfaces {
				if got := len(surface.ResizeLog()); got != 1 {
					t.Errorf("Pane %d delivered %d resizes, want exactly the final one", i+1, got)
				}
				want, ok := appview.GridFor(surface.Viewport())
				if !ok || surface.Grid() != want {
					t.Errorf("Pane %d grid = %+v, want %+v", i+1, surface.Grid(), want)
				}
			}
			tt.Advance(250 * time.Millisecond)
			if wm.run != nil {
				t.Fatalf("a warp started a run later")
			}
		})
	}
}

// TestPaneOpenInterruptionStartsFromTheCurrentVisual splits again during the
// first run, early and mid. The newest event wins, and the target's new move
// starts from where and how opaque the user saw it, not from its final layout
// or full opacity.
func TestPaneOpenInterruptionStartsFromTheCurrentVisual(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
	}{
		{"early", 10 * time.Millisecond},
		{"mid", 80 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, g.state, 1)
			view, wm := viewWithPaneMotion(g.state, paneMotionMode{})
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			settlePane(t, tt, g, 0)

			startPaneOpen(t, tt, g)
			first := wm.run
			if first == nil {
				t.Fatalf("the first split did not start a run")
			}
			tt.Advance(tc.elapsed)
			k, done := first.anim.Progress(tt.Now())
			if done || k <= 0 || k >= 1 {
				t.Fatalf("first run progress at %s = %v, %v", tc.elapsed, k, done)
			}
			target, _ := g.state.FocusedPaneID(0)
			visual, ok := first.anim.VisualFor(target)
			if !ok {
				t.Fatalf("the first run has no visual for the focused Pane")
			}
			seen, seenOpacity := first.anim.State(visual, k)
			// Popin fades in with its curve, so both interrupts happen while
			// the target is partly transparent. An interrupt that restarts at
			// opacity 1 would jump to fully opaque.
			if seenOpacity <= 0.01 || seenOpacity >= 0.99 {
				t.Fatalf("the visible opacity at %s = %v, want an in-flight popin value", tc.elapsed, seenOpacity)
			}

			if err := g.state.SplitDown(); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			second := wm.run
			if second == nil || second == first {
				t.Fatalf("the second split did not replace the run")
			}
			if second.seq <= first.seq {
				t.Fatalf("run sequence went backwards: %d then %d", first.seq, second.seq)
			}
			moved, ok := second.anim.VisualFor(target)
			if !ok {
				t.Fatalf("the second run does not move the split target")
			}
			if abs32(moved.From.X-seen.X) > 1 || abs32(moved.From.Y-seen.Y) > 1 ||
				abs32(moved.From.W-seen.W) > 1 || abs32(moved.From.H-seen.H) > 1 {
				t.Fatalf("the interrupting move starts at %+v, want the visible %+v", moved.From, seen)
			}
			if moved.From == moved.To {
				t.Fatalf("the interrupting move starts at its target %+v", moved.From)
			}
			if abs32(moved.FromOpacity-seenOpacity) > 0.001 {
				t.Fatalf("the interrupting move starts at opacity %v, want the visible %v",
					moved.FromOpacity, seenOpacity)
			}
			if moved.ToOpacity != 1 {
				t.Fatalf("the interrupting move fades to opacity %v, want 1", moved.ToOpacity)
			}
			// The interrupting run's first frame paints the carried opacity,
			// so the overlay's copy does not snap when it replaces the old one.
			startK, startDone := second.anim.Progress(tt.Now())
			if startDone || startK != 0 {
				t.Fatalf("the interrupting run starts at progress %v, %v", startK, startDone)
			}
			if _, startOpacity := second.anim.State(moved, startK); abs32(startOpacity-seenOpacity) > 0.001 {
				t.Fatalf("the interrupting run's first frame paints opacity %v, want the visible %v",
					startOpacity, seenOpacity)
			}

			tt.Advance(250 * time.Millisecond)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("the interrupting run did not finish")
			}
			root, _, _ := g.state.TabTree(0)
			if root == nil || len(motionPaneIDs(root)) != 3 {
				t.Fatalf("the Tab does not hold the three Panes after the interruption")
			}
			for i, surface := range paneSurfaces(t, g, 0) {
				want, ok := appview.GridFor(surface.Viewport())
				if !ok || surface.Grid() != want {
					t.Errorf("Pane %d grid after interruption = %+v, want %+v", i+1, surface.Grid(), want)
				}
			}
		})
	}
}

// paneOpenMidRun is the instant both routes are observed at: 60 ms into the
// 200 ms open, with the run's eased progress inside (0,1).
const paneOpenMidRun = 60 * time.Millisecond

// paneOpenScene is one Pane-open run over the same real nested split scene:
// the settled old-Pane boxes, the split's surfaces, and the run paused at
// paneOpenMidRun.
type paneOpenScene struct {
	tt       *ui.Tester
	graph    *realTerminalGraph
	wm       *workspacePaneMotion
	surfaces []*realTerminalSurface
	oldID    model.PaneID
	run      *paneMotionRun
	start    motion.PaneRects
	k        float32
}

// startPaneOpenScene builds the same real nested split scene in mode and
// stops it at paneOpenMidRun. The old Pane's start boxes are the settled
// frame before the split; the run then delivers its one resize per Pane at
// the change.
func startPaneOpenScene(t *testing.T, mode paneMotionMode) *paneOpenScene {
	t.Helper()
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, mode)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	// Count only the resizes the split delivers: the initial terminal
	// creation may already have delivered a default size.
	for _, surface := range settlePane(t, tt, g, 0) {
		surface.ResetResizeLog()
	}
	oldID, _ := g.state.FocusedPaneID(0)
	start, ok := wm.rects[oldID]
	if !ok || !start.Valid() {
		t.Fatalf("mode %+v recorded no old-Pane boxes: %+v", mode, start)
	}
	surfaces := startPaneOpen(t, tt, g)
	run := wm.run
	if run == nil {
		t.Fatalf("mode %+v did not start a Pane-open run", mode)
	}
	if run.native != mode.native || run.target != oldID {
		t.Fatalf("mode %+v run = native %v, target %q; want native %v, target %q",
			mode, run.native, run.target, mode.native, oldID)
	}
	tt.Advance(paneOpenMidRun)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("mode %+v progress at %s = %v, %v, want inside (0,1)",
			mode, paneOpenMidRun, k, done)
	}
	return &paneOpenScene{
		tt: tt, graph: g, wm: wm, surfaces: surfaces,
		oldID: oldID, run: run, start: start, k: k,
	}
}

// TestPaneOpenOldPaneShrinkFallsBackToTheOverlayAcrossTheNewSplit is ticket
// 16's acceptance criterion 4. Ticket 04 measured that MyGo's native Match
// cannot carry the old Pane's whole-box shrink across the split the change
// just created: the new split clips the shrinking side, so the real Pane's
// visible box is already its final slot from the first frame and no
// whole-Pane shrink shows. Ticket 16's Note accepts the opaque overlay
// fallback, which is production's route. This test observes both routes at
// the same mid-run instant on the same real split, then asserts both end at
// the same final boxes, viewports and grids with exactly one PTY resize per
// Pane.
func TestPaneOpenOldPaneShrinkFallsBackToTheOverlayAcrossTheNewSplit(t *testing.T) {
	overlay := startPaneOpenScene(t, paneMotionMode{labels: true})
	native := startPaneOpenScene(t, paneMotionMode{native: true, labels: true})
	if abs32(overlay.k-native.k) > 0.001 {
		t.Fatalf("the routes were sampled at different progress: overlay %v, native %v", overlay.k, native.k)
	}

	// The production route: the old Pane's opaque copy is strictly between
	// the box the settled frame showed and its final slot at the same
	// instant. A loose epsilon asserts movement, not an easing value.
	overlayFinal := overlay.run.final[overlay.oldID].Column
	if overlay.start.Column.W <= overlayFinal.W {
		t.Fatalf("the old Pane's start %+v is not wider than its final %+v",
			overlay.start.Column, overlayFinal)
	}
	overlayCopy, ok := overlay.tt.Find("pane-motion:" + string(overlay.oldID))
	if !ok {
		t.Fatalf("the overlay route built no old-Pane copy")
	}
	// The sample's eased width is about 596 DIP against a 892->441.5 move;
	// 50 DIP margins show movement without pinning an easing value.
	if overlayCopy.W <= overlayFinal.W+50 || overlayCopy.W >= overlay.start.Column.W-50 {
		t.Fatalf("the old Pane's overlay copy at k=%v is %v wide, want between the final %v and the old %v",
			overlay.k, overlayCopy.W, overlayFinal.W, overlay.start.Column.W)
	}

	// The native route at the same instant: the Pane's recorded box is its
	// final box, and its visible box (the PaneRegion label, clipped to the new
	// split's slot) already fills that slot instead of starting from the old
	// width. Ticket 04's limitation, and the reason production overlays the
	// old Pane instead.
	nativeFinal := native.run.final[native.oldID]
	if ids := motionPaneIDsMust(t, native.graph); ids[0] != native.oldID {
		t.Fatalf("the old Pane %q is not the first Pane %q, so PaneRegion(1) is not it", native.oldID, ids[0])
	}
	nativeLabel, ok := native.tt.Find(locales.In("en").PaneRegion(1))
	if !ok {
		t.Fatalf("the native route has no PaneRegion label")
	}
	nativeBox := native.wm.rects[native.oldID].Column
	if !rectNearOK(nativeBox, nativeFinal.Column, 1) {
		t.Fatalf("the native real Pane's box at k=%v = %+v, want its final %+v",
			native.k, nativeBox, nativeFinal.Column)
	}
	if abs32(nativeLabel.X-nativeFinal.Column.X) > 1 || abs32(nativeLabel.Y-nativeFinal.Column.Y) > 1 ||
		abs32(nativeLabel.H-nativeFinal.Column.H) > 1 {
		t.Fatalf("the native Pane's visible box at k=%v = %+v, want the final %+v",
			native.k, nativeLabel, nativeFinal.Column)
	}
	if nativeLabel.X+nativeLabel.W > nativeFinal.Slot.X+nativeFinal.Slot.W+1 {
		t.Fatalf("the native Pane's visible box at k=%v = %+v leaves its new slot %+v",
			native.k, nativeLabel, nativeFinal.Slot)
	}
	// The 4-DIP slack is the split's right drop gutter: the visible label
	// reaches the slot's edge, 4 DIP past the final Pane box. The real
	// geometry is the final one; nothing of the old 892-DIP width shows.
	if nativeLabel.W < nativeFinal.Column.W-1 || nativeLabel.W > nativeFinal.Column.W+5 {
		t.Fatalf("the native Pane's visible width at k=%v = %v, want its final %v within the split's gutter",
			native.k, nativeLabel.W, nativeFinal.Column.W)
	}
	if native.start.Column.W-nativeLabel.W <= 100 {
		t.Fatalf("the native Pane's visible width at k=%v = %v, too close to the old %v",
			native.k, nativeLabel.W, native.start.Column.W)
	}
	// The same scene and instant, side by side: the overlay copy is visibly
	// mid-shrink while the native Pane is already in its slot.
	if overlayCopy.W-nativeLabel.W <= 50 {
		t.Fatalf("at k=%v the overlay copy is %v wide and the native Pane %v; want the overlay mid-shrink past the native box",
			overlay.k, overlayCopy.W, nativeLabel.W)
	}

	// The visible Pane reaches into the split's 4-DIP drop gutter past its own
	// recorded box, so the label is the slot's visible edge, not a stale copy
	// laid out at the old width. MyGo exposes no accessor for the transition
	// attached to a built element, so the helper's fields are asserted next.
	if nativeLabel.W <= nativeBox.W+1 {
		t.Fatalf("the native Pane's label at k=%v is %v wide, not the final box %v; the Pane does not fill its new slot",
			native.k, nativeLabel.W, nativeBox)
	}
	if _, ok := native.tt.Find("pane-motion:" + string(native.oldID)); ok {
		t.Fatalf("the native route built an old-Pane copy, so this is not the native route")
	}

	transition, ok := native.wm.nativeTransition(native.oldID)
	if !ok {
		t.Fatalf("the native mode does not build the old Pane's transition")
	}
	if transition.Duration != 200*time.Millisecond || !transition.Position || !transition.Size ||
		transition.ScaleContent || transition.Match != (motion.PaneMatch{ID: native.oldID}) {
		t.Fatalf("native transition = %+v, want a 200ms Match move laid out at each box of %q",
			transition, native.oldID)
	}

	// Both routes end at the same final frame. Let the native Match's layout
	// residue settle before comparing the labels exactly.
	for _, scene := range []*paneOpenScene{overlay, native} {
		scene.tt.Advance(400 * time.Millisecond)
		scene.tt.Frame()
		if scene.wm.run != nil {
			t.Fatalf("the run did not finish")
		}
		if ids := motionPaneIDsMust(t, scene.graph); len(ids) != 2 {
			t.Fatalf("the Tab holds %d Panes after the split, want 2", len(ids))
		}
	}
	if got, want := len(native.surfaces), len(overlay.surfaces); got != want || got != 2 {
		t.Fatalf("native surfaces = %d, overlay = %d, want 2 each", got, want)
	}
	for i := range overlay.surfaces {
		a, b := overlay.surfaces[i], native.surfaces[i]
		if a.Viewport() != b.Viewport() || a.Grid() != b.Grid() {
			t.Fatalf("Pane %d final frame differs: overlay %+v/%+v, native %+v/%+v",
				i+1, a.Viewport(), a.Grid(), b.Viewport(), b.Grid())
		}
		for _, scene := range []*paneOpenScene{overlay, native} {
			if got := len(scene.surfaces[i].ResizeLog()); got != 1 {
				t.Errorf("Pane %d delivered %d resizes over the run, want exactly 1", i+1, got)
			}
		}
	}
	for i := 1; i <= 2; i++ {
		label := locales.In("en").PaneRegion(i)
		gotRect, gotOK := native.tt.Find(label)
		wantRect, wantOK := overlay.tt.Find(label)
		if gotRect != wantRect || gotOK != wantOK {
			t.Fatalf("Pane %d final box differs: native %+v/%v, overlay %+v/%v",
				i, gotRect, gotOK, wantRect, wantOK)
		}
	}
}

// TestPaneOpenPendingPlaceholderSwapsWithoutReplay keeps the new Session
// pending through the start of the run: the placeholder animates, and when
// the surface arrives the same run continues with content instead of
// restarting. The failure case keeps the error Pane.
func TestPaneOpenPendingPlaceholderSwapsWithoutReplay(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		factory := &gatedTransportFactory{release: make(chan struct{})}
		g, wm, tt := newControlledMotionGraph(t, factory)
		// The new Session is gated, so the split's surfaces are not both
		// there yet; consume the event and keep the placeholder.
		if err := g.state.SplitRight(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		run := wm.run
		if run == nil {
			t.Fatalf("the pending split did not start a run")
		}
		started := run.anim.Started
		newID := run.anim.Visuals[len(run.anim.Visuals)-1].Pane
		if info, ok := g.state.PaneInfoAt(0, newID); !ok || !info.Pending {
			t.Fatalf("the new Pane is not pending before its release")
		}
		if !tt.HasText(locales.In("en").PaneConnecting()) {
			t.Fatalf("the pending Pane's placeholder is not drawn")
		}
		close(factory.release)
		pumpUntil(t, tt, "the new Pane's surface", func() bool {
			info, ok := g.state.PaneInfoAt(0, newID)
			return ok && info.Surface != nil
		})
		if wm.run != run {
			t.Fatalf("the ready swap restarted the run")
		}
		if !wm.run.anim.Started.Equal(started) {
			t.Fatalf("the ready swap rewound the run")
		}
		tt.Advance(250 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the run did not finish after the swap")
		}
		for i, surface := range paneSurfaces(t, g, 0) {
			want, ok := appview.GridFor(surface.Viewport())
			if !ok || surface.Grid() != want {
				t.Errorf("Pane %d grid after the swap = %+v, want %+v", i+1, surface.Grid(), want)
			}
		}
	})
	t.Run("failure", func(t *testing.T) {
		factory := &failingTransportFactory{}
		g, wm, tt := newControlledMotionGraph(t, factory)
		if err := g.state.SplitRight(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		run := wm.run
		if run == nil {
			t.Fatalf("the failing split did not start a run")
		}
		newID := run.anim.Visuals[len(run.anim.Visuals)-1].Pane
		pumpUntil(t, tt, "the failed Pane", func() bool {
			info, ok := g.state.PaneInfoAt(0, newID)
			return ok && info.Err != nil
		})
		if wm.run != run {
			t.Fatalf("the failure restarted the run")
		}
		if info, ok := g.state.PaneInfoAt(0, newID); !ok || info.Err == nil || info.Surface != nil {
			t.Fatalf("the failed Pane lost its error: %+v", info)
		}
		tt.Advance(250 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the run did not finish after the failure")
		}
		// An untyped failure maps to the shell's internal-connection text, the
		// same message the overlay showed while the run played.
		if !tt.HasText(locales.In("en").ConnectionErrorInternal()) {
			t.Fatalf("the failed Pane's error is not shown")
		}
	})
}

// TestPaneOpenKeepsFocusAndTypingDuringTheRun is the keyboard regression: the
// moving copy is inert, so the real Pane keeps the live terminal, the keyboard,
// and the pointer while the mask covers it. A key typed mid-run must reach the
// focused Pane's terminal; the overlay must neither drop it nor send it to the
// Pane that was split.
func TestPaneOpenKeepsFocusAndTypingDuringTheRun(t *testing.T) {
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	oldID, _ := g.state.FocusedPaneID(0)
	oldSurface := surfaceForPane(t, g, 0, oldID)
	pumpUntil(t, tt, "the focused terminal", func() bool { return oldSurface.LiveFocused() })
	oldSurface.transport.reset()

	startPaneOpen(t, tt, g)
	run := wm.run
	if run == nil {
		t.Fatalf("the split did not start a run")
	}
	newID, _ := g.state.FocusedPaneID(0)
	newSurface := surfaceForPane(t, g, 0, newID)
	if newSurface == oldSurface {
		t.Fatalf("the split did not create a second surface")
	}
	if !newSurface.LiveFocused() {
		t.Fatalf("the run took the keyboard from the real Pane: no terminal input box is focused")
	}
	if oldSurface.LiveFocused() {
		t.Fatalf("the old Pane's terminal still holds the keyboard during the run")
	}

	// The copy covers the new Pane at its final box. A press there must pass
	// through the mask and the overlay and reach the real Pane's terminal.
	body := run.final[newID].Body
	tt.ClickAt(body.X+body.W/2, body.Y+body.H/2)
	if !newSurface.LiveFocused() || oldSurface.LiveFocused() {
		t.Fatalf("a press during the run moved the keyboard off the real Pane")
	}

	// Type while the run is still playing: the Tester clock does not advance
	// across Type, so the run cannot have finished.
	const text = "motion-key"
	tt.Type(text)
	pumpUntil(t, tt, "the key typed during the run", func() bool {
		return bytes.Contains(newSurface.transport.input(), []byte(text))
	})
	if wm.run == nil {
		t.Fatalf("the run finished before the typed key could be asserted")
	}
	if !newSurface.LiveFocused() {
		t.Fatalf("typing during the run lost the keyboard")
	}
	if bytes.Contains(oldSurface.transport.input(), []byte(text)) {
		t.Fatalf("the key typed during the run went to the Pane that was split: %q",
			oldSurface.transport.input())
	}
}

// TestPaneOpenWarpsWhenTheWindowWasHidden is the hidden-view direction: an
// event recorded while the window was hidden warps when the view next draws,
// even when it returns quickly. Visibility is explicit, so no wall clock can
// make a quick return replay an animation the user never saw.
func TestPaneOpenWarpsWhenTheWindowWasHidden(t *testing.T) {
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	surfaces := settlePane(t, tt, g, 0)
	for _, surface := range surfaces {
		surface.transport.reset()
	}

	// The hide frame makes the view's last visibility state hidden. The split
	// then happens out of sight; the view returns quickly when shown.
	tt.SetWindowHidden(true)
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 2)
	tt.Advance(48 * time.Millisecond)
	tt.SetWindowHidden(false)

	if wm.run != nil {
		t.Fatalf("a hidden view replayed the Pane-open it did not see")
	}
	if wm.animationFrames != 0 {
		t.Fatalf("the hidden warp asked for %d animation frames", wm.animationFrames)
	}
	newID, _ := g.state.FocusedPaneID(0)
	if _, ok := tt.Find("pane-motion:" + string(newID)); ok {
		t.Fatalf("the hidden warp built a moving copy")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(newID)); ok {
		t.Fatalf("the hidden warp built a mask")
	}
	if event, ok := g.state.MotionEvent(); ok && wm.controller.Unseen(event.Seq) {
		t.Fatalf("the hidden view did not catch its lastSeq up")
	}

	surfaces = paneSurfaces(t, g, 0)
	pumpUntil(t, tt, "the warped final grids", func() bool { return gridsSettled(surfaces...) })
	for i, surface := range surfaces {
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("Pane %d delivered %d resizes after the hidden warp, want exactly 1", i+1, got)
		}
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Errorf("Pane %d grid = %+v, want %+v", i+1, surface.Grid(), want)
		}
	}
}

// TestPaneOpenAnimatesWhenTheFirstVisibleFrameIsLate is the visible-view
// direction: a view that was on screen when the change happened animates the
// event it saw, however late its frame. The 150 ms delay is the scenario's
// input (the removed heuristic warped past 100 ms), not a synchronization
// wait.
func TestPaneOpenAnimatesWhenTheFirstVisibleFrameIsLate(t *testing.T) {
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)

	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	tt.Frame()

	run := wm.run
	if run == nil {
		t.Fatalf("a visible view with a late frame warped instead of animating")
	}
	event, ok := g.state.MotionEvent()
	if !ok || run.seq != event.Seq {
		t.Fatalf("the run seq = %d, want the event's %d", run.seq, event.Seq)
	}
	if run.anim.Duration != 200*time.Millisecond {
		t.Fatalf("the late run duration = %s, want 200ms", run.anim.Duration)
	}
	if _, ok := tt.Find("pane-motion:" + string(event.PaneID)); !ok {
		t.Fatalf("the late run built no moving copy")
	}
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the late run did not finish")
	}
}

// TestPaneOpenAnimatesWholePanesInEveryOpenStyle is ticket 16's whole-Pane
// contract: the frame and header animate with the body, for the old Pane and
// the new one, in all four styles. The copy is laid out at the final column and
// mapped to the style's rect, so each header/frame box is the mapped final box;
// fade keeps geometry and fades the whole copy.
func TestPaneOpenAnimatesWholePanesInEveryOpenStyle(t *testing.T) {
	for _, style := range []domainpreferences.MotionStyle{
		domainpreferences.MotionPopin,
		domainpreferences.MotionSlide,
		domainpreferences.MotionGnomed,
		domainpreferences.MotionFade,
	} {
		t.Run(string(style), func(t *testing.T) {
			g := motionGraph(t, paneMotionPreferences(true, true, style))
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, g.state, 1)
			view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			settlePane(t, tt, g, 0)

			surfaces := startPaneOpen(t, tt, g)
			run := wm.run
			if run == nil {
				t.Fatalf("style %s did not start a run", style)
			}
			tt.Advance(60 * time.Millisecond)
			tt.Frame()
			k, done := run.anim.Progress(tt.Now())
			if done || k <= 0 || k >= 1 {
				t.Fatalf("style %s progress = %v, %v", style, k, done)
			}
			frameTop, frameRight, _, frameLeft := kit.PaneFrameInset()
			if len(run.anim.Visuals) != 2 {
				t.Fatalf("style %s has %d visuals, want the old and the new Pane", style, len(run.anim.Visuals))
			}
			for _, v := range run.anim.Visuals {
				end, ok := run.final[v.Pane]
				if !ok {
					t.Fatalf("style %s Pane %s has no final boxes", style, v.Pane)
				}
				rect, opacity := run.anim.State(v, k)
				// A label partly outside the window reports only its visible
				// part: a slide's first half is off the edge.
				visible := clipToWindow(rect)

				// The mask covers the real Pane at its final column while the
				// real Pane stays live and focused underneath.
				mask, ok := tt.Find("pane-motion-mask:" + string(v.Pane))
				if !ok {
					t.Fatalf("style %s Pane %s has no mask", style, v.Pane)
				}
				rectNear(t, mask, clipToWindow(end.Column), 0.5, "style "+string(style)+" mask")
				copyRect, ok := tt.Find("pane-motion:" + string(v.Pane))
				if !ok {
					t.Fatalf("style %s Pane %s has no moving copy", style, v.Pane)
				}
				rectNear(t, copyRect, visible, 0.5, "style "+string(style)+" copy")

				// The frame is the whole copy, and the header is the final
				// header box mapped by the same transform, so both move,
				// scale, or stay with the body instead of waiting at final.
				frame, ok := tt.Find("pane-motion-frame:" + string(v.Pane))
				if !ok {
					t.Fatalf("style %s Pane %s has no animated frame", style, v.Pane)
				}
				rectNear(t, frame, visible, 0.5, "style "+string(style)+" frame")
				header, ok := tt.Find("pane-motion-header:" + string(v.Pane))
				if !ok {
					t.Fatalf("style %s Pane %s has no animated header", style, v.Pane)
				}
				headerFinal := ui.Rect{
					X: end.Column.X + frameLeft,
					Y: end.Column.Y + frameTop,
					W: end.Column.W - frameLeft - frameRight,
					H: end.Body.Y - end.Column.Y - frameTop,
				}
				if headerFinal.H <= 0 {
					t.Fatalf("style %s Pane %s has no header room: %+v", style, v.Pane, headerFinal)
				}
				kx, ky := float32(1), float32(1)
				if end.Column.W > 0 {
					kx = rect.W / end.Column.W
				}
				if end.Column.H > 0 {
					ky = rect.H / end.Column.H
				}
				wantHeader := ui.Rect{
					X: rect.X + (headerFinal.X-end.Column.X)*kx,
					Y: rect.Y + (headerFinal.Y-end.Column.Y)*ky,
					W: headerFinal.W * kx,
					H: headerFinal.H * ky,
				}
				rectNear(t, header, clipToWindow(wantHeader), 0.75, "style "+string(style)+" header")

				if v.Kind == motion.Enter && style == domainpreferences.MotionFade {
					// Fade keeps geometry still; only the copy's opacity
					// moves. Every other copy (and every old Pane's move)
					// must leave its final header box.
					rectNear(t, header, clipToWindow(headerFinal), 0.5, "style fade fixed header")
				} else if rectNearOK(header, clipToWindow(headerFinal), 0.5) {
					t.Fatalf("style %s Pane %s header stayed at its final box %+v (whole pane %+v)",
						style, v.Pane, header, rect)
				}

				if v.Kind == motion.Enter {
					switch style {
					case domainpreferences.MotionPopin, domainpreferences.MotionFade:
						if abs32(opacity-k) > 0.01 {
							t.Fatalf("style %s copy opacity = %v, want %v", style, opacity, k)
						}
					default:
						if opacity != 1 {
							t.Fatalf("style %s copy opacity = %v, want an explicit 1", style, opacity)
						}
					}
				} else if opacity != 1 {
					t.Fatalf("the old Pane's move opacity = %v, want 1", opacity)
				}
			}

			tt.Advance(250 * time.Millisecond)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("style %s did not finish", style)
			}
			for i, surface := range surfaces {
				want, ok := appview.GridFor(surface.Viewport())
				if !ok || surface.Grid() != want {
					t.Errorf("style %s Pane %d grid = %+v, want %+v", style, i+1, surface.Grid(), want)
				}
			}
		})
	}
}

// surfaceForPane returns the test surface of one Pane in a Tab.
func surfaceForPane(t *testing.T, g *realTerminalGraph, tabIndex int, id model.PaneID) *realTerminalSurface {
	t.Helper()
	root, _, ok := g.state.TabTree(tabIndex)
	if !ok || root == nil {
		t.Fatalf("no Tab at index %d", tabIndex)
	}
	ids := motionPaneIDs(root)
	surfaces := paneSurfaces(t, g, tabIndex)
	for i, paneID := range ids {
		if paneID == id {
			return surfaces[i]
		}
	}
	t.Fatalf("Pane %s is not in Tab %d", id, tabIndex)
	return nil
}

// rectNearOK reports that every edge of got is within eps of want.
func rectNearOK(got, want ui.Rect, eps float32) bool {
	return abs32(got.X-want.X) <= eps && abs32(got.Y-want.Y) <= eps &&
		abs32(got.W-want.W) <= eps && abs32(got.H-want.H) <= eps
}

// clipToWindow intersects a box with the 900x600 test window. A label whose
// element is partly off screen reports only the visible part.
func clipToWindow(r ui.Rect) ui.Rect {
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, 900), min(r.Y+r.H, 600)
	if x1 < x0 {
		x1 = x0
	}
	if y1 < y0 {
		y1 = y0
	}
	return ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// rectNear fails when got is not the expected box.
func rectNear(t *testing.T, got, want ui.Rect, eps float32, what string) {
	t.Helper()
	if !rectNearOK(got, want, eps) {
		t.Fatalf("%s = %+v, want %+v (eps %v)", what, got, want, eps)
	}
}

// gatedTransportFactory opens the first Session at once and keeps later ones
// pending until release is closed, so a test can see the placeholder.
type gatedTransportFactory struct {
	opened  int
	release chan struct{}
}

func (f *gatedTransportFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	f.opened++
	if f.opened == 1 {
		return newFakeTransport(), nil
	}
	<-f.release
	return newFakeTransport(), nil
}

// failingTransportFactory opens the first Session and fails every later one.
type failingTransportFactory struct {
	opened int
}

func (f *failingTransportFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	f.opened++
	if f.opened == 1 {
		return newFakeTransport(), nil
	}
	return nil, errors.New("test: the second Session failed")
}

// newControlledMotionGraph builds a graph whose Session transports come from
// factory, opens one Tab, and settles its surface. The returned tester uses
// the production overlay route.
func newControlledMotionGraph(t *testing.T, factory appsession.Factory) (*realTerminalGraph, *workspacePaneMotion, *ui.Tester) {
	t.Helper()
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	surfaces := &realTerminalSurfaceFactory{}
	st := state.NewState(state.WithSessions(service, surfaces.open))
	t.Cleanup(func() {
		for _, surface := range surfaces.all() {
			surface.Close()
		}
	})
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	view, wm := viewWithPaneMotion(st, paneMotionMode{})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	g := &realTerminalGraph{state: st, factory: surfaces}
	waitForRealSurfaces(t, st, 1)
	settlePane(t, tt, g, 0)
	return g, wm, tt
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// motionPaneIDs lists a tree's Pane IDs in tree order.
func motionPaneIDs(root *domainworkspace.Node) []model.PaneID {
	return domainworkspace.CollectPaneIDs(root)
}

// motionPaneIDsMust lists the active Tab's Pane IDs, failing when the Tab is
// missing.
func motionPaneIDsMust(t *testing.T, g *realTerminalGraph) []model.PaneID {
	t.Helper()
	root, _, ok := g.state.TabTree(0)
	if !ok || root == nil {
		t.Fatalf("the Tab tree is missing")
	}
	ids := motionPaneIDs(root)
	if len(ids) == 0 {
		t.Fatalf("the Tab tree holds no Panes")
	}
	return ids
}
