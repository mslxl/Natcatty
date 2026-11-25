package shell

import (
	"bytes"
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

// fakeMotionGraph is a split-capable graph whose Pane surfaces are the opaque
// fakes. It is enough for playback and independence: the real-terminal
// graph's viewport bookkeeping is covered by the Topdown no-reflow test.
func fakeMotionGraph(t *testing.T, setting domainpreferences.Motion) *workspaceViewGraph {
	t.Helper()
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	return &workspaceViewGraph{state: state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithMotion(func() domainpreferences.Motion { return setting }),
	)}
}

// topdownWithPaneMotion is the Topdown drop-down renderer with its own
// runtime exposed. Production builds the same runtime inside TopdownView.
func topdownWithPaneMotion(s *state.State, mode paneMotionMode) (func(*ui.Context), *workspacePaneMotion) {
	wm := newWorkspacePaneMotion(s, mode)
	return topdownView(s, wm), wm
}

// followWithPaneMotion is the Follow renderer with its Pane-only runtime
// exposed.
func followWithPaneMotion(s *state.State) (func(*ui.Context), *workspacePaneMotion) {
	wm := newActivePaneOnlyMotion(s)
	wm.mode = paneMotionMode{labels: true}
	return followView(s, wm), wm
}

// runVisual is the visual for id in wm's current run.
func runVisual(t *testing.T, wm *workspacePaneMotion, id model.PaneID) motion.Visual {
	t.Helper()
	run := wm.run
	if run == nil {
		t.Fatalf("no Pane run is playing")
	}
	visual, ok := run.anim.VisualFor(id)
	if !ok {
		t.Fatalf("Pane %q has no visual in %+v", id, run.anim.Visuals)
	}
	return visual
}

// TestTopdownViewPlaysItsOwnPaneOpen is ticket 21a's Topdown acceptance: the
// drop-down route owns a motion runtime, plays a Pane open from its own
// recorded boxes, does no grid math or PTY resize per animation frame, and
// rests at the final layout with exactly one resize per affected Pane.
func TestTopdownViewPlaysItsOwnPaneOpen(t *testing.T) {
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	surfaces := settlePane(t, tt, g, 0)
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	oldID, _ := g.state.FocusedPaneID(0)
	oldStart, ok := wm.rects[oldID]
	if !ok || !oldStart.Valid() {
		t.Fatalf("the settled Topdown frame recorded no old-Pane boxes: %+v", oldStart)
	}

	surfaces = startPaneOpen(t, tt, g)
	run := wm.run
	if run == nil {
		t.Fatal("the Topdown split did not start a Pane-open run")
	}
	if want, _ := g.state.TabID(0); run.tabID != want {
		t.Fatalf("run tab = %q, want the active Tab %q", run.tabID, want)
	}
	if run.target != oldID {
		t.Fatalf("run target = %q, want the split Pane %q", run.target, oldID)
	}
	if got := run.anim.Duration; got != 200*time.Millisecond {
		t.Fatalf("run duration = %s, want 200ms at standard speed", got)
	}
	if visual := runVisual(t, wm, oldID); visual.From != oldStart.Column {
		t.Fatalf("the old visual starts at %+v, want the view's own previous Pane %+v",
			visual.From, oldStart.Column)
	}
	newID, _ := g.state.FocusedPaneID(0)
	newVisual, ok := run.anim.VisualFor(newID)
	if !ok || newVisual.Kind != motion.Enter {
		t.Fatalf("the new Pane has no entry visual: %+v", run.anim.Visuals)
	}
	if _, ok := tt.Find("pane-motion:" + string(oldID)); !ok {
		t.Fatalf("the Topdown overlay has no old-Pane copy")
	}
	if _, ok := tt.Find("pane-motion:" + string(newID)); !ok {
		t.Fatalf("the Topdown overlay has no new-Pane copy")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(oldID)); !ok {
		t.Fatalf("the Topdown overlay has no old-Pane mask")
	}
	if got := len(surfaces[0].ResizeLog()); got != 1 {
		t.Fatalf("the old Pane delivered %d resizes at the change, want 1", got)
	}
	if got := len(surfaces[1].ResizeLog()); got != 1 {
		t.Fatalf("the new Pane delivered %d resizes at the change, want 1", got)
	}
	changeCalls := []int{surfaces[0].GridForCalls(), surfaces[1].GridForCalls()}
	changeLogs := []int{len(surfaces[0].ResizeLog()), len(surfaces[1].ResizeLog())}

	tt.Advance(80 * time.Millisecond)
	tt.Frame()
	k, done := run.anim.Progress(tt.Now())
	if done || k <= 0 || k >= 1 {
		t.Fatalf("mid-run progress = %v, %v, want inside (0,1)", k, done)
	}
	if got := surfaces[0].GridForCalls() - changeCalls[0]; got != 0 {
		t.Errorf("the old Pane computed %d grids during Topdown animation frames, want none", got)
	}
	if got := surfaces[1].GridForCalls() - changeCalls[1]; got != 0 {
		t.Errorf("the new Pane computed %d grids during Topdown animation frames, want none", got)
	}
	if got := len(surfaces[0].ResizeLog()) - changeLogs[0]; got != 0 {
		t.Errorf("the old Pane delivered %d resizes during Topdown animation frames, want none", got)
	}
	if got := len(surfaces[1].ResizeLog()) - changeLogs[1]; got != 0 {
		t.Errorf("the new Pane delivered %d resizes during Topdown animation frames, want none", got)
	}

	tt.Advance(200 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatal("the Topdown run is still playing after its duration")
	}
	if _, ok := tt.Find("pane-motion:" + string(oldID)); ok {
		t.Fatal("the Topdown old-Pane copy is still built after the run")
	}
	if _, ok := tt.Find("pane-motion-mask:" + string(newID)); ok {
		t.Fatal("the Topdown mask is still built after the run")
	}
	for i, surface := range surfaces {
		want, ok := appview.GridFor(surface.Viewport())
		if !ok || surface.Grid() != want {
			t.Errorf("Topdown Pane %d grid = %+v, want its final viewport's %+v", i+1, surface.Grid(), want)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Errorf("Topdown Pane %d delivered %d resizes over the run, want exactly 1", i+1, got)
		}
	}
}

