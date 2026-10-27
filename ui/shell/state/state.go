// Package state owns the main window's shell state: the ordered Tabs, their
// recursive split trees, and the requests the shell makes of the app.
// Window-scoped state lives in ui/<area>/state, as ADR 0006 requires, apart
// from the view that renders it.
package state

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainetmodel "github.com/mslxl/natcatty/domain/model/et"
	domoshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	uisearch "github.com/mslxl/natcatty/ui/search"
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

// TerminalFontSetter is implemented by a Surface whose terminal can restyle
// without being recreated. The Settings window uses it to apply the live
// terminal font to open Sessions.
type TerminalFontSetter interface {
	SetTerminalFont(family string, size float32)
}

// TerminalThemeSetter is implemented by a Surface whose terminal can recolor
// without being recreated. The Settings window uses it to apply a color scheme
// to open Sessions.
type TerminalThemeSetter interface {
	SetTerminalTheme(preset domaintheme.TerminalTheme)
}

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
	startup    StartupInput
	err        error
}

// StartupInput is input a newly opened Session receives once it exists. A
// Snippet run uses it to send the resolved command to every target Host
// without racing the dial; the shell delivers it after the Tab is attached.
type StartupInput struct {
	// Chunks are the exact bytes to send, in order. A nil or empty list sends
	// nothing.
	Chunks [][]byte
	// Delay waits between chunks, matching the Snippet's line-by-line mode.
	Delay time.Duration
}

// Empty reports that there is nothing to send.
func (i StartupInput) Empty() bool { return len(i.Chunks) == 0 }

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
	tabs          []*Tab
	active        int
	nextTabNumber int
	vaultVisible  bool
	vaultView     func(*ui.Context)
	logsVisible   bool
	logsView      func(*ui.Context)
	sftpVisible   bool
	sftpView      func(*ui.Context)
	// monitorVisible shows the Host monitoring surface. activate starts the
	// active panel and deactivate stops sampling when another surface shows.
	monitorVisible    bool
	monitorView       func(*ui.Context)
	monitorActivate   func()
	monitorDeactivate func()
	// sysManagerVisible shows the System manager surface. activate starts the
	// selected tab and deactivate stops sampling when another surface shows.
	sysManagerVisible    bool
	sysManagerView       func(*ui.Context)
	sysManagerActivate   func()
	sysManagerDeactivate func()
	// editorView renders the internal editor and its conflict, unsaved, and
	// upload-failure prompts above every surface.
	editorView func(*ui.Context)
	// serialView renders the serial connection dialog above every surface.
	// serialRequest opens it from a menu or command.
	serialView      func(*ui.Context)
	serialRequest   func()
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

	mu       sync.Mutex
	outcomes []openOutcome
	// pendingAdds carries dial-in-progress labels from callers outside the UI
	// thread, such as the external MCP host_open handler. Flush appends them to
	// pending on the UI thread; openOutcome then clears them as usual.
	pendingAdds  []string
	paneOutcomes []paneOutcome
	// startupFailures counts startup deliveries that failed since the last
	// Flush, so a later successful open cannot hide the problem.
	startupFailures int
	// activity queues Session change notifications from Session goroutines.
	// Flush transfers them to the activity markers on the UI thread.
	activity []model.SessionID
	problem  appconnection.Kind
	// problemErr keeps the last failure's cause so the view can distinguish a
	// missing OpenSSH agent from a missed security key deadline, which share
	// their protocol-level Kind with other failures.
	problemErr error

	// Broadcast and compose input state. The Session input hook runs off the
	// UI thread and touches only the fields under inputMu; Tabs and surfaces
	// remain UI-thread state. See input.go.
	inputMu              sync.Mutex
	broadcastPanel       bool
	broadcasting         bool
	broadcastTargets     map[model.SessionID]bool
	broadcastSessions    map[model.SessionID]bool
	broadcastTargetIDs   []model.SessionID
	broadcastResult      appsession.SendResult
	broadcastNotice      int
	composeOpen          bool
	composeDraft         string
	composeTrim          bool
	composeDecodeEscapes bool
	composeLineMode      appsession.ComposeLineMode
	composeSending       bool
	composeBroadcast     bool
	composeResult        appsession.SendResult
	composeSeq           uint64
	composeCancel        context.CancelFunc

	// desktopNotice is a non-blocking message for a deep-link or command-line
	// target the app could not open. It is set and read on the UI thread.
	desktopNotice DesktopNotice

	// searchState is the Quick Switcher and command palette overlay. A nil
	// state leaves the chords and the overlay out, which test shells use.
	searchState *uisearch.State
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

