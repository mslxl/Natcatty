// Package effects holds what the terminal draws when something asks for attention:
// Terminal's bell and drop ripples.
//
// Terminal answers a terminal bell, and a file dropped on a pane, with a ripple that
// runs out from where it happened: a full-surface distortion driven by a compositor
// filter. Its numbers, from the CSS filter it ships (`.tn-body.rip-bell` and
// `.rip-drop`): 4 px amplitude, 22 rad/s, 3.5 s⁻¹ decay and a 620 px/s wave front
// for 1.1 s for the bell, and 3 px, 30, 5, 480 px/s for 0.7 s for a drop. Both hold
// their amplitude for the first part of the run (45% and 40%) and then ramp to zero,
// so the wave fades out instead of stopping abruptly ("The bell and file-drop
// ripples fade out instead of stopping abruptly", Terminal's changelog). The bell also
// flashes a ring around the pane for half a second (`.tn-pane.bell .tn-body::after`,
// `tnBell`).
//
// MyGo has the same mechanism as a public API: a shader's DistortionEffect says
// where each pixel of an element reads its content, which is exactly Terminal's
// `textureLoad(src, device + delta * (amount / distance))`.
package effects

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// Kind is which ripple runs.
type Kind int

const (
	// Bell ripples out of the cursor when the program rings the terminal's bell.
	Bell Kind = iota
	// Drop ripples where a file was dropped on the pane.
	Drop
)

// Spec is one ripple's numbers, as Terminal's filter arguments carry them.
type Spec struct {
	// Amplitude is how far, in DIPs, the surface is displaced.
	Amplitude float32
	// Frequency is the wave's angular frequency, in radians per second.
	Frequency float32
	// Decay is how fast the wave dies away, per second.
	Decay float32
	// Speed is how fast the wave front travels, in DIPs per second.
	Speed float32
	// Duration is how long the ripple runs.
	Duration time.Duration
	// Hold is the share of the duration the amplitude is held before it fades.
	Hold float64
}

// BellSpec is the bell's numbers, as Terminal's `.rip-bell` filter passes them.
var BellSpec = Spec{Amplitude: 4, Frequency: 22, Decay: 3.5, Speed: 620, Duration: 1100 * time.Millisecond, Hold: 0.45}

// DropSpec is the drop's numbers, as Terminal's `.rip-drop` filter passes them.
var DropSpec = Spec{Amplitude: 3, Frequency: 30, Decay: 5, Speed: 480, Duration: 700 * time.Millisecond, Hold: 0.40}

// FlashDuration is how long the bell's ring lasts (Terminal's tnBell: 0.5 s).
const FlashDuration = 500 * time.Millisecond

// distortion samples the element's own content at a displaced point: the wave
// front reaches a pixel at distance/speed, oscillates there, and decays, so what
// shows is the surface bending like water.
var distortion = shader.MustCompile(`package main

// Time is seconds since the ripple started.
var Time float
// Origin is its centre, in DIPs from the element's corner.
var Origin vec2
// Amplitude is how far the surface bends, in DIPs.
var Amplitude float
// Frequency is the wave's angular frequency, in radians per second.
var Frequency float
// Decay is how fast the wave dies away, per second.
var Decay float
// Speed is how fast the wave front travels, in DIPs per second.
var Speed float

func DistortionEffect(pos vec2) vec2 {
	d := pos - Origin
	dist := length(d)
	front := Time - dist/Speed
	if front <= 0.0 || dist <= 0.000001 || Amplitude == 0.0 {
		return pos
	}
	amount := Amplitude * sin(Frequency*front) * exp(-Decay*front)
	return pos - d*(amount/dist)
}
`)

// Ripple is the ripple of one pane: which kind, where and when it started.
type Ripple struct {
	kind         Kind
	cellX, cellY int
	// origin is the exact point in the pane the ripple started at, when the
	// caller knew it: a pointer's position is inside a cell, not its nominal
	// centre. hasOrigin is false for a start named only by a cell.
	origin     ui.Point
	hasOrigin  bool
	started    time.Time
	running    bool
	starts     int
	lastStartX int
	lastStartY int
	// dropCellX and dropCellY are where the pointer last was in cells, and
	// dropOrigin is where it was in the pane: where a drop lands.
	dropCellX, dropCellY int
	dropOrigin           ui.Point
	hasDropOrigin        bool
	hasPointer           bool
	effects              int
}

// Start begins a ripple of a kind at a cell, at now. Its origin is the cell's
// nominal centre, because the caller named no point.
func (r *Ripple) Start(kind Kind, cellX, cellY int, now time.Time) {
	r.kind = kind
	r.cellX, r.cellY = cellX, cellY
	r.origin, r.hasOrigin = ui.Point{}, false
	r.started = now
	r.running = true
	r.starts++
	r.lastStartX, r.lastStartY = cellX, cellY
}

