package effects

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The bell's envelope: held for the first 45% of its second, then ramped to zero so
// the wave fades out instead of stopping abruptly (Terminal's tnRipBellFade).
func TestBellEnvelope(t *testing.T) {
	var r Ripple
	r.Start(Bell, 0, 0, time.Now())
	s := BellSpec
	if got := r.Amplitude(0); got != s.Amplitude {
		t.Errorf("at the start amplitude = %v, want %v", got, s.Amplitude)
	}
	held := time.Duration(float64(s.Duration) * (s.Hold - 0.01))
	if got := r.Amplitude(held); got != s.Amplitude {
		t.Errorf("while held amplitude = %v, want %v", got, s.Amplitude)
	}
	mid := time.Duration(float64(s.Duration) * (s.Hold + (1-s.Hold)/2))
	if got := r.Amplitude(mid); got <= 0 || got >= s.Amplitude {
		t.Errorf("while fading amplitude = %v, want between 0 and %v", got, s.Amplitude)
	}
	if got := r.Amplitude(s.Duration); got != 0 {
		t.Errorf("at the end amplitude = %v, want 0", got)
	}
	if got := r.Amplitude(s.Duration + time.Second); got != 0 {
		t.Errorf("after the end amplitude = %v, want 0", got)
	}
	// It fades monotonically.
	prev := r.Amplitude(time.Duration(float64(s.Duration) * s.Hold))
	for f := s.Hold + 0.05; f < 1; f += 0.05 {
		cur := r.Amplitude(time.Duration(float64(s.Duration) * f))
		if cur > prev {
			t.Errorf("amplitude went up while fading: %v then %v", prev, cur)
		}
		prev = cur
	}
}

// The drop's numbers are Terminal's other filter argument set, with its shorter run.
func TestDropSpec(t *testing.T) {
	var r Ripple
	r.Start(Drop, 0, 0, time.Now())
	if r.Kind() != Drop {
		t.Fatalf("kind = %v, want Drop", r.Kind())
	}
	if got := r.Spec(); got != DropSpec {
		t.Errorf("spec = %+v, want %+v", got, DropSpec)
	}
	if DropSpec.Amplitude != 3 || DropSpec.Frequency != 30 || DropSpec.Decay != 5 || DropSpec.Speed != 480 {
		t.Errorf("the drop's numbers are not Terminal's: %+v", DropSpec)
	}
	if DropSpec.Duration != 700*time.Millisecond || DropSpec.Hold != 0.40 {
		t.Errorf("the drop's timing is not Terminal's: %+v", DropSpec)
	}
	// A drop has no ring; the bell does.
	if got := r.Flash(0); got != 0 {
		t.Errorf("a drop flashed %v", got)
	}
	var bell Ripple
	bell.Start(Bell, 0, 0, time.Now())
	if got := bell.Flash(0); got != 1 {
		t.Errorf("the bell's ring at the start = %v, want 1", got)
	}
}

// The ring shows for half a second, from full to nothing (Terminal's tnBell).
func TestBellFlash(t *testing.T) {
	var r Ripple
	r.Start(Bell, 0, 0, time.Now())
	if got := r.Flash(0); got != 1 {
		t.Errorf("flash at the start = %v, want 1", got)
	}
	if got := r.Flash(FlashDuration / 2); got < 0.4 || got > 0.6 {
		t.Errorf("flash halfway = %v, want about 0.5", got)
	}
	if got := r.Flash(FlashDuration); got != 0 {
		t.Errorf("flash at the end = %v, want 0", got)
	}
	if got := r.Flash(2 * FlashDuration); got != 0 {
		t.Errorf("flash after the end = %v, want 0", got)
	}
}

