package shell

// This file is ticket 20's warm-exit and generic-removal regressions. MyGo
// copies an element that stops being built from the transition its last build
// attached. The strip now attaches a warm positive exit on every build, even
// with the switches off, so a switch turned on in the same update as a close
// finds a usable record; the generic removal guard is what keeps every other
// disappearance from replaying it. These tests pin the three confirmed
// defects: Follow's detach without a row, an event sharing the update with a
// detach, and a re-enable sharing the update with a close.

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/motion"
)

// warmExitSetting is tabStripPreferences with a chosen speed tier.
func warmExitSetting(speed domainpreferences.MotionSpeed) domainpreferences.Motion {
	setting := tabStripPreferences()
	setting.Speed = speed
	return setting
}

// followWarmStrip mounts the mini strip with its runtime exposed. The setting
// is read live, so a switch can turn off with no state event.
func followWarmStrip(t *testing.T, g *followGraph, strip *tabStripMotion, width, height int) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(followBarWithStrip(g.state, strip), width, height)
	tt.SetLocale("en")
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	return tt
}

// TestFollowBarDetachWithSwitchOffWarps is defect 1: a detach and a switch-off
// in the same update. Before the fix Follow passed no row, so the removal guard
// could not run, the removed mini Tab was not built, and MyGo replayed its
// positive exit record: a 180 ms copy ghosted the removed item while it was
// already gone from the layout. The row now lets the guard end the motion on
// the frame the Tab disappears, with every switch state; the motion-on control
// proves a detach is a stale removal, never a close.
func TestFollowBarDetachWithSwitchOffWarps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domainpreferences.Motion)
	}{
		{"Tab Close off", func(m *domainpreferences.Motion) { m.TabClose = false }},
		{"master off", func(m *domainpreferences.Motion) { m.Enabled = false }},
		{"motion on", func(*domainpreferences.Motion) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setting := warmExitSetting(domainpreferences.MotionStandard)
			g := newTabStripGraph(t, &setting)
			for range 3 {
				if err := g.state.NewTab(); err != nil {
					t.Fatalf("NewTab: %v", err)
				}
			}
			g.waitForSurfaces(t, 3)
			titles := []string{"warm-follow-a", "warm-follow-b", "warm-follow-c"}
			for index, title := range titles {
				pane, ok := g.state.FocusedPaneID(index)
				if !ok || !g.state.RenamePane(index, pane, title) {
					t.Fatalf("renaming Tab %d's Pane failed", index)
				}
			}
			strip := &tabStripMotion{positionOnly: true}
			const width, height = 900, 120
			tt := followWarmStrip(t, g, strip, width, height)
			viewport := ui.Rect{X: 0, Y: 0, W: width, H: height}
			if transition, ok := strip.transition(setting, false); !ok ||
				transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
				t.Fatalf("the settled mini strip attached %+v (%v), want a warm 180ms exit record", transition, ok)
			}
			boxes := []ui.Rect{
				followStripRect(t, tt, titles[0]),
				followStripRect(t, tt, titles[1]),
				followStripRect(t, tt, titles[2]),
			}

			// One update: the switch goes off and the last Tab leaves.
			tc.change(&setting)
			frames := strip.animationFrames
			if !g.state.DetachTab(2) {
				t.Fatalf("DetachTab failed")
			}
			tt.Frame()
			if strip.run != nil || strip.animate || !strip.warp {
				t.Fatalf("the off detach = run %+v animate=%v warp=%v, want a warp alone",
					strip.run, strip.animate, strip.warp)
			}
			if got := strip.animationFrames - frames; got != 0 {
				t.Fatalf("the off detach asked for %d mini strip frames, want none", got)
			}
			if tt.FramePending() {
				t.Fatalf("the off detach still asks for frames: MyGo is animating a ghost copy")
			}
			if _, ok := tt.Find(titles[2]); ok {
				t.Fatalf("the detached mini Tab is still drawn")
			}
			event := tt.Image()
			for _, step := range []time.Duration{90 * time.Millisecond, 400 * time.Millisecond} {
				tt.Advance(step)
				if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
					t.Fatalf("the off detach painted %d moving pixels after +%s: a ghost copy", got, step)
				}
			}
			for index, title := range titles[:2] {
				if got := followStripRect(t, tt, title); !rectNearOK(got, boxes[index], 1) {
					t.Fatalf("the off detach moved %q from %+v to %+v", title, boxes[index], got)
				}
			}
		})
	}
}

