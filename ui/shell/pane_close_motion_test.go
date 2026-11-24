package shell

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// paneClosePreferences returns a motion setting whose Pane close animates with
// style at standard speed, and whose Pane open stays on so a test can build a
// split through the same real motion path.
func paneClosePreferences(enabled, paneClose bool, style domainpreferences.MotionStyle) domainpreferences.Motion {
	setting := paneMotionPreferences(enabled, true, style)
	setting.PaneClose = paneClose
	setting.CloseStyle = style
	return setting
}

// splitAndSettle splits the only Pane right, finishes the open run, and waits
// until both terminals are at their split viewports' grids.
func splitAndSettle(t *testing.T, tt *ui.Tester, g *realTerminalGraph, wm *workspacePaneMotion) []*realTerminalSurface {
	t.Helper()
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 2)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the split's open run did not finish")
	}
	surfaces := paneSurfaces(t, g, 0)
	pumpUntil(t, tt, "the settled split grids", func() bool { return gridsSettled(surfaces...) })
	return surfaces
}

// TestPaneCloseShrinksTheSurvivorAndGhostsTheClosedPane is ticket 17's first
// acceptance: closing a non-last Pane records a PaneClose for the closed Pane,
// the survivor expands while the exiting Pane leaves as an opaque ghost drawn
// from its still-living surface, and the final frame is the final layout at
// the final grids with exactly one PTY resize for the survivor.
//
// This is the snapshot fallback on a real terminal surface. MyGo v0.9.2's
// ui.NewBitmap only wraps an image.Image the caller already owns; it offers no
// application-level raster capture of a Pane frame (the sole frame image
// accessor is the test harness's Tester.Image), so production cannot snapshot
// the terminal. The test feeds real screen output before the close, proves the
// ghost draws it from the retained live surface, and proves the surface is
// destroyed only after the run, exactly once.
func TestPaneCloseShrinksTheSurvivorAndGhostsTheClosedPane(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := splitAndSettle(t, tt, g, wm)
	if len(surfaces) != 2 {
		t.Fatalf("the split has %d surfaces, want 2", len(surfaces))
	}

	closedID, _ := g.state.FocusedPaneID(0)
	ids := motionPaneIDsMust(t, g)
	if len(ids) != 2 || ids[0] != closedID && ids[1] != closedID {
		t.Fatalf("the focused Pane %q is not in %v", closedID, ids)
	}
	survivorID := ids[0]
	if survivorID == closedID {
		survivorID = ids[1]
	}
	closedSurface := surfaceForPane(t, g, 0, closedID)
	survivorSurface := surfaceForPane(t, g, 0, survivorID)
	if closedSurface == survivorSurface {
		t.Fatalf("the split Panes share a surface")
	}
	closedOld, ok := wm.rects[closedID]
	if !ok || !closedOld.Valid() {
		t.Fatalf("the closing Pane has no recorded boxes: %+v", closedOld)
	}
	survivorOld, ok := wm.rects[survivorID]
	if !ok || !survivorOld.Valid() {
		t.Fatalf("the survivor has no recorded boxes: %+v", survivorOld)
	}
	closedGrid := closedSurface.Grid()
	survivorOldGrid := survivorSurface.Grid()
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	closedGridCalls := closedSurface.GridForCalls()

	// Put content on the closing Pane's real terminal. After the close the
	// Pane is out of the tree, so any text on screen can only come from the
	// ghost's still copy of this surface.
	const ghostText = "ghost-live-text"
	closedSurface.feed(ghostText + "\r\n")
	pumpUntil(t, tt, "the terminal content", func() bool { return tt.HasText(ghostText) })

	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Frame()
	event, ok := g.state.MotionEvent()
	if !ok || event.Kind != motion.PaneClose {
		t.Fatalf("the close recorded %+v, want a PaneClose event", event)
	}
	if event.PaneID != closedID || event.TargetID != survivorID || event.Magnified {
		t.Fatalf("the PaneClose event = %+v, want closed %q, target %q, not magnified",
			event, closedID, survivorID)
	}
	run := wm.run
	if run == nil {
		t.Fatalf("the close did not start a Pane-close run")
	}
	if run.kind != motion.PaneClose || run.seq != event.Seq || run.target != survivorID {
		t.Fatalf("the run = kind %v, seq %d, target %q; want PaneClose, %d, %q",
			run.kind, run.seq, run.target, event.Seq, survivorID)
	}
	if !run.native {
		t.Fatalf("the production close did not take the measured native survivor route")
	}
	if got := run.anim.Duration; got != 150*time.Millisecond {
		t.Fatalf("the close duration = %s, want 150ms at standard speed", got)
	}
	ghost, ok := run.ghosts[closedID]
	if !ok {
		t.Fatalf("the run has no ghost for the closed Pane")
	}
	if ghost.handle == nil || ghost.blank || ghost.info.Surface != closedSurface {
		t.Fatalf("the ghost = handle %v, blank %v, surface %T; want the retained real surface",
			ghost.handle != nil, ghost.blank, ghost.info.Surface)
	}
	if ghost.handle.Info.Surface != closedSurface || ghost.handle.ID != closedID {
		t.Fatalf("the retained ghost names %q/%T, want %q/%T",
			ghost.handle.ID, ghost.handle.Info.Surface, closedID, closedSurface)
	}
	ghostVisual, ok := run.anim.VisualFor(closedID)
	if !ok || ghostVisual.Kind != motion.Exit {
		t.Fatalf("the closed Pane has no exit visual: %+v", run.anim.Visuals)
	}
	if ghostVisual.From != closedOld.Column {
		t.Fatalf("the ghost starts at %+v, want the Pane's previous column %+v",
			ghostVisual.From, closedOld.Column)
	}
	if ghostVisual.FromOpacity != 1 {
		t.Fatalf("the ghost starts at opacity %v, want 1", ghostVisual.FromOpacity)
	}
	if _, ok := run.final[closedID]; ok {
		t.Fatalf("the closed Pane is still in the final layout")
	}
	end, ok := run.final[survivorID]
	if !ok || end.Column.W <= survivorOld.Column.W {
		t.Fatalf("the survivor's final column %+v is not wider than its old %+v",
			end.Column, survivorOld.Column)
	}
	if focused, _ := g.state.FocusedPaneID(0); focused != survivorID {
		t.Fatalf("focus after the close = %q, want the survivor %q", focused, survivorID)
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); !ok {
		t.Fatalf("the close run built no ghost overlay")
	}
	if !tt.HasText(ghostText) {
		t.Fatalf("the ghost is blank: the closed Pane's terminal text is not drawn")
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); ok {
		t.Fatalf("the native close built an overlay survivor copy")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(survivorID)); ok {
		t.Fatalf("the native close built a survivor mask")
	}
	if got := len(survivorSurface.ResizeLog()); got != 1 {
		t.Fatalf("the survivor delivered %d resizes at the close, want 1: %+v",
			got, survivorSurface.ResizeLog())
	}
	if got := len(closedSurface.ResizeLog()); got != 0 {
		t.Fatalf("the closed Pane delivered %d resizes at the close, want none: %+v",
			got, closedSurface.ResizeLog())
	}
	if got := closedSurface.GridForCalls() - closedGridCalls; got != 0 {
		t.Fatalf("the closed Pane computed %d grids at the close, want none", got)
	}
	if closedSurface.Closes() != 0 {
		t.Fatalf("the closed Pane's surface was destroyed before its ghost ran")
	}
	survivorChangeCalls := survivorSurface.GridForCalls()
	closedChangeCalls := closedSurface.GridForCalls()
	survivorChangeLog := len(survivorSurface.ResizeLog())
	closedChangeLog := len(closedSurface.ResizeLog())

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("mid-close progress = %v, %v, want inside (0,1)", k, done)
	}
	rect, opacity := run.anim.State(ghostVisual, k)
	if want := closedOld.Column.W * (1 - (1-motion.PopinStartScale)*k); abs32(rect.W-want) > 0.5 {
		t.Fatalf("the ghost width at k=%v = %v, want %v (87%% of the old %v)",
			k, rect.W, want, closedOld.Column.W)
	}
	if abs32(rect.X+rect.W/2-(closedOld.Column.X+closedOld.Column.W/2)) > 0.5 {
		t.Fatalf("the ghost is not centered on the old Pane: %+v vs %+v", rect, closedOld.Column)
	}
	if want := 1 - k; abs32(opacity-want) > 0.01 {
		t.Fatalf("the ghost opacity at k=%v = %v, want %v", k, opacity, want)
	}
	ghostLabel, ok := tt.Find("pane-close-ghost:" + string(closedID))
	if !ok {
		t.Fatalf("the ghost overlay disappeared mid-run")
	}
	rectNear(t, ghostLabel, clipToWindow(rect), 0.75, "the ghost overlay")
	if ghostLabel.W >= closedOld.Column.W-1 {
		t.Fatalf("the ghost did not shrink: %v vs %v", ghostLabel.W, closedOld.Column.W)
	}
	if !tt.HasText(ghostText) {
		t.Fatalf("the ghost is blank mid-run: the retained terminal is not drawn")
	}
	// The native route moves the real survivor itself. Its layout box is
	// already final, so the visible PaneRegion label is the measurement: it
	// must sit between the old and final widths instead of snapping.
	survivorLabel, ok := tt.Find(locales.In("en").PaneRegion(1))
	if !ok {
		t.Fatalf("the survivor has no PaneRegion label mid-run")
	}
	if survivorLabel.W <= survivorOld.Column.W+20 || survivorLabel.W >= end.Column.W-20 {
		t.Fatalf("the native survivor at k=%v is %v wide, want between its old %v and final %v",
			k, survivorLabel.W, survivorOld.Column.W, end.Column.W)
	}
	if abs32(survivorLabel.X-end.Column.X) > 5 {
		t.Fatalf("the native survivor's left edge at k=%v = %v, want the final slot's %v",
			k, survivorLabel.X, end.Column.X)
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); ok {
		t.Fatalf("the native survivor has an overlay copy mid-run")
	}
	if got := survivorSurface.GridForCalls() - survivorChangeCalls; got != 0 {
		t.Fatalf("the survivor computed %d grids during animation frames, want none", got)
	}
	if got := closedSurface.GridForCalls() - closedChangeCalls; got != 0 {
		t.Fatalf("the closed Pane computed %d grids during animation frames, want none", got)
	}
	if got := len(survivorSurface.ResizeLog()) - survivorChangeLog; got != 0 {
		t.Fatalf("the survivor delivered %d resizes during animation frames, want none", got)
	}
	if got := len(closedSurface.ResizeLog()) - closedChangeLog; got != 0 {
		t.Fatalf("the closed Pane delivered %d resizes during animation frames, want none", got)
	}

	// The ghost covers the old Pane's box, which overlaps the survivor's
	// expanding area. A press and a key there must reach the real survivor;
	// the ghost and the masks ignore pointer and focus.
	tt.ClickAt(ghostLabel.X+ghostLabel.W/2, ghostLabel.Y+ghostLabel.H/2)
	pumpUntil(t, tt, "the survivor keyboard after clicking the ghost", func() bool {
		return survivorSurface.LiveFocused()
	})
	const typed = "close-key"
	tt.Type(typed)
	pumpUntil(t, tt, "the key typed during the close", func() bool {
		return bytes.Contains(survivorSurface.transport.input(), []byte(typed))
	})
	if wm.run == nil {
		t.Fatalf("the run finished before the typed key could be asserted")
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the close run is still playing after its duration")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
		t.Fatalf("the ghost overlay is still built after the run")
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); ok {
		t.Fatalf("the survivor copy is still built after the run")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(survivorID)); ok {
		t.Fatalf("the survivor mask is still built after the run")
	}
	if tt.HasText(ghostText) {
		t.Fatalf("the ghost text is still drawn after the run")
	}
	if got := closedSurface.Closes(); got != 1 {
		t.Fatalf("the retained surface closed %d times, want exactly 1", got)
	}
	if closedSurface.Grid() != closedGrid {
		t.Fatalf("the closed terminal was resized after its Pane closed: %+v, was %+v",
			closedSurface.Grid(), closedGrid)
	}
	if got := len(survivorSurface.ResizeLog()); got != 1 {
		t.Fatalf("the survivor delivered %d resizes over the close, want exactly 1: %+v",
			got, survivorSurface.ResizeLog())
	}
	if survivorSurface.Grid() == survivorOldGrid {
		t.Fatalf("the survivor kept its pre-close grid %+v", survivorOldGrid)
	}
	want, ok := appview.GridFor(survivorSurface.Viewport())
	if !ok || survivorSurface.Grid() != want {
		t.Fatalf("the survivor final grid = %+v, want its final viewport's %+v",
			survivorSurface.Grid(), want)
	}
	tt.Frame()
	if got := len(survivorSurface.ResizeLog()); got != 1 {
		t.Fatalf("a settled frame delivered another resize: %+v", survivorSurface.ResizeLog())
	}
	root, _, _ := g.state.TabTree(0)
	if root == nil || len(motionPaneIDs(root)) != 1 {
		t.Fatalf("the Tab does not hold exactly the survivor after the close")
	}
}

