package shell

import (
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

// This file is ticket 20's at-rest warp regression: a warp must reclaim a
// close copy MyGo has already started. DisableAnimations deletes only the
// transition records whose copy does not exist yet
// (third_party/mygo/ui/transition.go:913); a record that already holds a ghost
// keeps sampling and keeps asking for frames (:971-1005). A strip run in
// flight therefore has to end through a new row generation, which drops the
// old row's children and their ghosts. Where no run is in flight, a close copy
// that outlived its run is covered by the runtime's ghost deadline.

// ghostStripShell mounts the real shell over a motion setting the test can
// change between builds. steppedStripGraph passes its setting by value; the
// at-rest warp cases need to turn a switch off or on after a close's copy has
// left, so the state reads the live variable through a pointer here.
func ghostStripShell(t *testing.T, setting *domainpreferences.Motion, n, width, height int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion, *workTabReveal) {
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

// stripCloseGhost closes the Tab at index and leaves its close copy mid-run:
// the event frame is built alone, then 50 ms of the run play. It returns the
// closed Tab and the box it painted before the close.
func stripCloseGhost(t *testing.T, s *state.State, tt *ui.Tester, reveal *workTabReveal, ids []model.TabID, index int) (model.TabID, ui.Rect) {
	t.Helper()
	closed := ids[index]
	box := stripTravel(t, tt, closed)
	if !s.CloseTab(index) {
		t.Fatalf("CloseTab(%d) failed", index)
	}
	stepStripWarp(t, s, tt, reveal, motion.TabClose)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose || reveal.strip.warp {
		t.Fatalf("the close = run %+v warp=%v, want a TabClose run", reveal.strip.run, reveal.strip.warp)
	}
	tt.Advance(50 * time.Millisecond)
	if reveal.strip.run == nil {
		t.Fatalf("the close run ended before 50 ms; the case needs a copy in flight")
	}
	return closed, box
}

// assertStripStill requires the strip viewport to stay pixel-identical to the
// warp frame at +17 ms, +90 ms and +400 ms. A started ghost that the warp did
// not reclaim moves in every one of those windows.
func assertStripStill(t *testing.T, tt *ui.Tester, viewport ui.Rect) {
	t.Helper()
	warp := tt.Image()
	at := time.Duration(0)
	for _, step := range []time.Duration{17 * time.Millisecond, 73 * time.Millisecond, 310 * time.Millisecond} {
		at += step
		tt.Advance(step)
		if got := differingStripPixels(tt.Image(), warp, viewport); got != 0 {
			t.Fatalf("the strip moved %d pixels by +%s after the warp; the final layout must show at once", got, at)
		}
	}
}

// stripThemes is every interface style the strip must hold in. Classic and
// macOS lay the row out at different widths, so both are measured.
var stripThemes = []struct {
	name  string
	style kit.Style
}{{"classic", kit.Classic}, {"macos", kit.MacOS}}

// TestTabStripWarpReclaimsStartedCloseGhost is the exact repro. Four fitting
// 1600-DIP Tabs, active index 3: a background Tab's close runs one event
// frame plus 50 ms, then each trigger warps the strip. Every trigger must end
// the run with a fresh row generation and leave the strip still from the warp
// frame on.
func TestTabStripWarpReclaimsStartedCloseGhost(t *testing.T) {
	type triggerFunc func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, reveal *workTabReveal, setting *domainpreferences.Motion)
	cases := []struct {
		name string
		// trigger performs the warp that follows the mid-exit close. checkWarp
		// is off for a trigger that settles several builds at one clock
		// (SetSize), where the last build's warp flag describes no event.
		trigger   triggerFunc
		checkWarp bool
		// survivors reports whether the next Tab (ids[2]) survives the trigger.
		survivors bool
	}{
		{
			name: "Tab Switch off and SelectTab",
			trigger: func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, reveal *workTabReveal, setting *domainpreferences.Motion) {
				setting.TabSwitch = false
				g.state.SelectTab(0)
				stepStripBuild(t, tt, reveal)
			},
			checkWarp: true,
			survivors: true,
		},
		{
			name: "resize",
			trigger: func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, reveal *workTabReveal, setting *domainpreferences.Motion) {
				tt.SetSize(1500, 600)
			},
			survivors: true,
		},
		{
			name: "CloseOtherTabs",
			trigger: func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, reveal *workTabReveal, setting *domainpreferences.Motion) {
				if !g.state.CloseOtherTabs(0) {
					t.Fatalf("CloseOtherTabs(0) failed")
				}
				stepStripBuild(t, tt, reveal)
			},
			checkWarp: true,
		},
		{
			name: "Tab Switch on and SelectTab",
			trigger: func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, reveal *workTabReveal, setting *domainpreferences.Motion) {
				setting.TabSwitch = true
				g.state.SelectTab(0)
				stepStripBuild(t, tt, reveal)
			},
			checkWarp: true,
			survivors: true,
		},
	}
	for _, theme := range stripThemes {
		for _, tc := range cases {
			t.Run(theme.name+"/"+tc.name, func(t *testing.T) {
				kit.SetStyle(theme.style)
				t.Cleanup(func() { kit.SetStyle(kit.Classic) })
				setting := tabStripPreferences()
				g, tt, _, reveal := ghostStripShell(t, &setting, 4, 1600, 600)
				viewport := stripWarpViewport(t, tt)
				ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
				_, box := stripCloseGhost(t, g.state, tt, reveal, ids, 1)

				generation := reveal.strip.generation
				tc.trigger(t, g, tt, reveal, &setting)
				if reveal.strip.run != nil {
					t.Fatalf("the warp left run=%+v, want the final layout alone", reveal.strip.run)
				}
				if tc.checkWarp && !reveal.strip.warp {
					t.Fatalf("the warp build did not mark itself final")
				}
				if reveal.strip.generation == generation {
					t.Fatalf("the warp kept generation %d while its close copy was moving; the started ghost needs a fresh row",
						generation)
				}
				// The survivors land on their final places on the warp frame.
				if tc.survivors {
					if got := stripTravel(t, tt, ids[2]); abs32(got.X-box.X) > 1 {
						t.Fatalf("the next Tab shows at x=%v on the warp frame, want the closed Tab's place %v", got.X, box.X)
					}
				}
				assertStripStill(t, tt, viewport)
			})
		}
	}
}