// TestTabStripEventWithDetachWarps is defect 2: a warp-triggering event
// (switch, open, reorder) and a detach in the same update. The event used to
// set warp without ending the strip's motion, the removal guard never ran, and
// the removed Tab's old positive record copied for 180 ms out of a layout that
// no longer held it. The generic removal guard now ends the motion on the
// frame the Tab disappears, whatever the event says; only an unseen TabClose
// of exactly the removed Tab may paint the exit.
func TestTabStripEventWithDetachWarps(t *testing.T) {
	for _, style := range []struct {
		name string
		s    kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			kit.SetStyle(style.s)
			t.Cleanup(func() { kit.SetStyle(kit.Classic) })
			for _, tc := range []struct {
				name   string
				change func(*domainpreferences.Motion)
				mutate func(t *testing.T, g *followGraph, ids []model.TabID)
				kind   motion.Kind
			}{
				{
					name:   "TabSwitch and detach with Tab Close off",
					change: func(m *domainpreferences.Motion) { m.TabClose = false },
					mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
						t.Helper()
						if !g.state.DetachTab(2) {
							t.Fatalf("DetachTab failed")
						}
						g.state.SelectTab(0)
					},
					kind: motion.TabSwitch,
				},
				{
					name:   "TabSwitch and detach with master off",
					change: func(m *domainpreferences.Motion) { m.Enabled = false },
					mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
						t.Helper()
						if !g.state.DetachTab(2) {
							t.Fatalf("DetachTab failed")
						}
						g.state.SelectTab(0)
					},
					kind: motion.TabSwitch,
				},
				{
					name:   "TabSwitch and detach with motion on",
					change: func(*domainpreferences.Motion) {},
					mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
						t.Helper()
						if !g.state.DetachTab(2) {
							t.Fatalf("DetachTab failed")
						}
						g.state.SelectTab(0)
					},
					kind: motion.TabSwitch,
				},
				{
					name:   "TabOpen and detach with Tab Close off",
					change: func(m *domainpreferences.Motion) { m.TabClose = false },
					mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
						t.Helper()
						if !g.state.DetachTab(2) {
							t.Fatalf("DetachTab failed")
						}
						if err := g.state.NewTab(); err != nil {
							t.Fatalf("NewTab: %v", err)
						}
					},
					kind: motion.TabOpen,
				},
				{
					name:   "TabReorder and detach with Tab Close off",
					change: func(m *domainpreferences.Motion) { m.TabClose = false },
					mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
						t.Helper()
						if !g.state.DetachTab(2) {
							t.Fatalf("DetachTab failed")
						}
						if !g.state.MoveTab(ids[0], ids[1], true) {
							t.Fatalf("MoveTab failed")
						}
					},
					kind: motion.TabReorder,
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					setting := warmExitSetting(domainpreferences.MotionStandard)
					tc.change(&setting)
					g := newTabStripGraph(t, &setting)
					ids := addStripTabs(t, g.state, 3)
					reveal := newWorkTabReveal()
					reveal.labels = true
					tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), 1600, 480)
					tt.SetLocale("en")
					tt.Advance(400 * time.Millisecond)
					viewport := stripWarpViewport(t, tt)

					frames := reveal.strip.animationFrames
					tc.mutate(t, g, ids)
					stepStripWarp(t, g.state, tt, reveal, tc.kind)
					if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
						t.Fatalf("the event+detach = run %+v animate=%v warp=%v, want a warp alone",
							reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
					}
					if got := reveal.strip.animationFrames - frames; got != 0 {
						t.Fatalf("the event+detach asked for %d strip frames, want none", got)
					}
					if _, ok := tt.Find(tabStripBoxLabel(ids[2])); ok {
						t.Fatalf("the detached Tab is still drawn")
					}
					event := tt.Image()
					for _, step := range []time.Duration{90 * time.Millisecond, 400 * time.Millisecond} {
						tt.Advance(step)
						if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
							t.Fatalf("the event+detach painted %d moving pixels after +%s: a ghost copy",
								got, step)
						}
					}
					if _, ok := tt.Find(tabStripBoxLabel(ids[2])); ok {
						t.Fatalf("the detached Tab came back after its warp")
					}
				})
			}
		})
	}
}