// TestPaneCloseStylesRenderTheirGeometryAndOpacity checks all four close
// styles at one mid-run frame on the same real split: popin shrinks around the
// old center to the fixed 87% and fades, slide leaves by 100% of the Pane's
// size through the nearest content edge and stays opaque, gnomed folds up to
// its bottom edge with the width fixed and stays opaque, and fade keeps the
// old geometry and fades. The whole Pane, frame and header included, follows
// the same transform.
func TestPaneCloseStylesRenderTheirGeometryAndOpacity(t *testing.T) {
	const eps = 0.5
	for _, style := range []domainpreferences.MotionStyle{
		domainpreferences.MotionPopin,
		domainpreferences.MotionSlide,
		domainpreferences.MotionGnomed,
		domainpreferences.MotionFade,
	} {
		t.Run(string(style), func(t *testing.T) {
			g := motionGraph(t, paneClosePreferences(true, true, style))
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, g.state, 1)
			view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			settlePane(t, tt, g, 0)
			surfaces := splitAndSettle(t, tt, g, wm)
			for _, surface := range surfaces {
				surface.ResetResizeLog()
			}

			closedID, _ := g.state.FocusedPaneID(0)
			ids := motionPaneIDsMust(t, g)
			survivorID := ids[0]
			if survivorID == closedID {
				survivorID = ids[1]
			}
			closedSurface := surfaceForPane(t, g, 0, closedID)
			old, ok := wm.rects[closedID]
			if !ok || !old.Valid() {
				t.Fatalf("style %s has no closed-Pane boxes: %+v", style, old)
			}
			if !g.state.CloseFocusedPane() {
				t.Fatalf("style %s: CloseFocusedPane returned false", style)
			}
			tt.Frame()
			run := wm.run
			if run == nil {
				t.Fatalf("style %s did not start a close run", style)
			}
			visual := mustVisual(t, run, closedID)
			if visual.Kind != motion.Exit {
				t.Fatalf("style %s exit visual = %+v", style, visual)
			}
			if visual.From != old.Column {
				t.Fatalf("style %s ghost starts at %+v, want the old column %+v",
					style, visual.From, old.Column)
			}

			tt.Advance(60 * time.Millisecond)
			tt.Frame()
			k, done := run.anim.Progress(tt.Now())
			if done || k <= 0 || k >= 1 {
				t.Fatalf("style %s progress = %v, %v, want inside (0,1)", style, k, done)
			}
			rect, opacity := run.anim.State(visual, k)
			switch style {
			case domainpreferences.MotionPopin:
				if want := old.Column.W * (1 - (1-motion.PopinStartScale)*k); abs32(rect.W-want) > eps {
					t.Fatalf("popin ghost width = %v, want %v (87%% at k=1)", rect.W, want)
				}
				if abs32(rect.X+rect.W/2-(old.Column.X+old.Column.W/2)) > eps {
					t.Fatalf("popin ghost is not centered on the old Pane: %+v vs %+v", rect, old.Column)
				}
				if want := 1 - k; abs32(opacity-want) > 0.01 {
					t.Fatalf("popin ghost opacity = %v, want %v", opacity, want)
				}
			case domainpreferences.MotionSlide:
				if rect.W != old.Column.W || rect.H != old.Column.H {
					t.Fatalf("slide ghost size = %vx%v, want the old %vx%v",
						rect.W, rect.H, old.Column.W, old.Column.H)
				}
				// The closed Pane is the right half, so its nearest content
				// edge is the right one and it leaves by its own width.
				if abs32(rect.X-(old.Column.X+old.Column.W*k)) > eps {
					t.Fatalf("slide ghost X = %v, want %v", rect.X, old.Column.X+old.Column.W*k)
				}
				if opacity != 1 {
					t.Fatalf("slide ghost opacity = %v, want an explicit 1", opacity)
				}
			case domainpreferences.MotionGnomed:
				if rect.W != old.Column.W || abs32(rect.H-old.Column.H*(1-k)) > eps {
					t.Fatalf("gnomed ghost = %+v, want width %v and height %v",
						rect, old.Column.W, old.Column.H*(1-k))
				}
				if abs32(rect.Y+rect.H-(old.Column.Y+old.Column.H)) > eps {
					t.Fatalf("gnomed ghost bottom = %v, want the old %v",
						rect.Y+rect.H, old.Column.Y+old.Column.H)
				}
				if opacity != 1 {
					t.Fatalf("gnomed ghost opacity = %v, want an explicit 1", opacity)
				}
			case domainpreferences.MotionFade:
				if rect != old.Column {
					t.Fatalf("fade ghost geometry = %+v, want the old %+v", rect, old.Column)
				}
				if want := 1 - k; abs32(opacity-want) > 0.01 {
					t.Fatalf("fade ghost opacity = %v, want %v", opacity, want)
				}
			}

			visible := clipToWindow(rect)
			ghost, ok := tt.Find("pane-close-ghost:" + string(closedID))
			if !ok {
				t.Fatalf("style %s built no ghost overlay", style)
			}
			rectNear(t, ghost, visible, 0.75, "style "+string(style)+" ghost")
			frame, ok := tt.Find("pane-motion-frame:" + string(closedID))
			if !ok {
				t.Fatalf("style %s built no ghost frame", style)
			}
			rectNear(t, frame, visible, 0.75, "style "+string(style)+" ghost frame")
			header, ok := tt.Find("pane-motion-header:" + string(closedID))
			if !ok {
				t.Fatalf("style %s built no ghost header", style)
			}
			frameTop, frameRight, _, frameLeft := kit.PaneFrameInset()
			headerBox := ui.Rect{
				X: old.Column.X + frameLeft,
				Y: old.Column.Y + frameTop,
				W: old.Column.W - frameLeft - frameRight,
				H: old.Body.Y - old.Column.Y - frameTop,
			}
			if headerBox.H <= 0 {
				t.Fatalf("style %s ghost has no header room: %+v", style, headerBox)
			}
			kx, ky := float32(1), float32(1)
			if visual.Box.W > 0 {
				kx = rect.W / visual.Box.W
			}
			if visual.Box.H > 0 {
				ky = rect.H / visual.Box.H
			}
			wantHeader := ui.Rect{
				X: rect.X + (headerBox.X-visual.Box.X)*kx,
				Y: rect.Y + (headerBox.Y-visual.Box.Y)*ky,
				W: headerBox.W * kx,
				H: headerBox.H * ky,
			}
			rectNear(t, header, clipToWindow(wantHeader), 0.75, "style "+string(style)+" ghost header")
			if style != domainpreferences.MotionFade && rectNearOK(header, clipToWindow(headerBox), 0.5) {
				t.Fatalf("style %s ghost header stayed at the old box %+v (whole pane %+v)",
					style, header, rect)
			}

			tt.Advance(300 * time.Millisecond)
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("style %s close run did not finish", style)
			}
			if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
				t.Fatalf("style %s ghost is still built", style)
			}
			if got := closedSurface.Closes(); got != 1 {
				t.Fatalf("style %s retained surface closed %d times, want 1", style, got)
			}
			survivor := surfaceForPane(t, g, 0, survivorID)
			if got := len(survivor.ResizeLog()); got != 1 {
				t.Fatalf("style %s survivor delivered %d resizes, want 1: %+v",
					style, got, survivor.ResizeLog())
			}
			want, ok := appview.GridFor(survivor.Viewport())
			if !ok || survivor.Grid() != want {
				t.Fatalf("style %s survivor grid = %+v, want %+v", style, survivor.Grid(), want)
			}
		})
	}
}

