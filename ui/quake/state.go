// Package quake owns the Dropdown Panel's own Workspace: a flat list of
// terminal Tabs, one Session and Surface each, with no split panes and no
// persistence. It is window-scoped state, as ADR 0006 requires, and it is
// deliberately memory-only: hiding and showing the Panel or switching modes
// keeps the live Sessions, but an app restart starts from a fresh local Shell.
package quake

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ErrNoSessions reports a Quake workspace without a Session opener or surface
// factory, so it cannot open a Tab.
var ErrNoSessions = errors.New("quake: no session opener")

// Surface is the terminal surface a Quake Tab shows. It has the same shape as
// the shell's Surface, so the app reuses its one Session surface factory.
type Surface interface {
	View(c *ui.Context) ui.Element
	Close()
}

// FocusedSurface is implemented by a Surface whose terminal takes the
// keyboard only when its Tab is the active one. The Quake view renders the
// active Tab through it every frame; MyGo prunes an unmounted subtree's
// element state, so focus has to be requested again after a switch.
type FocusedSurface interface {
	ViewFocused(c *ui.Context, focused bool) ui.Element
}

// TerminalFontSetter is implemented by a Surface whose terminal restyles
// without being recreated.
type TerminalFontSetter interface {
	SetTerminalFont(family string, size float32)
}

// TerminalCursorSetter is implemented by a Surface whose terminal takes a new
// cursor shape and blink preference without being recreated.
type TerminalCursorSetter interface {
	SetTerminalCursor(style domainpreferences.CursorStyle, blink bool)
}

// TerminalThemeSetter is implemented by a Surface whose terminal recolors
// without being recreated.
type TerminalThemeSetter interface {
	SetTerminalTheme(preset domaintheme.TerminalTheme)
}

// SessionOpener opens the Session a Quake Tab shows. The app implements it
// over the shared Session service, so a Quake Tab has the same transport,
// logging, Command History, and notification path as a main-window Tab;
// tests substitute a fake.
type SessionOpener interface {
	OpenLocal() (*appsession.Session, error)
	OpenHost(domainvault.Host) (*appsession.Session, error)
}

// HostSource lists the saved Hosts a Host picker offers. The app backs it
// with the Vault; tests provide a fake, so the view never reads storage.
type HostSource interface {
	Hosts() []domainvault.Host
}

// SurfaceFactory builds the terminal surface for a Session. The app wires the
// same factory the main shell uses; tests wire a fake.
type SurfaceFactory func(*appsession.Session) (Surface, error)

// Tab is one Quake Tab's live state. Label carries a Host label for a remote
// Tab; a local Shell leaves it empty and the view names it by Number. HostID
// is the saved Host a remote Tab opened, or empty for a local Shell.
type Tab struct {
	Number  int
	Label   string
	HostID  model.HostID
	Session *appsession.Session
	Surface Surface
}

// TabInfo is the immutable description the tab strip renders.
type TabInfo struct {
	Index  int
	Number int
	Label  string
	HostID model.HostID
	Active bool
}

// State is one Dropdown Panel's Quake workspace. It is mutated on the UI
// thread only.
type State struct {
	opener   SessionOpener
	surfaces SurfaceFactory
	redraw   func()
	// empty is called after the last Tab closes, so the app can hide the
	// Panel. The next Activate creates a new local Shell.
	empty func()
	// hosts backs the Host picker. A nil source leaves the picker with the
	// local Shell entry and an empty Host list.
	hosts HostSource
	tabs  []Tab
	// active is the index of the active Tab, or -1 when there is none.
	active int
	// nextNumber names local Tabs the way the main shell does, starting at 1.
	nextNumber int
	// pickerOpen is the Host picker's visible state and pickerQuery its
	// filter. Both are transient and never persist.
	pickerOpen  bool
	pickerQuery string
	// problem is the last failed open, for the view to explain.
	problem error
}

// Option customizes a State.
type Option func(*State)

// WithRedraw sets the function called when the workspace changes.
func WithRedraw(fn func()) Option { return func(s *State) { s.redraw = fn } }

// WithEmpty sets the function called when the last Tab closes. The app wires
// it to the Dropdown Panel's hide action.
func WithEmpty(fn func()) Option { return func(s *State) { s.empty = fn } }

