package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// ManifestSource decodes plugin.toml from a directory. The TOML adapter lives
// outside the application layer so this package stays dependency-free.
type ManifestSource interface {
	Load(directory string) (domainplugin.Manifest, error)
}

// UnknownFiberError reports an unload for an identity the container does not
// know.
type UnknownFiberError struct {
	ID domainplugin.FiberID
}

func (e *UnknownFiberError) Error() string {
	return fmt.Sprintf("plugin: unknown fiber %q", e.ID)
}

// MissingInjectionError reports a required injected service with no provider
// in the consumer's ancestor isolate scopes. The fiber is PENDING with the
// same service list.
type MissingInjectionError struct {
	Services []string
}

func (e *MissingInjectionError) Error() string {
	return "plugin: missing injected service(s): " + strings.Join(e.Services, ", ")
}

// ModuleIntegrityError reports an installed module whose bytes no longer match
// the manifest's sha256. Activation fails closed so tampering after install is
// caught before any guest code runs.
type ModuleIntegrityError struct {
	// Path is the manifest-relative module path.
	Path string
	// Want is the manifest sha256, lowercase.
	Want string
	// Got is the sha256 of the bytes on disk.
	Got string
}

func (e *ModuleIntegrityError) Error() string {
	return fmt.Sprintf("plugin: module %s sha256 is %s, want %s", e.Path, e.Got, e.Want)
}

// SettingsInheritance carries the values a group and a previous activation
// contribute to one plugin's configuration. The resolver gives the manifest's
// declared defaults precedence over Group defaults and lets Volatile values
// survive a reload.
type SettingsInheritance struct {
	// Group holds the defaults of the loader group the instance belongs to.
	Group map[string]any
	// Volatile holds the previous activation's values for settings fields
	// marked volatile. Reload preserves only these fields and the stored
	// configuration.
	Volatile map[string]any
}

// SettingsResolver returns the effective host-side configuration for one
// validated manifest. A *domainplugin.SettingsValidationError leaves the fiber
// PENDING with a readable reason instead of FAILED.
type SettingsResolver func(ctx context.Context, manifest domainplugin.Manifest,
	inherited SettingsInheritance) (map[string]any, error)

// RequiredPermissionChecker verifies one required permission at activation.
// A non-nil error leaves the fiber PENDING with a reason and no instance; the
// manager wires the host Authority here.
type RequiredPermissionChecker func(ref PluginRef, request domainplugin.PermissionRequest) error

// LoadSpec describes one Plugin Instance to load. Instance zero asks the
// container for the next free instance number in the group; an explicit
// number never silently replaces a live instance.
type LoadSpec struct {
	// Group is the loader namespace. An unknown group is created implicitly as
	// a root group with no defaults.
	Group string
	// Directory is the unpacked plugin directory.
	Directory string
	// Instance is the instance number, or zero for the next free one.
	Instance int
	// Development labels an unpacked directory loaded in developer mode.
	Development bool
	// Source labels the package source.
	Source domainplugin.Source
}

// Option customizes a Container.
type Option func(*Container)

// WithHostServices attaches the host-service implementation. Every fiber opens
// one service session before the module is instantiated and closes it when the
// fiber is unloaded, recycled, or fails to start.
func WithHostServices(services *HostServices) Option {
	return func(c *Container) { c.services = services }
}

// WithSettingsResolver supplies the schema-validated plugin configuration used
// for activation and for the guest's host-service session.
func WithSettingsResolver(resolve SettingsResolver) Option {
	return func(c *Container) { c.settings = resolve }
}

// WithCatalogSource loads a package's i18n/<bcp47>.toml catalogs at
// activation. Install validation runs the same loader, so a package that
// passed install cannot fail activation for catalog shape; a development
// directory is validated here for the first time. Without a source, host
// contribution keys fall back to their literals and the i18n service returns
// the key.
func WithCatalogSource(source CatalogSource) Option {
	return func(c *Container) { c.catalogs = source }
}

// WithLanguageHost attaches the editor language-service router. A manifest
// that declares a language.provider contribution requires it; every provider
// is registered when the fiber becomes ACTIVE and removed on unload.
func WithLanguageHost(host *LanguageHost) Option {
	return func(c *Container) { c.language = host }
}

// WithRequiredPermissions installs the activation-time permission check. The
// manager wires its Authority so an ungranted required permission leaves the
// fiber PENDING instead of failing it.
func WithRequiredPermissions(check RequiredPermissionChecker) Option {
	return func(c *Container) { c.requiredPermissions = check }
}

// WithFeedback receives visible output written through the host output
// import. The app turns it into a notification or dialog; tests record it.
func WithFeedback(fn func(PluginRef, string)) Option {
	return func(c *Container) { c.feedback = fn }
}

// WithLogf receives guest log lines. The container also keeps a bounded
// per-fiber ring for diagnostics.
func WithLogf(fn func(PluginRef, LogLevel, string)) Option {
	return func(c *Container) { c.logf = fn }
}

// WithClock overrides the clock used by the log rate limiter, fault history,
// and call timings. Tests use it to exercise quotas deterministically;
// production leaves the default.
func WithClock(now func() time.Time) Option {
	return func(c *Container) {
		if now != nil {
			c.now = now
		}
	}
}

// WithLifecycle observes every lifecycle state change. The reason is empty
// for ACTIVE and carries the readable failure or pending reason otherwise.
// Callbacks run without container or fiber locks held.
func WithLifecycle(fn func(PluginRef, domainplugin.State, string)) Option {
	return func(c *Container) { c.lifecycle = fn }
}

// WithFault observes a guest trap caught at a call boundary. A fault does not
// change the fiber state directly; the instance becomes unusable and the
// container recycles it. The fiber's fault history records traps and
// deadlines for diagnostics.
func WithFault(fn func(PluginRef, error)) Option {
	return func(c *Container) { c.fault = fn }
}

// WithSchedule arms the automatic-restart timer. It returns a stop function.
// Tests replace it with a deterministic scheduler; production uses
// time.AfterFunc.
func WithSchedule(schedule func(time.Duration, func()) func()) Option {
	return func(c *Container) {
		if schedule != nil {
			c.schedule = schedule
		}
	}
}

// WithRestartBackoff overrides the delay before an automatic restart. Values
// below MinRestartBackoff are raised to the minimum, so a host never spins on
// a crashing plugin.
func WithRestartBackoff(delay time.Duration) Option {
	return func(c *Container) { c.restartBackoff = normalizeRestartBackoff(delay) }
}

// WithUnloadTimeout bounds one fiber's teardown. Non-positive values keep
// DefaultUnloadTimeout.
func WithUnloadTimeout(timeout time.Duration) Option {
	return func(c *Container) {
		if timeout > 0 {
			c.unloadTimeout = timeout
		}
	}
}

// WithGlobalUnloadBudget bounds a whole Container.Close call, matching app
// quit. Non-positive values keep DefaultGlobalUnloadBudget.
func WithGlobalUnloadBudget(budget time.Duration) Option {
	return func(c *Container) {
		if budget > 0 {
			c.globalUnloadBudget = budget
		}
	}
}

// WithMCPToolsRegistrar publishes the manifest's declared MCP tools. Without a
// registrar a plugin may still declare tools, but none become callable: the
// host never invents a tool the client did not receive from this registry.
func WithMCPToolsRegistrar(registrar MCPToolRegistrar) Option {
	return func(c *Container) { c.mcpTools = registrar }
}

// WithConnectionProviders attaches the connection-provider host and the
// Session-facing registry. Host and registrar are required together: a manifest
// that declares a connection contribution fails activation without them,
// instead of activating a protocol no Session can open.
func WithConnectionProviders(host *ProviderHost, registrar ConnectionProviderRegistrar) Option {
	return func(c *Container) {
		c.connectionHost = host
		c.connectionRegistrar = registrar
	}
}

// WithDataProviders attaches the data-provider host. A manifest that declares
// a data-provider contribution fails activation without one, instead of
// activating a provider no profile can connect through.
func WithDataProviders(host *DataProviderHost) Option {
	return func(c *Container) { c.dataHost = host }
}

// WithMCPToolQueueDepth bounds the number of tool calls that may wait behind
// the one executing call on a plugin instance. Zero keeps MaxQueueDepth; tests
// use a small value to reach the busy error deterministically.
func WithMCPToolQueueDepth(depth int) Option {
	return func(c *Container) {
		if depth > 0 {
			c.toolQueueDepth = depth
		}
	}
}

// WithServiceAuthority installs the default-deny capability authority used by
// the cross-plugin service broker. Without one the broker skips permission
// checks; the manager always wires its authority.
func WithServiceAuthority(authority *Authority) Option {
	return func(c *Container) { c.serviceAuthority = authority }
}

