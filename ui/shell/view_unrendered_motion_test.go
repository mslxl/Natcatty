package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// assertPaneMotionWarped checks that a view consumed the latest Pane event as
// a warp: no run, no animation frame, the controller caught up, and the frame
// it just built is the final layout (the caller checks the drawing).
func assertPaneMotionWarped(t *testing.T, view string, wm *workspacePaneMotion, framesBefore int) {
	t.Helper()
	if wm.run != nil {
		t.Fatalf("%s replayed a Pane event it never rendered: %+v", view, wm.run)
	}
	if got := wm.animationFrames; got != framesBefore {
		t.Fatalf("%s's unrendered warp asked for %d animation frames, want none", view, got-framesBefore)
	}
	event, ok := wm.state.MotionEvent()
	if !ok || wm.controller.Unseen(event.Seq) {
		t.Fatalf("%s did not catch its sequence up to %d", view, event.Seq)
	}
}

// TestMainViewWarpsThePaneEventItDidNotRender is the verifier's main/Topdown
// probe: two renderers on one shell state, the event consumed by the one that
// was rendered, and the app's unrendered notice makes the other warp on its
// next single frame instead of replaying stale Pane boxes.
func TestMainViewWarpsThePaneEventItDidNotRender(t *testing.T) {
	g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 1)
	mainView, mainWM := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	topView, topWM := topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
	mainTT := ui.NewTester(mainView, 900, 600)
	topTT := ui.NewTester(topView, 900, 600)
	mainTT.SetLocale("en")
	topTT.SetLocale("en")
	for range 2 {
		mainTT.Frame()
		topTT.Frame()
	}

	// Topdown draws the event; the main window was bypassed (its placeholder
	// drew, or the app never ran it), so the app records the gap.
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	topTT.Frame()
	if topWM.run == nil {
		t.Fatal("Topdown did not consume the Pane open")
	}
	mainFrames := mainWM.animationFrames
	g.state.NoteMotionViewUnrendered(state.MotionViewMain)
	mainTT.Frame()
	assertPaneMotionWarped(t, "the main view", mainWM, mainFrames)
	if got := countText(mainTT.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("the main view drew %d Pane surfaces after its warp, want the final 2; texts %q",
			got, mainTT.Texts())
	}

	// Mirror: the main window consumes the next event, Topdown is the
	// bypassed view. The idle frame presents the warped final layout and lets
	// the runtime record the Pane it just created; the event after it must
	// animate, proving the notice was one-shot rather than sticky.
	mainTT.Frame()
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 3)
	mainTT.Frame()
	if mainWM.run == nil {
		t.Fatal("the main view did not animate the visible event after its warp")
	}
	topFrames := topWM.animationFrames
	g.state.NoteMotionViewUnrendered(state.MotionViewTopdown)
	topTT.Frame()
	assertPaneMotionWarped(t, "Topdown", topWM, topFrames)
	if got := countText(topTT.Texts(), "terminal-surface"); got != 3 {
		t.Fatalf("Topdown drew %d Pane surfaces after its warp, want the final 3; texts %q",
			got, topTT.Texts())
	}
}

// TestFollowViewWarpsThePaneEventItDidNotRender covers the Follow route: the
// app records an unrendered gap, the next single frame warps and catches up,
// and the following visible event still animates.
func TestFollowViewWarpsThePaneEventItDidNotRender(t *testing.T) {
	g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 1)
	view, wm := followWithPaneMotion(g.state)
	tt := ui.NewTester(view, 600, 400)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()

	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	frames := wm.animationFrames
	g.state.NoteMotionViewUnrendered(state.MotionViewFollow)
	tt.Frame()
	assertPaneMotionWarped(t, "Follow", wm, frames)
	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("Follow drew %d Pane surfaces after its warp, want the final 2; texts %q",
			got, tt.Texts())
	}

	// One-shot: the idle frame presents the warped final layout (and records
	// the Pane it created); the event after it animates normally.
	tt.Frame()
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 3)
	tt.Frame()
	if wm.run == nil {
		t.Fatal("Follow did not animate the visible event after its warp")
	}
}

