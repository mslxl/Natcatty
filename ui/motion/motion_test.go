package motion

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

func TestScaleMotionUsesTheSpeedTier(t *testing.T) {
	base := 200 * time.Millisecond
	for _, tc := range []struct {
		speed domainpreferences.MotionSpeed
		want  time.Duration
	}{
		{domainpreferences.MotionSlow, 300 * time.Millisecond},
		{domainpreferences.MotionStandard, 200 * time.Millisecond},
		{domainpreferences.MotionFast, 120 * time.Millisecond},
		{"", 200 * time.Millisecond},
	} {
		if got := ScaleMotion(base, tc.speed); got != tc.want {
			t.Errorf("ScaleMotion(%s, %q) = %s, want %s", base, tc.speed, got, tc.want)
		}
	}
	if got := BaseDuration(PaneOpen); got != 200*time.Millisecond {
		t.Errorf("BaseDuration(PaneOpen) = %s, want 200ms", got)
	}
	if got := BaseDuration(PaneClose); got != 150*time.Millisecond {
		t.Errorf("BaseDuration(PaneClose) = %s, want 150ms", got)
	}
	if got := BaseDuration(TabSwitch); got != 240*time.Millisecond {
		t.Errorf("BaseDuration(TabSwitch) = %s, want 240ms", got)
	}
}

func TestEnabledNeedsMasterAndEventSwitch(t *testing.T) {
	setting := domainpreferences.Defaults().Appearance.Motion
	if !Enabled(setting, PaneOpen) {
		t.Fatalf("the default setting does not enable Pane open")
	}
	off := setting
	off.Enabled = false
	if Enabled(off, PaneOpen) {
		t.Errorf("master off still enables Pane open")
	}
	eventOff := setting
	eventOff.PaneOpen = false
	if Enabled(eventOff, PaneOpen) {
		t.Errorf("PaneOpen off still enables Pane open")
	}
	if !Enabled(eventOff, PaneClose) {
		t.Errorf("PaneOpen off disabled an unrelated event")
	}
}

func TestControllerOnlyConsumesNewerSequences(t *testing.T) {
	controller := NewController(nil)
	if controller.Unseen(1) != true {
		t.Fatalf("a fresh controller has seen an event")
	}
	controller.MarkSeen(3)
	if controller.Unseen(3) || controller.Unseen(2) {
		t.Fatalf("an event at or before the last sequence is unseen")
	}
	if !controller.Unseen(4) {
		t.Fatalf("a newer event is not unseen")
	}
	anim := &Animation{Duration: time.Second}
	controller.Begin(anim)
	if controller.Animation() != anim {
		t.Fatalf("Begin did not keep the run")
	}
	if controller.Clear() != anim || controller.Animation() != nil {
		t.Fatalf("Clear did not report and drop the run")
	}
}

func TestControllerRecordsPaneBoxes(t *testing.T) {
	controller := NewController(nil)
	id := model.PaneID("pane-a")
	rects := PaneRects{
		Slot:   ui.Rect{X: 0, Y: 0, W: 900, H: 600},
		Column: ui.Rect{X: 4, Y: 4, W: 892, H: 592},
		Body:   ui.Rect{X: 4, Y: 32, W: 892, H: 560},
	}
	controller.RecordPane(id, rects)
	if got, ok := controller.Pane(id); !ok || got != rects {
		t.Fatalf("Pane(%s) = %+v, %v, want %+v", id, got, ok, rects)
	}
	controller.RecordPane(id, PaneRects{})
	if got, _ := controller.Pane(id); got != rects {
		t.Fatalf("an unmeasured box replaced the recorded one: %+v", got)
	}
	controller.ResetRects()
	if _, ok := controller.Pane(id); ok {
		t.Fatalf("ResetRects kept the Pane")
	}
}

func TestAnimationProgressAndState(t *testing.T) {
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	anim := &Animation{
		Started:  start,
		Duration: 200 * time.Millisecond,
		Visuals: []Visual{{
			Pane:        "pane-a",
			Kind:        Move,
			From:        ui.Rect{X: 0, Y: 0, W: 900, H: 560},
			To:          ui.Rect{X: 0, Y: 0, W: 449, H: 560},
			FromOpacity: 1, ToOpacity: 1,
		}},
	}
	if k, done := anim.Progress(start); k != 0 || done {
		t.Fatalf("progress at the start = %v, %v, want 0, false", k, done)
	}
	if _, done := anim.Progress(start.Add(200 * time.Millisecond)); !done {
		t.Fatalf("progress at the duration is not done")
	}
	if k, done := anim.Progress(start.Add(100 * time.Millisecond)); done || k <= 0 || k >= 1 {
		t.Fatalf("progress halfway = %v, %v, want inside (0,1)", k, done)
	}
	v, ok := anim.VisualFor("pane-a")
	if !ok {
		t.Fatalf("VisualFor did not find the visual")
	}
	rect, opacity := anim.State(v, 1)
	if rect != v.To || opacity != 1 {
		t.Fatalf("state at rest = %+v, %v, want the target box and opacity 1", rect, opacity)
	}
}

