package quake

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
)

// TestBarWarpsTheTabEventItDidNotRender covers the verifier's Quake probe: a
// Tab is opened while the bar's route is unselected, the app records the gap,
// and the bar's next single frame warps the row with no run and no animation
// frame.
func TestBarWarpsTheTabEventItDidNotRender(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	strip := &quakeStripMotion{}
	tester := ui.NewTester(barWithStrip(state, strip), 800, 120)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()

	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	frames := strip.animationFrames
	state.NoteBarUnrendered()
	tester.Frame()

	if strip.run != nil || strip.animate {
		t.Fatalf("the bar replayed an event it never rendered: run %+v animate %v",
			strip.run, strip.animate)
	}
	if got := strip.animationFrames; got != frames {
		t.Fatalf("the bar's unrendered warp asked for %d animation frames, want none", got-frames)
	}
	event, ok := state.MotionEvent()
	if !ok || strip.lastSeq != event.Seq {
		t.Fatalf("the bar caught up to %d, want the latest event %d", strip.lastSeq, event.Seq)
	}
	if !strip.warp {
		t.Fatal("the bar did not build its row as a warp")
	}
	if !tester.HasText(locales.In("en").LocalSessionTab(2)) {
		t.Fatalf("the bar did not draw the final row: %v", tester.Texts())
	}

	// The notice is one-shot: the next visible open animates.
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	tester.Frame()
	if run := strip.run; run == nil || run.kind != motion.TabOpen {
		t.Fatalf("the bar's next visible event = %+v, want a Tab open", run)
	}
}

// TestViewWarpsTheTabEventItDidNotRender covers the body half: the app marks
// the body when its route is bypassed, and the next frame is the final body
// with no run and no animation frame.
func TestViewWarpsTheTabEventItDidNotRender(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	body := &quakeBodyMotion{}
	tester := ui.NewTester(viewWithBody(state, body), 800, 300)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()

	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	frames := body.animationFrames
	state.NoteViewUnrendered()
	tester.Frame()

	if body.phase != quakeBodyIdle {
		t.Fatalf("the body played phase %v for an event it never rendered", body.phase)
	}
	if got := body.animationFrames; got != frames {
		t.Fatalf("the body's unrendered warp asked for %d animation frames, want none", got-frames)
	}
	event, ok := state.MotionEvent()
	if !ok || body.lastSeq != event.Seq {
		t.Fatalf("the body caught up to %d, want the latest event %d", body.lastSeq, event.Seq)
	}
	if !tester.HasText("surface-2") || tester.HasText("surface-1") {
		t.Fatalf("the body did not draw the final Tab: %v", tester.Texts())
	}

	// The notice is one-shot: the next visible open starts a body run.
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	tester.Frame()
	if body.phase == quakeBodyIdle || body.animationFrames == frames {
		t.Fatalf("the body did not animate the visible event after its warp: phase %v frames %d",
			body.phase, body.animationFrames-frames)
	}
}

// TestQuakeNativeWindowHiddenWarpsTheNextFrame keeps the window's own hidden
// signal working beside the app's notice for both of the Panel's runtimes.
func TestQuakeNativeWindowHiddenWarpsTheNextFrame(t *testing.T) {
	t.Run("bar", func(t *testing.T) {
		fixture := newViewFixture(t)
		state := fixture.state
		if err := state.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		strip := &quakeStripMotion{}
		tester := ui.NewTester(barWithStrip(state, strip), 800, 120)
		tester.SetLocale("en")
		tester.Frame()
		tester.Frame()
		tester.SetWindowHidden(true)
		tester.Frame() // a built frame that reads the window as hidden

		frames := strip.animationFrames
		if err := state.AddLocalTab(); err != nil {
			t.Fatalf("AddLocalTab: %v", err)
		}
		tester.SetWindowHidden(false)
		tester.Frame()
		if strip.run != nil || strip.animate {
			t.Fatalf("the bar replayed the event the hidden frame never drew: %+v", strip.run)
		}
		if got := strip.animationFrames; got != frames {
			t.Fatalf("the bar's hidden warp asked for %d animation frames, want none", got-frames)
		}
		event, ok := state.MotionEvent()
		if !ok || strip.lastSeq != event.Seq {
			t.Fatalf("the hidden bar caught up to %d, want the latest event %d", strip.lastSeq, event.Seq)
		}
	})

	t.Run("view", func(t *testing.T) {
		fixture := newViewFixture(t)
		state := fixture.state
		if err := state.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		body := &quakeBodyMotion{}
		tester := ui.NewTester(viewWithBody(state, body), 800, 300)
		tester.SetLocale("en")
		tester.Frame()
		tester.Frame()
		tester.SetWindowHidden(true)
		tester.Frame()

		frames := body.animationFrames
		if err := state.AddLocalTab(); err != nil {
			t.Fatalf("AddLocalTab: %v", err)
		}
		tester.SetWindowHidden(false)
		tester.Frame()
		if body.phase != quakeBodyIdle {
			t.Fatalf("the body played phase %v for an event the hidden frame never drew", body.phase)
		}
		if got := body.animationFrames; got != frames {
			t.Fatalf("the body's hidden warp asked for %d animation frames, want none", got-frames)
		}
		event, ok := state.MotionEvent()
		if !ok || body.lastSeq != event.Seq {
			t.Fatalf("the hidden body caught up to %d, want the latest event %d", body.lastSeq, event.Seq)
		}
		if !tester.HasText("surface-2") || tester.HasText("surface-1") {
			t.Fatalf("the hidden body did not settle on the new Tab: %v", tester.Texts())
		}
	})
}
