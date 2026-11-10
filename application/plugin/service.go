package plugin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// Service names and stable method ids come from the generated IDL. The
// application aliases them so host-service call sites do not repeat strings.
const (
	ServiceStorage       = domainpluginabi.ServiceStorage
	ServiceClipboard     = domainpluginabi.ServiceClipboard
	ServiceNotifications = domainpluginabi.ServiceNotifications
	ServiceEvents        = domainpluginabi.ServiceEvents
	ServiceSettings      = domainpluginabi.ServiceSettings
	ServiceSystemPanel   = domainpluginabi.ServiceSystempanel
	ServiceUI            = domainpluginabi.ServiceUi
	ServiceFiles         = domainpluginabi.ServiceFiles
	ServiceHosts         = domainpluginabi.ServiceHosts
	ServiceSessions      = domainpluginabi.ServiceSessions
	ServiceTerminal      = domainpluginabi.ServiceTerminal
	// ServiceServices is the cross-plugin broker host module.
	ServiceServices = domainpluginabi.ServiceServices
	// External access services. The host brokers every socket, authentication
	// flow, trust decision, and secret lease; the guest never owns transport.
	ServiceNetwork = domainpluginabi.ServiceNetwork
	ServiceAuth    = domainpluginabi.ServiceAuth
	ServiceTrust   = domainpluginabi.ServiceTrust
	ServiceVault   = domainpluginabi.ServiceVault
)

// Usage: response codes. The first five are the closed service-level
// vocabulary from the host-service contract; fault reports an unexpected
// host-side failure that is neither a caller error nor a quota.
const (
	ServiceCodeOK       = "ok"
	ServiceCodeDenied   = "denied"
	ServiceCodeNotFound = "not-found"
	ServiceCodeConflict = "conflict"
	ServiceCodeInvalid  = "invalid"
	ServiceCodeLimit    = "limit"
	ServiceCodeFault    = "fault"
)

// ServiceError is a typed host-service failure. Every denial is a
// ServiceError with Code denied, so a guest or a first-party caller can tell
// a permission decision from an empty result or a silent no-op.
type ServiceError struct {
	Service    string
	Method     string
	Code       string
	Permission domainplugin.Permission
	Scope      domainplugin.Scope
	Message    string
	Cause      error
}

func (e *ServiceError) Error() string {
	where := e.Service + "." + e.Method
	if e.Code != "" {
		where += ": " + e.Code
	}
	if e.Message != "" {
		where += ": " + e.Message
	}
	return "plugin service " + where
}

// Unwrap preserves the authority sentinels (ErrPermissionNotGranted,
// ErrScopeRequired, ErrScopeDenied) and quota errors.
func (e *ServiceError) Unwrap() error { return e.Cause }

// IsDenied reports whether err is a typed denial.
func IsDenied(err error) bool {
	var service *ServiceError
	return errors.As(err, &service) && service.Code == ServiceCodeDenied
}

// ServiceUnavailableError reports a service whose host port was not wired.
// It is a host configuration failure, not a permission decision.
var ErrServiceUnavailable = errors.New("plugin service: host port is not configured")

