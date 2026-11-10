// Package pluginwebview renders the host's sandboxed plugin pages. It holds
// no security policy: the application layer owns the per-plugin origin, the
// CSP, and the verified bridge. This package only places a native page widget
// in the native UI, so ui.WebView is called from the one package ADR-0007
// allows besides the host browser panel.
package pluginwebview

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// Native returns the native page widget the app created for this surface, or
// nil while it is not ready.
type Native func() ui.NativeWebView

// Option customizes a Surface.
type Option func(*Surface)

// WithPoll drives the guest poll export for a standalone webview View. The
// page's bridge requests are queued as native UI events; the guest learns of
// them only when it polls, exactly as a native View does. The callback runs at
// the start of a frame and must not call the guest when nothing is queued.
func WithPoll(fn func()) Option {
	return func(s *Surface) { s.poll = fn }
}

// Problem is the host-side reason a page is not shown. The renderer resolves
// it to the host locale's display string; no caller supplies user text.
type Problem uint8

const (
	// ProblemNone shows the native page widget.
	ProblemNone Problem = iota
	// ProblemUnavailable replaces a page that could not open.
	ProblemUnavailable
	// ProblemCrashed replaces a page whose renderer went away.
	ProblemCrashed
)

// Surface renders one standalone or embedded plugin webview. A page that
// crashed or is unavailable shows a problem line instead of an empty hole.
type Surface struct {
	native  Native
	problem Problem
	poll    func()
}

// New returns a surface over the app's native page.
func New(native Native, optionList ...Option) *Surface {
	surface := &Surface{native: native}
	for _, option := range optionList {
		option(surface)
	}
	return surface
}

// SetProblem replaces the page with a readable error surface. The host sets
// it when the page crashes; Reload clears it once the page works again.
func (s *Surface) SetProblem(problem Problem) {
	if s == nil {
		return
	}
	s.problem = problem
}

// Problem returns the current error surface, ProblemNone for a live page.
func (s *Surface) Problem() Problem {
	if s == nil {
		return ProblemNone
	}
	return s.problem
}

// View draws the native page or its error surface. The layout around the node
// decides its size; the web view stretches across the content box.
func (s *Surface) View(c *ui.Context) ui.Element {
	if s == nil {
		return ui.Element{}
	}
	if s.poll != nil {
		s.poll()
	}
	messages := locales.In(c.Locale())
	if s.problem != ProblemNone {
		switch s.problem {
		case ProblemCrashed:
			return ui.Text(c, messages.PluginWebviewCrashed())
		default:
			return ui.Text(c, messages.PluginWebviewUnavailable())
		}
	}
	if s.native == nil {
		return ui.Text(c, messages.PluginWebviewUnavailable())
	}
	native := s.native()
	if native == nil {
		return ui.Text(c, messages.PluginWebviewUnavailable())
	}
	return ui.WebView(c, native).Grow(1)
}

// Close detaches the renderer. The app owns the native page and destroys it
// through the application webview session.
func (s *Surface) Close() {}
