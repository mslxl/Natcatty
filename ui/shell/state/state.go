// Package state owns the main window's shell state: the ordered work tabs and
// the requests the shell makes of the app. Window-scoped state lives in
// ui/<area>/state, as ADR 0006 requires, apart from the view that renders it.
package state

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

// ErrNoSessions reports a shell that was not given a Session service, so it
// cannot open a Tab's Session.
var ErrNoSessions = errors.New("shell: no session service")

// Surface is the terminal surface a Tab shows. The UI owns it and the
// application owns the transport beneath it; the shell only renders and closes
// it.
type Surface interface {
	View(c *ui.Context) ui.Element
	Close()
}

// SurfaceFactory builds the terminal surface for a Session. The app wires the
// real MyGo terminal; tests wire a fake.
type SurfaceFactory func(*appsession.Session) (Surface, error)

// Tab is one work tab in the shell. Its number is stable for its lifetime and
// is what the title shows. A Tab hosts one Session and its terminal surface.
type Tab struct {
	Number  int
	session *appsession.Session
	surface Surface
}

// State is the main window's shell state. It is mutated only on the UI thread.
type State struct {
	tabs            []Tab
	active          int
	nextTabNumber   int
	vaultVisible    bool
	vaultView       func(*ui.Context)
	requestLocale   func(string)
	requestSettings func()
	requestRedraw   func()
	sessions        *appsession.Service
	surfaces        SurfaceFactory
}

// Option customizes a State.
type Option func(*State)

// WithLocaleRequest sets the function called when the shell asks the app to
// switch languages. The app wires this to mygo.App.SetLocale.
func WithLocaleRequest(fn func(string)) Option {
	return func(s *State) { s.requestLocale = fn }
}

// WithSettingsRequest sets the function called when the user opens Settings.
// The app wires this to the singleton Settings window.
func WithSettingsRequest(fn func()) Option {
	return func(s *State) { s.requestSettings = fn }
}

// WithRedrawRequest sets the function called when a Tab opens or closes. The
// app wires it to the main window's Invalidate.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.requestRedraw = fn }
}

// WithSessions gives the shell the Session service that opens local shells and
// the factory that builds their terminal surfaces.
func WithSessions(service *appsession.Service, surfaces SurfaceFactory) Option {
	return func(s *State) {
		s.sessions = service
		s.surfaces = surfaces
	}
}

// WithVault gives the shell the Vault surface. The app wires the real Vault
// view; tests wire a fake.
func WithVault(view func(*ui.Context)) Option {
	return func(s *State) { s.vaultView = view }
}

// NewState returns the initial shell state: the Vault is visible and there
// are no work tabs.
func NewState(opts ...Option) *State {
	s := &State{active: -1, nextTabNumber: 1, vaultVisible: true}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// VaultVisible reports whether the main window shows the Vault rather than a
// work Tab.
func (s *State) VaultVisible() bool { return s.vaultVisible }

// VaultView returns the Vault surface the app supplied, or nil.
func (s *State) VaultView() func(*ui.Context) { return s.vaultView }

// ShowVault makes the Vault the visible surface.
func (s *State) ShowVault() {
	s.vaultVisible = true
	s.RequestRedraw()
}

// TabCount returns the number of work tabs.
func (s *State) TabCount() int { return len(s.tabs) }

// ActiveTab returns the index of the active work tab, or -1 when there is
// none.
func (s *State) ActiveTab() int { return s.active }

// TabNumbers returns the stable numbers of the work tabs, in order.
func (s *State) TabNumbers() []int {
	numbers := make([]int, len(s.tabs))
	for i, tab := range s.tabs {
		numbers[i] = tab.Number
	}
	return numbers
}

// SelectTab makes the tab at index active and shows it. Out-of-range indexes
// are ignored.
func (s *State) SelectTab(index int) {
	if index >= 0 && index < len(s.tabs) {
		s.active = index
		s.vaultVisible = false
	}
}

// NewTab opens a local shell Session, builds its terminal surface, appends it
// as a work tab, and makes it active.
func (s *State) NewTab() error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	session, err := s.sessions.OpenLocal(context.Background())
	if err != nil {
		return err
	}
	surface, err := s.surfaces(session)
	if err != nil {
		_ = session.Close()
		return err
	}

	s.tabs = append(s.tabs, Tab{
		Number:  s.nextTabNumber,
		session: session,
		surface: surface,
	})
	s.nextTabNumber++
	s.active = len(s.tabs) - 1
	s.vaultVisible = false
	s.RequestRedraw()
	return nil
}

// ActiveSurface returns the terminal surface of the active tab, or nil when
// there is no tab.
func (s *State) ActiveSurface() Surface {
	if s.active < 0 || s.active >= len(s.tabs) {
		return nil
	}
	return s.tabs[s.active].surface
}

// ActiveSnapshot returns the Session snapshot of the active tab. It reports
// false when there is no tab.
func (s *State) ActiveSnapshot() (appsession.Snapshot, bool) {
	if s.active < 0 || s.active >= len(s.tabs) {
		return appsession.Snapshot{}, false
	}
	return s.tabs[s.active].session.Snapshot(), true
}

// CloseActiveTab closes the active work tab's terminal surface and Session,
// then removes the tab and selects the preceding tab, or the new first tab.
// It reports whether a tab was closed.
func (s *State) CloseActiveTab() bool {
	if s.active < 0 || s.active >= len(s.tabs) {
		return false
	}
	tab := s.tabs[s.active]
	if tab.surface != nil {
		tab.surface.Close()
	}
	if tab.session != nil {
		_ = tab.session.Close()
	}

	s.tabs = append(s.tabs[:s.active], s.tabs[s.active+1:]...)
	if len(s.tabs) == 0 {
		s.active = -1
		s.vaultVisible = true
		s.RequestRedraw()
		return true
	}
	if s.active >= len(s.tabs) {
		s.active = len(s.tabs) - 1
	}
	s.vaultVisible = false
	s.RequestRedraw()
	return true
}

// RequestRedraw asks the app to redraw the shell.
func (s *State) RequestRedraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}

// RequestLocale asks the app to show the language of tag.
func (s *State) RequestLocale(tag string) {
	if s.requestLocale != nil {
		s.requestLocale(tag)
	}
}

// RequestSettings asks the app to show the singleton Settings window.
func (s *State) RequestSettings() {
	if s.requestSettings != nil {
		s.requestSettings()
	}
}