// ServiceLimits bounds one host-service surface. A non-positive field means
// the contract default.
type ServiceLimits struct {
	// EventQueueDepth is the bounded queue size per subscription and event
	// type. Overflow drops the oldest event of that type and emits a gap.
	EventQueueDepth int
	// NotificationRate is the number of notifications one plugin may post
	// per NotificationWindow.
	NotificationRate int
	// NotificationWindow is the fixed rate-limit and deduplication window.
	NotificationWindow time.Duration
	// StorageValueBytes is the per-value storage quota.
	StorageValueBytes int
	// StoragePluginBytes is the per-plugin storage quota.
	StoragePluginBytes int64
	// FileHandles is the number of local file handles one fiber may hold open.
	FileHandles int
	// FileChunkBytes is the maximum bytes returned by one local or remote
	// range read, and the maximum bytes accepted by one range write.
	FileChunkBytes int
	// DirectoryEntries is the maximum entries one list call returns.
	DirectoryEntries int
	// TerminalObservations is the number of live terminal taps one fiber may
	// hold.
	TerminalObservations int
	// TerminalQueueBytes is the bounded lossy buffer behind one terminal tap.
	// When it overflows the oldest output is dropped and counted.
	TerminalQueueBytes int

	// NetworkConnections is the number of live external connections one
	// fiber may hold.
	NetworkConnections int
	// NetworkChunkBytes is the largest payload one network read or write call
	// accepts or returns.
	NetworkChunkBytes int
	// NetworkCreditBytes is the per-direction credit window reported to the
	// guest.
	NetworkCreditBytes int
	// NetworkDialTimeout caps one external dial. A guest may request a
	// shorter timeout; it can never extend this value.
	NetworkDialTimeout time.Duration
	// NetworkIdleTimeout closes an external connection that has had no
	// traffic for this long. It is enforced lazily before the next operation.
	NetworkIdleTimeout time.Duration
	// AuthChallenges is the number of live host-owned authentication flows
	// one fiber may hold.
	AuthChallenges int
	// SecretLeases is the number of live vault leases one fiber may hold.
	SecretLeases int
	// SecretLeaseTTL is the default and maximum lifetime of one vault lease.
	SecretLeaseTTL time.Duration
	// SecretValueBytes is the largest guest-deliverable secret one lease may
	// return.
	SecretValueBytes int
	// TrustConfirmTimeout bounds one host trust-confirmation prompt. It is
	// separate from the guest compute deadline because the host user, not the
	// plugin, decides how long the review takes.
	TrustConfirmTimeout time.Duration
}

// DefaultServiceLimits returns the contract defaults.
func DefaultServiceLimits() ServiceLimits {
	return ServiceLimits{
		EventQueueDepth:      MaxQueueDepth,
		NotificationRate:     5,
		NotificationWindow:   time.Minute,
		StorageValueBytes:    1 << 20,
		StoragePluginBytes:   32 << 20,
		FileHandles:          32,
		FileChunkBytes:       1 << 20,
		DirectoryEntries:     4096,
		TerminalObservations: 8,
		TerminalQueueBytes:   64 << 10,
		NetworkConnections:   DefaultNetworkConnections,
		NetworkChunkBytes:    StreamChunkSize,
		NetworkCreditBytes:   StreamInitialWindow,
		NetworkDialTimeout:   DefaultNetworkDialTimeout,
		NetworkIdleTimeout:   DefaultNetworkIdleTimeout,
		AuthChallenges:       DefaultAuthChallenges,
		SecretLeases:         DefaultSecretLeases,
		SecretLeaseTTL:       DefaultSecretLeaseTTL,
		SecretValueBytes:     DefaultSecretValueBytes,
		TrustConfirmTimeout:  DefaultTrustConfirmTimeout,
	}
}

// normalize replaces non-positive fields with the contract defaults.
func (l ServiceLimits) normalize() ServiceLimits {
	defaults := DefaultServiceLimits()
	if l.EventQueueDepth <= 0 {
		l.EventQueueDepth = defaults.EventQueueDepth
	}
	if l.NotificationRate <= 0 {
		l.NotificationRate = defaults.NotificationRate
	}
	if l.NotificationWindow <= 0 {
		l.NotificationWindow = defaults.NotificationWindow
	}
	if l.StorageValueBytes <= 0 {
		l.StorageValueBytes = defaults.StorageValueBytes
	}
	if l.StoragePluginBytes <= 0 {
		l.StoragePluginBytes = defaults.StoragePluginBytes
	}
	if l.FileHandles <= 0 {
		l.FileHandles = defaults.FileHandles
	}
	if l.FileChunkBytes <= 0 {
		l.FileChunkBytes = defaults.FileChunkBytes
	}
	if l.DirectoryEntries <= 0 {
		l.DirectoryEntries = defaults.DirectoryEntries
	}
	if l.TerminalObservations <= 0 {
		l.TerminalObservations = defaults.TerminalObservations
	}
	if l.TerminalQueueBytes <= 0 {
		l.TerminalQueueBytes = defaults.TerminalQueueBytes
	}
	if l.NetworkConnections <= 0 {
		l.NetworkConnections = defaults.NetworkConnections
	}
	if l.NetworkChunkBytes <= 0 {
		l.NetworkChunkBytes = defaults.NetworkChunkBytes
	}
	if l.NetworkCreditBytes <= 0 {
		l.NetworkCreditBytes = defaults.NetworkCreditBytes
	}
	if l.NetworkDialTimeout <= 0 {
		l.NetworkDialTimeout = defaults.NetworkDialTimeout
	}
	if l.NetworkIdleTimeout <= 0 {
		l.NetworkIdleTimeout = defaults.NetworkIdleTimeout
	}
	if l.AuthChallenges <= 0 {
		l.AuthChallenges = defaults.AuthChallenges
	}
	if l.SecretLeases <= 0 {
		l.SecretLeases = defaults.SecretLeases
	}
	if l.SecretLeaseTTL <= 0 {
		l.SecretLeaseTTL = defaults.SecretLeaseTTL
	}
	if l.SecretValueBytes <= 0 {
		l.SecretValueBytes = defaults.SecretValueBytes
	}
	if l.TrustConfirmTimeout <= 0 {
		l.TrustConfirmTimeout = defaults.TrustConfirmTimeout
	}
	return l
}