// WithHosts installs the saved Hosts the Host picker lists. It is read live,
// so a Vault edit shows on the next frame without rebuilding the Panel.
func WithHosts(source HostSource) Option { return func(s *State) { s.hosts = source } }

// NewState returns an empty Quake workspace. The first Activate opens the
// local Shell; a new State never restores a previous run's Tabs.
func NewState(opener SessionOpener, surfaces SurfaceFactory, opts ...Option) *State {
	state := &State{opener: opener, surfaces: surfaces, active: -1, nextNumber: 1}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Activate ensures the workspace has at least one Tab, opening the first
// local Shell when it is empty. The Panel's show path calls it; because it is
// idempotent, hiding and showing the Panel keeps the same Session and Surface.
func (s *State) Activate() error {
	// A show starts with no transient flyout open. A picker left open when the
	// Panel was hidden must not reappear over a freshly shown workspace.
	s.ClosePicker()
	if len(s.tabs) > 0 {
		return nil
	}
	return s.AddLocalTab()
}

// AddLocalTab opens another local Shell Tab and makes it active.
func (s *State) AddLocalTab() error {
	return s.openTab("", "", func() (*appsession.Session, error) {
		if s.opener == nil {
			return nil, ErrNoSessions
		}
		return s.opener.OpenLocal()
	})
}

// AddHostTab opens a Host in a new Quake Tab and makes it active. The Tab
// carries the Host's label, so the strip marks which Host it connects to. The
// Session is opened through the same SessionOpener port as the local Shell.
func (s *State) AddHostTab(host domainvault.Host) error {
	return s.openTab(hostTabLabel(host), host.ID, func() (*appsession.Session, error) {
		if s.opener == nil {
			return nil, ErrNoSessions
		}
		return s.opener.OpenHost(host)
	})
}

// openTab is the one Tab-opening path: it resolves the Session through open,
// builds its surface, appends the Tab, and makes it active. A failed open
// records the problem for the view and leaves the Tab list unchanged.
func (s *State) openTab(label string, hostID model.HostID, open func() (*appsession.Session, error)) error {
	if s.surfaces == nil {
		return ErrNoSessions
	}
	// Choosing an entry dismisses the picker even when the open fails, so the
	// error is visible in the body instead of behind the flyout.
	s.ClosePicker()
	s.problem = nil
	session, err := open()
	if err != nil {
		s.problem = err
		s.requestRedraw()
		return err
	}
	surface, err := s.surfaces(session)
	if err != nil {
		_ = session.Close()
		s.problem = err
		s.requestRedraw()
		return err
	}
	s.tabs = append(s.tabs, Tab{
		Number:  s.nextNumber,
		Label:   label,
		HostID:  hostID,
		Session: session,
		Surface: surface,
	})
	s.nextNumber++
	s.active = len(s.tabs) - 1
	s.requestRedraw()
	return nil
}

// hostTabLabel names a remote Tab. A Host without a label is shown by its
// address; a malformed Host with neither falls back to the empty label, and
// the view then names the Tab by its number like a local Shell.
func hostTabLabel(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	return strings.TrimSpace(host.Hostname)
}

// Hosts returns the saved Hosts the picker lists, in Vault order. It is empty
// when no Host source is wired.
func (s *State) Hosts() []domainvault.Host {
	if s.hosts == nil {
		return nil
	}
	return s.hosts.Hosts()
}

// PickerOpen reports whether the Host picker is showing.
func (s *State) PickerOpen() bool { return s.pickerOpen }

// Query returns the Host picker's current filter text.
func (s *State) Query() string { return s.pickerQuery }

// OpenPicker shows the Host picker with an empty filter.
func (s *State) OpenPicker() {
	if s.pickerOpen && s.pickerQuery == "" {
		return
	}
	s.pickerOpen = true
	s.pickerQuery = ""
	s.requestRedraw()
}

// ClosePicker hides the Host picker without touching the Tabs. It is a no-op
// when the picker is already closed.
func (s *State) ClosePicker() {
	if !s.pickerOpen && s.pickerQuery == "" {
		return
	}
	s.pickerOpen = false
	s.pickerQuery = ""
	s.requestRedraw()
}

// TogglePicker shows or hides the Host picker.
func (s *State) TogglePicker() {
	if s.pickerOpen {
		s.ClosePicker()
		return
	}
	s.OpenPicker()
}

// DismissOverlay handles a close request aimed at the Panel before the Panel
// itself hides. It closes the Host picker and reports true when it consumed
// the request, so the first Escape dismisses the picker and the next one
// hides the Panel.
func (s *State) DismissOverlay() bool {
	if !s.pickerOpen {
		return false
	}
	s.ClosePicker()
	return true
}

// QueryPtr is the Host picker's filter text for the view's search field.
func (s *State) QueryPtr() *string { return &s.pickerQuery }

// Tabs returns the current Tab descriptions in workspace order.
func (s *State) Tabs() []TabInfo {
	tabs := make([]TabInfo, len(s.tabs))
	for index, tab := range s.tabs {
		tabs[index] = TabInfo{
			Index:  index,
			Number: tab.Number,
			Label:  tab.Label,
			HostID: tab.HostID,
			Active: index == s.active,
		}
	}
	return tabs
}

// TabCount reports how many Tabs the workspace holds.
func (s *State) TabCount() int { return len(s.tabs) }

// ActiveIndex returns the active Tab's index, or -1 when the workspace is
// empty.
func (s *State) ActiveIndex() int { return s.active }

// Active returns the active Tab's live Session and Surface.
func (s *State) Active() (Tab, bool) {
	if s.active < 0 || s.active >= len(s.tabs) {
		return Tab{}, false
	}
	return s.tabs[s.active], true
}

// SelectTab makes the Tab at index active. An out-of-range index is a no-op.
func (s *State) SelectTab(index int) {
	if index < 0 || index >= len(s.tabs) || index == s.active {
		return
	}
	s.active = index
	s.requestRedraw()
}

// CloseTab closes the Tab at index, ends its Surface and Session, and keeps
// the active index valid. Closing the last Tab calls the empty callback so the
// app hides the Panel. It reports whether a Tab was closed.
func (s *State) CloseTab(index int) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	tab := s.tabs[index]
	if tab.Surface != nil {
		tab.Surface.Close()
	}
	if tab.Session != nil {
		_ = tab.Session.Close()
	}
	s.tabs = append(s.tabs[:index], s.tabs[index+1:]...)
	switch {
	case len(s.tabs) == 0:
		s.active = -1
	case index < s.active:
		s.active--
	case index == s.active && s.active >= len(s.tabs):
		// The active Tab was the last one; the new last Tab takes over.
		s.active = len(s.tabs) - 1
	}
	s.requestRedraw()
	if len(s.tabs) == 0 && s.empty != nil {
		s.empty()
	}
	return true
}