func TestEnterStylesGeometryAndOpacity(t *testing.T) {
	final := ui.Rect{X: 451, Y: 0, W: 449, H: 560}
	content := ui.Rect{X: 0, Y: 0, W: 900, H: 560}
	const eps = 0.01
	for _, tc := range []struct {
		name string
		test func(*testing.T)
	}{
		{"popin", func(t *testing.T) {
			r := EnterRect(final, domainpreferences.MotionPopin, EdgeAuto, content, 0)
			if d := abs(r.W - final.W*0.87); d > eps {
				t.Fatalf("popin width at 0 = %v, want 87%% of %v", r.W, final.W)
			}
			if abs(r.X+r.W/2-(final.X+final.W/2)) > eps ||
				abs(r.Y+r.H/2-(final.Y+final.H/2)) > eps {
				t.Fatalf("popin is not centered: %+v", r)
			}
			if r := EnterRect(final, domainpreferences.MotionPopin, EdgeAuto, content, 1); r != final {
				t.Fatalf("popin at rest = %+v, want %+v", r, final)
			}
			if got := EnterOpacity(domainpreferences.MotionPopin, 0); got != 0 {
				t.Fatalf("popin opacity at 0 = %v, want 0", got)
			}
			if got := EnterOpacity(domainpreferences.MotionPopin, 1); got != 1 {
				t.Fatalf("popin opacity at 1 = %v, want 1", got)
			}
		}},
		{"slide", func(t *testing.T) {
			// The right half's nearest content edge is the content's right
			// edge, so it slides in from the right by its own width.
			r := EnterRect(final, domainpreferences.MotionSlide, EdgeAuto, content, 0)
			if r.X != final.X+final.W || r.W != final.W || r.H != final.H {
				t.Fatalf("slide at 0 = %+v, want one Pane width to the right of %+v", r, final)
			}
			if r := EnterRect(final, domainpreferences.MotionSlide, EdgeAuto, content, 1); r != final {
				t.Fatalf("slide at rest = %+v, want %+v", r, final)
			}
			if got := EnterOpacity(domainpreferences.MotionSlide, 0); got != 1 {
				t.Fatalf("slide opacity = %v, want an explicit 1", got)
			}
		}},
		{"gnomed", func(t *testing.T) {
			r := EnterRect(final, domainpreferences.MotionGnomed, EdgeAuto, content, 0)
			if r.W != final.W || r.H != 0 || r.Y != final.Y+final.H {
				t.Fatalf("gnomed at 0 = %+v, want no height at the bottom edge", r)
			}
			r = EnterRect(final, domainpreferences.MotionGnomed, EdgeAuto, content, 0.5)
			if abs(r.H-final.H*0.5) > eps || abs(r.Y+r.H-(final.Y+final.H)) > eps {
				t.Fatalf("gnomed halfway = %+v, want the bottom edge fixed at %v", r, final.Y+final.H)
			}
			if got := EnterOpacity(domainpreferences.MotionGnomed, 0); got != 1 {
				t.Fatalf("gnomed opacity = %v, want an explicit 1", got)
			}
		}},
		{"fade", func(t *testing.T) {
			if r := EnterRect(final, domainpreferences.MotionFade, EdgeAuto, content, 0.4); r != final {
				t.Fatalf("fade geometry = %+v, want %+v at every progress", r, final)
			}
			if got := EnterOpacity(domainpreferences.MotionFade, 0.4); abs(got-0.4) > eps {
				t.Fatalf("fade opacity at 0.4 = %v, want 0.4", got)
			}
		}},
	} {
		t.Run(tc.name, tc.test)
	}
}

func TestResolveEdgePicksTheNearestContentEdge(t *testing.T) {
	content := ui.Rect{X: 0, Y: 0, W: 900, H: 600}
	for _, tc := range []struct {
		name  string
		final ui.Rect
		want  Edge
	}{
		{"left", ui.Rect{X: 0, Y: 0, W: 449, H: 600}, EdgeLeft},
		{"right", ui.Rect{X: 451, Y: 0, W: 449, H: 600}, EdgeRight},
		{"top", ui.Rect{X: 0, Y: 0, W: 900, H: 299}, EdgeTop},
		{"bottom", ui.Rect{X: 0, Y: 301, W: 900, H: 299}, EdgeBottom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveEdge(EdgeAuto, tc.final, content); got != tc.want {
				t.Fatalf("ResolveEdge = %v, want %v", got, tc.want)
			}
		})
	}
	if got := ResolveEdge(EdgeTop, ui.Rect{X: 0, Y: 0, W: 449, H: 600}, content); got != EdgeTop {
		t.Fatalf("an explicit edge was overridden: %v", got)
	}
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