// WithLogs gives the shell the Logs and History surface. The app wires it;
// tests wire a fake or the real view over a test store.
func WithLogs(view func(*ui.Context)) Option {
	return func(s *State) { s.logsView = view }
}

// WithSFTP gives the shell the SFTP surface. The app wires the real view;
// tests wire a fake. The SFTP state lives in its own package, so the shell
// only renders and shows it.
func WithSFTP(view func(*ui.Context)) Option {
	return func(s *State) { s.sftpView = view }
}

// WithMonitor gives the shell the Host monitoring surface. activate starts
// the active panel's sampling; deactivate stops it when the surface is left,
// so hiding monitoring never leaves metric commands running.
func WithMonitor(view func(*ui.Context), activate, deactivate func()) Option {
	return func(s *State) {
		s.monitorView = view
		s.monitorActivate = activate
		s.monitorDeactivate = deactivate
	}
}

// WithSystemManager gives the shell the System manager surface. activate
// starts the selected tab; deactivate stops it when the surface is left, so
// hiding the manager never leaves ps, ss, or systemctl running.
func WithSystemManager(view func(*ui.Context), activate, deactivate func()) Option {
	return func(s *State) {
		s.sysManagerView = view
		s.sysManagerActivate = activate
		s.sysManagerDeactivate = deactivate
	}
}

// WithEditor gives the shell the internal editor overlay. Opening a file still
// goes through the SFTP surface's association; the overlay is mounted here so
// its prompts remain visible above every surface.
func WithEditor(view func(*ui.Context)) Option {
	return func(s *State) { s.editorView = view }
}

// EditorView returns the internal editor overlay, or nil.
func (s *State) EditorView() func(*ui.Context) { return s.editorView }

// WithSerialDialog gives the shell the serial connection dialog. The dialog
// state lives in ui/serial; the shell only renders it and opens it from the
// serial command.
func WithSerialDialog(view func(*ui.Context), request func()) Option {
	return func(s *State) {
		s.serialView = view
		s.serialRequest = request
	}
}

// RequestSerial opens the serial connection dialog. A shell without a serial
// dialog ignores the request.
func (s *State) RequestSerial() {
	if s.serialRequest != nil {
		s.serialRequest()
	}
}

// SerialView returns the serial dialog the app supplied, or nil.
func (s *State) SerialView() func(*ui.Context) { return s.serialView }

// WithSearch gives the shell the Quick Switcher and command palette overlay.
// The app owns the state because it also dispatches the chords and the chosen
// command.
func WithSearch(search *uisearch.State) Option {
	return func(s *State) { s.searchState = search }
}

// SearchState returns the Quick Switcher state, or nil when the shell has no
// overlay.
func (s *State) SearchState() *uisearch.State { return s.searchState }

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
	s := &State{
		active: -1, nextTabNumber: 1, vaultVisible: true, ctx: ctx, cancel: cancel,
		broadcastTargets:  make(map[model.SessionID]bool),
		broadcastSessions: make(map[model.SessionID]bool),
		composeLineMode:   appsession.ComposePaste,
	}
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

// ProblemError returns the last connection failure's cause, or nil after a
// successful attempt.
func (s *State) ProblemError() error { return s.problemErr }

// setProblem records a failure and its cause, or clears both.
func (s *State) setProblem(err error) {
	if err == nil {
		s.problem = ""
		s.problemErr = nil
		return
	}
	s.problem = uiconnection.Kind(err)
	s.problemErr = err
}

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
	s.leaveMonitor()
	s.leaveSystemManager()
	s.vaultVisible = true
	s.logsVisible = false
	s.sftpVisible = false
	s.RequestRedraw()
}

// LogsVisible reports whether the main window shows Logs and History rather
// than a work Tab.
func (s *State) LogsVisible() bool { return s.logsVisible }

// LogsView returns the Logs surface the app supplied, or nil.
func (s *State) LogsView() func(*ui.Context) { return s.logsView }

// ShowLogs makes the Logs and History surface visible. The work Tab strip
// stays over it, so a user can return to a Session without leaving Logs
// first.
func (s *State) ShowLogs() {
	s.leaveMonitor()
	s.leaveSystemManager()
	s.logsVisible = true
	s.vaultVisible = false
	s.sftpVisible = false
	s.RequestRedraw()
}

// SFTPVisible reports whether the main window shows the SFTP surface.
func (s *State) SFTPVisible() bool { return s.sftpVisible }

