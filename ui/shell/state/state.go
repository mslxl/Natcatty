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
	"github.com/mslxl/natcatty/application/session/autologin"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainetmodel "github.com/mslxl/natcatty/domain/model/et"
	domoshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainprotocol "github.com/mslxl/natcatty/domain/model/protocol"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvnc "github.com/mslxl/natcatty/domain/model/vnc"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	uisearch "github.com/mslxl/natcatty/ui/search"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/vault/section"
)

// ErrNoSessions reports a shell that was not given a Session service, so it
// cannot open a Tab's Session.
var ErrNoSessions = errors.New("shell: no session service")

// ErrNoActiveTab reports a Pane action taken while no Tab is active.
var ErrNoActiveTab = errors.New("shell: no active Tab")

// ErrNoTextInserter reports a focused Surface that cannot insert text into its
// line editor. The caller must not fall back to typing, which could submit a
// multi-line command.
var ErrNoTextInserter = errors.New("shell: the focused terminal cannot insert text")

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

// KeyReleaser is implemented by a Surface that can tell its remote peer to
// release keyboard keys, synthesized modifiers, and pointer buttons the viewer
// last reported down. The shell calls it before a Surface stops rendering:
// switching Tabs, detaching a Pane, or closing it. MyGo does not deliver key-up
// events for a subtree it stops building, so without this seam a held key
// would stay down in the guest.
type KeyReleaser interface {
	ReleaseInput()
}

// BrowserOpener is implemented by a Session surface that can show its Host's
// browser panel. The Vault's "Open host browser" action opens a Session and
// asks the surface for the panel once it exists.
type BrowserOpener interface {
	OpenBrowser() error
}