// TestPaneCloseSurvivorNativeRouteAcrossTheCollapsedSplit measures MyGo's
// native Match + ScaleContent route for the expanding survivor across the
// split the close collapses. Ticket 04 measured the route failing when a
// split is created; a close removes one instead. The test observes the real
// Pane's visible box at a mid-run frame, not just its fields or final state,
// and compares it with the overlay fallback at the same instant.
func TestPaneCloseSurvivorNativeRouteAcrossTheCollapsedSplit(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := splitAndSettle(t, tt, g, wm)
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	closedID, _ := g.state.FocusedPaneID(0)
	ids := motionPaneIDsMust(t, g)
	survivorID := ids[0]
	if survivorID == closedID {
		survivorID = ids[1]
	}
	if ids[0] != survivorID {
		t.Fatalf("the survivor %q is not the first Pane %q, so PaneRegion(1) is not it", survivorID, ids[0])
	}
	old, ok := wm.rects[survivorID]
	if !ok || !old.Valid() {
		t.Fatalf("the survivor has no boxes: %+v", old)
	}
	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Frame()
	run := wm.run
	if run == nil || !run.native {
		t.Fatalf("the native close did not start a native run: %+v", run)
	}
	end := run.final[survivorID]
	// The survivor keeps a Move visual as interruption bookkeeping, but the
	// native route must not paint its opaque overlay copy over the real Pane.
	if v, ok := run.anim.VisualFor(survivorID); !ok || v.Kind != motion.Move {
		t.Fatalf("the native survivor has no move bookkeeping visual: %+v", run.anim.Visuals)
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); ok {
		t.Fatalf("the native route painted an overlay survivor copy")
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("native progress = %v, %v", k, done)
	}
	layout := wm.rects[survivorID].Column
	label, ok := tt.Find(locales.In("en").PaneRegion(1))
	if !ok {
		t.Fatalf("the survivor has no PaneRegion label")
	}
	// The real Pane's layout is already final, but what it paints is not: the
	// visible label is between the old and final widths at mid-run, so Match +
	// ScaleContent carries the whole survivor across the collapsed split. A
	// loose margin asserts movement, not an easing value.
	if label.W <= old.Column.W+50 || label.W >= end.Column.W-50 {
		t.Fatalf("native survivor at k=%v is %v wide, want between the old %v and final %v",
			k, label.W, old.Column.W, end.Column.W)
	}
	if abs32(label.X-end.Column.X) > 5 {
		t.Fatalf("native survivor left edge at k=%v = %v, want the final slot's %v",
			k, label.X, end.Column.X)
	}
	if !rectNearOK(layout, end.Column, 1) {
		t.Fatalf("native survivor layout at k=%v = %+v, want the final %+v", k, layout, end.Column)
	}
	transition, ok := wm.nativeTransition(survivorID)
	if !ok || transition.Duration != 150*time.Millisecond || !transition.Position ||
		!transition.Size || !transition.ScaleContent ||
		transition.Match != (motion.PaneMatch{ID: survivorID}) {
		t.Fatalf("native transition = %+v, want a 150ms Match + ScaleContent move of %q",
			transition, survivorID)
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); ok {
		t.Fatalf("the native route built an overlay survivor copy")
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the native close did not finish")
	}
	survivor := surfaceForPane(t, g, 0, survivorID)
	if got := len(survivor.ResizeLog()); got != 1 {
		t.Fatalf("the native close delivered %d survivor resizes, want 1: %+v", got, survivor.ResizeLog())
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || survivor.Grid() != want {
		t.Fatalf("the survivor final grid = %+v, want %+v", survivor.Grid(), want)
	}
}

// paneCloseScene is one real split at the moment just after its focused Pane
// closed: the run, the retained surfaces, and the mid-run sample.
type paneCloseScene struct {
	tt             *ui.Tester
	graph          *realTerminalGraph
	wm             *workspacePaneMotion
	closedID       model.PaneID
	survivorID     model.PaneID
	old            motion.PaneRects
	survivorOld    motion.PaneRects
	end            motion.PaneRects
	run            *paneMotionRun
	closedSurface  *realTerminalSurface
	survivor       *realTerminalSurface
	k              float32
	changeResizes  int
	changeGridCall int
}

// startPaneCloseScene builds a settled real split, closes its focused Pane,
// and samples the run 60 ms later. Mode selects the native or overlay route.
func startPaneCloseScene(t *testing.T, mode paneMotionMode) *paneCloseScene {
	t.Helper()
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, mode)
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := splitAndSettle(t, tt, g, wm)
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	closedID, _ := g.state.FocusedPaneID(0)
	ids := motionPaneIDsMust(t, g)
	survivorID := ids[0]
	if survivorID == closedID {
		survivorID = ids[1]
	}
	closedSurface := surfaceForPane(t, g, 0, closedID)
	survivor := surfaceForPane(t, g, 0, survivorID)
	old, ok := wm.rects[closedID]
	if !ok || !old.Valid() {
		t.Fatalf("mode %+v has no closed-Pane boxes: %+v", mode, old)
	}
	survivorOld, ok := wm.rects[survivorID]
	if !ok || !survivorOld.Valid() {
		t.Fatalf("mode %+v has no survivor boxes: %+v", mode, survivorOld)
	}
	if !g.state.CloseFocusedPane() {
		t.Fatalf("mode %+v: CloseFocusedPane returned false", mode)
	}
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("mode %+v did not start a close run", mode)
	}
	end, ok := run.final[survivorID]
	if !ok {
		t.Fatalf("mode %+v has no final survivor boxes", mode)
	}
	changeResizes := len(survivor.ResizeLog())
	changeGridCall := survivor.GridForCalls()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("mode %+v progress = %v, %v", mode, k, done)
	}
	return &paneCloseScene{
		tt: tt, graph: g, wm: wm, closedID: closedID, survivorID: survivorID,
		old: old, survivorOld: survivorOld, end: end, run: run,
		closedSurface: closedSurface, survivor: survivor, k: k,
		changeResizes: changeResizes, changeGridCall: changeGridCall,
	}
}