// TestMainAndTopdownPlayTheSameEventIndependently is ticket 21a's
// same-state/multiple-views acceptance: each renderer consumes the event with
// its own controller and own recorded boxes, and one renderer's clock
// finishing its copy leaves the other's run playing.
func TestMainAndTopdownPlayTheSameEventIndependently(t *testing.T) {
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
	oldID, _ := g.state.FocusedPaneID(0)
	mainOld, ok := mainWM.rects[oldID]
	if !ok || !mainOld.Valid() {
		t.Fatalf("the main view recorded no old-Pane box: %+v", mainOld)
	}
	topOld, ok := topWM.rects[oldID]
	if !ok || !topOld.Valid() {
		t.Fatalf("Topdown recorded no old-Pane box: %+v", topOld)
	}

	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	mainTT.Frame()
	mainRun := mainWM.run
	if mainRun == nil {
		t.Fatal("the main window did not start its Pane-open run")
	}
	if topWM.run != nil {
		t.Fatal("Topdown consumed the event before its own frame")
	}
	topTT.Frame()
	topRun := topWM.run
	if topRun == nil {
		t.Fatal("Topdown did not start its own Pane-open run")
	}
	if topRun == mainRun {
		t.Fatal("the two views share one run")
	}
	if topRun.seq != mainRun.seq || topRun.target != mainRun.target || topRun.tabID != mainRun.tabID {
		t.Fatalf("runs differ: main %+v, topdown %+v", mainRun, topRun)
	}
	if visual := runVisual(t, mainWM, oldID); visual.From != mainOld.Column {
		t.Fatalf("the main visual starts at %+v, want its own previous Pane %+v", visual.From, mainOld.Column)
	}
	if visual := runVisual(t, topWM, oldID); visual.From != topOld.Column {
		t.Fatalf("the Topdown visual starts at %+v, want its own previous Pane %+v", visual.From, topOld.Column)
	}

	mainTT.Advance(200 * time.Millisecond)
	mainTT.Frame()
	if mainWM.run != nil {
		t.Fatal("the main run is still playing after its own duration")
	}
	if topWM.run != topRun {
		t.Fatal("the main view finishing its run ended Topdown's copy")
	}
	if _, done := topRun.anim.Progress(topTT.Now()); done {
		t.Fatal("Topdown's copy finished on the main window's clock")
	}

	topTT.Advance(200 * time.Millisecond)
	topTT.Frame()
	if topWM.run != nil {
		t.Fatal("Topdown's run is still playing after its own duration")
	}
}