// TestTabStripEventWithDetachWarpsInTheShell repeats the switch repro over the
// real shell, where the body runtime and the strip share the frame. The body's
// Tab switch animates (its switch stays on), so no window-wide
// DisableAnimations can stand in for the strip's own suppression: the strip
// must warp and drop the removed Tab's exit copy while another transition is
// in flight.
func TestTabStripEventWithDetachWarpsInTheShell(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := warmExitSetting(domainpreferences.MotionStandard)
	setting.TabClose = false
	g, tt, wm, reveal := liveShellWindow(t, &setting, 3, 1600, 600)
	viewport := stripWarpViewport(t, tt)
	removed := g.mustTabID(t, 2)
	bodyFrames := wm.body.animationFrames

	if !g.state.DetachTab(2) {
		t.Fatalf("DetachTab failed")
	}
	g.state.SelectTab(0)
	stripFrames := reveal.strip.animationFrames
	stepStripWarp(t, g.state, tt, reveal, motion.TabSwitch)
	if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
		t.Fatalf("the shell event+detach = run %+v animate=%v warp=%v, want a warp alone",
			reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
	}
	if got := reveal.strip.animationFrames - stripFrames; got != 0 {
		t.Fatalf("the shell event+detach asked for %d strip frames, want none", got)
	}
	if wm.body.phase == bodyIdle || wm.body.animationFrames == bodyFrames {
		t.Fatalf("the body's Tab switch did not animate (phase %v, +%d frames); the case cannot show the strip suppressing on its own",
			wm.body.phase, wm.body.animationFrames-bodyFrames)
	}
	if _, ok := tt.Find(tabStripBoxLabel(removed)); ok {
		t.Fatalf("the detached Tab is still drawn")
	}
	event := tt.Image()
	for _, step := range []time.Duration{90 * time.Millisecond, 400 * time.Millisecond} {
		tt.Advance(step)
		if got := differingStripPixels(tt.Image(), event, viewport); got != 0 {
			t.Fatalf("the shell event+detach painted %d moving pixels after +%s: a ghost copy", got, step)
		}
	}
	if _, ok := tt.Find(tabStripBoxLabel(removed)); ok {
		t.Fatalf("the detached Tab came back after its warp")
	}
}