// TestPaneCloseOverlayFallbackAnimatesTheSurvivorToTheSameFinalFrame keeps the
// opaque-overlay survivor route exercised as the fallback: the copy moves
// between the old and final boxes with an opaque mask over the real final
// Pane, animation frames do no grid math, and the fallback ends at the same
// viewport, grid, and one-resize count as the production native route.
func TestPaneCloseOverlayFallbackAnimatesTheSurvivorToTheSameFinalFrame(t *testing.T) {
	overlay := startPaneCloseScene(t, paneMotionMode{overlayClose: true, labels: true})
	native := startPaneCloseScene(t, paneMotionMode{labels: true})
	if abs32(overlay.k-native.k) > 0.001 {
		t.Fatalf("the routes were sampled at different progress: overlay %v, native %v",
			overlay.k, native.k)
	}
	if overlay.run.native {
		t.Fatalf("the fallback scene took the native route")
	}
	if !native.run.native {
		t.Fatalf("the production scene did not take the native route")
	}

	visual := mustVisual(t, overlay.run, overlay.survivorID)
	if visual.Kind != motion.Move || visual.From != overlay.survivorOld.Column || visual.To != overlay.end.Column {
		t.Fatalf("the fallback survivor visual = %+v, want a move from %+v to %+v",
			visual, overlay.survivorOld.Column, overlay.end.Column)
	}
	copyBox, ok := overlay.tt.Find("pane-motion:" + string(overlay.survivorID))
	if !ok {
		t.Fatalf("the fallback built no survivor copy")
	}
	if copyBox.W <= overlay.survivorOld.Column.W+20 || copyBox.W >= overlay.end.Column.W-20 {
		t.Fatalf("the fallback survivor at k=%v is %v wide, want between its old %v and final %v",
			overlay.k, copyBox.W, overlay.survivorOld.Column.W, overlay.end.Column.W)
	}
	mask, ok := overlay.tt.Find("pane-motion-mask:" + string(overlay.survivorID))
	if !ok {
		t.Fatalf("the fallback built no survivor mask")
	}
	rectNear(t, mask, clipToWindow(overlay.end.Column), 0.5, "fallback survivor mask")
	if got := overlay.survivor.GridForCalls() - overlay.changeGridCall; got != 0 {
		t.Fatalf("the fallback computed %d survivor grids during animation frames, want none", got)
	}
	if got := len(overlay.survivor.ResizeLog()); got != overlay.changeResizes {
		t.Fatalf("the fallback delivered %d survivor resizes during animation frames, want %d",
			got, overlay.changeResizes)
	}
	if got := overlay.changeResizes; got != 1 {
		t.Fatalf("the fallback delivered %d survivor resizes at the close, want 1", got)
	}

	for _, scene := range []*paneCloseScene{overlay, native} {
		scene.tt.Advance(400 * time.Millisecond)
		scene.tt.Frame()
		if scene.wm.run != nil {
			t.Fatalf("the close run did not finish")
		}
		if _, ok := scene.tt.Find("pane-close-ghost:" + string(scene.closedID)); ok {
			t.Fatalf("the ghost is still built after the run")
		}
		if got := scene.closedSurface.Closes(); got != 1 {
			t.Fatalf("the retained surface closed %d times, want 1", got)
		}
		if got := len(scene.survivor.ResizeLog()); got != 1 {
			t.Fatalf("the survivor delivered %d resizes over the close, want 1: %+v",
				got, scene.survivor.ResizeLog())
		}
		want, ok := appview.GridFor(scene.survivor.Viewport())
		if !ok || scene.survivor.Grid() != want {
			t.Fatalf("the survivor final grid = %+v, want %+v", scene.survivor.Grid(), want)
		}
	}
	if _, ok := overlay.tt.Find("pane-motion:" + string(overlay.survivorID)); ok {
		t.Fatalf("the fallback copy is still built")
	}
	if overlay.survivor.Viewport() != native.survivor.Viewport() ||
		overlay.survivor.Grid() != native.survivor.Grid() {
		t.Fatalf("the routes end differently: overlay %+v/%+v, native %+v/%+v",
			overlay.survivor.Viewport(), overlay.survivor.Grid(),
			native.survivor.Viewport(), native.survivor.Grid())
	}
	label := locales.In("en").PaneRegion(1)
	gotRect, gotOK := overlay.tt.Find(label)
	wantRect, wantOK := native.tt.Find(label)
	if gotRect != wantRect || gotOK != wantOK {
		t.Fatalf("the routes end at different boxes: overlay %+v/%v, native %+v/%v",
			gotRect, gotOK, wantRect, wantOK)
	}
}

// TestPaneClosePendingAndFailedPanesKeepTheirBody is the ghost-content
// contract for a Pane without a live terminal: a pending Pane's placeholder is
// the ghost (never blank), and a failed Pane keeps its error body. Both Panes
// are removed from the Tab; the late ready outcome must not resurrect one.
func TestPaneClosePendingAndFailedPanesKeepTheirBody(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		factory := &gatedTransportFactory{release: make(chan struct{})}
		g, wm, tt := newControlledMotionGraph(t, factory)
		if err := g.state.SplitRight(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		pendingID, _ := g.state.FocusedPaneID(0)
		info, ok := g.state.PaneInfoAt(0, pendingID)
		if !ok || !info.Pending || info.Surface != nil {
			t.Fatalf("the split's Pane is not pending: %+v", info)
		}
		tt.Advance(300 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the pending open run did not finish")
		}

		if !g.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane returned false")
		}
		tt.Frame()
		run := wm.run
		if run == nil {
			t.Fatalf("the pending Pane's close did not start a run")
		}
		ghost, ok := run.ghosts[pendingID]
		if !ok || ghost.blank || ghost.handle == nil {
			t.Fatalf("the pending Pane's ghost = %+v, want a retained placeholder", ghost)
		}
		if !ghost.info.Pending || ghost.info.Err != nil || ghost.info.Surface != nil {
			t.Fatalf("the pending ghost's info = %+v", ghost.info)
		}
		connecting := locales.In("en").PaneConnecting()
		if !tt.HasText(connecting) {
			t.Fatalf("the pending ghost is blank: the placeholder is not drawn")
		}

		tt.Advance(300 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the pending close run did not finish")
		}
		// Let the gated Session finish after its Pane is gone. The state must
		// close it and not put the Pane or a surface back.
		close(factory.release)
		pumpUntil(t, tt, "the late open outcome", func() bool {
			g.state.Flush()
			root, _, ok := g.state.TabTree(0)
			return ok && root != nil && len(motionPaneIDs(root)) == 1
		})
		tt.Frame()
		root, _, ok := g.state.TabTree(0)
		if !ok || root == nil || len(motionPaneIDs(root)) != 1 {
			t.Fatalf("the late outcome restored the closed Pane")
		}
		if ids := motionPaneIDs(root); ids[0] == pendingID {
			t.Fatalf("the closed pending Pane %q is back", pendingID)
		}
		tt.Frame()
		if tt.HasText(connecting) {
			t.Fatalf("the placeholder ghost outlived its run")
		}
		if got := len(g.factory.all()); got != 1 {
			t.Fatalf("the late outcome created %d surfaces, want only the survivor's", got)
		}
	})

	t.Run("failure", func(t *testing.T) {
		factory := &failingTransportFactory{}
		g, wm, tt := newControlledMotionGraph(t, factory)
		if err := g.state.SplitRight(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		failedID, _ := g.state.FocusedPaneID(0)
		pumpUntil(t, tt, "the failed Pane", func() bool {
			g.state.Flush()
			info, ok := g.state.PaneInfoAt(0, failedID)
			return ok && info.Err != nil
		})
		tt.Advance(300 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the failed open run did not finish")
		}
		if !g.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane returned false")
		}
		tt.Frame()
		run := wm.run
		if run == nil {
			t.Fatalf("the failed Pane's close did not start a run")
		}
		ghost, ok := run.ghosts[failedID]
		if !ok || ghost.blank || ghost.handle == nil || ghost.info.Err == nil {
			t.Fatalf("the failed Pane's ghost = %+v, want the retained error", ghost)
		}
		if !tt.HasText(locales.In("en").ConnectionErrorInternal()) {
			t.Fatalf("the failed ghost is blank: the error is not drawn")
		}
		tt.Advance(300 * time.Millisecond)
		tt.Frame()
		if wm.run != nil {
			t.Fatalf("the failed close run did not finish")
		}
		root, _, ok := g.state.TabTree(0)
		if !ok || root == nil || len(motionPaneIDs(root)) != 1 {
			t.Fatalf("the Tab does not hold exactly the survivor after the failed close")
		}
	})
}

// TestPaneCloseOfTheLastPaneRoutesToTabClose keeps the last-Pane path out of
// Pane motion: closing it closes its Tab and records the TabClose that took
// the Tab away instead of a PaneClose. The Tab body's exit copy keeps each
// Surface until it is released, and then each Surface is destroyed once.
func TestPaneCloseOfTheLastPaneRoutesToTabClose(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 2)
	if event, ok := g.state.MotionEvent(); !ok || event.Kind != motion.TabOpen {
		t.Fatalf("creating the Tabs recorded %+v, want their TabOpen", event)
	}
	if !g.state.CloseFocusedPane() {
		t.Fatalf("closing the active Tab's only Pane returned false")
	}
	if got := g.state.TabCount(); got != 1 {
		t.Fatalf("the Tab count after the last-Pane close = %d, want 1", got)
	}
	event, ok := g.state.MotionEvent()
	if !ok || event.Kind != motion.TabClose || event.TargetTabID == "" {
		t.Fatalf("closing the last Pane recorded %+v, want the TabClose onto a replacement", event)
	}
	if event.Kind == motion.PaneClose {
		t.Fatalf("closing the last Pane recorded a PaneClose: %+v", event)
	}
	g.state.ReleaseTabGhosts()
	if !g.state.CloseFocusedPane() {
		t.Fatalf("closing the remaining Tab's only Pane returned false")
	}
	if got := g.state.TabCount(); got != 0 {
		t.Fatalf("the Tab count after both last-Pane closes = %d, want 0", got)
	}
	event, ok = g.state.MotionEvent()
	if !ok || event.Kind != motion.TabClose || event.TargetTabID != "" {
		t.Fatalf("the final last-Pane close recorded %+v, want a TabClose with no replacement", event)
	}
	if event.Kind == motion.PaneClose {
		t.Fatalf("the final last-Pane close recorded a PaneClose: %+v", event)
	}
	g.state.ReleaseTabGhosts()
	for i, surface := range g.factory.all() {
		if got := surface.Closes(); got != 1 {
			t.Errorf("surface %d closed %d times, want once", i+1, got)
		}
	}
}