// WithServiceAudit installs the audit sink for cross-plugin calls, streams,
// events, and dynamic registration. Payloads are never recorded.
func WithServiceAudit(sink AuditSink) Option {
	return func(c *Container) {
		if sink != nil {
			c.serviceAudit = sink
		}
	}
}

// WithServiceChanged observes a service binding change for one consumer. The
// manager turns it into the service.changed host event; the container itself
// does not depend on the host event bus.
func WithServiceChanged(fn func(PluginRef, ServiceChangedEvent)) Option {
	return func(c *Container) { c.serviceChanged = fn }
}

// Container owns the fibers for one runtime and one contribution registrar.
// Groups are namespaces, configuration boundaries, and isolate scopes; they
// never create a fiber of their own.
type Container struct {
	runtime        Runtime
	manifests      ManifestSource
	contributions  ContributionRegistrar
	mcpTools       MCPToolRegistrar
	toolQueueDepth int
	services       *HostServices
	language       *LanguageHost
	settings       SettingsResolver
	catalogs       CatalogSource
	feedback       func(PluginRef, string)
	logf           func(PluginRef, LogLevel, string)
	lifecycle      func(PluginRef, domainplugin.State, string)
	fault          func(PluginRef, error)
	now            func() time.Time

	requiredPermissions RequiredPermissionChecker
	schedule            func(time.Duration, func()) func()
	restartBackoff      time.Duration
	unloadTimeout       time.Duration
	globalUnloadBudget  time.Duration

	serviceAuthority *Authority
	serviceAudit     AuditSink
	serviceChanged   func(PluginRef, ServiceChangedEvent)
	broker           *ServiceBroker

	connectionHost      *ProviderHost
	connectionRegistrar ConnectionProviderRegistrar
	dataHost            *DataProviderHost

	mu            sync.Mutex
	fibers        map[domainplugin.FiberID]*Fiber
	groups        map[string]domainplugin.Group
	provides      map[string][]ServiceProvider
	restartTimers map[domainplugin.FiberID]func()
	closed        bool

	reevaluating atomic.Bool
}

// NewContainer returns a container. Runtime, manifests, and contributions
// are required; a nil port yields an error on load rather than a panic.
func NewContainer(runtime Runtime, manifests ManifestSource,
	contributions ContributionRegistrar, opts ...Option) *Container {
	c := &Container{
		runtime:        runtime,
		manifests:      manifests,
		contributions:  contributions,
		toolQueueDepth: MaxQueueDepth,
		now:            time.Now,
		schedule: func(delay time.Duration, fn func()) func() {
			timer := time.AfterFunc(delay, fn)
			return func() { timer.Stop() }
		},
		restartBackoff:     DefaultRestartBackoff,
		unloadTimeout:      DefaultUnloadTimeout,
		globalUnloadBudget: DefaultGlobalUnloadBudget,
		fibers:             make(map[domainplugin.FiberID]*Fiber),
		groups:             make(map[string]domainplugin.Group),
		provides:           make(map[string][]ServiceProvider),
		restartTimers:      make(map[domainplugin.FiberID]func()),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.schedule == nil {
		c.schedule = func(delay time.Duration, fn func()) func() {
			timer := time.AfterFunc(delay, fn)
			return func() { timer.Stop() }
		}
	}
	c.restartBackoff = normalizeRestartBackoff(c.restartBackoff)
	if c.unloadTimeout <= 0 {
		c.unloadTimeout = DefaultUnloadTimeout
	}
	if c.globalUnloadBudget <= 0 {
		c.globalUnloadBudget = DefaultGlobalUnloadBudget
	}
	if c.toolQueueDepth <= 0 {
		c.toolQueueDepth = MaxQueueDepth
	}
	c.broker = newServiceBroker(c, c.serviceAuthority, c.serviceAudit, c.now,
		c.serviceChanged)
	c.broker.schedule = c.schedule
	return c
}

// RuntimeStatus returns the engine state for diagnostics and the startup log.
// A runtime that cannot report its engine yields EngineUnknown rather than a
// guess.
func (c *Container) RuntimeStatus() RuntimeStatus {
	if c == nil || c.runtime == nil {
		return RuntimeStatus{Engine: EngineUnknown}
	}
	reporter, ok := c.runtime.(RuntimeStatusReporter)
	if !ok {
		return RuntimeStatus{Engine: EngineUnknown}
	}
	return reporter.RuntimeStatus()
}

// pendingKind identifies which activation precondition kept a fiber PENDING.
// Only an injection PENDING is re-evaluated automatically when the service
// graph changes; configuration and permission retries are explicit.
type pendingKind int

const (
	pendingNone pendingKind = iota
	pendingConfig
	pendingPermission
	pendingInjection
)

// effect is one host-authoritative disposer on a fiber's stack. It is
// idempotent even when the underlying disposer is not, so an unload, recycle,
// and forced quit can never remove the same host resource twice.
type effect struct {
	dispose Disposer
	once    sync.Once
	err     error
}

func (e *effect) run(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.once.Do(func() {
		if e.dispose != nil {
			e.err = e.dispose(ctx)
		}
	})
	return e.err
}

// Fiber is one Plugin Instance and its host-side effect stack.
type Fiber struct {
	id         domainplugin.FiberID
	ref        PluginRef
	manifest   domainplugin.Manifest
	manifestMu sync.RWMutex
	catalogsMu sync.RWMutex
	catalogs   domainplugin.Catalogs
	group      string
	instance   int
	directory  string

	// builtinSetup is non-nil for a compiled-in first-party plugin. It is set
	// before the fiber is published and never changes, so activation and
	// reload both take the direct-call path.
	builtinSetup BuiltinSetup

	mu          sync.Mutex
	state       domainplugin.State
	reason      string
	pendingKind pendingKind
	instanceRT  Instance
	settings    map[string]any
	disposers   []*effect
	logs        []string
	logLimiter  *LogLimiter
	droppedLogs int
	faults      []FaultRecord
	timings     map[string]*CallTiming
	memoryBytes int64
	generation  uint64

	missingInjections []string
	configErrors      []domainplugin.SettingsError

	restarts          int
	manualRestarts    int
	automaticRestarts int
	restartPending    bool

	now     func() time.Time
	onFault func(PluginRef, error)

	// callMu serializes guest calls with shutdown and close. Host callbacks
	// invoked synchronously during a guest call must be free to take mu, so
	// callMu is never held while mu is.
	callMu sync.Mutex

	// MCP tool calls: toolQueue admits at most toolQueueDepth waiters plus
	// the executing call; toolExec serializes execution with context-aware
	// cancellation; toolPending receives mcp_tool_result replies.
	toolQueue       chan struct{}
	toolQueueDepth  int
	toolExec        chan struct{}
	toolMu          sync.Mutex
	toolPending     map[uint32]*pendingToolCall
	nextToolRequest atomic.Uint32
}

// ID returns the fiber identity.
func (f *Fiber) ID() domainplugin.FiberID { return f.id }

// Directory returns the read-only package directory the plugin was loaded
// from. It is empty for a fiber that has no package on disk. The directory is
// fixed for the fiber's lifetime.
func (f *Fiber) Directory() string {
	if f == nil {
		return ""
	}
	return f.directory
}

// Group returns the loader namespace of the instance.
func (f *Fiber) Group() string { return f.group }

// Instance returns the instance number part of the identity.
func (f *Fiber) Instance() int { return f.instance }

// Ref returns the plugin identity.
func (f *Fiber) Ref() PluginRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ref
}

// Development reports whether this fiber was loaded from an unpacked
// directory in developer mode.
func (f *Fiber) Development() bool { return f.ref.Development }

// Manifest returns a copy of the manifest that created the fiber. A
// development reload replaces it with the newly decoded manifest.
func (f *Fiber) Manifest() domainplugin.Manifest {
	f.manifestMu.RLock()
	defer f.manifestMu.RUnlock()
	return f.manifest
}

// Catalogs returns a copy of the package catalogs loaded at activation.
func (f *Fiber) Catalogs() domainplugin.Catalogs {
	f.catalogsMu.RLock()
	defer f.catalogsMu.RUnlock()
	return f.catalogs.Clone()
}

// setCatalogs installs one activation's catalogs. A reload replaces them with
// the reloaded package's files.
func (f *Fiber) setCatalogs(catalogs domainplugin.Catalogs) {
	f.catalogsMu.Lock()
	f.catalogs = catalogs.Clone()
	f.catalogsMu.Unlock()
}

// manifestSnapshot reads the current manifest under the manifest lock.
func (f *Fiber) manifestSnapshot() domainplugin.Manifest {
	f.manifestMu.RLock()
	defer f.manifestMu.RUnlock()
	return f.manifest
}