// StartAt begins a ripple at a cell and the exact point in the pane it started
// at, at now: a pointer's own position is inside the cell rather than its
// nominal centre, which matters when rows are laid out by the text engine.
func (r *Ripple) StartAt(kind Kind, cellX, cellY int, at ui.Point, now time.Time) {
	r.Start(kind, cellX, cellY, now)
	r.origin, r.hasOrigin = at, true
}

// Origin returns the point the ripple began at, and whether the caller gave one
// rather than only a cell.
func (r *Ripple) Origin() (ui.Point, bool) { return r.origin, r.hasOrigin }

// LastStart is the cell the newest ripple began at, for a diagnostic.
func (r *Ripple) LastStart() (int, int) { return r.lastStartX, r.lastStartY }

// StartDrop begins a drop ripple where the pointer last was, and reports whether it
// started (there is nowhere without a pointer).
func (r *Ripple) StartDrop(now time.Time) bool {
	if !r.hasPointer {
		return false
	}
	if r.hasDropOrigin {
		r.StartAt(Drop, r.dropCellX, r.dropCellY, r.dropOrigin, now)
	} else {
		r.Start(Drop, r.dropCellX, r.dropCellY, now)
	}
	return true
}

// Pointer records the cell the pointer is over, which is where a drop lands.
func (r *Ripple) Pointer(cellX, cellY int) {
	r.hasPointer = true
	r.dropCellX, r.dropCellY = cellX, cellY
	r.dropOrigin, r.hasDropOrigin = ui.Point{}, false
}

// PointerAt records the cell and the exact point in the pane the pointer is
// over, which is where a drop lands.
func (r *Ripple) PointerAt(cellX, cellY int, at ui.Point) {
	r.Pointer(cellX, cellY)
	r.dropOrigin, r.hasDropOrigin = at, true
}

// HasPointer reports whether a pointer position is known.
func (r *Ripple) HasPointer() bool { return r.hasPointer }

// Kind is the ripple's kind.
func (r *Ripple) Kind() Kind { return r.kind }

// Running reports whether a ripple is still drawing.
func (r *Ripple) Running(now time.Time) bool {
	if !r.running {
		return false
	}
	if now.Sub(r.started) >= r.Spec().Duration {
		r.running = false
		return false
	}
	return true
}

// Age is how long ago the ripple started.
func (r *Ripple) Age(now time.Time) time.Duration { return now.Sub(r.started) }

// Starts is how many times a ripple has begun, for a check that an event reaches
// the screen.
func (r *Ripple) Starts() int { return r.starts }

// Cell returns the cell the ripple started from.
func (r *Ripple) Cell() (int, int) { return r.cellX, r.cellY }

// Spec is the numbers in force.
func (r *Ripple) Spec() Spec {
	if r.kind == Drop {
		return DropSpec
	}
	return BellSpec
}

// Amplitude is the displacement in DIPs at age: held for the first share of the
// duration, then ramped to zero, which is what makes it fade rather than stop.
func (r *Ripple) Amplitude(age time.Duration) float32 {
	s := r.Spec()
	a := float64(age) / float64(s.Duration)
	switch {
	case a >= 1:
		return 0
	case a <= 0, a < s.Hold:
		return s.Amplitude
	}
	return s.Amplitude * float32((1-a)/(1-s.Hold))
}

// Flash is how much of the bell's ring shows at age, from 1 to 0 over
// FlashDuration. A drop has no ring, as Terminal's has not.
func (r *Ripple) Flash(age time.Duration) float32 {
	if r.kind != Bell {
		return 0
	}
	f := 1 - float32(age)/float32(FlashDuration)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// Effects is how many frames were drawn with the distortion, for a check that the
// effect is in force (and, with reduced motion, that it is not).
func (r *Ripple) Effects() int { return r.effects }

// Effect is the distortion to draw on the pane, whose origin is the started cell in
// DIPs from the element's corner, at now.
func (r *Ripple) Effect(now time.Time, cell ui.Point) ui.Effect {
	r.effects++
	age := r.Age(now)
	s := r.Spec()
	return distortion.Effect(shader.Options{
		Uniforms: map[string]any{
			"Time":      age.Seconds(),
			"Origin":    []float32{cell.X, cell.Y},
			"Amplitude": r.Amplitude(age),
			"Frequency": s.Frequency,
			"Decay":     s.Decay,
			"Speed":     s.Speed,
		},
		// The wave pushes pixels up to the amplitude away, and the ramp near the
		// origin can read a little further out.
		MaxSampleOffset: ui.Point{X: s.Amplitude + 2, Y: s.Amplitude + 2},
	})
}