// TestTabStripReenableAndCloseSameUpdateAnimates is defect 3: a strip settled
// with every switch off records instant layouts, and the old off build armed
// -1/-1, so a switch turned on in the same update as a close dropped the
// closed Tab's copy and only its survivors moved. The warm record the off
// build now leaves lets the legitimate close paint its 180 ms × speed
// collapsing exit; the master switch and the speed tiers behave the same.
func TestTabStripReenableAndCloseSameUpdateAnimates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		speed   domainpreferences.MotionSpeed
		want    time.Duration
		disable func(*domainpreferences.Motion)
		enable  func(*domainpreferences.Motion)
	}{
		{
			name:    "Tab Close re-enabled",
			speed:   domainpreferences.MotionStandard,
			want:    180 * time.Millisecond,
			disable: func(m *domainpreferences.Motion) { m.TabClose = false },
			enable:  func(m *domainpreferences.Motion) { m.TabClose = true },
		},
		{
			name:    "master re-enabled",
			speed:   domainpreferences.MotionStandard,
			want:    180 * time.Millisecond,
			disable: func(m *domainpreferences.Motion) { m.Enabled = false },
			enable:  func(m *domainpreferences.Motion) { m.Enabled = true },
		},
		{
			name:    "slow tier",
			speed:   domainpreferences.MotionSlow,
			want:    270 * time.Millisecond,
			disable: func(m *domainpreferences.Motion) { m.TabClose = false },
			enable:  func(m *domainpreferences.Motion) { m.TabClose = true },
		},
		{
			name:    "fast tier",
			speed:   domainpreferences.MotionFast,
			want:    108 * time.Millisecond,
			disable: func(m *domainpreferences.Motion) { m.TabClose = false },
			enable:  func(m *domainpreferences.Motion) { m.TabClose = true },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kit.SetStyle(kit.Classic)
			t.Cleanup(func() { kit.SetStyle(kit.Classic) })
			setting := warmExitSetting(tc.speed)
			tc.disable(&setting)
			g, tt, reveal, _ := liveStripWindow(t, &setting, 3, 1600, 480)
			viewport := stripWarpViewport(t, tt)
			if transition, ok := reveal.strip.transition(setting, false); !ok ||
				transition.Duration != -1 || transition.ExitDuration != tc.want {
				t.Fatalf("the settled off strip attached %+v (%v), want a warm %s exit", transition, ok, tc.want)
			}

			active := g.state.ActiveTab()
			closed := followTabID(t, g.state, active)
			box := stripTravel(t, tt, closed)
			bar := stripBarFill(tt, viewport, box)
			preInk := tabInkPixels(tt, box, bar)
			if preInk < 60 {
				t.Fatalf("the current Tab has only %d ink pixels before the close; the case cannot see its copy", preInk)
			}

			// One update: the switch comes back on and the Tab closes.
			tc.enable(&setting)
			frames := reveal.strip.animationFrames
			if !g.state.CloseTab(active) {
				t.Fatalf("CloseTab failed")
			}
			stepStripWarp(t, g.state, tt, reveal, motion.TabClose)
			if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabClose ||
				reveal.strip.run.duration != tc.want || reveal.strip.warp || !reveal.strip.animate {
				t.Fatalf("the re-enabled close = run %+v warp=%v animate=%v, want a %s TabClose",
					reveal.strip.run, reveal.strip.warp, reveal.strip.animate, tc.want)
			}
			if got := reveal.strip.animationFrames - frames; got != 1 {
				t.Fatalf("the re-enabled close asked for %d strip frames, want its event frame's one", got)
			}
			start := tabInkPixels(tt, box, bar)
			if start < preInk/2 {
				t.Fatalf("the re-enabled close paints %d of its %d ink pixels on the event frame: the copy is missing",
					start, preInk)
			}
			tt.Advance(tc.want / 2)
			mid := tabInkPixels(tt, box, bar)
			if mid <= 0 || mid >= start {
				t.Fatalf("the re-enabled close paints %d pixels halfway through %s, want fewer than %d and more than 0",
					mid, tc.want, start)
			}
			tt.Advance(tc.want/2 + 17*time.Millisecond)
			tt.Advance(400 * time.Millisecond)
			if _, ok := tt.Find(tabStripBoxLabel(closed)); ok {
				t.Fatalf("the closed Tab is still labeled after its exit")
			}
			settled := tt.Image()
			tt.Advance(400 * time.Millisecond)
			if got := differingStripPixels(tt.Image(), settled, viewport); got != 0 {
				t.Fatalf("the re-enabled close moved %d pixels after it settled", got)
			}
		})
	}
}