// ServiceDeps wires the consumer-owned host-service ports and the shared
// authority and audit surfaces. A nil clipboard, notification sink, host
// settings, workspace, or terminal port makes that service return a typed
// unavailable error; nil storage root keeps the KV in memory for tests.
type ServiceDeps struct {
	Authority     *Authority
	Audit         AuditSink
	Now           func() time.Time
	DataDir       func(pluginID string) string
	Clipboard     Clipboard
	Notifications NotificationSink
	HostSettings  HostSettings
	Panel         *SystemPanel
	Hosts         HostCatalog
	Sessions      SessionCatalog
	Terminal      TerminalCatalog
	RemoteFiles   RemoteFiles
	// External access ports. A nil Network, Auth, or Vault port makes the
	// corresponding service return a typed unavailable error. A nil TrustPins
	// keeps an in-memory, process-local pin store; a nil TrustConfirmer
	// leaves unknown material untrusted.
	Network        NetworkBroker
	TrustPins      TrustPinStore
	TrustConfirmer TrustConfirmer
	Auth           AuthBroker
	Vault          VaultFieldSource
	Limits         ServiceLimits
	// ReservedPaths are host filesystem paths that remain inaccessible even
	// when a granted file root contains them. The composition root names the
	// append-only audit directory here.
	ReservedPaths []string
	// Webview is the platform page port. A nil port leaves every plugin
	// webview unavailable; the security boundary is still owned here.
	Webview WebviewPort
	// WebviewDevTools enables the page inspector. The app sets it only in
	// development mode; production always keeps it off.
	WebviewDevTools bool
	// Locale is the host locale at startup. The empty value means "en".
	Locale string
	// LocaleFormatter writes numbers, dates, and relative times, selects CLDR
	// plural categories, and reports writing direction for plugin text. Nil
	// keeps the language-neutral defaults.
	LocaleFormatter LocaleFormatter
}

// HostServices owns the host-side implementation of the baseline services.
// It is safe for concurrent use. One ServiceSession is opened per fiber and
// closed when the fiber unloads, traps, or is recycled.
type HostServices struct {
	authority         *Authority
	audit             AuditSink
	now               func() time.Time
	storage           *PrivateStorage
	clipboard         Clipboard
	notifications     *NotificationService
	notificationsSink NotificationSink
	bus               *eventBus
	hostSettings      HostSettings
	panel             *SystemPanel
	hostCatalog       HostCatalog
	sessionCatalog    SessionCatalog
	terminalCatalog   TerminalCatalog
	remoteFiles       RemoteFiles
	fileLocks         *fileLockRegistry
	reserved          []string
	network           NetworkBroker
	trustPins         TrustPinStore
	trustConfirmer    TrustConfirmer
	auth              AuthBroker
	vault             VaultFieldSource
	webview           WebviewPort
	webviewDevTools   bool
	limits            ServiceLimits

	localeMu          sync.RWMutex
	locale            string
	formatter         LocaleFormatter
	catalogGeneration uint64

	missingMu sync.Mutex
	missing   map[domainplugin.FiberID]map[string]uint64

	mu       sync.Mutex
	sessions map[domainplugin.FiberID]*ServiceSession
	closed   bool
}