// replaceManifest installs a freshly decoded development manifest without
// changing the fiber identity.
func (f *Fiber) replaceManifest(manifest domainplugin.Manifest) {
	f.manifestMu.Lock()
	f.manifest = manifest
	f.manifestMu.Unlock()
	f.mu.Lock()
	f.ref.Name = manifest.Name
	f.ref.Version = manifest.Version
	f.mu.Unlock()
}

// State returns the current lifecycle state.
func (f *Fiber) State() domainplugin.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Reason returns the readable failure or pending reason, if any.
func (f *Fiber) Reason() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reason
}

// MissingInjections returns the required services that kept the fiber
// PENDING.
func (f *Fiber) MissingInjections() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.missingInjections...)
}

// ConfigErrors returns the configuration field errors that kept the fiber
// PENDING.
func (f *Fiber) ConfigErrors() []domainplugin.SettingsError {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domainplugin.SettingsError(nil), f.configErrors...)
}

// EffectCount returns the number of host-side effects currently owned by the
// fiber.
func (f *Fiber) EffectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.disposers)
}

// Logs returns the bounded per-fiber log ring.
func (f *Fiber) Logs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

// DroppedLogs returns how many guest log lines were dropped by the rate
// limiter instead of being buffered.
func (f *Fiber) DroppedLogs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.droppedLogs
}

// appendLog buffers one line and reports whether the rate limiter accepted
// it. A rejected line is counted in droppedLogs and never reaches the ring or
// the host log hook, so neither can grow without bound.
func (f *Fiber) appendLog(level LogLevel, message string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.logLimiter.Take(now); err != nil {
		f.droppedLogs++
		return false
	}
	f.logs = append(f.logs, level.String()+": "+message)
	if len(f.logs) > MaxLogLines {
		f.logs = append([]string(nil), f.logs[len(f.logs)-MaxLogLines:]...)
	}
	return true
}

// LogLimiter is a fixed-window log-line rate limiter. Take returns a typed
// LimitError when the window is exhausted; the container drops the line and
// counts it in Fiber.DroppedLogs instead of letting the ring grow.
type LogLimiter struct {
	mu     sync.Mutex
	window time.Time
	count  int
	limit  int
}

// NewLogLimiter returns a limiter for limit lines per second. A non-positive
// limit means MaxLogsPerSecond.
func NewLogLimiter(limit int) *LogLimiter {
	if limit <= 0 {
		limit = MaxLogsPerSecond
	}
	return &LogLimiter{limit: limit}
}

// Take reports whether one log line fits in the current one-second window.
func (l *LogLimiter) Take(now time.Time) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limit <= 0 {
		l.limit = MaxLogsPerSecond
	}
	if l.window.IsZero() || now.Sub(l.window) >= time.Second || now.Before(l.window) {
		l.window = now
		l.count = 0
	}
	if l.count >= l.limit {
		return &LimitError{Resource: "log lines per second", Limit: l.limit}
	}
	l.count++
	return nil
}

// invoke dispatches one command while holding the call lock, so an unload
// cannot interleave with a call. It must not hold mu: host imports call back
// into appendLog synchronously while the guest call is in flight.
func (f *Fiber) invoke(ctx context.Context, commandID string) error {
	return f.withInstance(ctx, func(instance Instance) error {
		start := f.now()
		err := instance.Dispatch(ctx, commandID)
		f.observeCall("dispatch", start, err, instance)
		if err != nil {
			f.recordFault("dispatch", err)
		}
		return err
	})
}

// Dispatch runs one command on an ACTIVE fiber under the runtime's ordinary
// command deadline. It is exposed so the SDK fake host and the SDK CLI can
// drive a configured profile through the production lifecycle instead of
// owning a second instance by hand. Dispatch serializes with the lifecycle and
// every other guest call through the fiber call lock.
func (f *Fiber) Dispatch(ctx context.Context, commandID string) error {
	return f.invoke(ctx, commandID)
}

// withInstance runs one guest call while holding the call lock and the ACTIVE
// state check. A trap is reported to the fault observer; the caller decides
// whether to recycle.
func (f *Fiber) withInstance(ctx context.Context,
	call func(Instance) error) error {
	f.mu.Lock()
	instance := f.instanceRT
	active := f.state == domainplugin.StateActive && instance != nil
	f.mu.Unlock()
	if !active {
		return fmt.Errorf("%w: %s", ErrNotActive, f.id)
	}

	f.callMu.Lock()
	defer f.callMu.Unlock()

	// Re-check after taking the call lock: an unload may have completed
	// between the first check and here.
	f.mu.Lock()
	active = f.state == domainplugin.StateActive && f.instanceRT == instance
	f.mu.Unlock()
	if !active {
		return fmt.Errorf("%w: %s", ErrNotActive, f.id)
	}
	err := call(instance)
	if err != nil {
		var trap *TrapError
		if errors.As(err, &trap) && f.onFault != nil {
			f.onFault(f.ref, err)
		}
	}
	return err
}

// InvokeLanguage implements LanguageInvoker. It serializes with commands and
// the lifecycle through the same call lock, so there is still exactly one
// in-flight guest call per instance.
func (f *Fiber) InvokeLanguage(ctx context.Context,
	request LanguageRequest) ([]byte, error) {
	var response []byte
	err := f.withInstance(ctx, func(instance Instance) error {
		capable, ok := instance.(LanguageCapableInstance)
		if !ok {
			return &MissingExportError{Name: domainpluginabi.ExportLanguageRequest}
		}
		payload, err := capable.LanguageRequest(ctx, request)
		if err != nil {
			return err
		}
		response = payload
		return nil
	})
	return response, err
}

// CancelLanguage implements LanguageInvoker. It is best-effort: a request may
// still hold the call slot, in which case the guest learns cancellation from
// the language_cancelled poll instead.
func (f *Fiber) CancelLanguage(ctx context.Context, requestID uint64) error {
	return f.withInstance(ctx, func(instance Instance) error {
		capable, ok := instance.(LanguageCapableInstance)
		if !ok {
			return nil
		}
		return capable.CancelLanguage(ctx, requestID)
	})
}

// supportsLanguage reports whether the live instance exposes the optional
// language request export.
func (f *Fiber) supportsLanguage() bool {
	f.mu.Lock()
	instance := f.instanceRT
	f.mu.Unlock()
	capable, ok := instance.(LanguageCapableInstance)
	return ok && capable.SupportsLanguage()
}

// supportsProvider reports whether the live instance exposes the optional
// provider request export.
func (f *Fiber) supportsProvider() bool {
	f.mu.Lock()
	instance := f.instanceRT
	f.mu.Unlock()
	capable, ok := instance.(ProviderCapableInstance)
	return ok && capable.SupportsProvider()
}

// InvokeProvider implements ProviderInvoker. Provider calls share the fiber's
// call lock with dispatch and the lifecycle, so one instance still has at most
// one guest call in flight. A trap or deadline recycles the fiber like any
// other guest call, failing its provider Sessions and leasing siblings alone.
func (f *Fiber) InvokeProvider(ctx context.Context,
	request ProviderRequest) ([]byte, error) {

	var response []byte
	err := f.withInstance(ctx, func(instance Instance) error {
		capable, ok := instance.(ProviderCapableInstance)
		if !ok {
			return &MissingExportError{Name: domainpluginabi.ExportProviderRequest}
		}
		payload, err := capable.InvokeProvider(ctx, request)
		if err != nil {
			return err
		}
		response = payload
		return nil
	})
	return response, err
}

// SupportsProvider implements ProviderInvoker.
func (f *Fiber) SupportsProvider() bool { return f.supportsProvider() }

// supportsDataProvider reports whether the live instance exposes the optional
// data-provider request export.
func (f *Fiber) supportsDataProvider() bool {
	f.mu.Lock()
	instance := f.instanceRT
	f.mu.Unlock()
	capable, ok := instance.(DataProviderCapableInstance)
	return ok && capable.SupportsDataProvider()
}

// InvokeDataProvider implements DataProviderInvoker. Data calls share the
// fiber's call lock with dispatch and the lifecycle, so one instance still has
// at most one guest call in flight.
func (f *Fiber) InvokeDataProvider(ctx context.Context,
	request DataProviderRequest) ([]byte, error) {

	var response []byte
	err := f.withInstance(ctx, func(instance Instance) error {
		capable, ok := instance.(DataProviderCapableInstance)
		if !ok {
			return &MissingExportError{Name: domainpluginabi.ExportDataProviderRequest}
		}
		payload, err := capable.InvokeDataProvider(ctx, request)
		if err != nil {
			return err
		}
		response = payload
		return nil
	})
	return response, err
}

// SupportsDataProvider implements DataProviderInvoker.
func (f *Fiber) SupportsDataProvider() bool { return f.supportsDataProvider() }