// TestPaneCloseWarpsWhenMotionIsOff covers the master switch, the PaneClose
// switch, and Reduce Motion: each warps to the collapsed final layout with no
// run, no ghost, no animation frame, and still exactly one PTY resize for the
// survivor whose size changed. The closed Pane gets none.
func TestPaneCloseWarpsWhenMotionIsOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting domainpreferences.Motion
		reduce  bool
	}{
		{"master-off", paneClosePreferences(false, true, domainpreferences.MotionPopin), false},
		{"pane-close-off", paneClosePreferences(true, false, domainpreferences.MotionPopin), false},
		{"reduce-motion", paneClosePreferences(true, true, domainpreferences.MotionPopin), true},
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
			settlePane(t, tt, g, 0)
			surfaces := splitAndSettle(t, tt, g, wm)
			for _, surface := range surfaces {
				surface.ResetResizeLog()
			}
			closedID, _ := g.state.FocusedPaneID(0)
			ids := motionPaneIDsMust(t, g)
			survivorID := ids[0]
			if survivorID == closedID {
				survivorID = ids[1]
			}
			closedSurface := surfaceForPane(t, g, 0, closedID)
			survivor := surfaceForPane(t, g, 0, survivorID)
			closedOldGrid := closedSurface.Grid()
			animationBefore := wm.animationFrames

			if !g.state.CloseFocusedPane() {
				t.Fatalf("CloseFocusedPane returned false")
			}
			tt.Frame()
			if wm.run != nil {
				t.Fatalf("a warp started a close run")
			}
			if wm.animationFrames != animationBefore {
				t.Fatalf("the warp asked for %d animation frames", wm.animationFrames-animationBefore)
			}
			if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
				t.Fatalf("the warp built a ghost")
			}
			if _, ok := tt.Find("pane-motion:" + string(survivorID)); ok {
				t.Fatalf("the warp built a survivor copy")
			}
			if _, ok := tt.Find("pane-motion-mask:" + string(survivorID)); ok {
				t.Fatalf("the warp built a survivor mask")
			}
			if got := closedSurface.Closes(); got != 1 {
				t.Fatalf("the closed surface closed %d times at the warp, want 1", got)
			}
			if got := len(closedSurface.ResizeLog()); got != 0 {
				t.Fatalf("the closed Pane delivered %d resizes at the warp, want none", got)
			}
			pumpUntil(t, tt, "the warped final grids", func() bool { return gridsSettled(survivor) })
			if got := len(survivor.ResizeLog()); got != 1 {
				t.Fatalf("the survivor delivered %d resizes at the warp, want 1: %+v",
					got, survivor.ResizeLog())
			}
			want, ok := appview.GridFor(survivor.Viewport())
			if !ok || survivor.Grid() != want {
				t.Fatalf("the survivor final grid = %+v, want %+v", survivor.Grid(), want)
			}
			if closedSurface.Grid() != closedOldGrid {
				t.Fatalf("the closed terminal was resized: %+v, was %+v",
					closedSurface.Grid(), closedOldGrid)
			}
			tt.Advance(300 * time.Millisecond)
			tt.Frame()
			if wm.run != nil || wm.animationFrames != animationBefore {
				t.Fatalf("the warp started a run later")
			}
		})
	}
}

// TestPaneCloseWarpsWhenTheWindowWasHidden closes a Pane while the window is
// hidden: the shown frame warps instead of replaying an exit the user did not
// see, releases the retained surface once, and still sizes the survivor once.
func TestPaneCloseWarpsWhenTheWindowWasHidden(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := splitAndSettle(t, tt, g, wm)
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	closedID, _ := g.state.FocusedPaneID(0)
	ids := motionPaneIDsMust(t, g)
	survivorID := ids[0]
	if survivorID == closedID {
		survivorID = ids[1]
	}
	closedSurface := surfaceForPane(t, g, 0, closedID)
	survivor := surfaceForPane(t, g, 0, survivorID)
	animationBefore := wm.animationFrames

	tt.SetWindowHidden(true)
	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Advance(48 * time.Millisecond)
	tt.SetWindowHidden(false)

	if wm.run != nil {
		t.Fatalf("the shown view replayed the close it did not see")
	}
	if wm.animationFrames != animationBefore {
		t.Fatalf("the hidden warp asked for %d animation frames",
			wm.animationFrames-animationBefore)
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
		t.Fatalf("the hidden warp built a ghost")
	}
	if got := closedSurface.Closes(); got != 1 {
		t.Fatalf("the retained surface closed %d times at the hidden warp, want 1", got)
	}
	pumpUntil(t, tt, "the hidden warp's final grids", func() bool { return gridsSettled(survivor) })
	if got := len(survivor.ResizeLog()); got != 1 {
		t.Fatalf("the survivor delivered %d resizes after the hidden warp, want 1: %+v",
			got, survivor.ResizeLog())
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || survivor.Grid() != want {
		t.Fatalf("the survivor final grid = %+v, want %+v", survivor.Grid(), want)
	}
	if event, ok := g.state.MotionEvent(); ok && wm.controller.Unseen(event.Seq) {
		t.Fatalf("the hidden view did not catch its lastSeq up")
	}
}

// splitDownAndSettle splits the focused right Pane down, finishes the open
// run, and waits until all three terminals are at their final split grids.
func splitDownAndSettle(t *testing.T, tt *ui.Tester, g *realTerminalGraph, wm *workspacePaneMotion) []*realTerminalSurface {
	t.Helper()
	if err := g.state.SplitDown(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 3)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the split-down open run did not finish")
	}
	surfaces := paneSurfaces(t, g, 0)
	pumpUntil(t, tt, "the settled three-Pane grids", func() bool { return gridsSettled(surfaces...) })
	return surfaces
}

// TestPaneCloseResizesEveryExpandingSurvivorOnce closes the first child of a
// nested split, so both remaining Panes expand across the collapsed root. Each
// survivor gets exactly one PTY resize at the change, none during the native
// animation, and the closed Pane gets none. Its ghost still draws the whole
// Pane above them.
func TestPaneCloseResizesEveryExpandingSurvivorOnce(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	splitAndSettle(t, tt, g, wm)
	surfaces := splitDownAndSettle(t, tt, g, wm)
	if len(surfaces) != 3 {
		t.Fatalf("the nested split has %d surfaces, want 3", len(surfaces))
	}
	ids := motionPaneIDsMust(t, g)
	if len(ids) != 3 {
		t.Fatalf("the nested split has %d Panes, want 3", len(ids))
	}
	closedID := ids[0]
	if !g.state.FocusPaneID(0, closedID) {
		t.Fatalf("could not focus the first child %q", closedID)
	}
	tt.Frame()
	closedSurface := surfaceForPane(t, g, 0, closedID)
	const ghostText = "nested-ghost-text"
	closedSurface.feed(ghostText + "\r\n")
	pumpUntil(t, tt, "the first child's terminal content", func() bool { return tt.HasText(ghostText) })
	old := map[model.PaneID]motion.PaneRects{}
	for _, id := range ids {
		if r, ok := wm.rects[id]; ok {
			old[id] = r
		}
	}
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}

	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Frame()
	run := wm.run
	if run == nil || !run.native {
		t.Fatalf("the nested close did not start a native run: %+v", run)
	}
	if _, ok := run.ghosts[closedID]; !ok {
		t.Fatalf("the nested close has no ghost for %q", closedID)
	}
	if !tt.HasText(ghostText) {
		t.Fatalf("the nested ghost is blank")
	}
	var survivors []model.PaneID
	for _, id := range ids {
		if id == closedID {
			continue
		}
		end, ok := run.final[id]
		if !ok {
			t.Fatalf("survivor %q is missing from the final layout", id)
		}
		if end.Column.W <= old[id].Column.W+20 {
			t.Fatalf("survivor %q final %+v is not wider than its old %+v", id, end.Column, old[id].Column)
		}
		surface := surfaceForPane(t, g, 0, id)
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("survivor %q delivered %d resizes at the close, want 1: %+v",
				id, got, surface.ResizeLog())
		}
		if _, ok := run.anim.VisualFor(id); !ok {
			t.Fatalf("survivor %q has no interruption bookkeeping visual", id)
		}
		if _, ok := tt.Find("pane-motion:" + string(id)); ok {
			t.Fatalf("survivor %q has an overlay copy on the native route", id)
		}
		survivors = append(survivors, id)
	}
	if got := len(closedSurface.ResizeLog()); got != 0 {
		t.Fatalf("the closed Pane delivered %d resizes, want none", got)
	}
	changeCalls := map[model.PaneID]int{}
	for _, id := range survivors {
		surface := surfaceForPane(t, g, 0, id)
		changeCalls[id] = surface.GridForCalls()
	}
	changeLogs := map[model.PaneID]int{}
	for _, id := range survivors {
		changeLogs[id] = len(surfaceForPane(t, g, 0, id).ResizeLog())
	}

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("nested close progress = %v, %v", k, done)
	}
	for i, id := range survivors {
		surface := surfaceForPane(t, g, 0, id)
		if got := surface.GridForCalls() - changeCalls[id]; got != 0 {
			t.Errorf("survivor %q computed %d grids during animation frames, want none", id, got)
		}
		if got := len(surface.ResizeLog()) - changeLogs[id]; got != 0 {
			t.Errorf("survivor %q delivered %d resizes during animation frames, want none", id, got)
		}
		label, ok := tt.Find(locales.In("en").PaneRegion(i + 1))
		if !ok {
			t.Fatalf("survivor %q has no PaneRegion label", id)
		}
		end := run.final[id]
		if label.W <= old[id].Column.W+20 || label.W >= end.Column.W-20 {
			t.Fatalf("survivor %q at k=%v is %v wide, want between its old %v and final %v",
				id, k, label.W, old[id].Column.W, end.Column.W)
		}
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the nested close run did not finish")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
		t.Fatalf("the nested ghost is still built")
	}
	if tt.HasText(ghostText) {
		t.Fatalf("the nested ghost text is still drawn")
	}
	if got := closedSurface.Closes(); got != 1 {
		t.Fatalf("the nested ghost surface closed %d times, want 1", got)
	}
	for _, id := range survivors {
		surface := surfaceForPane(t, g, 0, id)
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("survivor %q delivered %d resizes over the close, want exactly 1: %+v",
				id, got, surface.ResizeLog())
		}
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Errorf("survivor %q grid = %+v, want %+v", id, surface.Grid(), want)
		}
	}
}