// NewHostServices returns the baseline services over the supplied ports.
func NewHostServices(deps ServiceDeps) *HostServices {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	audit := deps.Audit
	if audit == nil {
		audit = NopAudit{}
	}
	limits := deps.Limits.normalize()
	pins := deps.TrustPins
	if pins == nil {
		// Trust stays fail-closed without persistence: every unknown material
		// asks the confirmer again, and a nil confirmer leaves it unknown.
		pins = &MemoryTrustPinStore{}
	}
	h := &HostServices{
		authority:         deps.Authority,
		audit:             audit,
		now:               now,
		storage:           NewPrivateStorage(deps.DataDir, limits.StorageValueBytes, limits.StoragePluginBytes),
		clipboard:         deps.Clipboard,
		notificationsSink: deps.Notifications,
		hostSettings:      deps.HostSettings,
		panel:             deps.Panel,
		hostCatalog:       deps.Hosts,
		sessionCatalog:    deps.Sessions,
		terminalCatalog:   deps.Terminal,
		remoteFiles:       deps.RemoteFiles,
		fileLocks:         newFileLockRegistry(),
		reserved:          canonicalReservedPaths(deps.ReservedPaths),
		network:           deps.Network,
		trustPins:         pins,
		trustConfirmer:    deps.TrustConfirmer,
		auth:              deps.Auth,
		vault:             deps.Vault,
		webview:           deps.Webview,
		webviewDevTools:   deps.WebviewDevTools,
		limits:            limits,
		locale:            normalizeLocale(deps.Locale),
		formatter:         deps.LocaleFormatter,
		catalogGeneration: 1,
		missing:           make(map[domainplugin.FiberID]map[string]uint64),
		sessions:          make(map[domainplugin.FiberID]*ServiceSession),
		bus:               newEventBus(limits.EventQueueDepth),
	}
	if h.formatter == nil {
		h.formatter = defaultLocaleFormatter{}
	}
	h.notifications = newNotificationService(h)
	// An authority epoch change revokes every stateful handle issued under
	// the old epoch. Event subscriptions keep queued events, so they must be
	// destroyed here rather than only failing the next permission check.
	if deps.Authority != nil {
		deps.Authority.SetEpochHook(h.invalidateSubject)
	}
	return h
}

// invalidateSubject destroys every stateful handle owned by subject. It runs
// after the authority epoch advances, so a re-granted permission never revives
// an old subscription or delivers its pre-revocation queue.
func (h *HostServices) invalidateSubject(subject AuthoritySubject) {
	if h == nil {
		return
	}
	h.mu.Lock()
	sessions := make([]*ServiceSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		if session.subject == subject {
			sessions = append(sessions, session)
		}
	}
	h.mu.Unlock()
	for _, session := range sessions {
		session.revokeHandles()
	}
}

// Limits returns the effective service limits.
func (h *HostServices) Limits() ServiceLimits { return h.limits }

// Authorize is the single capability check for host-side callers (UI, MCP, and
// cross-plugin services). It delegates to the same default-deny authority and
// structured scopes the guest host-service boundary uses.
func (h *HostServices) Authorize(caller Caller, permission domainplugin.Permission,
	scope domainplugin.Scope) error {
	if h == nil || h.authority == nil {
		return fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
	}
	return h.authority.Authorize(caller, permission, scope)
}