// Poll gives one ACTIVE fiber a host-driven opportunity to drain queued native
// UI events from its poll export. It serializes with dispatch and shutdown; a
// trap or deadline recycles the instance like any other guest call. Event
// delivery itself never calls Poll, so a UI callback cannot re-enter the
// guest.
func (f *Fiber) Poll(ctx context.Context) error {
	f.mu.Lock()
	instance := f.instanceRT
	active := f.state == domainplugin.StateActive && instance != nil
	f.mu.Unlock()
	if !active {
		return fmt.Errorf("%w: %s", ErrNotActive, f.id)
	}

	f.callMu.Lock()
	defer f.callMu.Unlock()

	f.mu.Lock()
	active = f.state == domainplugin.StateActive && f.instanceRT == instance
	f.mu.Unlock()
	if !active {
		return fmt.Errorf("%w: %s", ErrNotActive, f.id)
	}
	err := instance.Poll(ctx)
	if err != nil {
		var trap *TrapError
		if errors.As(err, &trap) && f.onFault != nil {
			f.onFault(f.ref, err)
		}
	}
	return err
}

// LoadDevelopment loads an unpacked plugin directory and drives the fiber to
// ACTIVE. A validation, module, ABI, init, permission, injection, or
// configuration condition leaves the fiber PENDING or FAILED with a readable
// reason. Development plugins are labelled with the development source and
// the "dev" loader group; their directory is a path-scoped grant held by the
// caller.
func (c *Container) LoadDevelopment(ctx context.Context, directory string) (*Fiber, error) {
	return c.load(ctx, LoadSpec{
		Group: "dev", Directory: directory, Instance: 1,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
}

// LoadDevelopmentInGroup loads an unpacked development directory into an
// explicit isolate scope.
func (c *Container) LoadDevelopmentInGroup(ctx context.Context, group, directory string) (*Fiber, error) {
	return c.load(ctx, LoadSpec{
		Group: group, Directory: directory, Instance: 1,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
}

// LoadInstalled loads a package from the host-owned version directory. The
// end state the installer wrote is installed-but-disabled; this call is only
// made once the user enabled it.
func (c *Container) LoadInstalled(ctx context.Context, directory string,
	source domainplugin.Source) (*Fiber, error) {
	return c.load(ctx, LoadSpec{
		Group: "installed", Directory: directory, Instance: 1, Source: source,
	})
}

// LoadInstalledInGroup loads an installed package into an explicit isolate
// scope.
func (c *Container) LoadInstalledInGroup(ctx context.Context, group, directory string,
	source domainplugin.Source) (*Fiber, error) {
	return c.load(ctx, LoadSpec{
		Group: group, Directory: directory, Instance: 1, Source: source,
	})
}

// LoadInstance loads one Plugin Instance. Instance zero allocates the next
// free instance number in the group, which is how a manifest entry runs more
// than once without sharing identity, effects, configuration, or authority.
func (c *Container) LoadInstance(ctx context.Context, spec LoadSpec) (*Fiber, error) {
	return c.load(ctx, spec)
}

func (c *Container) load(ctx context.Context, spec LoadSpec) (*Fiber, error) {
	if c == nil || c.runtime == nil || c.manifests == nil || c.contributions == nil {
		return nil, errors.New("plugin: container is not configured")
	}
	group := strings.TrimSpace(spec.Group)
	if group == "" {
		group = "default"
	}
	if err := c.ensureGroup(group); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(spec.Directory)
	if err != nil {
		return nil, fmt.Errorf("plugin: resolve %q: %w", spec.Directory, err)
	}
	manifest, err := c.manifests.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("plugin: load manifest from %q: %w", dir, err)
	}
	ref := PluginRef{
		ID:          manifest.ID,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Development: spec.Development,
		Source:      spec.Source,
	}
	if err := manifest.Validate(); err != nil {
		return c.failManifest(ctx, group, spec.Instance, ref, err)
	}
	return c.loadValidated(ctx, spec, group, dir, manifest, ref)
}

// failManifest records a FAILED fiber when enough identity survived decoding
// to name it.
func (c *Container) failManifest(ctx context.Context, group string, instance int,
	ref PluginRef, cause error) (*Fiber, error) {
	if ref.ID == "" {
		return nil, cause
	}
	if instance != 0 {
		if err := c.clearTerminalSlot(ctx, group, ref.ID, instance); err != nil {
			return nil, err
		}
	}
	fiber := c.newFiber(group, instance, domainplugin.Manifest{ID: ref.ID, Name: ref.Name}, ref, "")
	if err := c.addFiber(fiber); err != nil {
		return nil, err
	}
	c.failFiber(fiber, cause)
	return fiber, cause
}

func (c *Container) loadValidated(ctx context.Context, spec LoadSpec, group, dir string,
	manifest domainplugin.Manifest, ref PluginRef) (*Fiber, error) {
	if spec.Instance != 0 {
		if err := c.clearTerminalSlot(ctx, group, manifest.ID, spec.Instance); err != nil {
			return nil, err
		}
	}
	fiber := c.newFiber(group, spec.Instance, manifest, ref, dir)
	if err := c.addFiber(fiber); err != nil {
		return nil, err
	}
	err := c.activateFiber(ctx, fiber, SettingsInheritance{
		Group: c.groupDefaults(group, manifest.ID),
	})
	return fiber, err
}

// clearTerminalSlot removes a FAILED, PENDING, or DISPOSED fiber occupying an
// explicit instance number so a retry starts from a clean activation. A live
// instance is never replaced silently.
func (c *Container) clearTerminalSlot(ctx context.Context, group, pluginID string,
	instance int) error {
	id := domainplugin.NewFiberID(group, pluginID, instance)
	c.mu.Lock()
	existing := c.fibers[id]
	c.mu.Unlock()
	if existing == nil {
		return nil
	}
	switch existing.State() {
	case domainplugin.StateActive, domainplugin.StateLoading, domainplugin.StateUnloading:
		return fmt.Errorf("plugin: fiber %q is already loaded", id)
	}
	c.mu.Lock()
	if c.fibers[id] == existing {
		delete(c.fibers, id)
	}
	c.mu.Unlock()
	return c.unload(ctx, existing)
}

// activateFiber drives one fiber through LOADING to ACTIVE, or records the
// PENDING/FAILED result. The caller must have registered the fiber. Activation
// creates no instance until every precondition is satisfied.
func (c *Container) activateFiber(ctx context.Context, fiber *Fiber,
	inherited SettingsInheritance) error {
	fiber.mu.Lock()
	fiber.state = domainplugin.StateLoading
	fiber.reason = ""
	fiber.pendingKind = pendingNone
	fiber.missingInjections = nil
	fiber.configErrors = nil
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateLoading, "")

	manifest := fiber.manifestSnapshot()
	settings, err := c.resolveSettings(ctx, manifest, inherited)
	if err != nil {
		var validation *domainplugin.SettingsValidationError
		if errors.As(err, &validation) {
			c.pendingFiber(fiber, pendingConfig, domainplugin.ConfigReason(err.Error()),
				nil, validation.Errors, settings)
			return err
		}
		c.failFiber(fiber, err)
		return err
	}
	if c.requiredPermissions != nil {
		required, _, requestErr := manifest.Permissions.Requests()
		if requestErr != nil {
			c.failFiber(fiber, requestErr)
			return requestErr
		}
		for _, request := range required {
			if err := c.requiredPermissions(fiber.ref, request); err != nil {
				reason := domainplugin.PermissionReason(
					domainplugin.ReasonPermissionDenied, request.Permission)
				c.pendingFiber(fiber, pendingPermission, reason, nil, nil, settings)
				return err
			}
		}
	}
	if problems := c.injectionProblems(fiber); len(problems) > 0 {
		missing := make([]string, 0, len(problems))
		for _, problem := range problems {
			missing = append(missing, problem.Service)
		}
		c.pendingFiber(fiber, pendingInjection, injectionReason(problems),
			missing, nil, settings)
		return &MissingInjectionError{Services: missing}
	}
	if err := c.start(ctx, fiber, settings); err != nil {
		c.failFiber(fiber, err)
		return err
	}
	fiber.mu.Lock()
	fiber.state = domainplugin.StateActive
	fiber.reason = ""
	fiber.pendingKind = pendingNone
	fiber.settings = cloneSettings(settings)
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateActive, "")
	if c.broker != nil {
		c.broker.publishFiber(fiber)
	} else {
		c.registerProvides(fiber)
	}
	c.reevaluate()
	return nil
}

// resolveSettings merges the group inheritance with the manifest's declared
// defaults. A container without a resolver still hands the inherited and
// default values to the guest.
func (c *Container) resolveSettings(ctx context.Context, manifest domainplugin.Manifest,
	inherited SettingsInheritance) (map[string]any, error) {
	if c.settings == nil {
		out := manifest.Settings.Defaults()
		for key, value := range inherited.Group {
			if _, declared := out[key]; !declared {
				out[key] = domainplugin.CloneSettingValue(value)
			}
		}
		for key, value := range inherited.Volatile {
			out[key] = domainplugin.CloneSettingValue(value)
		}
		return out, nil
	}
	return c.settings(ctx, manifest, inherited)
}

// pendingFiber records the PENDING state for one unmet activation
// precondition. No instance is created and the reason is readable.
func (c *Container) pendingFiber(fiber *Fiber, kind pendingKind, reason string,
	missing []string, configErrors []domainplugin.SettingsError, settings map[string]any) {
	fiber.mu.Lock()
	fiber.state = domainplugin.StatePending
	fiber.reason = reason
	fiber.pendingKind = kind
	fiber.missingInjections = append([]string(nil), missing...)
	fiber.configErrors = append([]domainplugin.SettingsError(nil), configErrors...)
	fiber.settings = cloneSettings(settings)
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StatePending, reason)
}