// TestFollowBarWarpsTheStripEventItDidNotRender covers the Panel's mini strip:
// the gap notice warps its row in one frame with no animation frames, and the
// next visible event still slides.
func TestFollowBarWarpsTheStripEventItDidNotRender(t *testing.T) {
	graph := newFollowGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)
	strip := &tabStripMotion{positionOnly: true}
	tt := ui.NewTester(followBarWithStrip(graph.state, strip), 600, 120)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	pane, ok := graph.state.FocusedPaneID(1)
	if !ok || !graph.state.RenamePane(1, pane, "unrendered-open") {
		t.Fatal("renaming the opening Tab's Pane failed")
	}
	frames := strip.animationFrames
	graph.state.NoteMotionViewUnrendered(state.MotionViewFollowBar)
	tt.Frame()
	if strip.run != nil || strip.animate {
		t.Fatalf("the mini strip replayed an event it never rendered: run %+v animate %v",
			strip.run, strip.animate)
	}
	if got := strip.animationFrames; got != frames {
		t.Fatalf("the mini strip's unrendered warp asked for %d animation frames, want none", got-frames)
	}
	event := stripEvent(t, graph.state, motion.TabOpen)
	if strip.lastSeq != event.Seq {
		t.Fatalf("the mini strip caught up to %d, want the latest event %d", strip.lastSeq, event.Seq)
	}
	if !strip.warp {
		t.Fatal("the mini strip did not build its row as a warp")
	}
	if !tt.HasText("unrendered-open") {
		t.Fatalf("the mini strip did not draw the final row: %v", tt.Texts())
	}
	// The row is final on this frame: the layout arrives at once and the
	// selected fill is not faded (Position-only, immediate).
	if transition, ok := strip.transition(graph.state.Motion(), false); !ok ||
		transition.Duration >= 0 || transition.Colors || !transition.Position {
		t.Fatalf("the mini strip's warp transition = %+v, want an immediate Position-only row", transition)
	}

	// One-shot: a visible open after the warp slides again.
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 3)
	tt.Frame()
	if run := strip.run; run == nil || run.kind != motion.TabOpen {
		t.Fatalf("the mini strip's next visible event = %+v, want a Tab open", run)
	}
}

// TestTopdownViewWarpsTheStripEventItDidNotRender covers the work strip of the
// Topdown route through the same unrendered notice.
func TestTopdownViewWarpsTheStripEventItDidNotRender(t *testing.T) {
	g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 1)
	reveal := newWorkTabReveal()
	view, _ := topdownWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	// A wide window keeps the control open below the strip's overflow
	// prediction, so it exercises the normal animated route.
	tt := ui.NewTester(view, 1600, 600)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	pane, ok := g.state.FocusedPaneID(1)
	if !ok || !g.state.RenamePane(1, pane, "topdown-unrendered-open") {
		t.Fatal("renaming the opening Tab's Pane failed")
	}
	strip := &reveal.strip
	frames := strip.animationFrames
	g.state.NoteMotionViewUnrendered(state.MotionViewTopdown)
	tt.Frame()
	if strip.run != nil || strip.animate {
		t.Fatalf("the Topdown strip replayed an event it never rendered: run %+v animate %v",
			strip.run, strip.animate)
	}
	if got := strip.animationFrames; got != frames {
		t.Fatalf("the Topdown strip's unrendered warp asked for %d animation frames, want none", got-frames)
	}
	event := stripEvent(t, g.state, motion.TabOpen)
	if strip.lastSeq != event.Seq {
		t.Fatalf("the Topdown strip caught up to %d, want the latest event %d", strip.lastSeq, event.Seq)
	}
	if !strip.warp {
		t.Fatal("the Topdown strip did not build its row as a warp")
	}
	if !tt.HasText("topdown-unrendered-open") {
		t.Fatalf("the Topdown strip did not draw the final row: %v", tt.Texts())
	}

	// One-shot: a visible open after the warp animates again.
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 3)
	tt.Frame()
	if run := strip.run; run == nil || run.kind != motion.TabOpen {
		t.Fatalf("the Topdown strip's next visible event = %+v, want a Tab open", run)
	}
}