// TestMainAndTopdownTabStripsPlayIndependently covers the strip half of the
// Topdown ownership rule: the same Tab open moves the main strip and the
// drop-down strip with separate runtimes, and a Topdown event it never showed
// warps without asking for a frame.
func TestMainAndTopdownTabStripsPlayIndependently(t *testing.T) {
	g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 1)
	mainReveal := newWorkTabReveal()
	topReveal := newWorkTabReveal()
	mainView, _ := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: mainReveal})
	topView, _ := topdownWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: topReveal})
	mainTT := ui.NewTester(mainView, 900, 600)
	topTT := ui.NewTester(topView, 900, 600)
	mainTT.SetLocale("en")
	topTT.SetLocale("en")
	for range 2 {
		mainTT.Frame()
		topTT.Frame()
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	mainTT.Frame()
	mainRun := mainReveal.strip.run
	if mainRun == nil || mainRun.kind != motion.TabOpen {
		t.Fatalf("the main strip run = %+v, want a Tab open", mainRun)
	}
	if topReveal.strip.run != nil {
		t.Fatal("Topdown's strip consumed the event before its own frame")
	}
	topTT.Frame()
	topRun := topReveal.strip.run
	if topRun == nil || topRun.kind != motion.TabOpen {
		t.Fatalf("the Topdown strip run = %+v, want its own Tab open", topRun)
	}
	if topRun == mainRun {
		t.Fatal("the main and Topdown strips share one run")
	}

	// A Topdown strip event seen only on the show frame warps and asks for no
	// frames, while the main strip's runtime is untouched.
	topFrames := topReveal.strip.animationFrames
	topTT.SetWindowHidden(true)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 3)
	topTT.Advance(48 * time.Millisecond)
	topTT.SetWindowHidden(false)
	if topReveal.strip.run != nil {
		t.Fatalf("the hidden Topdown strip replayed a run: %+v", topReveal.strip.run)
	}
	if got := topReveal.strip.animationFrames; got != topFrames {
		t.Fatalf("the hidden Topdown strip asked for %d animation frames, want none", got-topFrames)
	}
}

// TestMainAndFollowPlayTheSameEventIndependently repeats the independence
// acceptance for the Follow route, whose Pane-only runtime reads the same
// active Tab through a read-only ref and records boxes from its own, smaller
// window.
func TestMainAndFollowPlayTheSameEventIndependently(t *testing.T) {
	g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 1)
	mainView, mainWM := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	follow, followWM := followWithPaneMotion(g.state)
	mainTT := ui.NewTester(mainView, 900, 600)
	followTT := ui.NewTester(follow, 600, 400)
	mainTT.SetLocale("en")
	followTT.SetLocale("en")
	for range 2 {
		mainTT.Frame()
		followTT.Frame()
	}
	oldID, _ := g.state.FocusedPaneID(0)
	mainOld, ok := mainWM.rects[oldID]
	if !ok || !mainOld.Valid() {
		t.Fatalf("the main view recorded no old-Pane box: %+v", mainOld)
	}
	followOld, ok := followWM.rects[oldID]
	if !ok || !followOld.Valid() {
		t.Fatalf("Follow recorded no old-Pane box: %+v", followOld)
	}

	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	mainTT.Frame()
	mainRun := mainWM.run
	if mainRun == nil {
		t.Fatal("the main window did not start its Pane-open run")
	}
	followTT.Frame()
	followRun := followWM.run
	if followRun == nil {
		t.Fatal("Follow did not start its own Pane-open run")
	}
	if followRun == mainRun {
		t.Fatal("the main window and Follow share one run")
	}
	if followRun.seq != mainRun.seq || followRun.target != mainRun.target {
		t.Fatalf("Follow run %+v does not describe the same event as main %+v", followRun, mainRun)
	}
	if visual := runVisual(t, mainWM, oldID); visual.From != mainOld.Column {
		t.Fatalf("the main visual starts at %+v, want its own previous Pane %+v", visual.From, mainOld.Column)
	}
	if visual := runVisual(t, followWM, oldID); visual.From != followOld.Column {
		t.Fatalf("the Follow visual starts at %+v, want its own previous Pane %+v", visual.From, followOld.Column)
	}
	if mainOld.Column == followOld.Column {
		t.Fatalf("main and Follow recorded the same whole-Pane box %+v, so the views shared geometry",
			mainOld.Column)
	}

	mainTT.Advance(200 * time.Millisecond)
	mainTT.Frame()
	if mainWM.run != nil {
		t.Fatal("the main run is still playing after its own duration")
	}
	if followWM.run != followRun {
		t.Fatal("the main view finishing its run ended Follow's copy")
	}
	if _, done := followRun.anim.Progress(followTT.Now()); done {
		t.Fatal("Follow's copy finished on the main window's clock")
	}

	followTT.Advance(200 * time.Millisecond)
	followTT.Frame()
	if followWM.run != nil {
		t.Fatal("Follow's run is still playing after its own duration")
	}
}