// Open creates the service session for one fiber. It is called before the
// module is instantiated; BindHandles supplies the instance handle table once
// the runtime has created it. The optional catalogs are the package's
// i18n/<bcp47>.toml tables loaded at activation.
func (h *HostServices) Open(ref PluginRef, fiber domainplugin.FiberID,
	settings map[string]any, catalogs ...domainplugin.Catalogs) *ServiceSession {
	if h == nil {
		return nil
	}
	var declared domainplugin.Catalogs
	if len(catalogs) > 0 {
		declared = catalogs[0].Clone()
	}
	locale := h.Locale()
	direction := h.directionFor(locale)
	ui := newUISession(h.now)
	ui.initEnvironment(UIEnvironment{Locale: locale, Direction: direction})
	workCtx, cancelWork := context.WithCancel(context.Background())
	session := &ServiceSession{
		host:              h,
		ref:               ref,
		fiber:             fiber,
		subject:           SubjectForRef(ref),
		workCtx:           workCtx,
		cancelWork:        cancelWork,
		settings:          cloneSettings(settings),
		events:            make(map[Handle]*eventSubscription),
		ui:                ui,
		files:             make(map[Handle]*localFileHandle),
		terminals:         make(map[Handle]*terminalTap),
		network:           make(map[Handle]*networkConnection),
		leases:            make(map[Handle]*secretLease),
		challenges:        make(map[string]*authChallenge),
		createdSessions:   make(map[string]struct{}),
		locale:            locale,
		catalogs:          declared,
		catalogGeneration: h.CatalogGeneration(),
		createdAt:         h.now().UTC(),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.sessions[fiber] = session
	h.mu.Unlock()
	return session
}

// BindHandles attaches the instance's generation-tagged table to the session.
func (s *ServiceSession) BindHandles(handles *HandleTable) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.handles = handles
	s.mu.Unlock()
}

// Close releases the session and every event subscription it owns.
func (h *HostServices) Close(fiber domainplugin.FiberID) {
	if h == nil {
		return
	}
	h.mu.Lock()
	session := h.sessions[fiber]
	delete(h.sessions, fiber)
	h.mu.Unlock()
	if session != nil {
		session.close()
	}
}

// Session returns the live session for one fiber.
func (h *HostServices) Session(fiber domainplugin.FiberID) (*ServiceSession, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[fiber]
	return session, ok
}

// CloseAll releases every session. The manager calls it after the container
// has unloaded its fibers.
func (h *HostServices) CloseAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	sessions := make([]*ServiceSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessions = make(map[domainplugin.FiberID]*ServiceSession)
	h.closed = true
	h.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

// ServiceSession is the host-service view of one fiber. Every call checks the
// default-deny Authority at the boundary and appends one audit event.
type ServiceSession struct {
	host      *HostServices
	ref       PluginRef
	fiber     domainplugin.FiberID
	subject   AuthoritySubject
	createdAt time.Time

	mu             sync.Mutex
	handles        *HandleTable
	settings       map[string]any
	events         map[Handle]*eventSubscription
	ui             *UISession
	webview        *WebviewSession
	files          map[Handle]*localFileHandle
	terminals      map[Handle]*terminalTap
	network        map[Handle]*networkConnection
	leases         map[Handle]*secretLease
	challenges     map[string]*authChallenge
	pendingNetwork int
	pendingLeases  int
	pendingAuth    int
	// handleEpoch advances whenever every stateful handle is revoked. A slow
	// dial or lease fetch captures it so a revoked handle is never inserted
	// after the sweep.
	handleEpoch       uint64
	createdSessions   map[string]struct{}
	broker            *ServiceBroker
	locale            string
	catalogs          domainplugin.Catalogs
	catalogGeneration uint64
	// workCtx is canceled on revocation and on close. Host work that outlives
	// one guest compute slice (trust confirmation, network dial, auth broker)
	// derives from it so an unload or revocation stops the work even when the
	// guest call itself is not canceled.
	workCtx    context.Context
	cancelWork context.CancelFunc
	closed     bool
}

// NativeUI returns the native UI session for this fiber. It is non-nil even
// before a tree exists; calls fail with a typed status once the session
// closes.
func (s *ServiceSession) NativeUI() *UISession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ui
}

// Webview returns the sandboxed-webview half of this fiber, or nil when the
// host has no webview port attached.
func (s *ServiceSession) Webview() *WebviewSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.webview
}

// AttachWebview enables sandboxed pages for this fiber over the plugin's
// package directory. The container calls it after opening the service
// session; a nil filesystem leaves previews unavailable but keeps the typed
// error path.
func (s *ServiceSession) AttachWebview(files fs.FS) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed || s.webview != nil || s.host == nil || s.host.webview == nil {
		s.mu.Unlock()
		return
	}
	locale := s.locale
	ui := s.ui
	s.mu.Unlock()
	direction := UIDirectionLTR
	if ui != nil {
		env := ui.Environment()
		direction = env.Direction
	}
	s.mu.Lock()
	if s.closed || s.webview != nil {
		s.mu.Unlock()
		return
	}
	s.webview = newWebviewSession(s, s.host.webview, files, s.host.webviewDevTools, locale, direction)
	s.mu.Unlock()
}