// SFTPView returns the SFTP surface the app supplied, or nil.
func (s *State) SFTPView() func(*ui.Context) { return s.sftpView }

// ShowSFTP makes the SFTP surface visible. The terminal work Tab strip stays
// over it, so a user can return to a Session without leaving SFTP first.
func (s *State) ShowSFTP() {
	s.leaveMonitor()
	s.leaveSystemManager()
	s.sftpVisible = true
	s.vaultVisible = false
	s.logsVisible = false
	s.RequestRedraw()
}

// MonitorVisible reports whether the main window shows Host monitoring.
func (s *State) MonitorVisible() bool { return s.monitorVisible }

// MonitorView returns the monitoring surface the app supplied, or nil.
func (s *State) MonitorView() func(*ui.Context) { return s.monitorView }

// ShowMonitor makes the Host monitoring surface visible and asks it to start
// sampling. The work Tab strip stays over it like Logs and SFTP.
func (s *State) ShowMonitor() {
	s.leaveSystemManager()
	s.monitorVisible = true
	s.vaultVisible = false
	s.logsVisible = false
	s.sftpVisible = false
	if s.monitorActivate != nil {
		s.monitorActivate()
	}
	s.RequestRedraw()
}

// leaveMonitor hides monitoring and stops its samplers. It is a no-op when
// monitoring is already hidden.
func (s *State) leaveMonitor() {
	if !s.monitorVisible {
		return
	}
	s.monitorVisible = false
	if s.monitorDeactivate != nil {
		s.monitorDeactivate()
	}
}

// SysManagerVisible reports whether the main window shows System manager.
func (s *State) SysManagerVisible() bool { return s.sysManagerVisible }

// SysManagerView returns the System manager surface the app supplied, or nil.
func (s *State) SysManagerView() func(*ui.Context) { return s.sysManagerView }

// ShowSystemManager makes the System manager visible and asks it to start
// sampling. The work Tab strip stays over it like Logs and SFTP.
func (s *State) ShowSystemManager() {
	s.leaveMonitor()
	s.sysManagerVisible = true
	s.vaultVisible = false
	s.logsVisible = false
	s.sftpVisible = false
	if s.sysManagerActivate != nil {
		s.sysManagerActivate()
	}
	s.RequestRedraw()
}

// leaveSystemManager hides System manager and stops its samplers. It is a
// no-op when the surface is already hidden.
func (s *State) leaveSystemManager() {
	if !s.sysManagerVisible {
		return
	}
	s.sysManagerVisible = false
	if s.sysManagerDeactivate != nil {
		s.sysManagerDeactivate()
	}
}

// NewTab opens a local shell Session, builds its terminal surface, appends it
// as a work Tab, and makes it active.
func (s *State) NewTab() error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	s.setProblem(nil)
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
	s.leaveMonitor()
	s.vaultVisible = false
	s.logsVisible = false
	s.sftpVisible = false
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
	return s.OpenHostWithStartup(host, StartupInput{})
}

// OpenHostWithStartup opens a Host Session like OpenHost and sends startup
// once the Session is attached to its Tab. A Snippet run uses this to deliver
// the resolved command to each target Host without racing the connection.
func (s *State) OpenHostWithStartup(host domainvault.Host, startup StartupInput) error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	label := host.Label
	if label == "" {
		label = host.Hostname
	}
	s.setProblem(nil)
	s.pending = append(s.pending, label)

	plan := hostSessionTransport(host)
	// X11 forwarding requires both the Host preference and an explicit
	// security acknowledgement, and exists only on SSH.
	x11 := plan.kind == appsession.KindSSH && host.X11Enabled()
	ctx := s.ctx
	go func() {
		session, err := s.sessions.Open(ctx, appsession.StartSpec{
			Kind:          plan.kind,
			Label:         label,
			HostID:        host.ID,
			Hostname:      host.Hostname,
			Port:          host.Port,
			Username:      host.Username,
			X11Forwarding: x11,
			NetworkDevice: plan.kind == appsession.KindSSH && host.IsNetworkDevice(),
			Telnet:        plan.telnet,
			Mosh:          plan.mosh,
			ET:            plan.et,
			Reconnect:     plan.reconnect,
		})
		s.mu.Lock()
		s.outcomes = append(s.outcomes, openOutcome{
			session: session,
			label:   label,
			descriptor: domainworkspace.SessionDescriptor{
				Kind:          plan.descriptor,
				Label:         label,
				HostID:        host.ID,
				Hostname:      host.Hostname,
				Port:          host.Port,
				Username:      host.Username,
				X11Forwarding: x11,
			},
			startup: startup,
			err:     err,
		})
		s.mu.Unlock()
		s.RequestRedraw()
	}()
	return nil
}