// Problem returns the last failed open, or nil after a successful one.
func (s *State) Problem() error { return s.problem }

// Shutdown closes every Tab's Surface and Session without hiding the Panel.
// The app calls it on quit, after the Panel has been hidden or destroyed.
func (s *State) Shutdown() {
	for _, tab := range s.tabs {
		if tab.Surface != nil {
			tab.Surface.Close()
		}
		if tab.Session != nil {
			_ = tab.Session.Close()
		}
	}
	s.tabs = nil
	s.active = -1
	s.pickerOpen = false
	s.pickerQuery = ""
}

// SetTerminalFont applies a live font change to every open Quake terminal.
func (s *State) SetTerminalFont(family string, size float32) {
	s.eachSurface(func(surface Surface) {
		if setter, ok := surface.(TerminalFontSetter); ok {
			setter.SetTerminalFont(family, size)
		}
	})
}

// SetTerminalCursor applies a live cursor change to every open Quake terminal.
func (s *State) SetTerminalCursor(style domainpreferences.CursorStyle, blink bool) {
	s.eachSurface(func(surface Surface) {
		if setter, ok := surface.(TerminalCursorSetter); ok {
			setter.SetTerminalCursor(style, blink)
		}
	})
}

// SetTerminalTheme applies a live terminal scheme change to every open Quake
// terminal, so the Panel follows the current theme without a restart.
func (s *State) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	s.eachSurface(func(surface Surface) {
		if setter, ok := surface.(TerminalThemeSetter); ok {
			setter.SetTerminalTheme(preset)
		}
	})
}

func (s *State) eachSurface(fn func(Surface)) {
	for _, tab := range s.tabs {
		if tab.Surface != nil {
			fn(tab.Surface)
		}
	}
}

// requestRedraw asks the app for the next frame. It is a no-op without a
// redraw request.
func (s *State) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}
