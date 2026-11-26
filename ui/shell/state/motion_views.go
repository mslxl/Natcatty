package state

import "github.com/mslxl/natcatty/domain/model"

// MotionView names one renderer that owns a motion runtime over the shell
// state. The main window and the Dropdown Panel's routes share one State, but
// each mounted renderer builds its own Pane, body and strip runtimes, so a
// frame one renderer did not build is invisible only to that renderer. The app
// records the gap with NoteMotionViewUnrendered when its route bypasses the
// view; the view consumes it with TakeMotionViewUnrendered on its next frame
// and warps every runtime it owns, so an event the user never saw is not
// replayed. Wall-clock event age cannot tell a bypassed view from a late one,
// so the notice is explicit (ticket 08).
type MotionView uint8

const (
	// MotionViewMain is the main window's full shell renderer.
	MotionViewMain MotionView = iota
	// MotionViewFollow is the Dropdown Panel's Follow route.
	MotionViewFollow
	// MotionViewTopdown is the Dropdown Panel's top-down route.
	MotionViewTopdown
	// MotionViewFollowBar is the Dropdown Panel's mini Tab strip.
	MotionViewFollowBar
	// motionViewCount bounds the per-view records; a fixed array keeps the
	// notice allocation-free on every frame.
	motionViewCount
)

// NoteMotionViewUnrendered records that view built no frame since its last
// one, so its runtimes must not replay any event recorded in the gap. The
// notice is per view: the same state drives several renderers, and the one
// that did render saw its events. A nil State ignores the notice, so a caller
// does not have to guard a state another route may not have created. It is
// safe to call more than once; one notice is enough to make the next frame
// warp.
func (s *State) NoteMotionViewUnrendered(view MotionView) {
	if s == nil || view >= motionViewCount {
		return
	}
	s.motionUnrendered[view] = true
}

// TakeMotionViewUnrendered consumes the notice that view built no frame since
// its last one. The renderer calls it at the start of a frame, before its
// runtimes read the event stream, and marks the runtimes skipped so the frame
// warps to the final layout and requests no animation frame. It reports false
// for a view that did render its last frame; the notice is one-shot, so a
// later event still animates normally.
func (s *State) TakeMotionViewUnrendered(view MotionView) bool {
	if s == nil || view >= motionViewCount {
		return false
	}
	unrendered := s.motionUnrendered[view]
	s.motionUnrendered[view] = false
	return unrendered
}

// NoteDetachedViewUnrendered records that the detached window showing tabID
// built no frame since its last one, so its Pane runtime must warp the events
// recorded in the gap instead of replaying them. A detached window owns one
// runtime for its fixed Tab, so the notice is keyed by that Tab: MotionView
// cannot distinguish two detached windows. The composition root calls it when
// the App Lock gate replaces the window's content with the lock screen; the
// view consumes it with TakeDetachedViewUnrendered at the start of its next
// frame. A nil State ignores the notice, so a caller does not have to guard a
// state the window may not have. Calling it more than once is safe; one
// notice is enough to make the next frame warp.
func (s *State) NoteDetachedViewUnrendered(tabID model.TabID) {
	if s == nil {
		return
	}
	if s.detachedMotionUnrendered == nil {
		s.detachedMotionUnrendered = make(map[model.TabID]bool)
	}
	s.detachedMotionUnrendered[tabID] = true
}

// TakeDetachedViewUnrendered consumes the notice that the detached window
// showing tabID built no frame since its last one. The window's renderer calls
// it at the start of a frame, before its Pane runtime reads the event stream,
// and marks the runtime skipped so the frame warps to the final layout and
// requests no animation frame. It reports false for a window that did render
// its last frame; the notice is one-shot, so a later event still animates
// normally. The entry is deleted on take, so idle windows cost nothing.
func (s *State) TakeDetachedViewUnrendered(tabID model.TabID) bool {
	if s == nil {
		return false
	}
	unrendered := s.detachedMotionUnrendered[tabID]
	delete(s.detachedMotionUnrendered, tabID)
	return unrendered
}