// A ripple runs for its duration and stops by itself.
func TestRippleRunsForItsDuration(t *testing.T) {
	var r Ripple
	start := time.Now()
	if r.Running(start) {
		t.Fatalf("a ripple runs before it started")
	}
	r.Start(Bell, 3, 7, start)
	x, y := r.Cell()
	if x != 3 || y != 7 {
		t.Errorf("cell = (%d,%d), want (3,7)", x, y)
	}
	if !r.Running(start) {
		t.Fatalf("the ripple does not run at its start")
	}
	if !r.Running(start.Add(BellSpec.Duration - time.Millisecond)) {
		t.Errorf("the ripple stopped before its run was over")
	}
	if r.Running(start.Add(BellSpec.Duration)) {
		t.Errorf("the ripple still runs after its run")
	}
	if got := r.Age(start.Add(100 * time.Millisecond)); got < 99*time.Millisecond || got > 101*time.Millisecond {
		t.Errorf("age = %v, want about 100ms", got)
	}
	// The drop's run is shorter.
	r.Start(Drop, 1, 1, start)
	if !r.Running(start.Add(600 * time.Millisecond)) {
		t.Errorf("the drop ripple stopped too soon")
	}
	if r.Running(start.Add(750 * time.Millisecond)) {
		t.Errorf("the drop ripple outlived its 0.7 s")
	}
}

// Starting again moves the wave to the new cell.
func TestRippleRestarts(t *testing.T) {
	var r Ripple
	start := time.Now()
	r.Start(Bell, 1, 2, start)
	r.Start(Bell, 9, 4, start.Add(500*time.Millisecond))
	if x, y := r.Cell(); x != 9 || y != 4 {
		t.Errorf("cell = (%d,%d), want the second start", x, y)
	}
	if !r.Running(start.Add(600 * time.Millisecond)) {
		t.Errorf("the restarted ripple is not running")
	}
	if r.Starts() != 2 {
		t.Errorf("starts = %d, want 2", r.Starts())
	}
}

// A drop starts at the pointer's own point when the pane knows it: a cell centre
// is only the fallback for a ripple named by a cell alone (the bell). A later
// cell-only pointer position drops the point, so a stale one cannot move the
// next drop's wave.
func TestRippleExactOrigin(t *testing.T) {
	var r Ripple
	now := time.Now()
	r.StartAt(Bell, 2, 3, ui.Point{X: 10, Y: 20}, now)
	if x, y := r.Cell(); x != 2 || y != 3 {
		t.Errorf("cell = (%d,%d), want (2,3)", x, y)
	}
	if at, ok := r.Origin(); !ok || at != (ui.Point{X: 10, Y: 20}) {
		t.Errorf("origin = %v, %v, want the given point", at, ok)
	}
	r.Start(Bell, 1, 1, now)
	if _, ok := r.Origin(); ok {
		t.Error("a start named only by a cell kept an exact origin")
	}
	r.PointerAt(4, 5, ui.Point{X: 6, Y: 7})
	if !r.StartDrop(now) {
		t.Fatal("a drop did not start with a pointer")
	}
	if at, ok := r.Origin(); !ok || at.X != 6 || at.Y != 7 {
		t.Errorf("drop origin = %v, %v, want the pointer's (6,7)", at, ok)
	}
	if x, y := r.Cell(); x != 4 || y != 5 {
		t.Errorf("drop cell = (%d,%d), want (4,5)", x, y)
	}
	r.Pointer(8, 9)
	if !r.StartDrop(now) {
		t.Fatal("a second drop did not start")
	}
	if _, ok := r.Origin(); ok {
		t.Error("a cell-only pointer kept the previous exact origin")
	}
}

// A drop needs a pointer position to ripple from.
func TestDropNeedsPointer(t *testing.T) {
	var r Ripple
	now := time.Now()
	if r.StartDrop(now) {
		t.Fatalf("a drop started without a pointer")
	}
	r.Pointer(12, 34)
	if !r.StartDrop(now) {
		t.Fatalf("a drop did not start with a pointer")
	}
	if x, y := r.Cell(); x != 12 || y != 34 {
		t.Errorf("drop cell = (%d,%d), want the pointer's (12,34)", x, y)
	}
	if r.Kind() != Drop {
		t.Errorf("kind = %v, want Drop", r.Kind())
	}
}