// TestTabStripWarpReclaimsStartedCloseGhostRequestsNoFrames is the same
// switch-off repro on a strip-only window that builds only when MyGo asks.
// The view reports instant, as the shell does with no body or Pane transition
// moving, so this is exactly the path that used to rest MyGo's records in
// place. After the warp build no frame may be pending: a ghost left sampling
// would ask for every refresh while it moved.
func TestTabStripWarpReclaimsStartedCloseGhostRequestsNoFrames(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 4)
	reveal := newWorkTabReveal()
	reveal.labels = true
	reveal.canInstant = func() bool { return true }
	tt := ui.NewTester(stripOnlyView(g.state, reveal), 1600, 120)
	tt.SetLocale("en")
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatalf("the resting strip asks for frames")
	}
	viewport := stripWarpViewport(t, tt)

	box := stripTravel(t, tt, ids[1])
	if !g.state.CloseTab(1) {
		t.Fatalf("CloseTab(1) failed")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabClose)
	if reveal.strip.run == nil {
		t.Fatalf("the close did not start a run")
	}
	tt.Advance(50 * time.Millisecond)
	if reveal.strip.run == nil {
		t.Fatalf("the close run ended before 50 ms")
	}
	if !tt.FramePending() {
		t.Fatalf("the moving close copy asks for no frames; the case needs it in flight")
	}

	generation := reveal.strip.generation
	setting.TabSwitch = false
	g.state.SelectTab(0)
	tt.Frame()
	if reveal.strip.run != nil || reveal.strip.generation == generation {
		t.Fatalf("the warp left run=%+v generation %d; want the run ended with a fresh row", reveal.strip.run, reveal.strip.generation)
	}
	if tt.FramePending() {
		t.Fatalf("the warp still asks for frames; the started close copy was not reclaimed")
	}
	if got := stripTravel(t, tt, ids[2]); abs32(got.X-box.X) > 1 {
		t.Fatalf("the next Tab shows at x=%v after the warp, want the closed Tab's place %v", got.X, box.X)
	}
	assertStripStill(t, tt, viewport)
	if tt.FramePending() {
		t.Fatalf("the settled warp asks for frames again")
	}
}

