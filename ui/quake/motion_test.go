package quake

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/dropdown"
	"github.com/mslxl/natcatty/ui/motion"
)

// motionPanel is the Panel with the bar's and body's runtimes supplied, so a
// test can read the strip's run and the body's phase.
func motionPanel(state *State, strip *quakeStripMotion, body *quakeBodyMotion) func(*ui.Context) {
	return dropdown.View(nil, barWithStrip(state, strip), viewWithBody(state, body))
}

// standardMotion is the default motion setting at its standard speed.
func standardMotion() domainpreferences.Motion {
	return domainpreferences.Defaults().Appearance.Motion
}

// TestBarAndBodyPlayTabOpenSwitchAndClose is the Quake acceptance: the bar's
// strip and the body below it consume the same Tab event stream
// independently. An open moves the strip and leaves the old body for the new
// one; a switch changes the selected fill at once and animates only the body;
// closing an inactive Tab moves only the strip.
func TestBarAndBodyPlayTabOpenSwitchAndClose(t *testing.T) {
	setting := standardMotion()
	fixture := newViewFixture(t, WithMotion(func() domainpreferences.Motion { return setting }))
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	strip := &quakeStripMotion{}
	body := &quakeBodyMotion{}
	tester := ui.NewTester(motionPanel(state, strip, body), 800, 400)
	tester.SetLocale("en")
	tester.Frame()

	// The first frame sees the initial Tab open; it warps and asks for no
	// frames, and the body settles on surface-1. The tester builds a few
	// frames per Frame call, so a warp is observed by its frame count, not a
	// flag the last build already cleared.
	if strip.run != nil {
		t.Fatalf("the first Quake frame left a strip run: %+v", strip.run)
	}
	if strip.animationFrames != 0 {
		t.Fatalf("the first Quake frame asked for %d strip frames", strip.animationFrames)
	}
	if body.phase != quakeBodyIdle || body.animationFrames != 0 {
		t.Fatalf("the first Quake frame played a body run: phase %v, frames %d",
			body.phase, body.animationFrames)
	}
	if !tester.HasText("surface-1") {
		t.Fatalf("the first Tab's body is missing: %v", tester.Texts())
	}

	// A visible open moves the strip for 180 ms and starts the body's run
	// with the open/close styles and their 200/150 ms durations.
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	tester.Frame()
	run := strip.run
	if run == nil || run.kind != motion.TabOpen {
		t.Fatalf("the strip run = %+v, want a Tab open", run)
	}
	if run.duration != 180*time.Millisecond {
		t.Fatalf("strip duration = %s, want 180ms at standard speed", run.duration)
	}
	if !strip.animate || strip.generation != 0 {
		t.Fatalf("the strip did not animate in place: animate=%v generation=%d",
			strip.animate, strip.generation)
	}
	if strip.animationFrames == 0 {
		t.Fatal("the strip asked for no animation frames after the open")
	}
	if body.phase == quakeBodyIdle || body.out != 1 || !body.enterSet {
		t.Fatalf("body = phase %v out %d enterSet %v, want the old body leaving",
			body.phase, body.out, body.enterSet)
	}
	if body.enterDuration != 200*time.Millisecond || body.exitDuration != 150*time.Millisecond {
		t.Fatalf("body durations = enter %s exit %s, want 200ms/150ms",
			body.enterDuration, body.exitDuration)
	}
	if body.animationFrames == 0 {
		t.Fatal("the body asked for no animation frames after the open")
	}
	if body.shown != 2 || !tester.HasText("surface-2") {
		t.Fatalf("the open did not reach the new body: shown %d, texts %v", body.shown, tester.Texts())
	}

	tester.Frame()
	if body.phase != quakeBodyIn {
		t.Fatalf("the second frame body = phase %v, want the new body entering", body.phase)
	}
	tester.Advance(180 * time.Millisecond)
	tester.Frame()
	if strip.run != nil {
		t.Fatal("the strip run is still playing after 180ms")
	}
	if body.phase != quakeBodyIn {
		t.Fatalf("the body left its entrance before 200ms: phase %v", body.phase)
	}
	tester.Advance(40 * time.Millisecond)
	tester.Frame()
	if body.phase != quakeBodyIdle {
		t.Fatalf("the body run is still playing after 220ms: phase %v", body.phase)
	}
	if !tester.HasText("surface-2") || tester.HasText("surface-1") {
		t.Fatalf("the settled Panel shows the wrong body: %v", tester.Texts())
	}

	// A switch changes only the selection and the body. The strip attaches a
	// warp with no color transition, so the selected fill changes on this
	// frame and nothing slides or asks for a frame.
	stripFrames := strip.animationFrames
	state.SelectTab(0)
	tester.Frame()
	if strip.run != nil || strip.animate {
		t.Fatalf("a switch moved the strip: run=%+v animate=%v", strip.run, strip.animate)
	}
	if strip.animationFrames != stripFrames {
		t.Fatalf("a switch asked for %d strip frames, want none",
			strip.animationFrames-stripFrames)
	}
	transition := strip.transition(setting)
	if transition.Colors {
		t.Fatal("a Quake switch animates the selected fill")
	}
	if !transition.Position || transition.Duration >= 0 {
		t.Fatalf("a Quake switch transition = %+v, want an immediate position warp", transition)
	}
	if body.phase == quakeBodyIdle || body.out != 2 {
		t.Fatalf("the switch body = phase %v out %d, want the old body leaving",
			body.phase, body.out)
	}
	if body.event.Kind != motion.TabSwitch {
		t.Fatalf("the body is playing %v, want a Tab switch", body.event.Kind)
	}
	if body.enterDuration != 240*time.Millisecond || body.exitDuration != 240*time.Millisecond {
		t.Fatalf("switch durations = %s/%s, want 240ms", body.enterDuration, body.exitDuration)
	}
	tester.Frame()
	if body.phase != quakeBodyIn {
		t.Fatalf("the switch body did not enter on the next frame: phase %v", body.phase)
	}
	tester.Advance(240 * time.Millisecond)
	tester.Frame()
	if body.phase != quakeBodyIdle || !tester.HasText("surface-1") {
		t.Fatalf("the switch did not settle on surface-1: phase %v, texts %v", body.phase, tester.Texts())
	}

	// Closing an inactive Tab moves only the strip; the body keeps drawing
	// surface-1.
	beforeClose := body.animationFrames
	if !state.CloseTab(1) {
		t.Fatal("CloseTab returned false")
	}
	tester.Frame()
	if run := strip.run; run == nil || run.kind != motion.TabClose {
		t.Fatalf("the strip run = %+v, want a Tab close", run)
	}
	if body.phase != quakeBodyIdle || body.animationFrames != beforeClose {
		t.Fatalf("closing an inactive Tab moved the body: phase %v frames %d -> %d",
			body.phase, beforeClose, body.animationFrames)
	}
	if !tester.HasText("surface-1") {
		t.Fatalf("the remaining body is missing: %v", tester.Texts())
	}
	tester.Advance(180 * time.Millisecond)
	tester.Frame()
	if strip.run != nil {
		t.Fatal("the close strip run is still playing after 180ms")
	}
}

