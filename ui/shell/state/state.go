// Package state owns the main window's shell state: the ordered Tabs, their
// recursive split trees, and the requests the shell makes of the app.
// Window-scoped state lives in ui/<area>/state, as ADR 0006 requires, apart
// from the view that renders it.
package state

import (
	"context"
	"errors"
	"sync"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// ErrNoSessions reports a shell that was not given a Session service, so it
// cannot open a Tab's Session.
var ErrNoSessions = errors.New("shell: no session service")

// ErrNoActiveTab reports a Pane action taken while no Tab is active.
var ErrNoActiveTab = errors.New("shell: no active Tab")

// ErrHostUnavailable reports a restored Pane whose Host is no longer in the
// Vault. The Pane stays in the layout as an error surface so the user can see
// which Session is missing; nothing is invented in its place.
var ErrHostUnavailable = errors.New("shell: restored Host is unavailable")

// Surface is the terminal surface a Pane shows. The UI owns it and the
// application owns the transport beneath it; the shell only renders and closes
// it.
type Surface interface {
	View(c *ui.Context) ui.Element
	Close()
}

// SurfaceFactory builds the terminal surface for a Session. The app wires the
// real MyGo terminal; tests wire a fake.
type SurfaceFactory func(*appsession.Session) (Surface, error)

// HostResolver looks a Host up for restore and for reopening a split. It
// reports false when the Host no longer exists.
type HostResolver func(context.Context, model.HostID) (domainvault.Host, bool)

// openOutcome is the result of an SSH connection started off the UI thread for
// a new Tab. Outcome fields are written by the dial goroutine and applied on
// the UI thread by Flush.
type openOutcome struct {
	session    *appsession.Session
	label      string
	descriptor domainworkspace.SessionDescriptor
	err        error
}

// paneOutcome is the result of opening the Session behind one Pane that
// already exists in a Tab: a split, a duplicate, or a restore.
type paneOutcome struct {
	tabID   model.TabID
	paneID  model.PaneID
	session *appsession.Session
	// restore asks the Pane to reproduce its stored working directory after
	// the Session connects. Restores, splits, and duplicates all carry the
	// source Pane's last known directory.
	restore bool
	err     error
}

// State is the main window's shell state. It is mutated only on the UI thread,
// except the lock-protected queues that dial and Session goroutines append to
// and the host-key prompt, which synchronizes itself.
type State struct {
	tabs            []*Tab
	active          int
	nextTabNumber   int
	vaultVisible    bool
	vaultView       func(*ui.Context)
	requestLocale   func(string)
	requestSettings func()
	requestRedraw   func()
	sessions        *appsession.Service
	surfaces        SurfaceFactory
	trustPrompt     *sessionui.TrustPrompt
	authPrompt      *sessionui.AuthPrompt

	// workspace persists layout changes and supplies the restore payload.
	workspace   *appworkspace.Service
	resolveHost HostResolver
	// onDetach receives the ID of a Tab that left the main window. Ticket 13
	// owns the detached window itself; the seam keeps ownership explicit.
	onDetach func(model.TabID)
	// onDetachReattached receives the ID of a detached Tab that moved back
	// into the main Workspace, so the composition root can close its window.
	onDetachReattached func(model.TabID)
	// onDetachedClose receives a request from a detached window to close. The
	// composition root owns the window and its close intent.
	onDetachedClose func(model.TabID)
	// detached keeps Tabs whose Sessions stay alive outside the main window.
	detached []*Tab
	// restoring suppresses persistence while a payload is being applied.
	restoring bool

	// pending lists Host labels whose initial dial is still running. It is
	// mutated only on the UI thread.
	pending []string

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	outcomes     []openOutcome
	paneOutcomes []paneOutcome
	// activity queues Session change notifications from Session goroutines.
	// Flush transfers them to the activity markers on the UI thread.
	activity []model.SessionID
	problem  appconnection.Kind

	// desktopNotice is a non-blocking message for a deep-link or command-line
	// target the app could not open. It is set and read on the UI thread.
	desktopNotice DesktopNotice
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

// WithRedrawRequest sets the function called when the layout changes. The app
// wires it to the main window's Invalidate.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.requestRedraw = fn }
}

// WithSessions gives the shell the Session service that opens local shells and
// SSH Sessions, and the factory that builds their terminal surfaces.
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

// WithTrustPrompt gives the shell the host-key prompt to render. The same
// prompt is the SSH connection manager's Prompter.
func WithTrustPrompt(prompt *sessionui.TrustPrompt) Option {
	return func(s *State) { s.trustPrompt = prompt }
}

// WithAuthPrompt gives the shell the passphrase and keyboard-interactive
// prompt to render. The same prompt is the SSH connection manager's
// AuthPrompter.
func WithAuthPrompt(prompt *sessionui.AuthPrompt) Option {
	return func(s *State) { s.authPrompt = prompt }
}

// WithWorkspace gives the shell the Workspace persistence service and the Host
// resolver restore needs. Either may be nil, which disables restore.
func WithWorkspace(service *appworkspace.Service, resolve HostResolver) Option {
	return func(s *State) {
		s.workspace = service
		s.resolveHost = resolve
	}
}

// WithDetachHandler receives a Tab's ID after the Tab leaves the main window.
func WithDetachHandler(fn func(model.TabID)) Option {
	return func(s *State) { s.onDetach = fn }
}

// WithDetachReattachHandler receives a detached Tab's ID after it moves back
// into the main Workspace. The composition root closes that Tab's window.
func WithDetachReattachHandler(fn func(model.TabID)) Option {
	return func(s *State) { s.onDetachReattached = fn }
}

// WithDetachedCloseRequest receives a detached window's close request. The
// composition root prompts for close intent and owns the window lifecycle.
func WithDetachedCloseRequest(fn func(model.TabID)) Option {
	return func(s *State) { s.onDetachedClose = fn }
}

// NewState returns the initial shell state: the Vault is visible and there
// are no work Tabs.
func NewState(opts ...Option) *State {
	ctx, cancel := context.WithCancel(context.Background())
	s := &State{active: -1, nextTabNumber: 1, vaultVisible: true, ctx: ctx, cancel: cancel}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// TrustPrompt returns the host-key prompt the shell renders, or nil.
func (s *State) TrustPrompt() *sessionui.TrustPrompt { return s.trustPrompt }

// AuthPrompt returns the authentication prompt the shell renders, or nil.
func (s *State) AuthPrompt() *sessionui.AuthPrompt { return s.authPrompt }

// Problem returns the last connection failure, or "" when the last attempt
// succeeded or none has run.
func (s *State) Problem() appconnection.Kind { return s.problem }

// DesktopNotice identifies a deep-link or command-line target failure the
// shell shows as a non-blocking banner. It holds a code, not user-visible
// text: the view localizes it.
type DesktopNotice string

const (
	DesktopNoticeNone              DesktopNotice = ""
	DesktopNoticeInvalidTarget     DesktopNotice = "invalid_target"
	DesktopNoticeUnsupportedTarget DesktopNotice = "unsupported_target"
)

// SetDesktopNotice records the banner code. Passing DesktopNoticeNone clears
// it, which a successful target open does.
func (s *State) SetDesktopNotice(notice DesktopNotice) { s.desktopNotice = notice }

// DesktopNotice returns the current banner code.
func (s *State) DesktopNotice() DesktopNotice { return s.desktopNotice }

// PendingConnections returns the labels of Hosts whose initial dial has not
// finished. The view renders them as the "connecting" indicator.
func (s *State) PendingConnections() []string {
	return append([]string(nil), s.pending...)
}

func (s *State) clearPending(label string) {
	for i, pending := range s.pending {
		if pending == label {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
	}
}

// Shutdown cancels pending connections and trust prompts. The app calls it on
// quit, before closing Sessions.
func (s *State) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
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

// NewTab opens a local shell Session, builds its terminal surface, appends it
// as a work Tab, and makes it active.
func (s *State) NewTab() error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	s.problem = ""
	session, err := s.sessions.OpenLocal(context.Background())
	if err != nil {
		return err
	}
	surface, err := s.surfaces(session)
	if err != nil {
		_ = session.Close()
		return err
	}

	tab := s.newTab(TabOptions{AutoTitle: true, Descriptor: domainworkspace.SessionDescriptor{
		Kind: domainworkspace.SessionLocal,
	}})
	tab.attachOnlySession(session, surface)
	s.tabs = append(s.tabs, tab)
	s.active = len(s.tabs) - 1
	s.vaultVisible = false
	s.save()
	s.RequestRedraw()
	return nil
}

// OpenHost starts an SSH Session without blocking the UI thread. Dialing,
// host-key verification, and password authentication can wait on the network
// or on the user's trust decision, so the work runs on its own goroutine and
// its outcome is applied by the next Flush. It returns ErrNoSessions when the
// shell has no Session service.
func (s *State) OpenHost(host domainvault.Host) error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	label := host.Label
	if label == "" {
		label = host.Hostname
	}
	s.problem = ""
	s.pending = append(s.pending, label)

	ctx := s.ctx
	go func() {
		session, err := s.sessions.Open(ctx, appsession.StartSpec{
			Kind:     appsession.KindSSH,
			HostID:   host.ID,
			Hostname: host.Hostname,
			Port:     host.Port,
			Username: host.Username,
			// X11 forwarding requires both the Host preference and an explicit
			// security acknowledgement.
			X11Forwarding: host.X11Enabled(),
			// A dropped SSH transport retries every five seconds under the
			// original reason predicate. Local shells have no policy.
			Reconnect: &appsession.ReconnectPolicy{},
		})
		s.mu.Lock()
		s.outcomes = append(s.outcomes, openOutcome{
			session: session,
			label:   label,
			descriptor: domainworkspace.SessionDescriptor{
				Kind:          domainworkspace.SessionSSH,
				Label:         label,
				HostID:        host.ID,
				Hostname:      host.Hostname,
				Port:          host.Port,
				Username:      host.Username,
				X11Forwarding: host.X11Enabled(),
			},
			err: err,
		})
		s.mu.Unlock()
		s.RequestRedraw()
	}()
	return nil
}

// Flush applies connection outcomes that completed since the last frame. The
// view calls it on the UI thread before rendering, so Tabs and their terminal
// surfaces are still built and mutated only there.
func (s *State) Flush() {
	s.refreshWorkingDirectories()

	s.mu.Lock()
	paneOutcomes := s.paneOutcomes
	s.paneOutcomes = nil
	outcomes := s.outcomes
	s.outcomes = nil
	activity := s.activity
	s.activity = nil
	s.mu.Unlock()

	for _, id := range activity {
		s.applySessionActivity(id)
	}
	for _, outcome := range paneOutcomes {
		s.applyPaneOutcome(outcome)
	}
	for _, outcome := range outcomes {
		s.clearPending(outcome.label)
		if outcome.err != nil {
			s.problem = uiconnection.Kind(outcome.err)
			continue
		}
		surface, err := s.surfaces(outcome.session)
		if err != nil {
			_ = outcome.session.Close()
			s.problem = appconnection.KindInternal
			continue
		}
		number := s.nextTabNumber
		descriptor := outcome.descriptor
		descriptor.Number = number
		tab := s.newTab(TabOptions{Label: outcome.label, AutoTitle: true, Descriptor: descriptor})
		tab.attachOnlySession(outcome.session, surface)
		s.tabs = append(s.tabs, tab)
		s.active = len(s.tabs) - 1
		s.vaultVisible = false
		s.problem = ""
		s.save()
	}
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