// TestTopdownViewWarpsPaneMotionItDidNotShow is ticket 21a's hidden-view
// acceptance for the drop-down route: an event seen only on the show frame
// warps, asks for no animation frame, and catches its sequence up.
func TestTopdownViewWarpsPaneMotionItDidNotShow(t *testing.T) {
	g := fakeMotionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 1)
	view, wm := topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	before := wm.animationFrames

	tt.SetWindowHidden(true)
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	tt.Advance(48 * time.Millisecond)
	tt.SetWindowHidden(false)

	if wm.run != nil {
		t.Fatalf("the hidden Topdown view replayed the Pane-open it did not see")
	}
	if got := wm.animationFrames; got != before {
		t.Fatalf("the hidden Topdown warp asked for %d animation frames, want none", got-before)
	}
	newID, _ := g.state.FocusedPaneID(0)
	if _, ok := tt.Find("pane-motion:" + string(newID)); ok {
		t.Fatal("the hidden Topdown warp built a moving copy")
	}
	event, ok := g.state.MotionEvent()
	if !ok || wm.controller.Unseen(event.Seq) {
		t.Fatal("the hidden Topdown view did not catch its sequence up")
	}
}

// TestFollowViewWarpsPaneMotionItDidNotShow covers the unrendered gap the
// Follow route can really produce: while its placeholder is drawn no Pane tree
// exists, so the next real frame must warp, ask for no frame, and catch up.
func TestFollowViewWarpsPaneMotionItDidNotShow(t *testing.T) {
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
	before := wm.animationFrames

	g.state.ShowVault()
	tt.Frame()
	if !wm.skipped {
		t.Fatal("the Follow placeholder did not mark the gap as unrendered")
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	g.waitForSurfaces(t, 2)
	// The Pane opens and closes again before Follow draws a real tree. The
	// newest event's target is gone by the time the view returns, so the
	// final layout must warp with no stale ghost or moving copy.
	closedID, _ := g.state.FocusedPaneID(0)
	if !g.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	g.waitForSurfaces(t, 1)
	// Choosing the already-active Tab leaves the Vault without recording a Tab
	// switch; ShowHome would go back to the Vault page the home Tab last showed.
	g.state.SelectTab(0)
	tt.Advance(24 * time.Millisecond)
	tt.Frame()

	if wm.run != nil {
		t.Fatalf("Follow replayed a Pane event from its placeholder: %+v", wm.run)
	}
	if got := wm.animationFrames; got != before {
		t.Fatalf("Follow's unrendered warp asked for %d animation frames, want none", got-before)
	}
	newID, _ := g.state.FocusedPaneID(0)
	if _, ok := tt.Find("pane-motion:" + string(newID)); ok {
		t.Fatal("Follow's unrendered warp built a moving copy")
	}
	if _, ok := tt.Find("pane-close-ghost:" + string(closedID)); ok {
		t.Fatal("Follow's unrendered warp drew the stale closed target")
	}
	event, ok := g.state.MotionEvent()
	if !ok || wm.controller.Unseen(event.Seq) {
		t.Fatal("Follow did not catch its sequence up")
	}
}

// TestViewMotionSwitchesAndReduceMotionWarpPerView keeps every view's
// master, event and accessibility switches local: Topdown and Follow each
// warp the same split with no run and no animation frames.
func TestViewMotionSwitchesAndReduceMotionWarpPerView(t *testing.T) {
	type options struct {
		setting domainpreferences.Motion
		reduce  bool
	}
	cases := []struct {
		name string
		view string
		opts options
	}{
		{"topdown master off", "topdown", options{setting: paneMotionPreferences(false, true, domainpreferences.MotionPopin)}},
		{"topdown PaneOpen off", "topdown", options{setting: paneMotionPreferences(true, false, domainpreferences.MotionPopin)}},
		{"topdown Reduce Motion", "topdown", options{setting: paneMotionPreferences(true, true, domainpreferences.MotionPopin), reduce: true}},
		{"follow master off", "follow", options{setting: paneMotionPreferences(false, true, domainpreferences.MotionPopin)}},
		{"follow PaneOpen off", "follow", options{setting: paneMotionPreferences(true, false, domainpreferences.MotionPopin)}},
		{"follow Reduce Motion", "follow", options{setting: paneMotionPreferences(true, true, domainpreferences.MotionPopin), reduce: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := fakeMotionGraph(t, tc.opts.setting)
			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			g.waitForSurfaces(t, 1)
			var view func(*ui.Context)
			var wm *workspacePaneMotion
			if tc.view == "topdown" {
				view, wm = topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
			} else {
				view, wm = followWithPaneMotion(g.state)
			}
			tt := ui.NewTester(view, 900, 600)
			tt.SetLocale("en")
			if tc.opts.reduce {
				tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			}
			tt.Frame()
			tt.Frame()
			before := wm.animationFrames

			if err := g.state.SplitRight(); err != nil {
				t.Fatal(err)
			}
			g.waitForSurfaces(t, 2)
			tt.Frame()
			root, _, ok := g.state.TabTree(0)
			if !ok || len(domainworkspace.CollectPaneIDs(root)) != 2 {
				t.Fatal("the split did not reach its final two-Pane layout")
			}
			if wm.run != nil {
				t.Fatalf("%s played a run despite its switch", tc.name)
			}
			if got := wm.animationFrames; got != before {
				t.Fatalf("%s asked for %d animation frames, want none", tc.name, got-before)
			}
			event, ok := g.state.MotionEvent()
			if !ok || wm.controller.Unseen(event.Seq) {
				t.Fatalf("%s did not catch its sequence up", tc.name)
			}
			newID, _ := g.state.FocusedPaneID(0)
			if _, ok := tt.Find("pane-motion:" + string(newID)); ok {
				t.Fatalf("%s built a moving copy despite its switch", tc.name)
			}
			if !tt.HasText("terminal-surface") {
				t.Fatalf("%s lost the Pane surface: %v", tc.name, tt.Texts())
			}
		})
	}
}

// TestDetachedViewOwnsItsPaneRuntimeAndHasNoTabStrip keeps a detached window
// per-window: its Pane runtime reads only its own fixed Tab, ignores a main
// Tab's Pane event, asks for no frame, and builds no Tab strip or body.
func TestDetachedViewOwnsItsPaneRuntimeAndHasNoTabStrip(t *testing.T) {
	shellState, detached, _ := newDetachedGraph(t)
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	(&workspaceViewGraph{state: shellState}).waitForSurfaces(t, 1)
	if err := shellState.SplitRight(); err != nil {
		t.Fatal(err)
	}
	(&workspaceViewGraph{state: shellState}).waitForSurfaces(t, 2)
	root, _, ok := shellState.TabTree(0)
	if !ok {
		t.Fatal("no main Tab tree")
	}
	ids := domainworkspace.CollectPaneIDs(root)
	if len(ids) != 2 {
		t.Fatalf("main Tab Panes = %d, want 2", len(ids))
	}
	if !shellState.DetachPane(0, ids[1]) {
		t.Fatal("DetachPane returned false")
	}
	if len(*detached) != 1 {
		t.Fatalf("detached windows = %v, want one", *detached)
	}
	detachedID := (*detached)[0]
	// A Pane event for the main window's Tab is not this window's to play.
	if err := shellState.SplitRight(); err != nil {
		t.Fatal(err)
	}
	(&workspaceViewGraph{state: shellState}).waitForSurfaces(t, 2)

	wm := newDetachedPaneMotion(shellState, detachedID)
	view := detachedView(shellState, detachedID, wm)
	tt := ui.NewTester(view, 640, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()

	if got, ok := wm.tabID(); !ok || got != detachedID {
		t.Fatalf("the detached runtime reads Tab %q, %v; want %q", got, ok, detachedID)
	}
	if wm.body != nil {
		t.Fatal("the detached runtime owns a Tab body or strip")
	}
	if wm.ownsGhosts {
		t.Fatal("the detached runtime claims the shared ghost pools")
	}
	if wm.run != nil {
		t.Fatalf("the detached window animated a main-Tab Pane event: %+v", wm.run)
	}
	if wm.animationFrames != 0 {
		t.Fatalf("the detached window asked for %d animation frames", wm.animationFrames)
	}
	event, ok := shellState.MotionEvent()
	if !ok || wm.controller.Unseen(event.Seq) {
		t.Fatal("the detached view did not catch the main Tab's sequence up")
	}
	if tt.HasText(locales.In("en").WorkTabsLabel()) {
		t.Fatalf("the detached window built a Tab strip: %v", tt.Texts())
	}
	if !tt.HasText("terminal-surface") {
		t.Fatalf("the detached window lost its Pane: %v", tt.Texts())
	}
}

// TestTopdownOverlayPassesPointerAndWheelToTheRealPane proves the drop-down
// route's overlay stays view-local and inert: a press and a wheel over the
// moving copy's box reach the real Pane's terminal underneath, which keeps
// the focus and scrolls its own screen.
func TestTopdownOverlayPassesPointerAndWheelToTheRealPane(t *testing.T) {
	g := motionGraph(t, paneMotionPreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := topdownWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	for _, surface := range settlePane(t, tt, g, 0) {
		surface.ResetResizeLog()
	}
	oldID, _ := g.state.FocusedPaneID(0)
	oldSurface := surfaceForPane(t, g, 0, oldID)
	// The terminal program asks to hear pointer and wheel as SGR mouse
	// reports, so the test can see exactly what reached it. The marker after
	// the mode bytes proves the terminal consumed them in order.
	oldSurface.feed("\x1b[?1000h\x1b[?1006hMOUSE-READY")
	pumpUntil(t, tt, "the terminal's mouse mode", func() bool {
		return tt.HasText("MOUSE-READY")
	})

	surfaces := startPaneOpen(t, tt, g)
	run := wm.run
	if run == nil {
		t.Fatal("the split did not start a run")
	}
	newID, _ := g.state.FocusedPaneID(0)
	newSurface := surfaceForPane(t, g, 0, newID)
	if newSurface == oldSurface {
		t.Fatal("the split did not create a second surface")
	}
	// The moving old copy covers its final box at this point; a press there
	// must pass through the mask and the inert copy to the real Pane.
	body := run.final[oldID].Body
	tt.ClickAt(body.X+body.W/2, body.Y+body.H/2)
	pumpUntil(t, tt, "the old Pane keyboard after clicking through Topdown's overlay", func() bool {
		return oldSurface.LiveFocused()
	})
	if newSurface.LiveFocused() {
		t.Fatal("the Topdown overlay press left the keyboard on the new Pane")
	}
	if wm.run == nil {
		t.Fatal("the run ended before the wheel could be asserted")
	}
	// A wheel over the moving copy must reach the real terminal under it: the
	// program reports it as an SGR button-64 (wheel-up) event. The click above
	// already proved the pointer path; the wheel proves the scroll path.
	wheelReport := []byte("\x1b[<64;")
	tt.Scroll(body.X+body.W/2, body.Y+body.H/2, 0, -120)
	pumpUntil(t, tt, "the wheel report at the real terminal", func() bool {
		return bytes.Contains(oldSurface.transport.input(), wheelReport)
	})
	if wm.run == nil {
		t.Fatal("the run ended before the wheel finished")
	}
	if !tt.HasText("pane-motion:" + string(oldID)) {
		t.Fatalf("the old copy vanished while the run was still playing")
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatal("the run did not finish after its duration")
	}
	if got := len(surfaces[0].ResizeLog()); got != 1 || len(surfaces[1].ResizeLog()) != 1 {
		t.Fatalf("resizes = %d, %d; want one per Pane after the pointer and wheel",
			len(surfaces[0].ResizeLog()), len(surfaces[1].ResizeLog()))
	}
}
