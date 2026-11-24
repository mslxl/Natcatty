package motion

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// TestReorderSpringIsTheTicketSpring pins the exact reorder/zoom parameters:
// 240 ms at standard speed, 0.1 bounce, scaled in time only. It must not be
// one of MyGo's fixed 500 ms springs, whose duration ignores the setting.
func TestReorderSpringIsTheTicketSpring(t *testing.T) {
	for _, tc := range []struct {
		speed domainpreferences.MotionSpeed
		want  time.Duration
	}{
		{domainpreferences.MotionSlow, 360 * time.Millisecond},
		{domainpreferences.MotionStandard, 240 * time.Millisecond},
		{domainpreferences.MotionFast, 144 * time.Millisecond},
	} {
		got := ReorderSpring(tc.speed)
		if got.Duration != tc.want || got.Bounce != 0.1 {
			t.Fatalf("ReorderSpring(%v) = %+v, want duration %s bounce 0.1",
				tc.speed, got, tc.want)
		}
	}
	spring := ReorderSpring(domainpreferences.MotionStandard)
	for name, builtin := range map[string]ui.Animation{
		"smooth": ui.Smooth, "snappy": ui.Snappy, "bouncy": ui.Bouncy,
	} {
		if builtin.Spring.Duration == spring.Duration && builtin.Spring.Bounce == spring.Bounce {
			t.Fatalf("the reorder spring equals ui.%s", name)
		}
	}
}

// TestSpringVisualKeepsItsVelocityWhenRetargeted is the interruption
// contract: a new spring starts at the rect and velocity of the old one, so
// a reverse event turns the target around immediately without dropping the
// speed the Pane already has.
func TestSpringVisualKeepsItsVelocityWhenRetargeted(t *testing.T) {
	start := time.Unix(100, 0)
	opts := ReorderSpring(domainpreferences.MotionStandard)
	from := ui.Rect{X: 100, Y: 200, W: 300, H: 400}
	to := ui.Rect{X: 500, Y: 200, W: 300, H: 400}
	first := &Animation{
		Started: start,
		Visuals: []Visual{{
			Pane: "pane-a", Kind: Move, Box: to, From: from, To: to,
			FromOpacity: 1, ToOpacity: 1, Spring: &opts,
		}},
	}
	first.Measure()
	if first.Duration <= 0 {
		t.Fatalf("the measured spring has no duration")
	}
	firstVisual := first.Visuals[0]
	mid := start.Add(40 * time.Millisecond)
	current, _ := first.StateAt(firstVisual, mid)
	velocity := first.VelocityAt(firstVisual, mid)
	if velocity.X <= 0 {
		t.Fatalf("the moving spring's X velocity = %v, want positive", velocity.X)
	}
	if current.X <= from.X || current.X >= to.X {
		t.Fatalf("the spring at mid-flight is at %+v, want between %+v and %+v", current, from, to)
	}

	reverse := ui.Rect{X: 100, Y: 200, W: 300, H: 400}
	second := &Animation{
		Started: mid,
		Visuals: []Visual{{
			Pane: "pane-a", Kind: Move, Box: reverse, From: current, To: reverse,
			FromOpacity: 1, ToOpacity: 1, Spring: &opts, Velocity: velocity,
		}},
	}
	second.Measure()
	if second.Visuals[0].Velocity != velocity {
		t.Fatalf("the retargeted visual velocity = %+v, want the current %+v",
			second.Visuals[0].Velocity, velocity)
	}
	if got := second.VelocityAt(second.Visuals[0], mid); got.X <= 0 {
		t.Fatalf("the retargeted velocity at the change = %v, want the old positive velocity kept", got.X)
	}
	// The target has already reversed, so the Pane heads back and ends there
	// rather than completing the old journey.
	secondAtEnd, _ := second.StateAt(second.Visuals[0], mid.Add(second.Duration))
	if !rectClose(secondAtEnd, reverse) {
		t.Fatalf("the retargeted spring ended at %+v, want the reversed target %+v", secondAtEnd, reverse)
	}
}