// TestTabStripStripOnlyWarpReclaimsStartedCloseGhost keeps the same sequence
// on the strip-only seam. Its signals never report instant, so the remount is
// the only path; the case guards that no future change makes it conditional
// on the instant flag alone.
func TestTabStripStripOnlyWarpReclaimsStartedCloseGhost(t *testing.T) {
	setting := tabStripPreferences()
	g, tt, reveal, ids := stripWarpWindow(t, setting, 4, 1600, 480, nil)
	viewport := stripWarpViewport(t, tt)
	_, box := stripCloseGhost(t, g.state, tt, reveal, ids, 1)

	generation := reveal.strip.generation
	g.state.SelectTab(0)
	stepStripBuild(t, tt, reveal)
	if reveal.strip.run != nil || reveal.strip.generation == generation {
		t.Fatalf("the strip-only warp left run=%+v generation %d; want a fresh row", reveal.strip.run, reveal.strip.generation)
	}
	if got := stripTravel(t, tt, ids[2]); abs32(got.X-box.X) > 1 {
		t.Fatalf("the next Tab shows at x=%v, want the closed Tab's place %v", got.X, box.X)
	}
	assertStripStill(t, tt, viewport)
}

// TestTabStripWarpReclaimsCloseGhostOnOtherPaths ends the same mid-exit ghost
// through the strip's other warp entry points: a detach and a rename (the
// removal guard and the row-consistency rule), a reorder interrupted by a row
// change, and an open whose reveal overflows the strip.
func TestTabStripWarpReclaimsCloseGhostOnOtherPaths(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })

	t.Run("detach", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, _, reveal := ghostStripShell(t, &setting, 4, 1600, 600)
		viewport := stripWarpViewport(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		stripCloseGhost(t, g.state, tt, reveal, ids, 1)
		generation := reveal.strip.generation
		if !g.state.DetachTab(1) {
			t.Fatalf("DetachTab(1) failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.generation == generation {
			t.Fatalf("the detach warp left run=%+v generation %d; want a fresh row", reveal.strip.run, reveal.strip.generation)
		}
		assertStripStill(t, tt, viewport)
	})

	t.Run("rename", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, _, reveal := ghostStripShell(t, &setting, 4, 1600, 600)
		viewport := stripWarpViewport(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		stripCloseGhost(t, g.state, tt, reveal, ids, 1)
		generation := reveal.strip.generation
		if !g.state.RenameTab(0, "renamed-mid-exit") {
			t.Fatalf("RenameTab failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.generation == generation {
			t.Fatalf("the rename warp left run=%+v generation %d; want a fresh row", reveal.strip.run, reveal.strip.generation)
		}
		assertStripStill(t, tt, viewport)
	})

	t.Run("reorder interruption", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, _, reveal := ghostStripShell(t, &setting, 4, 1600, 600)
		viewport := stripWarpViewport(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		stripCloseGhost(t, g.state, tt, reveal, ids, 1)
		if !g.state.MoveTab(g.mustTabID(t, 0), g.mustTabID(t, 2), true) {
			t.Fatalf("MoveTab failed")
		}
		stepStripBuild(t, tt, reveal)
		stripEvent(t, g.state, motion.TabReorder)
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
			t.Fatalf("the reorder did not replace the close run: %+v", reveal.strip.run)
		}
		tt.Advance(30 * time.Millisecond)
		generation := reveal.strip.generation
		if !g.state.RenameTab(1, "interrupted-reorder") {
			t.Fatalf("RenameTab failed")
		}
		stepStripBuild(t, tt, reveal)
		if reveal.strip.run != nil || reveal.strip.generation == generation {
			t.Fatalf("the interrupted reorder left run=%+v generation %d; want a fresh row",
				reveal.strip.run, reveal.strip.generation)
		}
		assertStripStill(t, tt, viewport)
	})

	t.Run("open overflow reveal", func(t *testing.T) {
		setting := tabStripPreferences()
		g, tt, _, reveal := ghostStripShell(t, &setting, 4, 720, 480)
		if reveal.scroll.MaxX <= 0.5 {
			t.Fatalf("the four-Tab strip does not overflow (MaxX=%v)", reveal.scroll.MaxX)
		}
		viewport := stripWarpViewport(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		// Scroll back to the start so the background close does not clamp.
		g.state.SelectTab(0)
		stepStripBuild(t, tt, reveal)
		if reveal.scroll.X > 10 {
			t.Fatalf("the strip did not scroll back to its start (X=%v)", reveal.scroll.X)
		}
		stripCloseGhost(t, g.state, tt, reveal, ids, 1)
		generation := reveal.strip.generation
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		stepStripBuild(t, tt, reveal)
		stripEvent(t, g.state, motion.TabOpen)
		if reveal.strip.run != nil || !reveal.strip.warp || reveal.strip.generation == generation {
			t.Fatalf("the overflowing open warp left run=%+v warp=%v generation %d; want a fresh warp row",
				reveal.strip.run, reveal.strip.warp, reveal.strip.generation)
		}
		assertStripStill(t, tt, viewport)
	})
}

// TestFollowBarWarpReclaimsStartedCloseGhost drives Follow's mini strip
// through the same sequence. Its signals never report instant either, so the
// case proves a switch after a mini close copy has left leaves nothing moving.
func TestFollowBarWarpReclaimsStartedCloseGhost(t *testing.T) {
	graph := newFollowGraph(t)
	for range 4 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.waitForSurfaces(t, 4)
	strip := &tabStripMotion{positionOnly: true}
	tt := ui.NewTester(followBarWithStrip(graph.state, strip), 600, 120)
	tt.SetLocale("en")
	tt.Frame()

	if !graph.state.CloseTab(1) {
		t.Fatalf("CloseTab(1) failed")
	}
	tt.Frame()
	stripEvent(t, graph.state, motion.TabClose)
	if strip.run == nil || strip.run.kind != motion.TabClose {
		t.Fatalf("the mini close did not start a run: %+v", strip.run)
	}
	tt.Advance(50 * time.Millisecond)
	if strip.run == nil {
		t.Fatalf("the mini close run ended before 50 ms")
	}

	generation := strip.generation
	graph.state.SelectTab(0)
	tt.Frame()
	if strip.run != nil || strip.generation == generation {
		t.Fatalf("the mini switch left run=%+v generation %d; want a fresh row", strip.run, strip.generation)
	}
	bar := ui.Rect{X: 0, Y: 0, W: 600, H: 120}
	warp := tt.Image()
	for _, step := range []time.Duration{17 * time.Millisecond, 73 * time.Millisecond, 310 * time.Millisecond} {
		tt.Advance(step)
		if got := differingStripPixels(tt.Image(), warp, bar); got != 0 {
			t.Fatalf("the mini strip moved %d pixels after the switch", got)
		}
	}
}

// TestTabStripWarpAfterNaturalCloseEndLeavesNoMovement pins the natural end at
// each speed tier: the close copy finishes within its own duration, the strip
// settles without a warp, and a warp just after the run ended neither remounts
// the row nor leaves movement.
func TestTabStripWarpAfterNaturalCloseEndLeavesNoMovement(t *testing.T) {
	cases := []struct {
		name  string
		speed domainpreferences.MotionSpeed
		want  time.Duration
	}{
		{"standard", domainpreferences.MotionStandard, 180 * time.Millisecond},
		{"slow", domainpreferences.MotionSlow, 270 * time.Millisecond},
		{"fast", domainpreferences.MotionFast, 108 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kit.SetStyle(kit.Classic)
			t.Cleanup(func() { kit.SetStyle(kit.Classic) })
			setting := tabStripPreferences()
			setting.Speed = tc.speed
			g, tt, _, reveal := ghostStripShell(t, &setting, 4, 1600, 600)
			viewport := stripWarpViewport(t, tt)
			ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
			stripCloseGhost(t, g.state, tt, reveal, ids, 1)
			// 50 ms of the run have played; cross the rest and one refresh.
			tt.Advance(tc.want - 50*time.Millisecond + 17*time.Millisecond)
			if reveal.strip.run != nil {
				t.Fatalf("the run outlived its %s duration: %+v", tc.want, reveal.strip.run)
			}
			settled := tt.Image()
			tt.Advance(400 * time.Millisecond)
			if got := differingStripPixels(tt.Image(), settled, viewport); got != 0 {
				t.Fatalf("the %s close copy moved %d pixels after its run ended", tc.name, got)
			}

			generation := reveal.strip.generation
			setting.TabSwitch = false
			g.state.SelectTab(0)
			stepStripBuild(t, tt, reveal)
			if reveal.strip.generation != generation {
				t.Fatalf("the warp after the %s run remounted the row (%d to %d); its copy was done",
					tc.name, generation, reveal.strip.generation)
			}
			assertStripStill(t, tt, viewport)
		})
	}
}

