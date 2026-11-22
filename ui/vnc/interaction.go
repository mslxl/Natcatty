package vnc

import (
	"github.com/egoist/mygo/ui"

	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/ui/kit"
)

// ScaleMode returns the viewer's current scaling behavior.
func (s *Surface) ScaleMode() modelvnc.FitMode { return s.scaleMode }

// SetScaleMode selects a scaling behavior and recenters the clip viewport.
// Fit re-letterboxes the desktop, 1:1 centers it without scaling, and clip
// keeps 1:1 pixels with a draggable viewport.
func (s *Surface) SetScaleMode(mode modelvnc.FitMode) {
	s.scaleMode = modelvnc.NormalizeFitMode(mode)
	s.panX, s.panY = 0, 0
	s.panPending = false
	s.panning = false
	s.requestRedraw()
}

// CycleScale advances fit -> 1:1 -> clip -> fit and reports the new mode.
func (s *Surface) CycleScale() modelvnc.FitMode {
	switch s.scaleMode {
	case modelvnc.OneToOne:
		s.SetScaleMode(modelvnc.Clip)
	case modelvnc.Clip:
		s.SetScaleMode(modelvnc.Fit)
	default:
		s.SetScaleMode(modelvnc.OneToOne)
	}
	return s.scaleMode
}

// CursorMode returns whether the viewer draws the local cursor or lets the
// server paint it.
func (s *Surface) CursorMode() modelvnc.CursorMode { return s.cursorMode }

// ViewOnly reports whether keyboard and pointer writes are suppressed.
func (s *Surface) ViewOnly() bool { return s.viewOnly }

// SetViewOnly enters or leaves read-only mode. Entering it releases every key,
// synthesized modifier, and pointer button the guest still believes down, so a
// held chord cannot stay stuck after the toolbar is clicked.
func (s *Surface) SetViewOnly(viewOnly bool) {
	if s.viewOnly == viewOnly {
		return
	}
	if viewOnly {
		s.releaseHeldKeys()
		// A clip press that had not resolved yet must not replay as a click
		// after the mode changes; the next gesture starts fresh.
		s.panPending = false
		s.panning = false
	} else {
		// The refusal notices only explain the mode they belong to. A
		// clipboard refusal is stale as soon as sending is allowed again;
		// other clipboard outcomes describe actions that still stand and are
		// left alone.
		s.viewOnlyNotice = ""
		s.mu.Lock()
		if s.clipboardResult != nil && *s.clipboardResult == ClipboardViewOnly {
			s.clipboardResult = nil
		}
		s.mu.Unlock()
	}
	s.viewOnly = viewOnly
	s.requestRedraw()
}

// ViewerChrome reports that this Surface's Toolbar is the VNC viewer's
// per-kind chrome. The shell renders it even in a window that does not manage
// Pane commands (a detached Tab and the Quake panel); terminal Surfaces keep
// their existing toolbar placement.
func (s *Surface) ViewerChrome() bool { return true }

// KeyboardGrab reports whether the app's accelerators stand down so chords
// reach the guest.
func (s *Surface) KeyboardGrab() bool { return s.keyboardGrab }

// SetKeyboardGrab enables or disables keyboard grab. The app reads this
// through the shell when it decides whether to register window shortcuts.
func (s *Surface) SetKeyboardGrab(grab bool) {
	s.keyboardGrab = grab
	s.requestRedraw()
}

// ToggleFullscreen asks the injected window callback to toggle fullscreen.
// The viewer never opens a window itself, so offscreen tests stay offscreen.
func (s *Surface) ToggleFullscreen() {
	if s.opts.Fullscreen != nil {
		s.opts.Fullscreen()
	}
}

// notices renders the local-scaling fallback and the latest clipboard outcome
// in the Pane's chrome row. Both are explicit user-visible results; keeping
// them out of the desktop means a notice never covers the pixels the user is
// interacting with or changes the pane size.
func (s *Surface) notices(c *ui.Context, t *ui.Theme) {
	if fallback := s.ResizeFallbackNotice(); fallback != "" {
		kit.Badge(c, t.Warning, fallback)
	}
	s.mu.Lock()
	result := s.clipboardResult
	viewOnlyNotice := s.viewOnlyNotice
	s.mu.Unlock()
	if viewOnlyNotice != "" {
		kit.Badge(c, t.Warning, viewOnlyNotice)
	}
	if result == nil {
		return
	}
	tone := t.Warning
	if !clipboardFailed(*result) {
		tone = t.Success
	}
	kit.Badge(c, tone, s.ClipboardMessage(*result))
}

// setViewOnlyNotice records the localized explanation for an action view-only
// refused. The chrome row shows it beside the clipboard outcome.
func (s *Surface) setViewOnlyNotice(message string) {
	s.mu.Lock()
	s.viewOnlyNotice = message
	s.mu.Unlock()
	s.requestRedraw()
}