// TestReorderPartsKeepTheirOwnDurations checks the mixed run: the close
// ghost takes 150 ms, the open overlay 200 ms and the sibling spring its
// own settle, all inside one animation whose end is the longest part.
func TestReorderPartsKeepTheirOwnDurations(t *testing.T) {
	start := time.Unix(200, 0)
	opts := ReorderSpring(domainpreferences.MotionStandard)
	anim := &Animation{
		Started: start,
		Visuals: []Visual{
			{
				Pane: "ghost", Kind: Exit, Box: ui.Rect{W: 100, H: 100},
				From: ui.Rect{W: 100, H: 100}, To: ui.Rect{W: 100, H: 100},
				FromOpacity: 1, Duration: 150 * time.Millisecond,
				Style: domainpreferences.MotionPopin,
			},
			{
				Pane: "enter", Kind: Enter, Box: ui.Rect{X: 100, W: 100, H: 100},
				To:       ui.Rect{X: 100, W: 100, H: 100},
				Duration: 200 * time.Millisecond, Style: domainpreferences.MotionPopin,
				FromOpacity: 0, ToOpacity: 1,
			},
			{
				Pane: "sibling", Kind: Move,
				From: ui.Rect{W: 50, H: 50}, To: ui.Rect{X: 300, W: 50, H: 50},
				Box:    ui.Rect{X: 300, W: 50, H: 50},
				Spring: &opts, FromOpacity: 1, ToOpacity: 1,
			},
		},
	}
	anim.Measure()
	if anim.Duration <= 200*time.Millisecond {
		t.Fatalf("the mixed run duration = %s, want the spring's longer settle", anim.Duration)
	}
	ghost, enter, sibling := anim.Visuals[0], anim.Visuals[1], anim.Visuals[2]
	at160 := start.Add(160 * time.Millisecond)
	if _, opacity := anim.StateAt(ghost, at160); opacity != 0 {
		t.Fatalf("the ghost opacity after its 150ms = %v, want 0", opacity)
	}
	if rect, _ := anim.StateAt(enter, at160); rect.W >= enter.To.W {
		t.Fatalf("the enter overlay finished early at %+v", rect)
	}
	if _, opacity := anim.StateAt(enter, start.Add(210*time.Millisecond)); opacity != 1 {
		t.Fatalf("the enter overlay opacity after its 200ms = %v, want 1", opacity)
	}
	if rect, _ := anim.StateAt(sibling, start.Add(60*time.Millisecond)); rect.X <= sibling.From.X || rect.X >= sibling.To.X {
		t.Fatalf("the sibling spring at 60ms = %+v, want between %+v and %+v",
			rect, sibling.From, sibling.To)
	}
}

// TestSpringSettleEndsAtTheTarget checks a measured spring's own duration is
// its rest time, so a run that ends there leaves the Pane exactly at final.
func TestSpringSettleEndsAtTheTarget(t *testing.T) {
	start := time.Unix(300, 0)
	opts := ReorderSpring(domainpreferences.MotionStandard)
	from := ui.Rect{X: 10, Y: 20, W: 100, H: 80}
	to := ui.Rect{X: 310, Y: 220, W: 400, H: 380}
	anim := &Animation{
		Started: start,
		Visuals: []Visual{{
			Pane: "pane", Kind: Move, Box: to, From: from, To: to,
			FromOpacity: 1, ToOpacity: 1, Spring: &opts,
		}},
	}
	anim.Measure()
	if anim.Visuals[0].Duration != anim.Duration || anim.Duration <= 0 {
		t.Fatalf("the spring duration = %s, run = %s", anim.Visuals[0].Duration, anim.Duration)
	}
	rect, _ := anim.StateAt(anim.Visuals[0], start.Add(anim.Duration))
	if !rectClose(rect, to) {
		t.Fatalf("the spring at its settle time = %+v, want %+v", rect, to)
	}
}

// rectClose reports that every edge is within a thousandth of its target.
func rectClose(a, b ui.Rect) bool {
	close := func(x, y float32) bool {
		d := x - y
		if d < 0 {
			d = -d
		}
		return d <= 0.001
	}
	return close(a.X, b.X) && close(a.Y, b.Y) && close(a.W, b.W) && close(a.H, b.H)
}

// TestEventIsZoomDistinguishesMovesFromZoom pins the event model the view
// uses to route a PaneReorder: only a zoom carries a magnification field.
func TestEventIsZoomDistinguishesMovesFromZoom(t *testing.T) {
	move := Event{Kind: PaneReorder, PaneID: "pane-a", TargetID: "pane-b"}
	if move.IsZoom() {
		t.Fatalf("a same-Tab move was classified as a zoom")
	}
	for _, event := range []Event{
		{Kind: PaneReorder, FromMagnified: "pane-a"},
		{Kind: PaneReorder, ToMagnified: "pane-b"},
		{Kind: PaneReorder, FromMagnified: "pane-a", ToMagnified: "pane-b"},
	} {
		if !event.IsZoom() {
			t.Fatalf("event %+v was not classified as a zoom", event)
		}
	}
	if (Event{Kind: PaneOpen, FromMagnified: "pane-a"}).IsZoom() {
		t.Fatalf("a PaneOpen was classified as a PaneReorder zoom")
	}
}