// TestQuakeMotionSwitchesWarpTheBarAndBody keeps every switch in the Quake
// Panel local. Motion off, the Tab-open event off, Reduce Motion and a hidden
// window all warp the newest event with no run and no animation frames, and
// the settled body is still the new Tab's.
func TestQuakeMotionSwitchesWarpTheBarAndBody(t *testing.T) {
	cases := []struct {
		name    string
		setting domainpreferences.Motion
		reduce  bool
		hidden  bool
	}{
		{"master off", func() domainpreferences.Motion {
			setting := standardMotion()
			setting.Enabled = false
			return setting
		}(), false, false},
		{"Tab open off", func() domainpreferences.Motion {
			setting := standardMotion()
			setting.TabOpen = false
			return setting
		}(), false, false},
		{"Reduce Motion", standardMotion(), true, false},
		{"hidden window", standardMotion(), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := tc.setting
			fixture := newViewFixture(t, WithMotion(func() domainpreferences.Motion { return setting }))
			state := fixture.state
			if err := state.Activate(); err != nil {
				t.Fatalf("Activate: %v", err)
			}
			strip := &quakeStripMotion{}
			body := &quakeBodyMotion{}
			tester := ui.NewTester(motionPanel(state, strip, body), 800, 400)
			tester.SetLocale("en")
			if tc.reduce {
				tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			}
			tester.Frame()
			if tc.hidden {
				tester.SetWindowHidden(true)
			}
			beforeStrip, beforeBody := strip.animationFrames, body.animationFrames

			if err := state.AddLocalTab(); err != nil {
				t.Fatalf("AddLocalTab: %v", err)
			}
			if tc.hidden {
				// The show frame consumes the event the hide frame never
				// drew, exactly as the window's visibility contract says.
				tester.SetWindowHidden(false)
			} else {
				tester.Frame()
			}
			if strip.run != nil || strip.animate {
				t.Fatalf("strip = run %+v animate %v, want a warp",
					strip.run, strip.animate)
			}
			if got := strip.animationFrames - beforeStrip; got != 0 {
				t.Fatalf("the strip asked for %d animation frames", got)
			}
			if body.phase != quakeBodyIdle {
				t.Fatalf("the body played phase %v", body.phase)
			}
			if got := body.animationFrames - beforeBody; got != 0 {
				t.Fatalf("the body asked for %d animation frames", got)
			}
			if transition := strip.transition(setting); transition.Duration >= 0 {
				t.Fatalf("the warp transition = %+v, want a negative duration", transition)
			}
			tester.Frame()
			if !tester.HasText("surface-2") || tester.HasText("surface-1") {
				t.Fatalf("the settled Panel shows the wrong body: %v", tester.Texts())
			}
			event, ok := state.MotionEvent()
			if !ok {
				t.Fatal("no Tab event was recorded")
			}
			switch event.Kind {
			case motion.TabOpen, motion.TabClose, motion.TabSwitch, motion.TabReorder:
			default:
				t.Fatalf("Quake recorded a non-Tab event: %+v", event)
			}
		})
	}
}

