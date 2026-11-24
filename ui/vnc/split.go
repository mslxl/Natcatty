package vnc

import (
	"errors"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
)

// A Tab may only split a VNC Pane when the remote can give each Pane its own
// screen. Two Panes showing the same framebuffer would be the same desktop
// twice, so the split is refused instead. The server may need to be asked for
// the extra screen first (TigerVNC's Xvnc creates one RandR output per screen
// on request); a server that ignores the screen list (QEMU) is detected by the
// reply and latches multiScreenRefused.

// CanSplit reports whether this Pane may split into another Pane. It is
// optimistic until the server has answered a multi-screen request: support is
// learned by asking, and a refusal latches the answer for the rest of the
// Session.
func (s *Surface) CanSplit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.multiScreenRefused && !s.resizeUnsupported && !s.hibernated
}

// ScreenCount is the number of screens the server currently reports. It is 1
// before the first layout arrives, which is the single-screen desktop the
// server starts with.
func (s *Surface) ScreenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(len(s.screens), 1)
}

// PrepareSplit asks the server to provide at least count screens so a new Pane
// can show one. It is a no-op when the layout already has that many. A refusal
// that means the server cannot do multi-screen at all latches CanSplit false;
// a transient failure such as an outstanding resize does not.
func (s *Surface) PrepareSplit(count int) error {
	if count < 1 {
		return nil
	}
	if s.ScreenCount() >= count {
		return nil
	}
	err := s.session.SetScreenCount(count)
	s.mu.Lock()
	switch {
	case err == nil:
		s.multiScreenRefused = false
	case errors.Is(err, appsession.ErrMultiScreenUnsupported):
		s.multiScreenRefused = true
	case errors.Is(err, appsession.ErrDesktopResizeUnsupported):
		// Without ExtendedDesktopSize there is no screen list at all. The
		// local-scaling fallback already explains this state.
		s.resizeUnsupported = true
	}
	s.mu.Unlock()
	s.requestRedraw()
	return err
}

// SplitNotice is the user-facing explanation for a disabled Split action, or ""
// when splitting is available. The Pane chrome shows it once the server has
// refused a multi-screen layout.
func (s *Surface) SplitNotice() string {
	s.mu.Lock()
	refused := s.multiScreenRefused
	s.mu.Unlock()
	if !refused {
		return ""
	}
	return locales.SessionVNCMultiScreenUnavailable()
}