// OpenHostForMCP opens a real Host Session for an external MCP host_open call.
// Unlike OpenHostWithStartup it waits for the transport, so the caller can
// return the new Session id and grant ownership. The Tab is attached on the
// next Flush, exactly like any other opened Host.
func (s *State) OpenHostForMCP(ctx context.Context, host domainvault.Host) (model.SessionID, error) {
	if s.sessions == nil || s.surfaces == nil {
		return "", ErrNoSessions
	}
	label := host.Label
	if label == "" {
		label = host.Hostname
	}
	// This runs on the external MCP handler goroutine, not the UI thread.
	// Queue the connecting label and let Flush mutate pending.
	s.mu.Lock()
	s.pendingAdds = append(s.pendingAdds, label)
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	plan := hostSessionTransport(host)
	x11 := plan.kind == appsession.KindSSH && host.X11Enabled()
	session, err := s.sessions.Open(ctx, appsession.StartSpec{
		Kind:          plan.kind,
		Label:         label,
		HostID:        host.ID,
		Hostname:      host.Hostname,
		Port:          host.Port,
		Username:      host.Username,
		X11Forwarding: x11,
		NetworkDevice: plan.kind == appsession.KindSSH && host.IsNetworkDevice(),
		Telnet:        plan.telnet,
		Mosh:          plan.mosh,
		ET:            plan.et,
		Reconnect:     plan.reconnect,
	})
	if err != nil {
		// Flush clears the pending label and records the problem on the UI
		// thread, exactly like a failed background Host open.
		s.mu.Lock()
		s.outcomes = append(s.outcomes, openOutcome{label: label, err: err})
		s.mu.Unlock()
		s.RequestRedraw()
		return "", err
	}
	s.mu.Lock()
	s.outcomes = append(s.outcomes, openOutcome{
		session: session,
		label:   label,
		descriptor: domainworkspace.SessionDescriptor{
			Kind:          plan.descriptor,
			Label:         label,
			HostID:        host.ID,
			Hostname:      host.Hostname,
			Port:          host.Port,
			Username:      host.Username,
			X11Forwarding: x11,
		},
	})
	s.mu.Unlock()
	s.RequestRedraw()
	return session.ID(), nil
}

// hostTransportPlan is the transport, restore descriptor kind, endpoint, and
// reconnect policy one saved Host maps onto.
type hostTransportPlan struct {
	kind       appsession.Kind
	descriptor domainworkspace.SessionKind
	telnet     *domaintelnet.Config
	mosh       *domoshmodel.Config
	et         *domainetmodel.Config
	reconnect  *appsession.ReconnectPolicy
}

// hostSessionTransport maps a saved Host's protocol onto the Session
// transport, its restore descriptor, and its reconnect policy. A Telnet Host
// gets the Telnet endpoint and a policy that permits the manual Reconnect
// action without automatically retrying a dropped socket, matching the
// original's SSH-only auto-reconnect.
func hostSessionTransport(host domainvault.Host) hostTransportPlan {
	switch host.Protocol {
	case domainvault.ProtocolTelnet:
		cfg := domaintelnet.Config{Hostname: host.Hostname, Port: host.Port}.Normalized()
		return hostTransportPlan{
			kind:       appsession.KindTelnet,
			descriptor: domainworkspace.SessionTelnet,
			telnet:     &cfg,
			reconnect:  telnetReconnectPolicy(),
		}
	case domainvault.ProtocolMosh:
		return hostTransportPlan{
			kind:       appsession.KindMosh,
			descriptor: domainworkspace.SessionMosh,
			mosh:       moshConfig(host),
			reconnect:  &appsession.ReconnectPolicy{},
		}
	case domainvault.ProtocolET:
		return hostTransportPlan{
			kind:       appsession.KindET,
			descriptor: domainworkspace.SessionET,
			et:         etConfig(host),
			reconnect:  &appsession.ReconnectPolicy{},
		}
	}
	// A dropped SSH transport retries every five seconds under the original
	// reason predicate. Local shells have no policy.
	return hostTransportPlan{
		kind:       appsession.KindSSH,
		descriptor: domainworkspace.SessionSSH,
		reconnect:  &appsession.ReconnectPolicy{},
	}
}