// addFiber reserves a fiber identity. A closed container or a duplicate
// identity leaves the fiber unregistered.
func (c *Container) addFiber(fiber *Fiber) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if fiber.instance <= 0 {
		instance := 1
		for {
			id := domainplugin.NewFiberID(fiber.group, fiber.ref.ID, instance)
			if _, exists := c.fibers[id]; !exists {
				fiber.instance = instance
				fiber.id = id
				break
			}
			instance++
		}
	}
	if _, exists := c.fibers[fiber.id]; exists {
		return fmt.Errorf("plugin: fiber %q is already loaded", fiber.id)
	}
	c.fibers[fiber.id] = fiber
	return nil
}

func (c *Container) newFiber(group string, instance int, manifest domainplugin.Manifest,
	ref PluginRef, directory string) *Fiber {
	depth := c.toolQueueDepth
	if depth <= 0 {
		depth = MaxQueueDepth
	}
	return &Fiber{
		id:             domainplugin.NewFiberID(group, manifest.ID, instance),
		ref:            ref,
		manifest:       manifest,
		group:          group,
		instance:       instance,
		directory:      directory,
		state:          domainplugin.StateLoading,
		logLimiter:     NewLogLimiter(MaxLogsPerSecond),
		timings:        make(map[string]*CallTiming),
		now:            c.now,
		onFault:        c.fault,
		toolQueue:      make(chan struct{}, depth+1),
		toolQueueDepth: depth,
		toolExec:       make(chan struct{}, 1),
		toolPending:    make(map[uint32]*pendingToolCall),
	}
}

// openHostSink builds the fiber-scoped host sink shared by guest and built-in
// activation: bounded logging and feedback, the service session, language
// cancellation, and MCP tool delivery. A nil service host leaves both the
// session and its dispatcher nil. The caller owns closing the session. The
// optional catalogs are the activation's i18n tables; a built-in may pass none.
func (c *Container) openHostSink(fiber *Fiber,
	settings map[string]any, catalogs ...domainplugin.Catalogs) (HostSink, *ServiceSession, error) {
	var declared domainplugin.Catalogs
	if len(catalogs) > 0 {
		declared = catalogs[0]
	}
	sink := HostSink{
		Log: func(level LogLevel, message string) {
			if fiber.appendLog(level, message, c.now()) && c.logf != nil {
				c.logf(fiber.ref, level, message)
			}
		},
		Output: func(data []byte) {
			if c.feedback != nil {
				c.feedback(fiber.ref, string(data))
			}
		},
		ToolResult:   fiber.deliverToolResult,
		ToolProgress: fiber.deliverToolProgress,
	}
	if c.connectionHost != nil {
		sink.Provider = c.connectionHost.Callbacks(fiber.id)
	}
	if c.dataHost != nil {
		sink.Data = c.dataHost.Callbacks(fiber.id)
	}
	var session *ServiceSession
	if c.services != nil {
		session = c.services.Open(fiber.ref, fiber.id, settings, declared)
		if session == nil {
			return HostSink{}, nil, fmt.Errorf("plugin %s: host services are closed", fiber.ref.ID)
		}
		session.SetBroker(c.broker)
		if dir := fiber.Directory(); dir != "" {
			session.AttachWebview(os.DirFS(dir))
		}
		sink.Services = session.Dispatcher()
	}
	if c.language != nil {
		sink.LanguageCancelled = func(requestID uint64) bool {
			return c.language.Canceled(fiber.id, requestID)
		}
	}
	return sink, session, nil
}

func (c *Container) start(ctx context.Context, fiber *Fiber, settings map[string]any) error {
	if fiber.builtinSetup != nil {
		return c.startBuiltin(ctx, fiber, settings)
	}
	manifest := fiber.manifestSnapshot()
	catalogs, err := c.loadCatalogs(fiber, manifest)
	if err != nil {
		return err
	}
	fiber.setCatalogs(catalogs)
	wasmPath, err := resolveModulePath(fiber.directory, manifest.Wasm.Path)
	if err != nil {
		return err
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("plugin %s: read module %q: %w", fiber.ref.ID, manifest.Wasm.Path, err)
	}
	// Installed packages were hash-verified at install time; verify again at
	// activation so a module replaced on disk fails closed. Development
	// directories are rebuilt in place, so their manifest hash is advisory.
	if !fiber.ref.Development {
		if err := verifyModuleIntegrity(manifest.Wasm, wasm); err != nil {
			return fmt.Errorf("plugin %s: %w", fiber.ref.ID, err)
		}
	}
	sink, session, err := c.openHostSink(fiber, settings, catalogs)
	if err != nil {
		return err
	}
	instance, err := c.runtime.Instantiate(ctx, ModuleSpec{
		Name:           fiber.ref.ID,
		WASM:           wasm,
		MemoryMaxBytes: manifest.Wasm.EffectiveMemoryMaxBytes(),
	}, sink)
	if err != nil {
		c.closeServices(fiber)
		return fmt.Errorf("plugin %s: instantiate module: %w", fiber.ref.ID, err)
	}
	if session != nil {
		session.BindHandles(instance.Handles())
	}
	fiber.mu.Lock()
	fiber.instanceRT = instance
	fiber.mu.Unlock()

	start := c.now()
	initErr := instance.Init(ctx)
	fiber.observeCall("init", start, initErr, instance)
	if initErr != nil {
		_ = instance.Close(context.Background())
		c.closeServices(fiber)
		return fmt.Errorf("plugin %s: init failed: %w", fiber.ref.ID, initErr)
	}
	if err := c.registerContributions(fiber); err != nil {
		cleanup := []error{err, c.disposeEffects(ctx, fiber)}
		start = c.now()
		shutdownErr := instance.Shutdown(context.Background())
		fiber.observeCall("shutdown", start, shutdownErr, instance)
		if shutdownErr != nil {
			cleanup = append(cleanup, fmt.Errorf("plugin %s: shutdown after failed registration: %w",
				fiber.ref.ID, shutdownErr))
		}
		if closeErr := instance.Close(context.Background()); closeErr != nil {
			cleanup = append(cleanup, fmt.Errorf("plugin %s: close after failed registration: %w",
				fiber.ref.ID, closeErr))
		}
		c.closeServices(fiber)
		return errors.Join(cleanup...)
	}
	return nil
}

// loadCatalogs reads the package's i18n assets. A host without a catalog
// source leaves the tables empty, so contribution keys still fall back to
// their literals instead of failing activation.
func (c *Container) loadCatalogs(fiber *Fiber,
	manifest domainplugin.Manifest) (domainplugin.Catalogs, error) {
	if c == nil || c.catalogs == nil || fiber == nil {
		return domainplugin.Catalogs{}, nil
	}
	catalogs, err := c.catalogs.LoadCatalogs(fiber.directory, manifest.DefaultLocale)
	if err != nil {
		return domainplugin.Catalogs{}, fmt.Errorf("plugin %s: %w", fiber.ref.ID, err)
	}
	return catalogs.Clone(), nil
}

// closeServices releases one fiber's host-service session. It is idempotent
// and safe on a fiber that never opened a session.
func (c *Container) closeServices(fiber *Fiber) {
	if c == nil || c.services == nil || fiber == nil {
		return
	}
	c.services.Close(fiber.id)
}