// TestTabStripWarpReclaimsGhostOutlivingItsRun covers the residual where no
// run is in flight but a close copy still moves: a second close replaces the
// run with a faster tier while the first copy keeps its slower exit. Before
// the ghost deadline, a warp must still remount the row; after it, a warp must
// take the instant path again.
func TestTabStripWarpReclaimsGhostOutlivingItsRun(t *testing.T) {
	newScene := func(t *testing.T) (*realTerminalGraph, *ui.Tester, *workTabReveal, []model.TabID, ui.Rect, *domainpreferences.Motion) {
		t.Helper()
		kit.SetStyle(kit.Classic)
		t.Cleanup(func() { kit.SetStyle(kit.Classic) })
		setting := tabStripPreferences()
		setting.Speed = domainpreferences.MotionSlow
		g, tt, _, reveal := ghostStripShell(t, &setting, 4, 1600, 600)
		viewport := stripWarpViewport(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		stripCloseGhost(t, g.state, tt, reveal, ids, 1)
		setting.Speed = domainpreferences.MotionFast
		if !g.state.CloseTab(1) {
			t.Fatalf("the second CloseTab failed")
		}
		stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
		if reveal.strip.run == nil || reveal.strip.run.duration != 108*time.Millisecond {
			t.Fatalf("the second run = %+v, want the fast 108 ms", reveal.strip.run)
		}
		// The second run is over while the copies armed with the slow exit
		// still move.
		tt.Advance(120 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the fast run is still active: %+v", reveal.strip.run)
		}
		return g, tt, reveal, ids, viewport, &setting
	}

	t.Run("switch warp", func(t *testing.T) {
		g, tt, reveal, _, viewport, setting := newScene(t)
		generation := reveal.strip.generation
		setting.TabSwitch = false
		g.state.SelectTab(0)
		stepStripBuild(t, tt, reveal)
		if reveal.strip.generation == generation {
			t.Fatalf("the switch warp kept generation %d while a copy outlived its run", generation)
		}
		assertStripStill(t, tt, viewport)
	})

	t.Run("resize warp", func(t *testing.T) {
		_, tt, reveal, _, viewport, _ := newScene(t)
		generation := reveal.strip.generation
		tt.SetSize(1500, 600)
		if reveal.strip.generation == generation {
			t.Fatalf("the resize warp kept generation %d while a copy outlived its run", generation)
		}
		assertStripStill(t, tt, viewport)
	})

	t.Run("warp after the copy finished keeps identity", func(t *testing.T) {
		g, tt, reveal, _, _, setting := newScene(t)
		tt.Advance(400 * time.Millisecond)
		generation := reveal.strip.generation
		setting.TabSwitch = false
		g.state.SelectTab(0)
		stepStripBuild(t, tt, reveal)
		if reveal.strip.generation != generation {
			t.Fatalf("the warp after every copy finished remounted the row (%d to %d)",
				generation, reveal.strip.generation)
		}
	})
}

// TestTabStripAtRestWarpsKeepIdentityAndFocus is the control set: with no strip
// run and no moving copy, a warp keeps the row's generation, keeps the add
// control's focus, and asks for no strip frames.
func TestTabStripAtRestWarpsKeepIdentityAndFocus(t *testing.T) {
	message := locales.In("en")
	addLabel := message.MenuNewTab()

	focusAdd := func(t *testing.T, tt *ui.Tester) {
		t.Helper()
		add, ok := tt.Find(addLabel)
		if !ok {
			t.Fatalf("the strip has no add control")
		}
		// The press focuses the control; the release is off it, so the button
		// cannot click and open a Tab.
		tt.Press(add.X+add.W/2, add.Y+add.H/2)
		tt.Move(add.X+add.W/2, add.Y+add.H/2+40)
		tt.Release(add.X+add.W/2, add.Y+add.H/2+40)
		tt.Frame()
		if !tt.Focused(addLabel) {
			t.Fatalf("the add control does not hold the focus")
		}
	}

	t.Run("Tab switch without a run", func(t *testing.T) {
		kit.SetStyle(kit.Classic)
		t.Cleanup(func() { kit.SetStyle(kit.Classic) })
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		focusAdd(t, tt)
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames
		g.state.SelectTab(0)
		tt.Frame()
		stripEvent(t, g.state, motion.TabSwitch)
		if reveal.strip.generation != generation {
			t.Fatalf("the at-rest switch remounted the row (%d to %d)", generation, reveal.strip.generation)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the at-rest switch asked for %d strip frames", got)
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("the at-rest switch lost the add control's focus")
		}
	})

	t.Run("batch close without a run", func(t *testing.T) {
		kit.SetStyle(kit.Classic)
		t.Cleanup(func() { kit.SetStyle(kit.Classic) })
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		// The active Tab is the last one; select the first so the batch close
		// does not legitimately move the keyboard focus to a newly shown
		// terminal.
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		focusAdd(t, tt)
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		tt.Frame()
		if reveal.strip.generation != generation {
			t.Fatalf("the at-rest batch close remounted the row (%d to %d)", generation, reveal.strip.generation)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the at-rest batch close asked for %d strip frames", got)
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("the at-rest batch close lost the add control's focus")
		}
	})

	t.Run("close clamp without a run", func(t *testing.T) {
		kit.SetStyle(kit.Classic)
		t.Cleanup(func() { kit.SetStyle(kit.Classic) })
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 6, 720, 480)
		if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
			t.Fatalf("the strip is not scrolled at 6 Tabs (X=%v MaxX=%v)", reveal.scroll.X, reveal.scroll.MaxX)
		}
		viewport, ok := tt.Find(message.WorkTabsLabel())
		if !ok {
			t.Fatalf("the strip has no viewport label")
		}
		tt.Wheel(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 200, 0)
		tt.Advance(17 * time.Millisecond)
		focusAdd(t, tt)
		generation, frames, beforeX := reveal.strip.generation, reveal.strip.animationFrames, reveal.scroll.X
		if !g.state.CloseTab(1) {
			t.Fatalf("CloseTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabClose)
		if reveal.scroll.X >= beforeX-0.5 {
			t.Fatalf("the close did not clamp the scroll (X=%v from %v)", reveal.scroll.X, beforeX)
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the clamped close remounted the row (%d to %d)", generation, reveal.strip.generation)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the clamped close asked for %d strip frames", got)
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("the clamped close lost the add control's focus")
		}
	})

	t.Run("resize and open warp without a run", func(t *testing.T) {
		kit.SetStyle(kit.Classic)
		t.Cleanup(func() { kit.SetStyle(kit.Classic) })
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		focusAdd(t, tt)
		// Narrow enough that the fifth Tab's reveal overflows the strip.
		tt.SetSize(800, 480)
		if !tt.Focused(addLabel) {
			t.Fatalf("the resize alone lost the add control's focus")
		}
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		waitForRealSurfaces(t, g.state, 5)
		tt.Frame()
		stripEvent(t, g.state, motion.TabOpen)
		if reveal.strip.generation != generation {
			t.Fatalf("the at-rest resize+open warp remounted the row (%d to %d)", generation, reveal.strip.generation)
		}
		if got := reveal.strip.animationFrames - frames; got != 0 {
			t.Fatalf("the at-rest resize+open warp asked for %d strip frames", got)
		}
		// The open activates a new Tab and its terminal rightly takes the
		// keyboard focus; that is not the strip remounting its row. The
		// generation assertion above is the identity guarantee here.
	})
}