// TestPaneCloseMagnifiedStartsFromTheFullWindowAndRevealsTheFinalSplit
// magnifies one member of a split, closes it, and checks the ghost starts from
// the full pane region while the underlying split is already at its final
// layout. The survivor gets one resize; closing the then-last Pane still
// routes to Tab close with no PaneClose event.
func TestPaneCloseMagnifiedStartsFromTheFullWindowAndRevealsTheFinalSplit(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	splitAndSettle(t, tt, g, wm)
	ids := motionPaneIDsMust(t, g)
	magnifiedID := ids[0]
	survivorID := ids[1]
	magnifiedSurface := surfaceForPane(t, g, 0, magnifiedID)
	survivor := surfaceForPane(t, g, 0, survivorID)
	if !g.state.SetMagnifiedPane(0, magnifiedID) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", magnifiedID)
	}
	tt.Frame()
	region := wm.region
	// The magnified Pane fills the Tab's width; its terminal is settled at
	// the grid that room fits. The surface viewport is its own body, slightly
	// smaller than the Pane column, so the bound is loose.
	pumpUntil(t, tt, "the magnified Pane's full viewport", func() bool {
		vp := magnifiedSurface.Viewport()
		return vp.W >= region.W*0.9 && vp.H >= region.H*0.8 && gridsSettled(magnifiedSurface)
	})
	for _, surface := range []*realTerminalSurface{magnifiedSurface, survivor} {
		surface.ResetResizeLog()
	}
	closedOld, ok := wm.rects[magnifiedID]
	if !ok || !closedOld.Valid() {
		t.Fatalf("the magnified Pane has no boxes: %+v", closedOld)
	}
	if !rectNearOK(closedOld.Column, region, 8) {
		t.Fatalf("the magnified Pane's column = %+v, want the pane region %+v", closedOld.Column, region)
	}
	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Frame()
	event, ok := g.state.MotionEvent()
	if !ok || !event.Magnified || event.PaneID != magnifiedID {
		t.Fatalf("the magnified close event = %+v", event)
	}
	run := wm.run
	if run == nil {
		t.Fatalf("the magnified close did not start a run")
	}
	if !run.magnified || run.native {
		t.Fatalf("the magnified run = magnified %v, native %v; want magnified and no survivor native run",
			run.magnified, run.native)
	}
	if len(run.anim.Visuals) != 1 {
		t.Fatalf("the magnified run has %d visuals, want only the ghost", len(run.anim.Visuals))
	}
	ghostVisual := mustVisual(t, run, magnifiedID)
	if ghostVisual.Kind != motion.Exit {
		t.Fatalf("the magnified ghost visual = %+v", ghostVisual)
	}
	if !rectNearOK(ghostVisual.From, region, 1) {
		t.Fatalf("the magnified ghost starts at %+v, want the full region %+v", ghostVisual.From, region)
	}
	if !rectNearOK(ghostVisual.Box, closedOld.Column, 1) {
		t.Fatalf("the magnified ghost content box = %+v, want the Pane column %+v",
			ghostVisual.Box, closedOld.Column)
	}
	end, ok := run.final[survivorID]
	if !ok || !rectNearOK(end.Column, region, 8) {
		t.Fatalf("the revealed survivor final column = %+v, want the full pane region %+v",
			end.Column, region)
	}
	if got := len(survivor.ResizeLog()); got != 1 {
		t.Fatalf("the revealed survivor delivered %d resizes at the change, want 1: %+v",
			got, survivor.ResizeLog())
	}
	if got := magnifiedSurface.Closes(); got != 0 {
		t.Fatalf("the magnified surface was closed before its ghost ran")
	}
	if got := len(magnifiedSurface.ResizeLog()); got != 0 {
		t.Fatalf("the magnified Pane delivered %d resizes at its close, want none", got)
	}
	survivorCalls := survivor.GridForCalls()

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("magnified close progress = %v, %v", k, done)
	}
	rect, opacity := run.anim.State(ghostVisual, k)
	if rect.W >= region.W || rect.W <= region.W*motion.PopinStartScale {
		t.Fatalf("the magnified ghost width at k=%v = %v, want between %v and %v",
			k, rect.W, region.W*motion.PopinStartScale, region.W)
	}
	if want := 1 - k; abs32(opacity-want) > 0.01 {
		t.Fatalf("the magnified ghost opacity at k=%v = %v, want %v", k, opacity, want)
	}
	ghost, ok := tt.Find("pane-close-ghost:" + string(magnifiedID))
	if !ok {
		t.Fatalf("the magnified close built no ghost")
	}
	rectNear(t, ghost, clipToWindow(rect), 0.75, "the magnified ghost")
	if got := survivor.GridForCalls() - survivorCalls; got != 0 {
		t.Fatalf("the revealed survivor computed %d grids during the ghost, want none", got)
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the magnified close did not finish")
	}
	if got := magnifiedSurface.Closes(); got != 1 {
		t.Fatalf("the magnified ghost surface closed %d times, want 1", got)
	}
	if got := len(survivor.ResizeLog()); got != 1 {
		t.Fatalf("the revealed survivor delivered %d resizes over the close, want 1: %+v",
			got, survivor.ResizeLog())
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || survivor.Grid() != want {
		t.Fatalf("the revealed survivor final grid = %+v, want %+v", survivor.Grid(), want)
	}
	previous, ok := g.state.MotionEvent()
	if !ok {
		t.Fatalf("the magnified close's event disappeared")
	}
	if !g.state.CloseFocusedPane() {
		t.Fatalf("closing the then-last Pane returned false")
	}
	if got := g.state.TabCount(); got != 0 {
		t.Fatalf("the last Pane's close left %d Tabs, want 0", got)
	}
	after, ok := g.state.MotionEvent()
	if !ok || after.Kind != motion.TabClose || after.TabID != previous.TabID {
		t.Fatalf("the last Pane's close recorded %+v, want the TabClose of %q (was %+v)", after, previous.TabID, previous)
	}
	if after.Kind == motion.PaneClose {
		t.Fatalf("the last Pane's close recorded a PaneClose: %+v", after)
	}
	g.state.ReleaseTabGhosts()
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the last Pane's close started a Pane run")
	}
}