// TestDetachedViewWarpsThePaneEventItNeverShowed covers a detached window: a
// Pane event recorded while its content was replaced (the app marks the window
// when the App Lock gate draws the lock screen) is warped by the next single
// frame, with no new animation frame, and the following visible event still
// animates.
//
// A detached window's runtime only plays events for its own fixed Tab, and the
// state has no split operation on a detached Tab. The test therefore reattaches
// the same Tab (its identity is preserved), splits it through the production
// command, and detaches it again; the recorded PaneOpen's TabID is still the
// detached window's Tab, and the view never rendered the gap.
func TestDetachedViewWarpsThePaneEventItNeverShowed(t *testing.T) {
	shellState, detached, _ := newDetachedGraph(t)
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph := &workspaceViewGraph{state: shellState}
	graph.waitForSurfaces(t, 1)
	if !shellState.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	tabID := (*detached)[0]

	wm := newDetachedPaneMotion(shellState, tabID)
	tt := ui.NewTester(detachedView(shellState, tabID, wm), 640, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()

	// Grow the same Tab's tree through the production split, then return it to
	// its own window before the view renders again.
	if !shellState.ReattachDetachedTab(tabID) {
		t.Fatal("ReattachDetachedTab returned false")
	}
	if err := shellState.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	graph.waitForSurfaces(t, 2)
	if !shellState.DetachTab(0) {
		t.Fatal("the second DetachTab returned false")
	}
	if ids := shellState.DetachedTabIDs(); len(ids) != 1 || ids[0] != tabID {
		t.Fatalf("detached Tabs = %v, want the original %q", ids, tabID)
	}
	event, ok := shellState.MotionEvent()
	if !ok || event.Kind != motion.PaneOpen || event.TabID != tabID {
		t.Fatalf("latest event = %+v, want this window's Pane open", event)
	}

	frames := wm.animationFrames
	shellState.NoteDetachedViewUnrendered(tabID)
	tt.Frame()
	assertPaneMotionWarped(t, "the detached view", wm, frames)
	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("the detached view drew %d Pane surfaces after its warp, want the final 2; texts %q",
			got, tt.Texts())
	}
	// The idle frame presents the warped final layout and records the Pane the
	// warp created, so the next event has real starting boxes.
	tt.Frame()

	// One-shot: another event of the same Tab, seen while the window is
	// visible, animates again.
	if !shellState.ReattachDetachedTab(tabID) {
		t.Fatal("the second ReattachDetachedTab returned false")
	}
	if err := shellState.SplitRight(); err != nil {
		t.Fatalf("the second SplitRight: %v", err)
	}
	graph.waitForSurfaces(t, 3)
	if !shellState.DetachTab(0) {
		t.Fatal("the third DetachTab returned false")
	}
	tt.Frame()
	if wm.run == nil {
		t.Fatal("the detached view did not animate the visible event after its warp")
	}
}

// TestNativeWindowHiddenWarpsTheNextFramePerView keeps the window's own
// hidden signal working beside the app's notice: a rendered frame while
// hidden, an event, then the show frame warps and asks for no frame in every
// shell renderer.
func TestNativeWindowHiddenWarpsTheNextFramePerView(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			g.waitForSurfaces(t, 1)
			view, wm := tc.mount(g)
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			tt.Frame()
			tt.Frame()

			tt.SetWindowHidden(true)
			tt.Frame() // a built frame that reads the window as hidden
			frames := wm.animationFrames
			if err := g.state.SplitRight(); err != nil {
				t.Fatal(err)
			}
			g.waitForSurfaces(t, 2)
			tt.SetWindowHidden(false)
			tt.Frame()
			assertPaneMotionWarped(t, tc.name, wm, frames)
			if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
				t.Fatalf("%s drew %d Pane surfaces after the show frame, want the final 2; texts %q",
					tc.name, got, tt.Texts())
			}
		})
	}
}