// BrowserShown is implemented by a Surface whose browser panel is open. The
// panel owns a native webview that cannot be mounted in a second window, so a
// follow or topdown renderer checks this and draws a neutral placeholder
// instead of rendering the Surface elsewhere.
type BrowserShown interface {
	BrowserOpen() bool
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

// FocusedSurface is implemented by a Surface whose terminal takes the keyboard
// only when its Pane is the focused one. The shell renders every Pane through
// it so a split cannot steal the keyboard from the focused Pane or from
// Compose.
type FocusedSurface interface {
	ViewFocused(c *ui.Context, focused bool) ui.Element
}

// TerminalThemeSetter is implemented by a Surface whose terminal can recolor
// without being recreated. The Settings window uses it to apply a color scheme
// to open Sessions.
type TerminalThemeSetter interface {
	SetTerminalTheme(preset domaintheme.TerminalTheme)
}

// TerminalCursorSetter is implemented by a Surface whose terminal can take a
// new cursor shape and blink preference without being recreated. The Settings
// window uses it to apply a cursor change to open Sessions.
type TerminalCursorSetter interface {
	SetTerminalCursor(style domainpreferences.CursorStyle, blink bool)
}

// TextInserter is implemented by a Surface that can put text into its
// terminal's line editor without submitting it. The History surface's Insert
// action uses it so a multi-line command is never typed as several lines.
type TextInserter interface {
	InsertText(text string) error
}

// SuggestionInvalidator is implemented by a Surface whose open autocomplete
// popup must be recomputed after the shared history journal changes.
type SuggestionInvalidator interface {
	InvalidateSuggestions()
}

// HostResolver looks a Host up for restore and for reopening a split. It
// reports false when the Host no longer exists.
type HostResolver func(context.Context, model.HostID) (domainvault.Host, bool)

// openOutcome is the result of an SSH connection started off the UI thread for
// a new Tab. Outcome fields are written by the dial goroutine and applied on
// the UI thread by Flush.
type openOutcome struct {
	session *appsession.Session
	label   string
	// browser asks Flush to open the new Session's browser panel once its
	// surface exists.
	browser    bool
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
	// vaultSection is the Vault page the sidebar chose.
	vaultSection section.Section
	// hostListChord resolves the catalog's host-list toggle for the active
	// scheme and the user's overrides. The shell registers it on the terminal
	// Pane and the Vault list rather than at window scope, so a focused text
	// field keeps the chord. hostListFocusPending asks the Vault list to take
	// the keyboard after the chord shows it, so the same chord can hide it.
	hostListChord        func() (ui.Modifiers, ui.Key, bool)
	hostListFocusPending bool
	// home is the page of the home Tab shown last: the Vault, Logs,
	// monitoring, or System manager. Choosing the home Tab returns to it.
	home        HomePage
	vaultView   func(*ui.Context)
	logsVisible bool
	logsView    func(*ui.Context)
	sftpVisible bool
	sftpView    func(*ui.Context)
	// sftpTabShown is the Layout setting that hides the top bar's SFTP Tab.
	sftpTabShown func() bool
	// filesPanel is the SFTP panel Session Tabs open beside their terminal;
	// filesOpen holds what each Tab's open panel browses, and filesShown the
	// target the panel was last pointed at.
	filesPanel *FilesPanel
	filesOpen  map[model.TabID]filesTarget
	filesShown *filesTarget
	filesRatio float32
	filesWidth float32
	// sideTools are the side panel's tools besides SFTP. sideChosen holds each
	// Tab's chosen one. sideDrawn and sideLast are per renderer: the tool a
	// renderer drew this frame and the one it drew the frame before, so a
	// tool that stops drawing in one window can rest only once no renderer
	// shows it. sideHidden remembers tools already told to rest until a
	// renderer draws them again, so the last two renderers stopping in one
	// frame hide a tool once, not twice.
	sideTools  []SideTool
	sideChosen map[model.TabID]SideToolID
	sideDrawn  map[RenderOwner]SideToolID
	sideLast   map[RenderOwner]SideToolID
	sideHidden map[SideToolID]bool
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
	serialView    func(*ui.Context)
	serialRequest func()
	requestLocale func(string)
	// brandMark reads the app icon the sidebar shows beside the app's name.
	brandMark func() *ui.Bitmap
	// terminalTheme is the scheme open terminals draw with, for the chrome
	// around them; hasTerminalTheme is false until the app sets one.
	terminalTheme    domaintheme.TerminalTheme
	hasTerminalTheme bool
	requestSettings  func()
	requestRedraw    func()
	sessions         *appsession.Service
	// localShell resolves the custom shell a local Session starts: the argv
	// and whether the configured path was unusable, so the OS login shell
	// opened instead. Nil asks the OS directly.
	localShell  func() (command []string, fallback bool)
	surfaces    SurfaceFactory
	trustPrompt *sessionui.TrustPrompt
	authPrompt  *sessionui.AuthPrompt
	// plaintextPrompt asks the user to accept an unencrypted VNC connection
	// before an unrouted Host dials. It is the same prompt the VNC transport
	// factory waits on.
	plaintextPrompt *sessionui.VNCPlaintextPrompt
	// etResumeStore lets the shell clear a saved ET credential pair when no
	// live Session owns it any more: a restored reattach the server refuses as
	// unsupported, or an ET Pane closed while its Session never opened. Nil
	// leaves such stale pairs in place.
	etResumeStore ETResumeStore

	// workspace persists layout changes and supplies the restore payload.
	workspace   *appworkspace.Service
	resolveHost HostResolver
	// serialAutoLogin resolves a serial profile's expect engine options from
	// its saved credentials. Nil leaves auto-login off.
	serialAutoLogin func(context.Context, domainserial.Config) *autologin.Options
	// telnetAutoLogin resolves a Telnet Host's expect engine options from its
	// saved credentials and the one-shot password of a Quick Connect. Nil
	// leaves auto-login off.
	telnetAutoLogin func(context.Context, domainvault.Host, string) *autologin.Options
	// dynamicTabTitleMode is the live Dynamic tab title setting. Nil keeps the
	// default: dynamic titles for every Session.
	dynamicTabTitleMode func() domainterminal.DynamicTabTitleMode
	// hostLabel resolves a Host's current label, so an open Tab follows a Vault
	// rename. Nil falls back to the descriptor's label snapshot.
	hostLabel func(model.HostID) (string, bool)
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
	mainSessionIDs       []model.SessionID
	broadcastResult      appsession.SendResult
	broadcastNotice      int
	composeTargets       map[model.SessionID]bool
	composeExplicit      bool
	composeNotice        int
	composeOpen          bool
	composeDraft         string
	composeTrim          bool
	composeDecodeEscapes bool
	composeLineMode      appsession.ComposeLineMode
	composeSending       bool
	composeReconnecting  bool
	composeConnection    ComposeConnection
	composeBroadcast     bool
	composeResult        appsession.SendResult
	composeSeq           uint64
	composeCancel        context.CancelFunc

	// The compose directory picker. Listings run off the UI thread and write
	// these fields under inputMu; composeDirectorySeq makes a superseded or
	// canceled listing unable to replace a newer one.
	composeDirectory        ComposeDirectorySource
	composeDirectoryOpen    bool
	composeDirectorySession model.SessionID
	composeDirectoryShell   string
	composeDirectoryTracked string
	composeDirectoryPath    string
	composeDirectoryTyped   string
	// composeDirectoryPendingTyped is the resolved path a finished listing
	// wants the editor to show. The listing goroutine writes only this field;
	// the UI thread copies it into composeDirectoryTyped, which is the string
	// the TextInput binds directly and never shares with another goroutine.
	composeDirectoryPendingTyped string
	composeDirectoryRequested    string
	composeDirectoryEntries      []ComposeDirectoryEntry
	composeDirectoryUnknown      bool
	composeDirectoryLoading      bool
	composeDirectoryFailed       bool
	composeDirectoryUnsupported  bool
	composeDirectorySeq          uint64
	composeDirectoryCancel       context.CancelFunc

	// desktopNotice is a non-blocking message for a deep-link or command-line
	// target the app could not open. It is set and read on the UI thread.
	desktopNotice DesktopNotice

	// quickConnect is the tab bar's quick-connect dialog. It opens an Unsaved
	// Host Session and never writes to the Vault.
	quickConnect quickConnectState

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

// WithBrandMark sets the reader of the app icon the home sidebar shows beside
// the app's name, which follows the chosen icon variant. Without it, or while
// it returns nil, the sidebar shows a terminal glyph.
func WithBrandMark(fn func() *ui.Bitmap) Option {
	return func(s *State) { s.brandMark = fn }
}

// BrandMark returns the sidebar's app icon, or nil.
func (s *State) BrandMark() *ui.Bitmap {
	if s.brandMark == nil {
		return nil
	}
	return s.brandMark()
}

// TerminalTheme is the scheme open terminals draw with, if the app set one.
func (s *State) TerminalTheme() (domaintheme.TerminalTheme, bool) {
	return s.terminalTheme, s.hasTerminalTheme
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

// WithLocalShell supplies the custom shell for local Sessions. command is the
// argv to start; fallback reports that the configured program was unusable,
// so the OS login shell opens and the shell warns. Without this option a local
// Tab asks the transport for the OS login shell.
func WithLocalShell(resolve func() (command []string, fallback bool)) Option {
	return func(s *State) { s.localShell = resolve }
}

// WithSerialAutoLogin gives the shell the resolver that turns a serial
// profile's stored credentials into an expect engine. The resolver reads the
// password from the app's encrypted field-secret store; it returns nil when
// auto-login is off. Without it, serial Sessions open without automation.
func WithSerialAutoLogin(resolve func(context.Context, domainserial.Config) *autologin.Options) Option {
	return func(s *State) { s.serialAutoLogin = resolve }
}

// WithTelnetAutoLogin gives the shell the resolver that turns a Telnet Host's
// Vault login into an expect engine. password is the one-shot credential of a
// Quick Connect, or "" for a saved Host. It returns nil when the Host has no
// login to replay.
func WithTelnetAutoLogin(resolve func(context.Context, domainvault.Host, string) *autologin.Options) Option {
	return func(s *State) { s.telnetAutoLogin = resolve }
}

// ETResumeStore removes a saved ET session pair from the secret store. The app
// backs it with the ET adapter's SecretSessionStore; tests use a fake.
type ETResumeStore interface {
	Delete(ctx context.Context, resumeKey string) error
}

// WithETResumeStore lets the shell clear ET credentials that no live Session
// can clear itself: a stale pair refused by a legacy server during restore, or
// one left by an ET Pane whose Session never opened.
func WithETResumeStore(store ETResumeStore) Option {
	return func(s *State) { s.etResumeStore = store }
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

// WithSFTPTabShown supplies the live "Show SFTP tab" setting. Without it the
// Tab is shown.
func WithSFTPTabShown(shown func() bool) Option {
	return func(s *State) { s.sftpTabShown = shown }
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

// WithVNCPlaintextPrompt gives the shell the unencrypted-VNC confirmation to
// render. The same prompt blocks the VNC transport factory after the stream is
// open and the security type is known, before any credential is sent.
func WithVNCPlaintextPrompt(prompt *sessionui.VNCPlaintextPrompt) Option {
	return func(s *State) { s.plaintextPrompt = prompt }
}

// WithDynamicTabTitleMode supplies the live Dynamic tab title setting. Without
// it the shell follows the default: every Session's title.
func WithDynamicTabTitleMode(mode func() domainterminal.DynamicTabTitleMode) Option {
	return func(s *State) { s.dynamicTabTitleMode = mode }
}

// WithHostLabel supplies a Host's current label so open Tabs follow a Vault
// rename. The getter runs on the UI thread.
func WithHostLabel(label func(model.HostID) (string, bool)) Option {
	return func(s *State) { s.hostLabel = label }
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
		active: -1, nextTabNumber: 1, vaultVisible: true, vaultSection: section.Hosts, ctx: ctx, cancel: cancel,
		broadcastTargets:  make(map[model.SessionID]bool),
		broadcastSessions: make(map[model.SessionID]bool),
		composeTargets:    make(map[model.SessionID]bool),
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

// VNCPlaintextPrompt returns the unencrypted-VNC confirmation the shell
// renders, or nil.
func (s *State) VNCPlaintextPrompt() *sessionui.VNCPlaintextPrompt { return s.plaintextPrompt }

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
	DesktopNoticeNone               DesktopNotice = ""
	DesktopNoticeInvalidTarget      DesktopNotice = "invalid_target"
	DesktopNoticeUnsupportedTarget  DesktopNotice = "unsupported_target"
	DesktopNoticeLocalShellFallback DesktopNotice = "local_shell_fallback"
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

// VaultSection returns the Vault page the sidebar chose. The Vault surface
// draws it.
func (s *State) VaultSection() section.Section { return s.vaultSection }

// ShowVaultSection shows the Vault at page.
func (s *State) ShowVaultSection(page section.Section) {
	s.vaultSection = page
	s.ShowVault()
}

// HomePage is one page of the home Tab. The Vault's sidebar lists them beside
// the Vault's own pages.
type HomePage int

const (
	// HomeVault is the Vault at its chosen section.
	HomeVault HomePage = iota
	// HomeLogs is Logs and History.
	HomeLogs
	// HomeMonitor is Host monitoring.
	HomeMonitor
	// HomeSystem is System manager.
	HomeSystem
)

// HomeVisible reports whether the home Tab is the visible one: the Vault or
// one of the pages its sidebar lists.
func (s *State) HomeVisible() bool {
	return s.vaultVisible || s.logsVisible || s.monitorVisible || s.sysManagerVisible
}

// ShowHome shows the home Tab at the page it showed last.
func (s *State) ShowHome() {
	switch s.home {
	case HomeLogs:
		s.ShowLogs()
	case HomeMonitor:
		s.ShowMonitor()
	case HomeSystem:
		s.ShowSystemManager()
	default:
		s.ShowVault()
	}
}

// WithHostListChord supplies the host-list toggle's effective chord. The app
// resolves it through the dispatcher, so a user rebind reaches the surfaces
// that register it.
func WithHostListChord(chord func() (ui.Modifiers, ui.Key, bool)) Option {
	return func(s *State) { s.hostListChord = chord }
}

// HostListChord returns the effective host-list chord, if the app wired one.
func (s *State) HostListChord() (ui.Modifiers, ui.Key, bool) {
	if s.hostListChord == nil {
		return 0, 0, false
	}
	return s.hostListChord()
}

// ToggleHostList shows the Vault's Host list, or returns to the active work
// Tab when the list is already showing. It is the host-list binding's action
// in the Go port, where the Vault is the host list surface. Hiding needs a
// work Tab to return to; with none the Vault stays, as the only surface there
// is. It reports whether the visible surface changed.
func (s *State) ToggleHostList() bool {
	if s.vaultVisible {
		if s.TabCount() == 0 {
			return false
		}
		s.vaultVisible = false
		s.logsVisible = false
		s.sftpVisible = false
		s.hostListFocusPending = false
		s.RequestRedraw()
		return true
	}
	s.vaultSection = section.Hosts
	// Set the focus request before showing the Vault: the frame the show
	// schedules must already know the list takes the keyboard, so the chord
	// can hide the Vault on its next press.
	s.hostListFocusPending = true
	s.ShowVault()
	return true
}

// ConsumeHostListFocus reports whether the Vault list should take the
// keyboard this frame. ToggleHostList asks for it when the chord shows the
// list, so pressing the chord again hides it without a click.
func (s *State) ConsumeHostListFocus() bool {
	pending := s.hostListFocusPending
	s.hostListFocusPending = false
	return pending
}

// ShowVault makes the Vault the visible surface.
func (s *State) ShowVault() {
	// The work Tab stops rendering, so its focused pane must not leave guest
	// input held.
	s.releaseActiveInput()
	s.home = HomeVault
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
	s.releaseActiveInput()
	s.home = HomeLogs
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

// SFTPTabShown reports whether the top bar shows the SFTP Tab. It is shown
// unless the Layout setting hides it, as the original's.
func (s *State) SFTPTabShown() bool {
	return s.sftpTabShown == nil || s.sftpTabShown()
}

// ShowSFTP makes the SFTP surface visible. The terminal work Tab strip stays
// over it, so a user can return to a Session without leaving SFTP first.
func (s *State) ShowSFTP() {
	if !s.SFTPTabShown() {
		return
	}
	s.releaseActiveInput()
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
	s.releaseActiveInput()
	s.home = HomeMonitor
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
	s.releaseActiveInput()
	s.home = HomeSystem
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
func (s *State) NewTab() error { return s.NewTabWithShell("") }

// NewTabWithShell opens a local terminal Tab with a chosen shell, as
// --open-terminal-shell does. An empty shell uses the platform default;
// otherwise it is a program name or executable path. A failed shell start
// returns the error without appending an empty Tab.
func (s *State) NewTabWithShell(shell string) error {
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	s.setProblem(nil)
	var command []string
	if s.localShell != nil {
		// A new local Tab re-reads the preference. Clear only this feature's
		// fallback so a fixed path stops warning without hiding an unrelated
		// deep-link problem.
		if s.DesktopNotice() == DesktopNoticeLocalShellFallback {
			s.SetDesktopNotice(DesktopNoticeNone)
		}
		var fallback bool
		command, fallback = s.localShell()
		if fallback {
			s.SetDesktopNotice(DesktopNoticeLocalShellFallback)
		}
	}
	// An explicit shell argument wins over the configured preference.
	if shell != "" {
		command = []string{shell}
	}
	session, err := s.sessions.OpenLocalCommand(context.Background(), command)
	if err != nil {
		return err
	}
	surface, err := s.surfaces(session)
	if err != nil {
		_ = session.Close()
		return err
	}

	tab := s.newTab(TabOptions{AutoTitle: true, Descriptor: domainworkspace.SessionDescriptor{
		Kind:  domainworkspace.SessionLocal,
		Label: session.Label(),
	}})
	tab.attachOnlySession(session, surface)
	s.tabs = append(s.tabs, tab)
	s.activateTab(len(s.tabs) - 1)
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
	return s.openHost(host, StartupInput{}, false)
}

// OpenHostBrowser opens a Host Session and, once its surface exists, opens the
// Session's browser panel. A surface without a browser route leaves the
// terminal running and records the failure the Session view shows.
func (s *State) OpenHostBrowser(host domainvault.Host) error {
	return s.openHost(host, StartupInput{}, true)
}

// OpenHostWithStartup opens a Host Session like OpenHost and sends startup
// once the Session is attached to its Tab. A Snippet run uses this to deliver
// the resolved command to each target Host without racing the connection.
func (s *State) OpenHostWithStartup(host domainvault.Host, startup StartupInput) error {
	return s.openHost(host, startup, false)
}

// openHost is the one connection path: OpenHost, OpenHostWithStartup, and
// OpenHostBrowser differ only in what they do once the Tab is attached.
func (s *State) openHost(host domainvault.Host, startup StartupInput, browser bool) error {
	return s.openHostWithPassword(host, startup, browser, "")
}

// openHostWithPassword is openHost plus a one-shot password for a target that
// has no saved Host, as Quick Connect does. The password stays in memory: it is
// passed to the Session but never stored in the workspace descriptor.
func (s *State) openHostWithPassword(host domainvault.Host, startup StartupInput, browser bool, password string) error {
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
	spec := HostSessionSpec(host)
	// The one-shot password is never stored in the workspace descriptor; it
	// only travels into this Session's StartSpec.
	spec.Password = password
	resumeKey, x11 := spec.ResumeKey, spec.X11Forwarding
	ctx := s.ctx
	go func() {
		s.attachAutoLogin(ctx, &spec, host, password)
		session, err := s.sessions.Open(ctx, spec)
		s.mu.Lock()
		s.outcomes = append(s.outcomes, openOutcome{
			session: session,
			label:   label,
			browser: browser,
			descriptor: domainworkspace.SessionDescriptor{
				Kind:           plan.descriptor,
				Label:          label,
				HostID:         host.ID,
				Hostname:       host.Hostname,
				Port:           host.Port,
				Username:       host.Username,
				X11Forwarding:  x11,
				AutoAttachTmux: plan.tmux,
				ResumeKey:      resumeKey,
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
	spec := HostSessionSpec(host)
	resumeKey, x11 := spec.ResumeKey, spec.X11Forwarding
	s.attachAutoLogin(ctx, &spec, host, "")
	session, err := s.sessions.Open(ctx, spec)
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
			Kind:           plan.descriptor,
			Label:          label,
			HostID:         host.ID,
			Hostname:       host.Hostname,
			Port:           host.Port,
			Username:       host.Username,
			X11Forwarding:  x11,
			AutoAttachTmux: plan.tmux,
			ResumeKey:      resumeKey,
		},
	})
	s.mu.Unlock()
	s.RequestRedraw()
	return session.ID(), nil
}

// HostSessionSpec maps a saved Host onto the shared Session service's start
// spec under the same protocol, capture, tmux, and reconnect rules the main
// shell uses. A window-scoped workspace that opens a Host without adding a
// main Tab (the Dropdown Panel's Quake workspace) uses it, so both paths keep
// one transport mapping.
func HostSessionSpec(host domainvault.Host) appsession.StartSpec {
	label := host.Label
	if label == "" {
		label = host.Hostname
	}
	plan := hostSessionTransport(host)
	return appsession.StartSpec{
		Kind:           plan.kind,
		Label:          label,
		StartupCommand: host.StartupCommand,
		HostID:         host.ID,
		Hostname:       host.Hostname,
		Port:           host.Port,
		Username:       host.Username,
		X11Forwarding:  plan.kind == appsession.KindSSH && host.X11Enabled(),
		NetworkDevice:  plan.kind == appsession.KindSSH && host.IsNetworkDevice(),
		AutoAttachTmux: plan.tmux,
		Telnet:         plan.telnet,
		Mosh:           plan.mosh,
		ET:             plan.et,
		VNC:            plan.vnc,
		// Input compatibility is local-only: the Session translates typed keys
		// before they reach this transport.
		InputCompatibility: host.InputCompatibility(),
		ResumeKey:          plan.newResumeKey(),
		Reconnect:          plan.reconnect,
	}
}

// hostTransportPlan is the transport, restore descriptor kind, endpoint, and
// reconnect policy one saved Host maps onto.
type hostTransportPlan struct {
	kind       appsession.Kind
	descriptor domainworkspace.SessionKind
	telnet     *domaintelnet.Config
	mosh       *domoshmodel.Config
	et         *domainetmodel.Config
	vnc        *domainvnc.SessionConfig
	reconnect  *appsession.ReconnectPolicy
	// tmux carries the Host's opt-in auto-attach setting. Only a plain SSH
	// Session may use it; Mosh and ET keep their own persistence.
	tmux bool
}

// newResumeKey returns a fresh opaque locator for an ET Session's saved
// credentials, or "" for a transport without resume state. The same value goes
// into the StartSpec and the persisted descriptor.
func (p hostTransportPlan) newResumeKey() string {
	if p.kind == appsession.KindET {
		return newETResumeKey()
	}
	return ""
}

// hostSessionTransport maps a saved Host's protocol onto the Session
// transport, its restore descriptor, and its reconnect policy. A Telnet Host
// gets the Telnet endpoint and a policy that permits the manual Reconnect
// action without automatically retrying a dropped socket, matching the
// original's SSH-only auto-reconnect.
func hostSessionTransport(host domainvault.Host) hostTransportPlan {
	// The protocol descriptor owns transport selection; this switch only
	// builds the kind-specific start payload and restore descriptor.
	switch host.Protocol.SessionTransport() {
	case domainprotocol.TransportLocal:
		// A saved local-shell Host opens a shell on this machine, exactly like
		// the original's local Hosts. Its label is the Tab's connection label.
		return hostTransportPlan{
			kind:       appsession.KindLocal,
			descriptor: domainworkspace.SessionLocal,
		}
	case domainprotocol.TransportTelnet:
		cfg := domaintelnet.Config{Hostname: host.Hostname, Port: host.Port}.Normalized()
		return hostTransportPlan{
			kind:       appsession.KindTelnet,
			descriptor: domainworkspace.SessionTelnet,
			telnet:     &cfg,
			reconnect:  telnetReconnectPolicy(),
		}
	case domainprotocol.TransportMosh:
		return hostTransportPlan{
			kind:       appsession.KindMosh,
			descriptor: domainworkspace.SessionMosh,
			mosh:       moshConfig(host),
			reconnect:  &appsession.ReconnectPolicy{},
		}
	case domainprotocol.TransportET:
		return hostTransportPlan{
			kind:       appsession.KindET,
			descriptor: domainworkspace.SessionET,
			et:         etConfig(host),
			reconnect:  &appsession.ReconnectPolicy{},
		}
	case domainprotocol.TransportVNC:
		// VNC follows the SSH reconnect policy: a dropped pixel stream retries,
		// but an authentication failure never does.
		return hostTransportPlan{
			kind:       appsession.KindVNC,
			descriptor: domainworkspace.SessionVNC,
			vnc:        vncConfig(host),
			reconnect:  &appsession.ReconnectPolicy{},
		}
	}
	// A dropped SSH transport retries every five seconds under the original
	// reason predicate. Local shells have no policy.
	return hostTransportPlan{
		kind:       appsession.KindSSH,
		descriptor: domainworkspace.SessionSSH,
		reconnect:  &appsession.ReconnectPolicy{},
		tmux:       host.AutoAttachTmuxEnabled(),
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

// vncConfig is the kind-selected start section for a VNC Host. The password is
// resolved by the app from the Host's own secret slot; this mapping carries the
// endpoint, the non-secret viewer configuration, and the route references.
func vncConfig(host domainvault.Host) *domainvnc.SessionConfig {
	configuration := domainvnc.DefaultConfig()
	if host.VNC != nil {
		configuration = *domainvnc.NormalizeConfig(host.VNC)
	}
	cfg := domainvnc.SessionConfig{
		Hostname:      host.Hostname,
		Port:          host.Port,
		Username:      host.Username,
		Configuration: configuration,
		Route: domainvnc.Route{
			Jumps:            host.HostChain.IDs(),
			ProxyProfileID:   host.ProxyProfileID,
			NetworkProfileID: host.NetworkProfileID,
			DirectProxy:      host.DirectProxy,
		},
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

// attachAutoLogin fills the expect engine for a console transport. A serial
// profile carries its own non-secret rules; a Telnet Host reuses its Vault
// login. Every other Kind authenticates before its shell exists and ignores
// the field. A resolver that finds nothing leaves auto-login off rather than
// failing the connection.
func (s *State) attachAutoLogin(ctx context.Context, spec *appsession.StartSpec, host domainvault.Host, password string) {
	switch spec.Kind {
	case appsession.KindSerial:
		if s.serialAutoLogin != nil && spec.Serial != nil {
			spec.AutoLogin = s.serialAutoLogin(ctx, *spec.Serial)
		}
	case appsession.KindTelnet:
		if s.telnetAutoLogin != nil {
			spec.AutoLogin = s.telnetAutoLogin(ctx, host, password)
		}
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
		spec := appsession.StartSpec{
			Kind:   appsession.KindSerial,
			Label:  label,
			Serial: &serialConfig,
		}
		s.attachAutoLogin(ctx, &spec, domainvault.Host{}, "")
		session, err := s.sessions.Open(ctx, spec)
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
		if outcome.browser {
			if opener, ok := surface.(BrowserOpener); ok {
				_ = opener.OpenBrowser()
			}
		}
		s.tabs = append(s.tabs, tab)
		s.activateTab(len(s.tabs) - 1)
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
	// A tmux detach ends the local wrapper cleanly while the remote session
	// keeps running. Removing the Pane here, on the UI thread, is what makes
	// detach close the Tab without an error.
	s.closeDetachedSessions()
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