// TestPaneCloseDuringOpenStartsFromTheEnteringVisual closes a Pane while its
// own open run is still playing. The latest event wins: the exit ghost starts
// at the entering Pane's current rect and opacity, the survivor continues from
// its current shrink through the overlay route (no second native animation
// under the copy), and the final frame still has one resize per affected Pane.
func TestPaneCloseDuringOpenStartsFromTheEnteringVisual(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
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
	tt.Frame()
	waitForRealSurfaces(t, g.state, 2)
	tt.Frame()
	openRun := wm.run
	if openRun == nil {
		t.Fatalf("the split did not start an open run")
	}
	openID, _ := g.state.FocusedPaneID(0)
	ids := motionPaneIDsMust(t, g)
	survivorID := ids[0]
	if survivorID == openID {
		survivorID = ids[1]
	}
	openSurface := surfaceForPane(t, g, 0, openID)
	survivor := surfaceForPane(t, g, 0, survivorID)
	const ghostText = "interrupt-open-text"
	openSurface.feed(ghostText + "\r\n")
	pumpUntil(t, tt, "the entering terminal content", func() bool { return tt.HasText(ghostText) })

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k1, done := openRun.anim.Progress(tt.Now())
	if done || k1 <= 0 || k1 >= 1 {
		t.Fatalf("open progress = %v, %v", k1, done)
	}
	enterVisual := mustVisual(t, openRun, openID)
	seen, seenOpacity := openRun.anim.State(enterVisual, k1)
	if seenOpacity <= 0.01 || seenOpacity >= 0.99 {
		t.Fatalf("the entering Pane's visible opacity = %v, want an in-flight popin value", seenOpacity)
	}
	survivorVisual := mustVisual(t, openRun, survivorID)
	survivorSeen, _ := openRun.anim.State(survivorVisual, k1)
	openLog := len(openSurface.ResizeLog())
	survivorLog := len(survivor.ResizeLog())

	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Frame()
	closeRun := wm.run
	if closeRun == nil || closeRun == openRun {
		t.Fatalf("the close did not replace the open run")
	}
	if closeRun.kind != motion.PaneClose || closeRun.seq <= openRun.seq {
		t.Fatalf("the interrupting run = kind %v, seq %d; want PaneClose after %d",
			closeRun.kind, closeRun.seq, openRun.seq)
	}
	if closeRun.native {
		t.Fatalf("the interrupted close took the native route under the overlay")
	}
	if closeRun.anim.Duration != 150*time.Millisecond {
		t.Fatalf("the interrupting close duration = %s, want 150ms", closeRun.anim.Duration)
	}
	ghost, ok := closeRun.ghosts[openID]
	if !ok || ghost.blank || ghost.handle == nil || ghost.info.Surface != openSurface {
		t.Fatalf("the interrupting ghost = %+v, want the entering surface", ghost)
	}
	ghostVisual := mustVisual(t, closeRun, openID)
	if ghostVisual.Kind != motion.Exit {
		t.Fatalf("the interrupting ghost visual = %+v", ghostVisual)
	}
	if !rectNearOK(ghostVisual.From, seen, 1) {
		t.Fatalf("the ghost starts at %+v, want the visible entering rect %+v", ghostVisual.From, seen)
	}
	if abs32(ghostVisual.FromOpacity-seenOpacity) > 0.001 {
		t.Fatalf("the ghost starts at opacity %v, want the visible %v",
			ghostVisual.FromOpacity, seenOpacity)
	}
	if ghostVisual.Box != enterVisual.Box {
		t.Fatalf("the ghost content box = %+v, want the entering Pane's layout %+v",
			ghostVisual.Box, enterVisual.Box)
	}
	survivorMove := mustVisual(t, closeRun, survivorID)
	if survivorMove.Kind != motion.Move {
		t.Fatalf("the survivor visual = %+v", survivorMove)
	}
	if !rectNearOK(survivorMove.From, survivorSeen, 1) {
		t.Fatalf("the survivor starts at %+v, want its visible shrink %+v",
			survivorMove.From, survivorSeen)
	}
	if survivorMove.To != closeRun.final[survivorID].Column {
		t.Fatalf("the survivor move ends at %+v, want the final %+v",
			survivorMove.To, closeRun.final[survivorID].Column)
	}
	if transition, ok := wm.nativeTransition(survivorID); !ok || transition.Duration != -1 {
		t.Fatalf("the interrupted survivor has native duration %+v, want an idle match only", transition)
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); !ok {
		t.Fatalf("the interrupted close built no overlay survivor copy")
	}
	if !tt.HasText(ghostText) {
		t.Fatalf("the interrupting ghost is blank")
	}
	if got := len(survivor.ResizeLog()) - survivorLog; got != 1 {
		t.Fatalf("the survivor delivered %d resizes at the interrupting close, want 1", got)
	}
	if got := len(openSurface.ResizeLog()) - openLog; got != 0 {
		t.Fatalf("the closed Pane delivered %d resizes at the interrupting close, want none", got)
	}
	survivorCalls := survivor.GridForCalls()
	closeLog := len(survivor.ResizeLog())

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("pane-close-ghost:" + string(openID)); !ok {
		t.Fatalf("the interrupting ghost disappeared")
	}
	if _, ok := tt.Find("pane-motion:" + string(survivorID)); !ok {
		t.Fatalf("the interrupting survivor copy disappeared")
	}
	if got := survivor.GridForCalls() - survivorCalls; got != 0 {
		t.Fatalf("the survivor computed %d grids during the interrupting animation, want none", got)
	}
	if got := len(survivor.ResizeLog()) - closeLog; got != 0 {
		t.Fatalf("the survivor delivered %d resizes during the interrupting animation, want none", got)
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the interrupting close did not finish")
	}
	if got := openSurface.Closes(); got != 1 {
		t.Fatalf("the entering ghost surface closed %d times, want 1", got)
	}
	if got := len(survivor.ResizeLog()); got != survivorLog+1 {
		t.Fatalf("the survivor delivered %d resizes over the interruption, want %d",
			got, survivorLog+1)
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || survivor.Grid() != want {
		t.Fatalf("the survivor final grid = %+v, want %+v", survivor.Grid(), want)
	}
	if focused, _ := g.state.FocusedPaneID(0); focused != survivorID {
		t.Fatalf("focus after the interrupting close = %q, want %q", focused, survivorID)
	}
	if tt.HasText(ghostText) {
		t.Fatalf("the interrupting ghost outlived its run")
	}
}

// TestPaneCloseDuringCloseContinuesBothGhostsFromTheirCurrentVisuals closes a
// second Pane while a close is playing. The new run keeps the first ghost and
// starts both the new ghost and the old one from the rect and opacity on
// screen, not from stale settled boxes; every retained surface closes once.
func TestPaneCloseDuringCloseContinuesBothGhostsFromTheirCurrentVisuals(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	splitAndSettle(t, tt, g, wm)
	surfaces := splitDownAndSettle(t, tt, g, wm)
	if len(surfaces) != 3 {
		t.Fatalf("the nested split has %d surfaces, want 3", len(surfaces))
	}
	ids := motionPaneIDsMust(t, g)
	if len(ids) != 3 {
		t.Fatalf("the nested split has %d Panes, want 3", len(ids))
	}
	survivorID, firstClosedID, secondClosedID := ids[0], ids[1], ids[2]
	if focused, _ := g.state.FocusedPaneID(0); focused != secondClosedID {
		t.Fatalf("the nested split's focus = %q, want %q", focused, secondClosedID)
	}
	firstSurface := surfaceForPane(t, g, 0, firstClosedID)
	secondSurface := surfaceForPane(t, g, 0, secondClosedID)
	survivor := surfaceForPane(t, g, 0, survivorID)
	const firstText = "first-ghost-text"
	const secondText = "second-ghost-text"
	firstSurface.feed(firstText + "\r\n")
	secondSurface.feed(secondText + "\r\n")
	pumpUntil(t, tt, "both terminal contents", func() bool {
		return tt.HasText(firstText) && tt.HasText(secondText)
	})
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}

	if !g.state.CloseFocusedPane() {
		t.Fatalf("the first CloseFocusedPane returned false")
	}
	tt.Frame()
	first := wm.run
	if first == nil || !first.native {
		t.Fatalf("the first close did not start a native run: %+v", first)
	}
	firstGhost, ok := first.ghosts[secondClosedID]
	if !ok || firstGhost.handle == nil {
		t.Fatalf("the first close retained no ghost for %q", secondClosedID)
	}
	if len(first.ghosts) != 1 {
		t.Fatalf("the first close has %d ghosts, want 1", len(first.ghosts))
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	k1, done := first.anim.Progress(tt.Now())
	if done || k1 <= 0 || k1 >= 1 {
		t.Fatalf("first close progress = %v, %v", k1, done)
	}
	firstMove := mustVisual(t, first, firstClosedID)
	firstSeen, firstSeenOpacity := first.anim.State(firstMove, k1)
	secondExit := mustVisual(t, first, secondClosedID)
	secondSeen, secondSeenOpacity := first.anim.State(secondExit, k1)
	// The interruption's starts must not be the layout boxes recorded for the
	// first close: those are already final and would snap the ghosts.
	if rectNearOK(firstSeen, wm.rects[firstClosedID].Column, 1) {
		t.Fatalf("the first close's visible survivor rect %+v is already its layout box", firstSeen)
	}
	if rectNearOK(secondSeen, wm.rects[secondClosedID].Column, 1) {
		t.Fatalf("the first ghost's visible rect %+v is already its layout box", secondSeen)
	}
	if !tt.HasText(firstText) || !tt.HasText(secondText) {
		t.Fatalf("the first close's Panes are not both visible")
	}
	if !g.state.FocusPaneID(0, firstClosedID) {
		t.Fatalf("could not focus the second closing Pane %q", firstClosedID)
	}
	tt.Frame()

	if !g.state.CloseFocusedPane() {
		t.Fatalf("the second CloseFocusedPane returned false")
	}
	tt.Frame()
	second := wm.run
	if second == nil || second == first {
		t.Fatalf("the second close did not replace the first run")
	}
	if second.seq <= first.seq || second.kind != motion.PaneClose {
		t.Fatalf("the second run = kind %v, seq %d; want PaneClose after %d",
			second.kind, second.seq, first.seq)
	}
	if second.native {
		t.Fatalf("the interrupting close took the native route under its overlays")
	}
	if len(second.ghosts) != 2 {
		t.Fatalf("the second close has %d ghosts, want the new one and the carried one", len(second.ghosts))
	}
	if got := second.ghosts[secondClosedID].handle; got != firstGhost.handle {
		t.Fatalf("the carried ghost's handle changed across the interruption")
	}
	if got, ok := second.ghosts[firstClosedID]; !ok || got.handle == nil {
		t.Fatalf("the second close retained no ghost for %q", firstClosedID)
	}
	newGhost := mustVisual(t, second, firstClosedID)
	if newGhost.Kind != motion.Exit {
		t.Fatalf("the second close's new ghost visual = %+v", newGhost)
	}
	if !rectNearOK(newGhost.From, firstSeen, 1) {
		t.Fatalf("the second ghost starts at %+v, want the visible %+v", newGhost.From, firstSeen)
	}
	if abs32(newGhost.FromOpacity-firstSeenOpacity) > 0.001 {
		t.Fatalf("the second ghost starts at opacity %v, want the visible %v",
			newGhost.FromOpacity, firstSeenOpacity)
	}
	if newGhost.Box != firstMove.Box {
		t.Fatalf("the second ghost content box = %+v, want the first move's %+v",
			newGhost.Box, firstMove.Box)
	}
	carried := mustVisual(t, second, secondClosedID)
	if carried.Kind != motion.Exit {
		t.Fatalf("the carried ghost visual = %+v", carried)
	}
	if !rectNearOK(carried.From, secondSeen, 1) {
		t.Fatalf("the carried ghost starts at %+v, want the visible %+v", carried.From, secondSeen)
	}
	if abs32(carried.FromOpacity-secondSeenOpacity) > 0.001 {
		t.Fatalf("the carried ghost starts at opacity %v, want the visible %v",
			carried.FromOpacity, secondSeenOpacity)
	}
	survivorMove, ok := second.anim.VisualFor(survivorID)
	if !ok || survivorMove.Kind != motion.Move {
		t.Fatalf("the final survivor has no move visual: %+v", second.anim.Visuals)
	}
	if survivorMove.To != second.final[survivorID].Column {
		t.Fatalf("the final survivor ends at %+v, want %+v",
			survivorMove.To, second.final[survivorID].Column)
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(firstClosedID)); !ok {
		t.Fatalf("the second close built no ghost for the newly closed Pane")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(secondClosedID)); !ok {
		t.Fatalf("the second close dropped the carried ghost")
	}
	if !tt.HasText(firstText) || !tt.HasText(secondText) {
		t.Fatalf("the second close's ghosts are not both drawn")
	}
	if got := len(firstSurface.ResizeLog()); got != 1 {
		t.Fatalf("the interrupted survivor delivered %d resizes in the first close, want 1", got)
	}
	if got := len(secondSurface.ResizeLog()); got != 0 {
		t.Fatalf("the first closed Pane delivered %d resizes, want none", got)
	}
	if got := len(survivor.ResizeLog()); got != 1 {
		t.Fatalf("the final survivor delivered %d resizes at the second close, want 1", got)
	}
	survivorCalls := survivor.GridForCalls()
	firstLog := len(firstSurface.ResizeLog())
	secondLog := len(secondSurface.ResizeLog())
	survivorLog := len(survivor.ResizeLog())

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("pane-close-ghost:" + string(firstClosedID)); !ok {
		t.Fatalf("the new ghost disappeared mid-run")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(secondClosedID)); !ok {
		t.Fatalf("the carried ghost disappeared mid-run")
	}
	if got := survivor.GridForCalls() - survivorCalls; got != 0 {
		t.Fatalf("the final survivor computed %d grids during the interruption, want none", got)
	}
	if got := len(survivor.ResizeLog()) - survivorLog; got != 0 {
		t.Fatalf("the final survivor delivered %d resizes during the interruption, want none", got)
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the second close did not finish")
	}
	if got := firstSurface.Closes(); got != 1 {
		t.Fatalf("the second closed surface closed %d times, want 1", got)
	}
	if got := secondSurface.Closes(); got != 1 {
		t.Fatalf("the first closed surface closed %d times, want 1", got)
	}
	if got := len(firstSurface.ResizeLog()); got != firstLog {
		t.Fatalf("the second closed Pane delivered %d resizes during the interruption, want %d",
			got, firstLog)
	}
	if got := len(secondSurface.ResizeLog()); got != secondLog {
		t.Fatalf("the first closed Pane delivered %d resizes during the interruption, want %d",
			got, secondLog)
	}
	if got := len(survivor.ResizeLog()); got != survivorLog {
		t.Fatalf("the final survivor delivered %d resizes over the interruption, want %d",
			got, survivorLog)
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || survivor.Grid() != want {
		t.Fatalf("the final survivor grid = %+v, want %+v", survivor.Grid(), want)
	}
	if tt.HasText(firstText) || tt.HasText(secondText) {
		t.Fatalf("a ghost outlived the run")
	}
}

// TestPaneCloseBlankGhostWhenTheSurfaceCannotDrawAStillCopy is the last
// resort: when a retained surface cannot paint its still copy, the ghost is an
// opaque Pane-shaped background with no content. It must not panic, draw the
// connecting placeholder, reflow the terminal, or leak the surface.
func TestPaneCloseBlankGhostWhenTheSurfaceCannotDrawAStillCopy(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)
	surfaces := splitAndSettle(t, tt, g, wm)
	closedID, _ := g.state.FocusedPaneID(0)
	ids := motionPaneIDsMust(t, g)
	survivorID := ids[0]
	if survivorID == closedID {
		survivorID = ids[1]
	}
	closedSurface := surfaceForPane(t, g, 0, closedID)
	survivor := surfaceForPane(t, g, 0, survivorID)
	old := wm.rects[closedID]
	const content = "blank-fallback-text"
	closedSurface.feed(content + "\r\n")
	pumpUntil(t, tt, "the terminal content before the blank fallback", func() bool {
		return tt.HasText(content)
	})
	// Model a Surface that cannot draw a still copy. The state still has its
	// info, so this is the surface-level blank fallback, not a missing ghost.
	closedSurface.dead = true
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}

	if !g.state.CloseFocusedPane() {
		t.Fatalf("CloseFocusedPane returned false")
	}
	tt.Frame()
	run := wm.run
	if run == nil {
		t.Fatalf("the blank-fallback close did not start a run")
	}
	ghost, ok := run.ghosts[closedID]
	if !ok || ghost.blank || ghost.handle == nil || ghost.info.Surface != closedSurface {
		t.Fatalf("the blank-fallback ghost = %+v, want the retained surface info", ghost)
	}
	ghostLabel, ok := tt.Find("pane-close-ghost:" + string(closedID))
	if !ok {
		t.Fatalf("the blank fallback built no opaque ghost element")
	}
	rectNear(t, ghostLabel, clipToWindow(old.Column), 0.75, "the blank ghost")
	if tt.HasText(content) {
		t.Fatalf("the blank fallback drew the terminal content")
	}
	if tt.HasText(locales.In("en").PaneConnecting()) {
		t.Fatalf("the blank fallback drew the connecting placeholder")
	}
	if got := len(closedSurface.ResizeLog()); got != 0 {
		t.Fatalf("the blank ghost Pane delivered %d resizes, want none", got)
	}
	if got := len(survivor.ResizeLog()); got != 1 {
		t.Fatalf("the survivor delivered %d resizes at the blank close, want 1", got)
	}
	survivorCalls := survivor.GridForCalls()

	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); !ok {
		t.Fatalf("the blank ghost disappeared mid-run")
	}
	if got := survivor.GridForCalls() - survivorCalls; got != 0 {
		t.Fatalf("the survivor computed %d grids during the blank ghost, want none", got)
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatal("the blank-fallback close did not finish")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
		t.Fatalf("the blank ghost outlived the run")
	}
	if got := closedSurface.Closes(); got != 1 {
		t.Fatalf("the blank fallback surface closed %d times, want 1", got)
	}
	want, ok := appview.GridFor(survivor.Viewport())
	if !ok || survivor.Grid() != want {
		t.Fatalf("the survivor final grid = %+v, want %+v", survivor.Grid(), want)
	}
}

// mustVisual returns the running visual for a Pane, failing when it is absent.
func mustVisual(t *testing.T, run *paneMotionRun, id model.PaneID) motion.Visual {
	t.Helper()
	v, ok := run.anim.VisualFor(id)
	if !ok {
		t.Fatalf("the run has no visual for Pane %s: %+v", id, run.anim.Visuals)
	}
	return v
}