func (c *Container) registerContributions(fiber *Fiber) error {
	manifest := fiber.manifestSnapshot()
	for _, command := range manifest.Contributions.Commands {
		command := command
		commandID := domainplugin.QualifiedCommandID(fiber.ref.ID, command.ID)
		run := func(ctx context.Context) error {
			err := fiber.invoke(ctx, commandID)
			if IsUnusable(err) {
				// A trap, host-import panic, or deadline leaves the
				// instance unusable: recycle it and keep siblings alive.
				c.recycle(fiber, err)
			}
			return err
		}
		disposer, err := c.contributions.RegisterCommand(fiber.ref, command, run)
		if err != nil {
			return fmt.Errorf("plugin %s: register command %q: %w",
				fiber.ref.ID, commandID, err)
		}
		if err := fiber.addEffect(disposer); err != nil {
			_ = disposer(context.Background())
			return fmt.Errorf("plugin %s: register command %q: %w",
				fiber.ref.ID, commandID, err)
		}
	}
	for _, menu := range manifest.Contributions.Menus {
		disposer, err := c.contributions.RegisterMenu(fiber.ref, menu)
		if err != nil {
			return fmt.Errorf("plugin %s: register menu item for %q: %w",
				fiber.ref.ID, menu.Command, err)
		}
		if err := fiber.addEffect(disposer); err != nil {
			_ = disposer(context.Background())
			return fmt.Errorf("plugin %s: register menu item for %q: %w",
				fiber.ref.ID, menu.Command, err)
		}
	}
	for _, binding := range manifest.Contributions.Keybindings {
		disposer, err := c.contributions.RegisterKeybinding(fiber.ref, binding)
		if err != nil {
			return fmt.Errorf("plugin %s: register keybinding for %q: %w",
				fiber.ref.ID, binding.Command, err)
		}
		if err := fiber.addEffect(disposer); err != nil {
			_ = disposer(context.Background())
			return fmt.Errorf("plugin %s: register keybinding for %q: %w",
				fiber.ref.ID, binding.Command, err)
		}
	}
	if registrar, ok := c.contributions.(ViewRegistrar); ok {
		for _, view := range fiber.manifest.Contributions.Views {
			disposer, err := registrar.RegisterView(fiber.ref, view)
			if err != nil {
				return fmt.Errorf("plugin %s: register view %q: %w",
					fiber.ref.ID, view.ID, err)
			}
			if err := fiber.addEffect(disposer); err != nil {
				_ = disposer(context.Background())
				return fmt.Errorf("plugin %s: register view %q: %w",
					fiber.ref.ID, view.ID, err)
			}
		}
	}
	for _, connection := range fiber.manifest.Contributions.Connections {
		connection := connection
		if c.connectionHost == nil || c.connectionRegistrar == nil {
			return fmt.Errorf("plugin %s: connection provider %q requires a provider host",
				fiber.ref.ID, connection.Protocol)
		}
		provider, err := c.connectionHost.Register(fiber.id, fiber.ref, connection, fiber)
		if err != nil {
			return fmt.Errorf("plugin %s: register connection provider %q: %w",
				fiber.ref.ID, connection.Protocol, err)
		}
		unregister := func(context.Context) error {
			return c.connectionHost.Unregister(provider)
		}
		if err := fiber.addEffect(unregister); err != nil {
			_ = unregister(context.Background())
			return fmt.Errorf("plugin %s: register connection provider %q: %w",
				fiber.ref.ID, connection.Protocol, err)
		}
		disposer, err := c.connectionRegistrar.RegisterConnectionProvider(
			fiber.ref, connection, provider)
		if err != nil {
			return fmt.Errorf("plugin %s: register connection provider %q: %w",
				fiber.ref.ID, connection.Protocol, err)
		}
		if err := fiber.addEffect(disposer); err != nil {
			_ = disposer(context.Background())
			return fmt.Errorf("plugin %s: register connection provider %q: %w",
				fiber.ref.ID, connection.Protocol, err)
		}
	}
	for _, contribution := range fiber.manifest.Contributions.DataProviders {
		contribution := contribution
		if c.dataHost == nil {
			return fmt.Errorf("plugin %s: data provider %q requires a data provider host",
				fiber.ref.ID, contribution.ID)
		}
		if !fiber.supportsDataProvider() {
			return fmt.Errorf("plugin %s: data provider %q requires the %s export",
				fiber.ref.ID, contribution.ID, domainpluginabi.ExportDataProviderRequest)
		}
		scope := fiber.manifest.Permissions.Scopes[string(domainplugin.PermissionProviderData)]
		provider, err := c.dataHost.Register(fiber.id, fiber.ref, contribution,
			domainplugin.DataAccessMode(scope.Access), fiber)
		if err != nil {
			return fmt.Errorf("plugin %s: register data provider %q: %w",
				fiber.ref.ID, contribution.ID, err)
		}
		unregister := func(context.Context) error {
			return c.dataHost.Unregister(provider)
		}
		if err := fiber.addEffect(unregister); err != nil {
			_ = unregister(context.Background())
			return fmt.Errorf("plugin %s: register data provider %q: %w",
				fiber.ref.ID, contribution.ID, err)
		}
	}
	for _, provider := range fiber.manifest.Contributions.Language.Providers {
		provider := provider
		if c.language == nil {
			return fmt.Errorf("plugin %s: language provider %q requires a language host",
				fiber.ref.ID, provider.ID)
		}
		if !fiber.supportsLanguage() {
			return fmt.Errorf("plugin %s: language provider %q requires the %s export",
				fiber.ref.ID, provider.ID, domainpluginabi.ExportLanguageRequest)
		}
		disposer, err := c.language.Register(fiber.ref, fiber.id, provider, fiber)
		if err != nil {
			return fmt.Errorf("plugin %s: register language provider %q: %w",
				fiber.ref.ID, provider.ID, err)
		}
		if err := fiber.addEffect(disposer); err != nil {
			_ = disposer(context.Background())
			return fmt.Errorf("plugin %s: register language provider %q: %w",
				fiber.ref.ID, provider.ID, err)
		}
	}
	if c.mcpTools != nil {
		for _, tool := range fiber.manifest.Contributions.MCPTools {
			tool := tool
			caller := fiberToolCaller{container: c, fiber: fiber}
			disposer, err := c.mcpTools.RegisterMCPTool(fiber.ref, tool, caller)
			if err != nil {
				return fmt.Errorf("plugin %s: register MCP tool %q: %w",
					fiber.ref.ID, domainplugin.QualifiedMCPToolName(fiber.ref.ID, tool.Name), err)
			}
			if err := fiber.addEffect(disposer); err != nil {
				_ = disposer(context.Background())
				return fmt.Errorf("plugin %s: register MCP tool %q: %w",
					fiber.ref.ID, domainplugin.QualifiedMCPToolName(fiber.ref.ID, tool.Name), err)
			}
		}
	}
	return nil
}

func (f *Fiber) addEffect(disposer Disposer) error {
	if disposer == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.disposers) >= MaxEffectRegistrations {
		return &LimitError{Resource: "effect registrations", Limit: MaxEffectRegistrations}
	}
	f.disposers = append(f.disposers, &effect{dispose: disposer})
	return nil
}

