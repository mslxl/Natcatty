package appview

import (
	"github.com/egoist/mygo/ui"
)

// Follow keeps the live screen pinned to the end of the flow while the reader is
// there, and leaves them alone once they scroll up: a terminal follows new output
// only when it is showing the bottom.
//
// Following is the default, and the only thing that stops it is the reader
// scrolling up, which Live reports with Detach when a wheel goes towards the
// start; scrolling back to the end follows again. Pinning writes the end the
// container last laid out (MaxY) rather than a "so far down it must be the end"
// value: with the live screen the layout clamps a far value to the top before the
// content is measured, so a flow pinned that way sat at Y=0 and the wheel had
// nowhere to go.
type Follow struct {
	pinned   bool
	detached bool // the reader scrolled up: keep their place
	seen     bool // the container has laid the content out once
	maxY     float32
}

// Detach stops following, as a wheel towards the start does: the flow keeps the
// place the reader scrolled to.
func (f *Follow) Detach() { f.detached = true }

// Update decides for this frame and pins the scroll when it follows.
func (f *Follow) Update(sc *ui.ScrollState) bool {
	if sc == nil {
		return true
	}
	// At the end again, the reader is following once more.
	atEnd := sc.MaxY > 0 && sc.Y >= sc.MaxY-1
	if atEnd {
		f.detached = false
	}
	grew := sc.MaxY > f.maxY+0.5
	switch {
	case sc.MaxY <= 0, !f.seen:
		// Until the container has laid the content out (first frames) there is no
		// end to be away from, so keep following.
		f.pinned = true
	case f.detached:
		f.pinned = false
	case f.pinned && !grew && sc.Y < sc.MaxY-2:
		// The content did not grow, yet the offset is away from the end this flow
		// pinned: the reader scrolled, as dragging the scroll bar does, whose
		// events Live never sees.
		f.pinned, f.detached = false, true
	default:
		f.pinned = true
	}
	if sc.MaxY > 0 {
		f.seen = true
	}
	f.maxY = sc.MaxY
	if f.pinned && sc.MaxY > 0 {
		sc.Y = sc.MaxY // the end this frame's content was laid out to last time
	}
	return f.pinned
}

// Pinned reports whether the flow is following its end.
func (f *Follow) Pinned() bool { return f.pinned }