// moshConfig is the non-secret bootstrap half of a Mosh Session. Credentials,
// jump trust, and MFA stay with the SSH Connection Manager.
func moshConfig(host domainvault.Host) *domoshmodel.Config {
	cfg := domoshmodel.Config{
		Hostname:   host.Hostname,
		Port:       host.Port,
		Username:   host.Username,
		ServerPath: host.MoshServerPath,
	}.Normalized()
	return &cfg
}

// etConfig is the non-secret half of an EternalTerminal Session. The app adds
// credentials, host-key snapshots, and the single jump Host after resolving the
// route.
func etConfig(host domainvault.Host) *domainetmodel.Config {
	cfg := domainetmodel.Config{
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
		ETPort:   host.ETPort,
	}.Normalized()
	return &cfg
}

// telnetReconnectPolicy allows the Session menu's manual Reconnect action but
// never retries a dropped Telnet socket automatically.
func telnetReconnectPolicy() *appsession.ReconnectPolicy {
	return &appsession.ReconnectPolicy{
		ShouldRetry: func(appsession.End) bool { return false },
	}
}

// OpenSerial opens a serial Session without blocking the UI thread. The
// configuration is validated before the background open, so a malformed port
// or an unsupported setting returns the typed error to the dialog that asked.
// Serial Sessions have no reconnect policy: the original required a manual
// reconnect, and a device that vanished should not be reopened automatically.
func (s *State) OpenSerial(cfg domainserial.Config) error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return err
	}
	label := appsession.SerialLabel(normalized)
	s.setProblem(nil)
	s.pending = append(s.pending, label)

	ctx := s.ctx
	go func() {
		serialConfig := normalized
		session, err := s.sessions.Open(ctx, appsession.StartSpec{
			Kind:   appsession.KindSerial,
			Label:  label,
			Serial: &serialConfig,
		})
		s.mu.Lock()
		s.outcomes = append(s.outcomes, openOutcome{
			session: session,
			label:   label,
			descriptor: domainworkspace.SessionDescriptor{
				Kind:   domainworkspace.SessionSerial,
				Label:  label,
				Serial: &serialConfig,
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
	pendingAdds := s.pendingAdds
	s.pendingAdds = nil
	paneOutcomes := s.paneOutcomes
	s.paneOutcomes = nil
	outcomes := s.outcomes
	s.outcomes = nil
	activity := s.activity
	s.activity = nil
	startupFailures := s.startupFailures
	s.startupFailures = 0
	s.mu.Unlock()

	for _, label := range pendingAdds {
		s.pending = append(s.pending, label)
	}
	for _, id := range activity {
		s.applySessionActivity(id)
	}
	for _, outcome := range paneOutcomes {
		s.applyPaneOutcome(outcome)
	}
	for _, outcome := range outcomes {
		s.clearPending(outcome.label)
		if outcome.err != nil {
			s.setProblem(outcome.err)
			continue
		}
		surface, err := s.surfaces(outcome.session)
		if err != nil {
			_ = outcome.session.Close()
			s.setProblem(err)
			continue
		}
		number := s.nextTabNumber
		descriptor := outcome.descriptor
		descriptor.Number = number
		tab := s.newTab(TabOptions{Label: outcome.label, AutoTitle: true, Descriptor: descriptor})
		tab.attachOnlySession(outcome.session, surface)
		s.tabs = append(s.tabs, tab)
		s.active = len(s.tabs) - 1
		s.leaveMonitor()
		s.vaultVisible = false
		s.logsVisible = false
		s.sftpVisible = false
		s.setProblem(nil)
		s.save()
		if !outcome.startup.Empty() {
			// Line-by-line delivery waits between chunks, so it cannot run on
			// the UI thread. The Session and its transport already exist.
			go s.deliverStartup(outcome.session.ID(), outcome.startup)
		}
	}
	// Startup delivery failures belong to Sessions opened in earlier frames.
	// Apply them after the open outcomes, whose success path clears problem,
	// so one failed delivery stays visible even when another Tab attached.
	if startupFailures > 0 {
		s.problem = appconnection.KindInternal
		s.problemErr = nil
	}
	// Tabs and Panes may have changed; keep the input-thread broadcast
	// recipient snapshot exact.
	s.refreshBroadcastState()
}

// deliverStartup sends a new Session's startup input off the UI thread and
// queues a failure for the next Flush.
func (s *State) deliverStartup(id model.SessionID, startup StartupInput) {
	result := s.sessions.SendChunks([]model.SessionID{id}, startup.Chunks, startup.Delay)
	if result.Complete() {
		return
	}
	s.mu.Lock()
	s.startupFailures++
	s.mu.Unlock()
	s.RequestRedraw()
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