// SetBroker attaches the cross-plugin broker to a session. The container
// calls it before the module is instantiated, so the services host module
// routes every call through the same broker.
func (s *ServiceSession) SetBroker(broker *ServiceBroker) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.broker = broker
	s.mu.Unlock()
}

// Broker returns the session's cross-plugin broker.
func (s *ServiceSession) Broker() *ServiceBroker {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broker
}

// Ref returns the owning plugin identity.
func (s *ServiceSession) Ref() PluginRef { return s.ref }

// FiberID returns the owning fiber.
func (s *ServiceSession) FiberID() domainplugin.FiberID { return s.fiber }

// Config returns a copy of the effective settings handed to the guest.
func (s *ServiceSession) Config() map[string]any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettings(s.settings)
}

// UpdateConfig replaces the effective configuration for a live session.
func (s *ServiceSession) UpdateConfig(values map[string]any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.settings = cloneSettings(values)
	s.mu.Unlock()
}

func (s *ServiceSession) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	ui := s.ui
	webview := s.webview
	s.mu.Unlock()
	if ui != nil {
		ui.close()
	}
	if webview != nil {
		webview.close()
	}
	s.revokeHandles()
}

// revokeHandles invalidates every stateful handle and discards its host-side
// state. It runs on revocation and on unload: a handle released here can never
// be reused, even after the permission is granted again. Local file handles
// are closed (releasing their advisory locks), terminal taps are destroyed,
// and fiber-created Session exemptions from sessions.connect are forgotten.
func (s *ServiceSession) revokeHandles() {
	if s == nil {
		return
	}
	s.mu.Lock()
	subs := s.events
	files := s.files
	terminals := s.terminals
	network := s.network
	leases := s.leases
	challenges := s.challenges
	handles := s.handles
	ui := s.ui
	webview := s.webview
	s.webview = nil
	s.events = make(map[Handle]*eventSubscription)
	s.files = make(map[Handle]*localFileHandle)
	s.terminals = make(map[Handle]*terminalTap)
	s.network = make(map[Handle]*networkConnection)
	s.leases = make(map[Handle]*secretLease)
	s.challenges = make(map[string]*authChallenge)
	s.pendingNetwork = 0
	s.pendingLeases = 0
	s.pendingAuth = 0
	s.handleEpoch++
	s.createdSessions = make(map[string]struct{})
	// Cancel host work captured before this revocation and issue a fresh
	// lifecycle for calls that start after it.
	cancelWork := s.cancelWork
	s.workCtx, s.cancelWork = context.WithCancel(context.Background())
	s.mu.Unlock()
	if cancelWork != nil {
		cancelWork()
	}
	if ui != nil {
		ui.close()
	}
	if webview != nil {
		webview.close()
	}
	for handle, sub := range subs {
		if handles != nil {
			_ = handles.Release(handle)
		}
		sub.close()
	}
	for handle, file := range files {
		if handles != nil {
			_ = handles.Release(handle)
		}
		file.close()
	}
	for handle, tap := range terminals {
		if handles != nil {
			_ = handles.Release(handle)
		}
		tap.close()
	}
	// A revoked connection is closed before its leases are dropped, so a
	// connection-bound lease can never outlive the socket.
	for handle, conn := range network {
		if handles != nil {
			_ = handles.Release(handle)
		}
		conn.close()
	}
	for handle, lease := range leases {
		if handles != nil {
			_ = handles.Release(handle)
		}
		lease.clear()
	}
	for _, challenge := range challenges {
		challenge.cancel(s.host.auth)
	}
}