// TestQuakeLastTabCloseLeavesTheEmptyPanel covers the one body run with no
// incoming Tab: the last Tab closes over 150 ms and the empty Panel follows
// with no entrance animation.
func TestQuakeLastTabCloseLeavesTheEmptyPanel(t *testing.T) {
	setting := standardMotion()
	fixture := newViewFixture(t, WithMotion(func() domainpreferences.Motion { return setting }))
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	strip := &quakeStripMotion{}
	body := &quakeBodyMotion{}
	tester := ui.NewTester(motionPanel(state, strip, body), 800, 400)
	tester.SetLocale("en")
	tester.Frame()

	if !state.CloseTab(0) {
		t.Fatal("CloseTab returned false")
	}
	tester.Frame()
	if body.phase == quakeBodyIdle || body.out != 1 {
		t.Fatalf("body = phase %v out %d, want the last body leaving", body.phase, body.out)
	}
	if body.enterSet {
		t.Fatal("the last close expects an incoming body")
	}
	if body.exitDuration != 150*time.Millisecond {
		t.Fatalf("the last close exit = %s, want 150ms", body.exitDuration)
	}
	if body.animationFrames == 0 {
		t.Fatal("the last close asked for no animation frames")
	}
	tester.Advance(160 * time.Millisecond)
	tester.Frame()
	if body.phase != quakeBodyIdle {
		t.Fatalf("the last close is still playing after 160ms: %v", body.phase)
	}
	if state.TabCount() != 0 {
		t.Fatalf("tabs = %d after closing the last one", state.TabCount())
	}
	if tester.HasText("surface-1") {
		t.Fatalf("the closed Tab's terminal stayed on the empty Panel: %v", tester.Texts())
	}
}