// disposeEffects runs every remaining disposer in reverse registration order.
// Each disposer is idempotent and receives a cancellable context; once the
// context is done the remaining disposers are force-released without waiting
// so an unload always returns within its budget.
func (c *Container) disposeEffects(ctx context.Context, fiber *Fiber) error {
	fiber.mu.Lock()
	effects := fiber.disposers
	fiber.disposers = nil
	fiber.mu.Unlock()
	var errs []error
	for i := len(effects) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			for j := i; j >= 0; j-- {
				effect := effects[j]
				go func() { _ = effect.run(context.Background()) }()
			}
			errs = append(errs, ctx.Err())
			break
		}
		if err := runDisposer(ctx, effects[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runDisposer waits for one disposer but never past ctx. A disposer that
// ignores cancellation cannot pin the unload.
func runDisposer(ctx context.Context, effect *effect) error {
	done := make(chan error, 1)
	go func() { done <- effect.run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Container) failFiber(fiber *Fiber, err error) {
	fiber.mu.Lock()
	fiber.state = domainplugin.StateFailed
	fiber.reason = err.Error()
	fiber.pendingKind = pendingNone
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateFailed, err.Error())
}

// verifyModuleIntegrity compares module bytes with the manifest hash.
func verifyModuleIntegrity(wasm domainplugin.Wasm, module []byte) error {
	sum := sha256.Sum256(module)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, wasm.SHA256) {
		return &ModuleIntegrityError{Path: wasm.Path, Want: strings.ToLower(wasm.SHA256), Got: got}
	}
	return nil
}

func (c *Container) notifyLifecycle(fiber *Fiber, state domainplugin.State, reason string) {
	if c == nil || c.lifecycle == nil || fiber == nil {
		return
	}
	c.lifecycle(fiber.ref, state, reason)
}

// recycle disposes an instance that became unusable after a guest trap, a
// host-import panic, or a missed deadline. The fiber becomes FAILED, the
// module is closed, every host-owned handle is revoked, and the fiber's
// effects are removed in reverse order. Sibling fibers are untouched. A
// runtime fault gets at most one automatic restart after the backoff; a
// second fault waits for a manual restart.
func (c *Container) recycle(fiber *Fiber, cause error) {
	fiber.callMu.Lock()
	defer fiber.callMu.Unlock()
	c.recycleLocked(fiber, cause)
}

// recycleLocked tears down a fiber whose call lock the caller already holds.
// The MCP tool path keeps callMu for the whole tool call, including the
// dispatch or poll failure that recycles a trapped instance, so re-acquiring
// it here would deadlock. The caller must hold fiber.callMu.
func (c *Container) recycleLocked(fiber *Fiber, cause error) {
	fiber.mu.Lock()
	if fiber.state != domainplugin.StateActive {
		fiber.mu.Unlock()
		return
	}
	fiber.state = domainplugin.StateFailed
	fiber.reason = cause.Error()
	fiber.pendingKind = pendingNone
	instance := fiber.instanceRT
	fiber.instanceRT = nil
	fiber.mu.Unlock()

	// A crash-induced activation change is a lifecycle event: audit observers
	// must see FAILED with the trap or deadline reason, not just ACTIVE.
	c.notifyLifecycle(fiber, domainplugin.StateFailed, cause.Error())

	ctx, cancel := context.WithTimeout(context.Background(), c.unloadTimeout)
	errs := c.teardown(ctx, fiber, instance)
	cancel()
	if len(errs) > 0 {
		fiber.mu.Lock()
		fiber.reason = errors.Join(append([]error{cause}, errs...)...).Error()
		fiber.mu.Unlock()
	}
	c.releaseProvides(fiber)
	c.reconcileServices()
	c.scheduleAutoRestart(fiber, cause)
}

// scheduleAutoRestart arms the single automatic restart a runtime fault is
// allowed. The delay is at least MinRestartBackoff. Init failures never reach
// this path: they are FAILED from activation and are not auto-retried.
func (c *Container) scheduleAutoRestart(fiber *Fiber, cause error) {
	if c == nil || fiber == nil {
		return
	}
	fiber.mu.Lock()
	if fiber.automaticRestarts >= MaxAutomaticRestarts || fiber.state != domainplugin.StateFailed {
		fiber.mu.Unlock()
		return
	}
	fiber.automaticRestarts++
	fiber.restartPending = true
	fiber.mu.Unlock()

	id := fiber.id
	stop := c.schedule(c.restartBackoff, func() {
		c.mu.Lock()
		delete(c.restartTimers, id)
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
		fiber.mu.Lock()
		fiber.restartPending = false
		fiber.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(),
			c.unloadTimeout+DefaultInitTimeout)
		defer cancel()
		_, _ = c.reload(ctx, id, ReloadAutomatic)
	})
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		stop()
		return
	}
	c.restartTimers[id] = stop
	c.mu.Unlock()
}

// ReloadReason records why a fiber was reloaded. Reload preserves only the
// stored configuration and volatile fields; no other guest state survives.
type ReloadReason string

const (
	// ReloadManual is an operator-initiated reload or restart.
	ReloadManual ReloadReason = "manual"
	// ReloadAutomatic is the one restart granted after a runtime fault.
	ReloadAutomatic ReloadReason = "automatic"
	// ReloadDevelopment is a change to an unpacked development directory.
	ReloadDevelopment ReloadReason = "development"
)

// Reload tears down the instance and activates it again with the same identity,
// stored configuration, and volatile fields. Effects and contributions are
// recreated; no other state crosses the reload.
func (c *Container) Reload(ctx context.Context, id domainplugin.FiberID) (*Fiber, error) {
	return c.reload(ctx, id, ReloadManual)
}

// Restart is a manual restart. It grants a fresh automatic-restart budget, so
// a plugin that exhausted its one automatic retry can recover on request.
func (c *Container) Restart(ctx context.Context, id domainplugin.FiberID) (*Fiber, error) {
	c.mu.Lock()
	fiber := c.fibers[id]
	c.mu.Unlock()
	if fiber == nil {
		return nil, &UnknownFiberError{ID: id}
	}
	fiber.mu.Lock()
	fiber.automaticRestarts = 0
	fiber.mu.Unlock()
	return c.reload(ctx, id, ReloadManual)
}

// DevelopmentChanged reloads an unpacked development plugin after its
// manifest or module changed. Each call performs exactly one restart; a failed
// reload leaves the fiber FAILED instead of retrying in a loop.
func (c *Container) DevelopmentChanged(ctx context.Context,
	id domainplugin.FiberID) (*Fiber, error) {
	return c.reload(ctx, id, ReloadDevelopment)
}

func (c *Container) reload(ctx context.Context, id domainplugin.FiberID,
	reason ReloadReason) (*Fiber, error) {
	c.mu.Lock()
	fiber := c.fibers[id]
	c.mu.Unlock()
	if fiber == nil {
		return nil, &UnknownFiberError{ID: id}
	}
	// A manual or development reload supersedes a pending automatic restart.
	c.cancelRestart(id)

	fiber.mu.Lock()
	switch fiber.state {
	case domainplugin.StateDisposed:
		fiber.mu.Unlock()
		return fiber, nil
	case domainplugin.StateLoading, domainplugin.StateUnloading:
		fiber.mu.Unlock()
		return fiber, fmt.Errorf("plugin: fiber %q is already transitioning", id)
	}
	previousSettings := cloneSettings(fiber.settings)
	instance := fiber.instanceRT
	fiber.instanceRT = nil
	fiber.state = domainplugin.StateUnloading
	fiber.reason = ""
	fiber.restartPending = false
	fiber.generation++
	fiber.restarts++
	if reason == ReloadManual {
		fiber.manualRestarts++
	}
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateUnloading, "")
	// Unblock consumers before teardown waits on the provider's call slot. A
	// reload closes both ends of every stream as unavailable and cancels
	// in-flight calls; calls are never queued across a reload.
	if c.broker != nil {
		c.broker.forgetFiber(fiber.id)
	}

	unloadCtx, cancel := c.unloadContext(ctx)
	fiber.callMu.Lock()
	errs := c.teardown(unloadCtx, fiber, instance)
	fiber.callMu.Unlock()
	cancel()
	c.releaseProvides(fiber)

	if reason == ReloadDevelopment {
		if err := c.refreshDevelopmentManifest(ctx, fiber); err != nil {
			c.failFiber(fiber, err)
			// The old provisions are gone and the fiber ends FAILED, so
			// dependents must be suspended even though activation never ran.
			c.reconcileServices()
			return fiber, errors.Join(append(errs, err)...)
		}
	}
	manifest := fiber.manifestSnapshot()
	activationErr := c.activateFiber(ctx, fiber, SettingsInheritance{
		Group:    c.groupDefaults(fiber.group, manifest.ID),
		Volatile: volatileSettings(manifest.Settings, previousSettings),
	})
	if activationErr != nil {
		errs = append(errs, activationErr)
	}
	// Activation reevaluates only when the fiber reaches ACTIVE. A reload that
	// ends PENDING or FAILED left its dependents without a provider.
	c.reconcileServices()
	return fiber, errors.Join(errs...)
}

// refreshDevelopmentManifest re-decodes the development directory's manifest
// for a reload. The identity must not change: a renamed id is a different
// plugin, not a code change.
func (c *Container) refreshDevelopmentManifest(ctx context.Context, fiber *Fiber) error {
	if fiber == nil || !fiber.ref.Development || fiber.directory == "" || c.manifests == nil {
		return nil
	}
	manifest, err := c.manifests.Load(fiber.directory)
	if err != nil {
		return fmt.Errorf("plugin: reload development manifest from %q: %w", fiber.directory, err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("plugin: reload development manifest from %q: %w", fiber.directory, err)
	}
	if manifest.ID != fiber.ref.ID {
		return fmt.Errorf("plugin: development directory now declares id %q, want %q",
			manifest.ID, fiber.ref.ID)
	}
	fiber.replaceManifest(manifest)
	_ = ctx
	return nil
}

// cancelRestart stops a scheduled automatic restart for one fiber.
func (c *Container) cancelRestart(id domainplugin.FiberID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	stop := c.restartTimers[id]
	delete(c.restartTimers, id)
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Retry re-evaluates a PENDING fiber after its precondition changed, for
// example when an administrator granted a required permission or repaired the
// stored configuration. ACTIVE fibers are not disturbed.
func (c *Container) Retry(ctx context.Context, id domainplugin.FiberID) (*Fiber, error) {
	c.mu.Lock()
	fiber := c.fibers[id]
	c.mu.Unlock()
	if fiber == nil {
		return nil, &UnknownFiberError{ID: id}
	}
	return c.reload(ctx, id, ReloadManual)
}

// teardown gives the guest one bounded shutdown, disposes every effect in
// reverse order, closes the instance, and releases the host-service session.
// The caller holds the fiber's call lock.
func (c *Container) teardown(ctx context.Context, fiber *Fiber, instance Instance) []error {
	var errs []error
	if instance != nil {
		start := c.now()
		err := instance.Shutdown(ctx)
		fiber.observeCall("shutdown", start, err, instance)
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: shutdown: %w", fiber.ref.ID, err))
		}
		if handles := instance.Handles(); handles != nil {
			handles.RevokeAll()
		}
	}
	if err := c.disposeEffects(ctx, fiber); err != nil {
		errs = append(errs, err)
	}
	if instance != nil {
		if err := instance.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: close instance: %w", fiber.ref.ID, err))
		}
	}
	c.closeServices(fiber)
	return errs
}