func (s *ServiceSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// check verifies one permission at the service boundary. A denial is audited
// and returned as a typed ServiceError; the authority sentinel remains in the
// chain for errors.Is.
func (s *ServiceSession) check(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope) error {
	return s.checkWith(service, method, permission, scope, func() error {
		return s.host.Authorize(Caller{Subject: s.subject, Kind: CallerGuest},
			permission, scope)
	})
}

// holds reports whether the guest currently has the permission, without
// auditing or constructing an error. Native UI dispatch uses it to choose
// between ui.native and ui.webview for the event pipeline both permissions
// can drive.
func (s *ServiceSession) holds(permission domainplugin.Permission,
	scope domainplugin.Scope) bool {
	if s == nil || s.host == nil {
		return false
	}
	return s.host.Authorize(Caller{Subject: s.subject, Kind: CallerGuest},
		permission, scope) == nil
}

// checkNetwork verifies one concrete endpoint against a network grant. Unlike
// the generic exact-scope check, a grant may name a port list, so a dial for
// one granted port succeeds without granting the whole Host.
func (s *ServiceSession) checkNetwork(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope) error {
	return s.checkWith(service, method, permission, scope, func() error {
		return s.host.authority.CheckNetwork(s.subject, permission, scope)
	})
}

func (s *ServiceSession) checkWith(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope,
	verify func() error) error {
	if s == nil {
		return &ServiceError{Service: service, Method: method, Code: ServiceCodeFault,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	if s.isClosed() {
		return &ServiceError{Service: service, Method: method, Code: ServiceCodeInvalid,
			Message: "the plugin instance is closed"}
	}
	if err := verify(); err != nil {
		s.record(service, method, permission, scope, ServiceCodeDenied)
		return &ServiceError{
			Service: service, Method: method, Code: ServiceCodeDenied,
			Permission: permission, Scope: scope, Message: err.Error(), Cause: err,
		}
	}
	return nil
}

// workContext joins a caller context with the session lifecycle. Host work
// such as a trust confirmation, a network dial, or an auth broker call may
// outlive one guest compute slice, so it must not inherit that slice's
// deadline. It still stops when the caller cancels, the fiber unloads, or the
// authority revokes. The returned cancel function releases both observers.
func (s *ServiceSession) workContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return context.WithCancel(ctx)
	}
	s.mu.Lock()
	lifecycle := s.workCtx
	s.mu.Unlock()
	if lifecycle == nil {
		return context.WithCancel(ctx)
	}
	joined, cancel := context.WithCancel(ctx)
	stopCaller := context.AfterFunc(ctx, cancel)
	stopLifecycle := context.AfterFunc(lifecycle, cancel)
	return joined, func() {
		stopCaller()
		stopLifecycle()
		cancel()
	}
}

// handleEpochSnapshot returns the current revocation generation. Host work
// that waits on a broker captures it first and rechecks it afterwards.
func (s *ServiceSession) handleEpochSnapshot() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handleEpoch
}

// capabilityRevoked reports an unload, close, or revocation observed after a
// slow host port call returned. It checks both the session generation and the
// exact grant, so a grant replaced without an epoch change is caught too.
func (s *ServiceSession) capabilityRevoked(epoch uint64,
	permission domainplugin.Permission, scope domainplugin.Scope) error {
	if s == nil {
		return &CapabilityRevokedError{Permission: permission}
	}
	s.mu.Lock()
	closed := s.closed
	current := s.handleEpoch == epoch
	s.mu.Unlock()
	if closed || !current {
		return &CapabilityRevokedError{Subject: s.subject, Permission: permission}
	}
	if s.host == nil || s.host.authority == nil {
		return &CapabilityRevokedError{Subject: s.subject, Permission: permission}
	}
	if err := s.host.authority.Check(s.subject, permission, scope); err != nil {
		return &CapabilityRevokedError{Subject: s.subject, Permission: permission}
	}
	return nil
}

// networkCapabilityRevoked is capabilityRevoked for the network.dial
// capability. Network authorization deliberately accepts a grant that lists
// several ports, so the recheck uses CheckNetwork with the connection's host,
// protocol, TLS policy, and single dialed port instead of exact Scope.Equal.
// An exact check would report a live multi-port grant as revoked.
func (s *ServiceSession) networkCapabilityRevoked(epoch uint64,
	scope domainplugin.Scope) error {
	permission := domainplugin.PermissionNetworkDial
	if s == nil {
		return &CapabilityRevokedError{Permission: permission}
	}
	s.mu.Lock()
	closed := s.closed
	current := s.handleEpoch == epoch
	s.mu.Unlock()
	if closed || !current {
		return &CapabilityRevokedError{Subject: s.subject, Permission: permission}
	}
	if s.host == nil || s.host.authority == nil {
		return &CapabilityRevokedError{Subject: s.subject, Permission: permission}
	}
	if err := s.host.authority.CheckNetwork(s.subject, permission, scope); err != nil {
		return &CapabilityRevokedError{Subject: s.subject, Permission: permission}
	}
	return nil
}