// TestTabStripFreshOffBuildWarmAndInert is the off build's own contract: an
// instant layout with a warm positive exit record, no animation frames, and no
// ghost pixels; a close under the still-off switch paints no copy and asks no
// frame; a switch turned on afterwards animates a close again.
func TestTabStripFreshOffBuildWarmAndInert(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := warmExitSetting(domainpreferences.MotionStandard)
	setting.TabClose = false
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 3)
	reveal := newWorkTabReveal()
	reveal.labels = true
	// No stepping view: the Tester can tell whether MyGo still asks for a
	// frame, which is the ghost copy's observable signal.
	tt := ui.NewTester(stripOnlyView(g.state, reveal), 1600, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	viewport := stripWarpViewport(t, tt)

	if got := reveal.strip.animationFrames; got != 0 {
		t.Fatalf("a strip mounted with every Tab event off built %d animation frames", got)
	}
	if transition, ok := reveal.strip.transition(setting, false); !ok ||
		transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
		t.Fatalf("the fresh off build attached %+v (%v), want an instant layout with a warm 180ms exit", transition, ok)
	}
	if tt.FramePending() {
		t.Fatalf("the fresh off build asks for a frame")
	}
	pre := tt.Image()
	boxes := stripBoxesFor(t, tt, ids)
	assertStripPixelsStill(t, tt, pre, viewport, "the fresh off build")
	assertStripBoxes(t, tt, ids, boxes, "the fresh off build's later frames")

	// A close while still off: the event warps and the removal guard suppresses
	// the warm record, so no copy and no frame.
	active := g.state.ActiveTab()
	closed := followTabID(t, g.state, active)
	box := stripTravel(t, tt, closed)
	bar := stripBarFill(tt, viewport, box)
	frames := reveal.strip.animationFrames
	builds := reveal.strip.frames
	if !g.state.CloseTab(active) {
		t.Fatalf("CloseTab failed")
	}
	tt.Frame()
	if got := reveal.strip.frames - builds; got != 1 {
		t.Fatalf("the still-off close built %d frames, want one", got)
	}
	stripEvent(t, g.state, motion.TabClose)
	if reveal.strip.run != nil || reveal.strip.animate || !reveal.strip.warp {
		t.Fatalf("the still-off close = run %+v animate=%v warp=%v, want a warp alone",
			reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
	}
	if got := reveal.strip.animationFrames - frames; got != 0 {
		t.Fatalf("the still-off close asked for %d strip frames, want none", got)
	}
	if got := countTabFill(tt, box, bar); got != 0 {
		t.Fatalf("the still-off close painted %d copy pixels", got)
	}
	if tt.FramePending() {
		t.Fatalf("the still-off close still asks for a frame: a ghost copy")
	}
	preClose := tt.Image()
	tt.Advance(90 * time.Millisecond)
	if got := differingStripPixels(tt.Image(), preClose, viewport); got != 0 {
		t.Fatalf("the still-off close painted %d moving pixels at +90ms", got)
	}

	// Switch on, then close, on a strip mounted off under the stepping view: the
	// warm record the off builds left animates. (The non-stepping Tester above
	// settles a live animation in one Frame, so it cannot show the event paint.)
	setting2 := warmExitSetting(domainpreferences.MotionStandard)
	setting2.TabClose = false
	g2, tt2, reveal2, _ := liveStripWindow(t, &setting2, 3, 1600, 480)
	viewport2 := stripWarpViewport(t, tt2)
	setting2.TabClose = true
	remaining := g2.state.ActiveTab()
	next := followTabID(t, g2.state, remaining)
	nextBox := stripTravel(t, tt2, next)
	nextBar := stripBarFill(tt2, viewport2, nextBox)
	preInk := tabInkPixels(tt2, nextBox, nextBar)
	if preInk < 60 {
		t.Fatalf("the current Tab has only %d ink pixels before the close; the case cannot see its copy", preInk)
	}
	if !g2.state.CloseTab(remaining) {
		t.Fatalf("CloseTab failed")
	}
	stepStripWarp(t, g2.state, tt2, reveal2, motion.TabClose)
	if reveal2.strip.run == nil || reveal2.strip.run.kind != motion.TabClose ||
		reveal2.strip.run.duration != 180*time.Millisecond || reveal2.strip.warp {
		t.Fatalf("the enabled close = run %+v warp=%v, want a 180ms TabClose",
			reveal2.strip.run, reveal2.strip.warp)
	}
	if got := tabInkPixels(tt2, nextBox, nextBar); got < preInk/2 {
		t.Fatalf("the enabled close paints %d of its %d ink pixels on the event frame", got, preInk)
	}
	tt2.Advance(200 * time.Millisecond)
	tt2.Advance(400 * time.Millisecond)
	if _, ok := tt2.Find(tabStripBoxLabel(next)); ok {
		t.Fatalf("the closed Tab is still labeled after its exit")
	}
}