// unloadContext bounds one fiber's teardown with the unload timeout, unless
// the caller already supplied a shorter deadline (the global quit budget).
func (c *Container) unloadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= c.unloadTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.unloadTimeout)
}

// Unload shuts the guest down, removes every contribution in reverse order,
// and closes the instance. It is idempotent for a disposed fiber.
func (c *Container) Unload(ctx context.Context, id domainplugin.FiberID) error {
	c.mu.Lock()
	fiber, ok := c.fibers[id]
	if ok {
		delete(c.fibers, id)
	}
	c.mu.Unlock()
	if !ok {
		return &UnknownFiberError{ID: id}
	}
	return c.unload(ctx, fiber)
}

func (c *Container) unload(ctx context.Context, fiber *Fiber) error {
	c.cancelRestart(fiber.id)
	fiber.mu.Lock()
	if fiber.state == domainplugin.StateDisposed {
		fiber.mu.Unlock()
		return nil
	}
	fiber.state = domainplugin.StateUnloading
	fiber.reason = ""
	fiber.pendingKind = pendingNone
	fiber.restartPending = false
	instance := fiber.instanceRT
	fiber.instanceRT = nil
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateUnloading, "")
	// An unload cancels the fiber's calls and streams and removes its
	// subscriptions before teardown, so the peer is never left blocked.
	if c.broker != nil {
		c.broker.forgetFiber(fiber.id)
	}

	unloadCtx, cancel := c.unloadContext(ctx)
	defer cancel()

	// Wait for any in-flight guest call to finish before shutting down.
	fiber.callMu.Lock()
	errs := c.teardown(unloadCtx, fiber, instance)
	fiber.callMu.Unlock()

	c.releaseProvides(fiber)
	fiber.mu.Lock()
	fiber.state = domainplugin.StateDisposed
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateDisposed, "")
	c.reconcileServices()
	return errors.Join(errs...)
}

// UpdateSettings records the effective configuration of a live fiber after a
// host-side settings change. Volatile values therefore survive a later reload,
// while persisted values are re-read from the configuration store.
func (c *Container) UpdateSettings(id domainplugin.FiberID, values map[string]any) {
	if c == nil {
		return
	}
	fiber, ok := c.Fiber(id)
	if !ok || fiber == nil {
		return
	}
	fiber.mu.Lock()
	fiber.settings = cloneSettings(values)
	fiber.mu.Unlock()
}

// Fiber returns one live fiber.
func (c *Container) Fiber(id domainplugin.FiberID) (*Fiber, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fiber, ok := c.fibers[id]
	return fiber, ok
}

// NativeUI returns the native UI session of one live fiber. A host without
// host services, or a fiber that is not active, reports false.
func (c *Container) NativeUI(ref PluginRef) (*UISession, bool) {
	if c == nil || c.services == nil {
		return nil, false
	}
	fiber, ok := c.FiberByRef(ref)
	if !ok || fiber == nil {
		return nil, false
	}
	session, ok := c.services.Session(fiber.id)
	if !ok || session == nil {
		return nil, false
	}
	ui := session.NativeUI()
	return ui, ui != nil
}

// Webviews returns the sandboxed-webview session of one live fiber. It is
// non-nil only after AttachWebview ran and the host configured a port.
func (c *Container) Webviews(ref PluginRef) (*WebviewSession, bool) {
	if c == nil || c.services == nil {
		return nil, false
	}
	fiber, ok := c.FiberByRef(ref)
	if !ok || fiber == nil {
		return nil, false
	}
	session, ok := c.services.Session(fiber.id)
	if !ok || session == nil {
		return nil, false
	}
	webviews := session.Webview()
	return webviews, webviews != nil
}

// PollUI drives the guest poll export for one live fiber. The caller schedules
// it; the UI thread must not call it from an event callback. A missing fiber
// reports ErrNotActive.
func (c *Container) PollUI(ctx context.Context, ref PluginRef) error {
	if c == nil {
		return fmt.Errorf("%w: %s", ErrNotActive, ref.ID)
	}
	fiber, ok := c.FiberByRef(ref)
	if !ok || fiber == nil {
		return fmt.Errorf("%w: %s", ErrNotActive, ref.ID)
	}
	err := fiber.Poll(ctx)
	if IsUnusable(err) {
		c.recycle(fiber, err)
	}
	return err
}

// FiberByRef returns the live fiber for one plugin identity. Installed and
// development copies of the same id are distinct refs and never match each
// other.
func (c *Container) FiberByRef(ref PluginRef) (*Fiber, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fiber := range c.fibers {
		if fiber.ref.ID == ref.ID && fiber.ref.Source == ref.Source &&
			fiber.ref.Development == ref.Development {
			return fiber, true
		}
	}
	return nil, false
}

// PanelFibers returns the System panel's view of every live fiber. A fiber
// that is not ACTIVE keeps its manifest but has no invoke function, so the
// panel never shows sections of a stopped plugin.
func (c *Container) PanelFibers() []PanelFiber {
	fibers := c.Fibers()
	out := make([]PanelFiber, 0, len(fibers))
	for _, fiber := range fibers {
		active := fiber.State() == domainplugin.StateActive
		entry := PanelFiber{
			Ref:      fiber.Ref(),
			Manifest: fiber.Manifest(),
			Active:   active,
		}
		if active {
			fiber := fiber
			entry.Invoke = func(ctx context.Context, command string) error {
				return fiber.invoke(ctx, command)
			}
		}
		out = append(out, entry)
	}
	return out
}

// Fibers returns every live fiber in identity order.
func (c *Container) Fibers() []*Fiber {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]domainplugin.FiberID, 0, len(c.fibers))
	for id := range c.fibers {
		ids = append(ids, id)
	}
	sortFiberIDs(ids)
	out := make([]*Fiber, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.fibers[id])
	}
	return out
}

func sortFiberIDs(ids []domainplugin.FiberID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

// Close unloads every fiber, closes the runtime, and completes within the
// global unload budget. A guest or disposer that ignores cancellation cannot
// keep app quit blocked.
func (c *Container) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	fibers := make([]*Fiber, 0, len(c.fibers))
	for _, fiber := range c.fibers {
		fibers = append(fibers, fiber)
	}
	c.fibers = make(map[domainplugin.FiberID]*Fiber)
	timers := c.restartTimers
	c.restartTimers = make(map[domainplugin.FiberID]func())
	c.provides = make(map[string][]ServiceProvider)
	c.mu.Unlock()
	for _, stop := range timers {
		stop()
	}

	if ctx == nil {
		ctx = context.Background()
	}
	closeCtx, cancel := context.WithTimeout(ctx, c.globalUnloadBudget)
	defer cancel()

	var errs []error
	for _, fiber := range fibers {
		if err := c.unloadWithin(closeCtx, fiber); err != nil {
			errs = append(errs, err)
		}
	}
	if c.runtime != nil {
		if err := c.runtime.Close(closeCtx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// unloadWithin waits for one unload but never past the global budget.
func (c *Container) unloadWithin(ctx context.Context, fiber *Fiber) error {
	done := make(chan error, 1)
	go func() { done <- c.unload(ctx, fiber) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resolveModulePath keeps the manifest's relative module path inside the
// plugin directory, including on platforms with different separators.
func resolveModulePath(directory, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("plugin: invalid module path %q", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin: module path %q escapes the plugin directory", relative)
	}
	full := filepath.Join(directory, clean)
	rel, err := filepath.Rel(directory, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin: module path %q escapes the plugin directory", relative)
	}
	return full, nil
}

// ServeWebview serves one plugin sandbox asset for the app's custom-scheme
// handler. It returns the application-level typed errors so the adapter can
// choose a status without widening any capability.
func (c *Container) ServeWebview(pluginID, origin, requestPath string) (WebviewAsset, error) {
	if c == nil || c.services == nil {
		return WebviewAsset{}, ErrWebviewUnavailable
	}
	return c.services.ServeWebview(pluginID, origin, requestPath)
}