// revokedServiceError builds the capability-revoked status an in-flight call
// returns after its authority was revoked.
func revokedServiceError(service, method string, subject AuthoritySubject,
	permission domainplugin.Permission, scope domainplugin.Scope,
	problem string) *ServiceError {
	return &ServiceError{
		Service: service, Method: method, Code: ServiceCodeInvalid,
		Permission: permission, Scope: scope, Message: problem,
		Cause: &CapabilityRevokedError{Subject: subject, Permission: permission},
	}
}

// record appends one audit row for a service access. No payload content is
// ever recorded.
func (s *ServiceSession) record(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope, result string) {
	if s == nil || s.host.audit == nil {
		return
	}
	event := domainplugin.AuditEvent{
		Time:       s.host.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   s.ref.ID,
		Source:     s.ref.Source,
		Version:    s.ref.Version,
		Permission: permission,
		Reason:     domainplugin.ServiceAuditReason(service, method, result),
	}
	if !scope.IsZero() {
		copy := scope
		event.Scope = &copy
	}
	s.host.audit.Record(event)
}

// fail records a failed access and returns the same error. It is used for
// limits, not-found, and host failures that are not permission decisions.
func (s *ServiceSession) fail(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope, result string,
	err error) error {
	s.record(service, method, permission, scope, result)
	return err
}

// serviceError converts a storage, port, or validation failure into a typed
// service error without losing the underlying cause.
func serviceError(service, method string, permission domainplugin.Permission,
	scope domainplugin.Scope, err error) *ServiceError {
	if err == nil {
		return nil
	}
	var existing *ServiceError
	if errors.As(err, &existing) {
		return existing
	}
	code := ServiceCodeFault
	var invalid *StorageKeyError
	var missing *StorageNotFoundError
	var limit *LimitError
	var failure *workspaceFailure
	var handle *InvalidHandleError
	switch {
	case errors.As(err, &failure):
		code = failure.Code
	case errors.As(err, &missing):
		code = ServiceCodeNotFound
	case errors.As(err, &invalid):
		code = ServiceCodeInvalid
	case errors.As(err, &handle):
		code = ServiceCodeInvalid
	case errors.Is(err, os.ErrNotExist):
		code = ServiceCodeNotFound
	case errors.Is(err, os.ErrPermission):
		code = ServiceCodeDenied
	case errors.Is(err, os.ErrExist):
		code = ServiceCodeConflict
	case errors.As(err, &limit):
		code = ServiceCodeLimit
	}
	return &ServiceError{
		Service: service, Method: method, Code: code,
		Permission: permission, Scope: scope, Message: err.Error(), Cause: err,
	}
}

func cloneSettings(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// ServiceCall is one host-service invocation at the ABI boundary.
type ServiceCall struct {
	Service string
	Method  uint32
	Request []byte
}

// ServiceDispatcher executes one service call and returns the response
// payload. An error is encoded by the session as a service-level envelope;
// the dispatcher itself only reports host failures.
type ServiceDispatcher interface {
	Call(ctx context.Context, call ServiceCall) []byte
}

// Dispatcher returns the ABI-facing dispatcher for this session.
func (s *ServiceSession) Dispatcher() ServiceDispatcher { return serviceDispatcher{session: s} }

// Dispatcher returns a dispatcher for a service session; a nil session makes
// every call return a typed unavailable envelope.
func (h *HostServices) Dispatcher(session *ServiceSession) ServiceDispatcher {
	return serviceDispatcher{session: session}
}

// AbiServiceCall builds a ServiceCall value for tests and adapters.
func AbiServiceCall(service string, method uint32, request []byte) ServiceCall {
	return ServiceCall{Service: service, Method: method, Request: request}
}
