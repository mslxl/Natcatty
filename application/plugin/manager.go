package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// PluginStatus is the plugin manager's display state.
type PluginStatus string

const (
	// StatusDisabled is installed but not enabled.
	StatusDisabled PluginStatus = "disabled"
	// StatusActive is enabled and ACTIVE.
	StatusActive PluginStatus = "active"
	// StatusFailed is enabled but could not activate.
	StatusFailed PluginStatus = "failed"
	// StatusPending is enabled but inactive for a readable reason, such as a
	// denied required permission.
	StatusPending PluginStatus = "pending"
	// StatusDevelopment is an unpacked directory, never a registry install.
	StatusDevelopment PluginStatus = "development"
)

// PermissionStatus is one permission row in the plugin manager.
type PermissionStatus struct {
	Permission domainplugin.Permission
	Scope      domainplugin.Scope
	Required   bool
	Granted    bool
	Pending    bool
}

// InstalledPlugin is the plugin manager's read model. Source and Development
// are separate so a development directory never looks registry-installed.
type InstalledPlugin struct {
	ID            string
	Key           string
	Name          string
	Version       string
	Source        domainplugin.Source
	Development   bool
	Status        PluginStatus
	Enabled       bool
	Reason        string
	PackageSHA256 string
	InstalledAt   time.Time
	Permissions   []PermissionStatus
	State         domainplugin.State
	// HasPrevious reports a retained version available to rollback.
	HasPrevious bool
	// Warnings are the registry notices for the installed version: yanked,
	// deprecated, or revoked. They are advisory only and never uninstall.
	Warnings []domainplugin.ReleaseWarning
}

// ManagerOption customizes a Manager.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	feedback         func(PluginRef, string)
	logf             func(PluginRef, LogLevel, string)
	now              func() time.Time
	audit            AuditSink
	registry         Registry
	trust            TrustStore
	preferences      PreferencesStore
	configStore      ConfigStore
	clipboard        Clipboard
	notificationSink NotificationSink
	hostSettings     HostSettings
	hostCatalog      HostCatalog
	sessionCatalog   SessionCatalog
	terminal         TerminalCatalog
	remoteFiles      RemoteFiles
	network          NetworkBroker
	trustPins        TrustPinStore
	trustConfirmer   TrustConfirmer
	authBroker       AuthBroker
	vaultFields      VaultFieldSource
	webview          WebviewPort
	webviewDevTools  bool
	reservedPaths    []string
	serviceLimits    ServiceLimits
	serviceDataDir   func(pluginID string) string
	panel            *SystemPanel
	mcpTools         MCPToolRegistrar
	mcpToolsChanged  func()

	appVersion         string
	builtinState       BuiltinStateStore
	unloadTimeout      time.Duration
	globalUnloadBudget time.Duration

	catalogSource       CatalogSource
	locale              string
	localeFormatter     LocaleFormatter
	connectionHost      *ProviderHost
	connectionRegistrar ConnectionProviderRegistrar
	dataHost            *DataProviderHost
	pluginDataPurger    PluginDataPurger
	pluginDataDiag      PluginDataDiagnosticSource
}

// WithManagerPluginDataDiagnostics adds the provider-data diagnostics source
// to the manager's diagnostics report.
func WithManagerPluginDataDiagnostics(source PluginDataDiagnosticSource) ManagerOption {
	return func(o *managerOptions) { o.pluginDataDiag = source }
}

// PluginDataProviderStates resolves every loaded plugin's live lifecycle and
// declared namespace schema version. Reading it never starts a fiber.
func (m *Manager) PluginDataProviderStates(ctx context.Context) (map[string]PluginDataProviderState, error) {
	if m == nil || m.container == nil {
		return nil, ErrPluginDataUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	states := map[string]PluginDataProviderState{}
	for _, fiber := range m.container.Fibers() {
		manifest := fiber.Manifest()
		if manifest.ID == "" {
			continue
		}
		entry := PluginDataProviderState{
			Active:        fiber.State() == domainplugin.StateActive,
			SchemaVersion: manifest.EffectivePluginDataSchemaVersion(),
		}
		current, exists := states[manifest.ID]
		if !exists || entry.Active && !current.Active {
			states[manifest.ID] = entry
		}
	}
	return states, nil
}

// PluginDataPurger removes the host-opaque namespaces a plugin owns from every
// Host and DataProfile. Uninstall keeps this data by default; the manager calls
// the purger only from the explicit, confirmed purge paths.
type PluginDataPurger interface {
	PurgePluginData(ctx context.Context, pluginID string) error
}

// WithPluginDataPurger attaches the Vault adapter that clears a plugin's
// provider-defined field namespaces when the user confirms a purge.
func WithPluginDataPurger(purger PluginDataPurger) ManagerOption {
	return func(o *managerOptions) { o.pluginDataPurger = purger }
}

// WithAppVersion sets the host application version stamped onto compiled-in
// plugins. A built-in tracks the app it ships with; a non-semantic developer
// version leaves the manifest's declared version in place.
func WithAppVersion(version string) ManagerOption {
	return func(o *managerOptions) { o.appVersion = strings.TrimSpace(version) }
}

// WithBuiltinStateStore persists the enabled flag of compiled-in plugins. The
// host store owns the file; without one built-ins are enabled each launch.
func WithBuiltinStateStore(store BuiltinStateStore) ManagerOption {
	return func(o *managerOptions) {
		if store != nil {
			o.builtinState = store
		}
	}
}

// WithManagerUnloadTimeout bounds one fiber's teardown for every manager
// action. Non-positive values keep the container default.
func WithManagerUnloadTimeout(timeout time.Duration) ManagerOption {
	return func(o *managerOptions) { o.unloadTimeout = timeout }
}

// WithManagerGlobalUnloadBudget bounds Manager.Close, matching app quit.
// Non-positive values keep the container default.
func WithManagerGlobalUnloadBudget(budget time.Duration) ManagerOption {
	return func(o *managerOptions) { o.globalUnloadBudget = budget }
}

// WithSystemPanel attaches the host-owned System panel coordinator. The
// manager binds it to its container and routes the systempanel service
// through it. Without one, the manager creates a coordinator with no snapshot
// source, so plugin sections still render their own pushed data.
func WithSystemPanel(panel *SystemPanel) ManagerOption {
	return func(o *managerOptions) { o.panel = panel }
}

// WithMCPToolRegistrar attaches the external MCP tool registry the container
// publishes manifest-declared tools into.
func WithMCPToolRegistrar(registrar MCPToolRegistrar) ManagerOption {
	return func(o *managerOptions) { o.mcpTools = registrar }
}

// WithManagerConnectionProviders attaches the connection-provider host and the
// Session-facing registry. The host enforces protocol claims and owns the
// per-fiber provider instance; the registrar publishes each activated protocol
// so a Session can resolve and open it. Both are required for a manifest that
// declares a connection contribution.
func WithManagerConnectionProviders(host *ProviderHost, registrar ConnectionProviderRegistrar) ManagerOption {
	return func(o *managerOptions) {
		o.connectionHost = host
		o.connectionRegistrar = registrar
	}
}

// WithManagerDataProviders attaches the data-provider host. The host owns the
// profile records, pools, and access-mode enforcement; the container
// publishes each activated data provider into it. Without one, a manifest that
// declares a data contribution fails activation.
func WithManagerDataProviders(host *DataProviderHost) ManagerOption {
	return func(o *managerOptions) { o.dataHost = host }
}

// WithMCPToolsChanged observes a change to the set of visible plugin MCP
// tools. The app forwards it as notifications/tools/list_changed. The hook
// runs without manager or container locks held.
func WithMCPToolsChanged(fn func()) ManagerOption {
	return func(o *managerOptions) { o.mcpToolsChanged = fn }
}

// WithConfigStore attaches the host-side, schema-validated configuration
// store. Without one, plugin settings stay at their declared defaults.
func WithConfigStore(store ConfigStore) ManagerOption {
	return func(o *managerOptions) { o.configStore = store }
}

// WithClipboard attaches the host clipboard port used by clipboard.read and
// clipboard.write. Without one those calls fail with a typed error.
func WithClipboard(clipboard Clipboard) ManagerOption {
	return func(o *managerOptions) { o.clipboard = clipboard }
}

// WithNotificationSink attaches the host notification UI. Without one
// notifications.post fails with a typed error instead of silently dropping.
func WithNotificationSink(sink NotificationSink) ManagerOption {
	return func(o *managerOptions) { o.notificationSink = sink }
}

// WithHostSettings attaches the read-only host settings namespace used by
// settings.read. The app supplies key-scoped values; plugins cannot write.
func WithHostSettings(settings HostSettings) ManagerOption {
	return func(o *managerOptions) { o.hostSettings = settings }
}

// WithHostCatalog attaches the Vault-backed Host port used by the hosts
// service. Without one, hosts calls fail with a typed fault.
func WithHostCatalog(catalog HostCatalog) ManagerOption {
	return func(o *managerOptions) { o.hostCatalog = catalog }
}

// WithSessionCatalog attaches the session-manager port used by the sessions
// service. Without one, sessions calls fail with a typed fault.
func WithSessionCatalog(catalog SessionCatalog) ManagerOption {
	return func(o *managerOptions) { o.sessionCatalog = catalog }
}

// WithTerminal attaches the terminal observation and input port. Without one,
// terminal calls fail with a typed fault; the tap grants no other capability.
func WithTerminal(terminal TerminalCatalog) ManagerOption {
	return func(o *managerOptions) { o.terminal = terminal }
}

// WithRemoteFiles attaches the Host-mediated remote file port used by
// files.remote_*. Without one, remote calls fail with a typed fault.
func WithRemoteFiles(files RemoteFiles) ManagerOption {
	return func(o *managerOptions) { o.remoteFiles = files }
}

// WithNetworkBroker attaches the host-owned transport used by network.dial.
// Without one, every dial fails with a typed fault: a plugin can never fall
// back to guest-side sockets.
func WithNetworkBroker(broker NetworkBroker) ManagerOption {
	return func(o *managerOptions) { o.network = broker }
}

// WithExternalTrust attaches the host-owned trust pin store and confirmer
// used by trust.verify. A nil store keeps a process-local in-memory store; a
// nil confirmer leaves unknown material untrusted.
func WithExternalTrust(pins TrustPinStore, confirmer TrustConfirmer) ManagerOption {
	return func(o *managerOptions) {
		o.trustPins = pins
		o.trustConfirmer = confirmer
	}
}

// WithAuthBroker attaches the host-owned authentication engine used by
// auth.begin/respond/cancel. Plugins can drive a flow but never see or supply
// a credential.
func WithAuthBroker(broker AuthBroker) ManagerOption {
	return func(o *managerOptions) { o.authBroker = broker }
}

// WithVaultFields attaches the host-side Vault descriptor and value source
// used by vault.metadata, vault.fields, and vault.leases. Field class is read
// from this source; a plugin can never declare a field guest-deliverable.
func WithVaultFields(source VaultFieldSource) ManagerOption {
	return func(o *managerOptions) { o.vaultFields = source }
}

// WithWebviewPort attaches the platform webview port. Without it, plugin
// webview surfaces report a typed unavailable error.
func WithWebviewPort(port WebviewPort) ManagerOption {
	return func(o *managerOptions) { o.webview = port }
}

// WithWebviewDevTools enables the sandboxed page inspector. The app passes
// true only in development mode; production leaves it off.
func WithWebviewDevTools(enabled bool) ManagerOption {
	return func(o *managerOptions) { o.webviewDevTools = enabled }
}

// WithReservedPaths marks host filesystem directories that stay inaccessible
// even when a granted file root contains them. The composition root passes the
// append-only plugin audit directory so a plugin can never read, write, or
// delete it through files.*.
func WithReservedPaths(paths ...string) ManagerOption {
	return func(o *managerOptions) { o.reservedPaths = append(o.reservedPaths, paths...) }
}

// WithServiceLimits overrides the host-service quotas. Zero fields keep their
// contract defaults.
func WithServiceLimits(limits ServiceLimits) ManagerOption {
	return func(o *managerOptions) { o.serviceLimits = limits }
}

// WithServiceDataDir supplies the version-independent plugin data directory
// used by private storage. Without one, storage stays in memory.
func WithServiceDataDir(dir func(pluginID string) string) ManagerOption {
	return func(o *managerOptions) { o.serviceDataDir = dir }
}

// WithPluginCatalogSource attaches the i18n catalog loader. The same loader
// validates .natplug installs and first-party embedded catalogs.
func WithPluginCatalogSource(source CatalogSource) ManagerOption {
	return func(o *managerOptions) { o.catalogSource = source }
}

// WithLocale sets the host locale used by plugin surfaces and the i18n
// service. The app updates it through SetLocale when the user changes the
// language, so plugin instances are never reloaded for a locale change.
func WithLocale(locale string) ManagerOption {
	return func(o *managerOptions) { o.locale = locale }
}

// WithLocaleFormatter attaches the host formatter for numbers, dates, and
// relative times in plugin text.
func WithLocaleFormatter(formatter LocaleFormatter) ManagerOption {
	return func(o *managerOptions) { o.localeFormatter = formatter }
}

// WithManagerFeedback receives visible guest output.
func WithManagerFeedback(fn func(PluginRef, string)) ManagerOption {
	return func(o *managerOptions) { o.feedback = fn }
}

// WithManagerLogf receives guest log lines.
func WithManagerLogf(fn func(PluginRef, LogLevel, string)) ManagerOption {
	return func(o *managerOptions) { o.logf = fn }
}

// WithManagerClock overrides the time source for tests and deterministic
// install metadata.
func WithManagerClock(now func() time.Time) ManagerOption {
	return func(o *managerOptions) {
		if now != nil {
			o.now = now
		}
	}
}

// WithAudit sets the append-only audit sink.
func WithAudit(sink AuditSink) ManagerOption {
	return func(o *managerOptions) {
		if sink != nil {
			o.audit = sink
		}
	}
}

// WithRegistry configures the static plugin registry used for search, install,
// update, rollback, and dependency resolution.
func WithRegistry(registry Registry) ManagerOption {
	return func(o *managerOptions) { o.registry = registry }
}

// WithTrustStore configures persistent trust-on-first-use pins. The default is
// an in-memory store for the life of the process.
func WithTrustStore(trust TrustStore) ManagerOption {
	return func(o *managerOptions) {
		if trust != nil {
			o.trust = trust
		}
	}
}

// WithPreferencesStore configures the persisted registry policy.
func WithPreferencesStore(preferences PreferencesStore) ManagerOption {
	return func(o *managerOptions) {
		if preferences != nil {
			o.preferences = preferences
		}
	}
}

// Manager owns local install, enable/disable consent, authority grants,
// uninstall/purge, audit events, and development-directory loading. It owns
// one container, so the same registry and runtime serve installed and
// development plugins.
type Manager struct {
	container       *Container
	store           PackageStore
	validator       PackageValidator
	manifests       ManifestSource
	configStore     ConfigStore
	authority       *Authority
	services        *HostServices
	language        *LanguageHost
	panel           *SystemPanel
	audit           AuditSink
	now             func() time.Time
	registry        Registry
	trust           TrustStore
	preferences     PreferencesStore
	mcpToolsChanged func()
	catalogSource   CatalogSource

	appVersion       string
	builtinState     BuiltinStateStore
	pluginDataPurger PluginDataPurger
	pluginDataDiag   PluginDataDiagnosticSource

	mu           sync.Mutex
	dev          map[string]*DevelopmentPlugin
	order        []string
	diagnostics  map[string]DiagnosticsEntry
	builtins     map[string]*builtinEntry
	builtinOrder []string
	// builtinMu serializes registration and enable/disable so a host action
	// and app startup cannot interleave one built-in's activation.
	builtinMu sync.Mutex
}

// DevelopmentPlugin is an unpacked directory loaded in developer mode.
type DevelopmentPlugin struct {
	Directory string
	Fiber     *Fiber
	// PathGrant is the canonical directory the plugin may read as a
	// development source.
	PathGrant domainplugin.Scope
}

// NewManager wires the container, authority, audit, and package services. A
// nil store yields errors on install/list rather than a panic.
func NewManager(runtime Runtime, manifests ManifestSource,
	contributions ContributionRegistrar, validator PackageValidator,
	store PackageStore, opts ...ManagerOption) *Manager {
	o := managerOptions{
		now:         time.Now,
		audit:       NopAudit{},
		trust:       NewMemoryTrustStore(),
		preferences: NewMemoryPreferencesStore(),
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.trust == nil {
		o.trust = NewMemoryTrustStore()
	}
	if o.preferences == nil {
		o.preferences = NewMemoryPreferencesStore()
	}
	m := &Manager{
		store:            store,
		validator:        validator,
		manifests:        manifests,
		configStore:      o.configStore,
		authority:        NewAuthority(),
		audit:            o.audit,
		now:              o.now,
		registry:         o.registry,
		trust:            o.trust,
		preferences:      o.preferences,
		mcpToolsChanged:  o.mcpToolsChanged,
		appVersion:       o.appVersion,
		builtinState:     o.builtinState,
		pluginDataPurger: o.pluginDataPurger,
		pluginDataDiag:   o.pluginDataDiag,
		catalogSource:    o.catalogSource,
		dev:              make(map[string]*DevelopmentPlugin),
		diagnostics:      make(map[string]DiagnosticsEntry),
		builtins:         make(map[string]*builtinEntry),
	}
	panel := o.panel
	if panel == nil {
		panel = NewSystemPanel(PanelDeps{})
	}
	// The manager owns the canonical audit sink and clock, so section actions
	// and lifecycle events always land in the same append-only log.
	panel.audit = m.audit
	panel.now = m.now
	m.panel = panel
	m.services = NewHostServices(ServiceDeps{
		Authority:       m.authority,
		Audit:           m.audit,
		Now:             m.now,
		DataDir:         o.serviceDataDir,
		Clipboard:       o.clipboard,
		Notifications:   o.notificationSink,
		HostSettings:    o.hostSettings,
		Panel:           panel,
		Hosts:           o.hostCatalog,
		Sessions:        o.sessionCatalog,
		Terminal:        o.terminal,
		RemoteFiles:     o.remoteFiles,
		Network:         o.network,
		TrustPins:       o.trustPins,
		TrustConfirmer:  o.trustConfirmer,
		Auth:            o.authBroker,
		Vault:           o.vaultFields,
		Webview:         o.webview,
		WebviewDevTools: o.webviewDevTools,
		ReservedPaths:   o.reservedPaths,
		Limits:          o.serviceLimits,
		Locale:          o.locale,
		LocaleFormatter: o.localeFormatter,
	})
	m.language = NewLanguageHost(LanguageHostOptions{
		Authority: m.authority,
		Audit:     m.audit,
		Now:       m.now,
		Builtin:   DefaultLanguageBuiltin(),
	})
	containerOptions := []Option{
		WithLifecycle(m.recordLifecycle),
		WithFault(m.recordFault),
		WithHostServices(m.services),
		WithLanguageHost(m.language),
		WithSettingsResolver(m.resolveSettings),
		WithRequiredPermissions(m.checkRequiredPermission),
		WithServiceAuthority(m.authority),
		WithServiceAudit(m.audit),
		WithServiceChanged(func(ref PluginRef, event ServiceChangedEvent) {
			m.services.PublishTo(ref, domainplugin.EventServiceChanged, event)
		}),
		WithCatalogSource(o.catalogSource),
	}
	if o.unloadTimeout > 0 {
		containerOptions = append(containerOptions, WithUnloadTimeout(o.unloadTimeout))
	}
	if o.globalUnloadBudget > 0 {
		containerOptions = append(containerOptions, WithGlobalUnloadBudget(o.globalUnloadBudget))
	}
	panel.SetLocalize(m.Localize)
	if o.feedback != nil {
		containerOptions = append(containerOptions, WithFeedback(o.feedback))
	}
	if o.logf != nil {
		containerOptions = append(containerOptions, WithLogf(o.logf))
	}
	if o.mcpTools != nil {
		containerOptions = append(containerOptions, WithMCPToolsRegistrar(o.mcpTools))
	}
	if o.connectionHost != nil && o.connectionRegistrar != nil {
		containerOptions = append(containerOptions,
			WithConnectionProviders(o.connectionHost, o.connectionRegistrar))
	}
	if o.dataHost != nil {
		containerOptions = append(containerOptions, WithDataProviders(o.dataHost))
	}
	m.container = NewContainer(runtime, manifests, contributions, containerOptions...)
	panel.Bind(m.container.PanelFibers)
	return m
}

// Services exposes the host-service implementation for diagnostics and for
// host-initiated notification actions.
func (m *Manager) Services() *HostServices { return m.services }

// Language exposes the editor language-service router used by the editor and
// by diagnostics.
func (m *Manager) Language() *LanguageHost { return m.language }

// Container exposes the underlying container for diagnostics. The manager
// owns its lifecycle; callers must not close it separately.
func (m *Manager) Container() *Container { return m.container }

// Authority exposes the capability authority for host-service boundaries and
// diagnostics.
func (m *Manager) Authority() *Authority { return m.authority }

// Audit returns the audit sink, for tests and diagnostics.
func (m *Manager) Audit() AuditSink { return m.audit }

// auditDropCounter is implemented by sinks that count events they discarded.
type auditDropCounter interface {
	Dropped() uint64
}

// AuditDropped reports how many audit events the sink discarded because its
// queue or disk write failed. The host surfaces a non-zero count at shutdown;
// sinks without a counter report zero.
func (m *Manager) AuditDropped() uint64 {
	if m == nil {
		return 0
	}
	if counter, ok := m.audit.(auditDropCounter); ok {
		return counter.Dropped()
	}
	return 0
}

// List returns every installed plugin, every development directory, and every
// compiled-in first-party plugin in stable order. Fiber state is read live.
// Built-ins appear even in a host without a package store, which is how a
// first-party-only composition lists them.
func (m *Manager) List(ctx context.Context) ([]InstalledPlugin, error) {
	if m == nil {
		return nil, errors.New("plugin: package store is not configured")
	}
	m.mu.Lock()
	builtinCount := len(m.builtinOrder)
	m.mu.Unlock()
	if m.store == nil && builtinCount == 0 {
		return nil, errors.New("plugin: package store is not configured")
	}
	var records []domainplugin.InstallRecord
	if m.store != nil {
		var err error
		records, err = m.store.Records(ctx)
		if err != nil {
			return nil, err
		}
	}
	out := make([]InstalledPlugin, 0, len(records))
	for _, record := range records {
		summary := m.summary(record)
		m.attachRegistryWarnings(ctx, &summary)
		out = append(out, summary)
	}
	m.mu.Lock()
	keys := append([]string(nil), m.order...)
	m.mu.Unlock()
	for _, key := range keys {
		m.mu.Lock()
		dev := m.dev[key]
		m.mu.Unlock()
		if dev == nil {
			continue
		}
		out = append(out, m.developmentSummary(key, dev))
	}
	out = append(out, m.builtinSummaries()...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Key < out[j].Key
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *Manager) summary(record domainplugin.InstallRecord) InstalledPlugin {
	summary := InstalledPlugin{
		ID:            record.ID,
		Key:           string(record.Source) + "/" + record.ID,
		Name:          record.Name,
		Version:       record.Version,
		Source:        record.Source,
		Status:        StatusDisabled,
		Enabled:       record.Enabled,
		Reason:        record.Reason,
		PackageSHA256: record.PackageSHA256,
		InstalledAt:   record.InstalledAt,
		HasPrevious:   record.Previous != nil,
	}
	if fiber, ok := m.fiberFor(record.ID); ok {
		summary.State = fiber.State()
		summary.Reason = fiber.Reason()
		if summary.Reason == "" && fiber.State() != domainplugin.StateActive {
			summary.Reason = record.Reason
		}
		switch fiber.State() {
		case domainplugin.StateActive:
			summary.Status = StatusActive
		case domainplugin.StateFailed:
			summary.Status = StatusFailed
		case domainplugin.StatePending, domainplugin.StateLoading, domainplugin.StateUnloading:
			summary.Status = StatusPending
		}
	}
	if record.Enabled && summary.Status == StatusDisabled {
		summary.Status = StatusPending
	}
	// A record that is not enabled but carries a reason was denied or had a
	// required permission revoked. Issue 05 calls that state PENDING, not the
	// plain disabled state of a never-enabled install.
	if !record.Enabled && record.Reason != "" {
		summary.Status = StatusPending
	}
	summary.Permissions = permissionStatuses(record)
	return summary
}

func (m *Manager) developmentSummary(key string, dev *DevelopmentPlugin) InstalledPlugin {
	manifest := dev.Fiber.Manifest()
	summary := InstalledPlugin{
		ID:          manifest.ID,
		Key:         string(domainplugin.SourceDevelopment) + "/" + key,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Source:      domainplugin.SourceDevelopment,
		Development: true,
		Status:      StatusDevelopment,
		Enabled:     true,
		State:       dev.Fiber.State(),
		Reason:      dev.Fiber.Reason(),
	}
	for _, request := range manifest.Permissions.Required {
		info, ok := domainplugin.PermissionByName(request)
		if !ok {
			continue
		}
		status := PermissionStatus{Permission: info.Permission, Required: true}
		if scope, ok := manifest.Permissions.Scopes[request]; ok {
			scope.Kind = info.Scope
			status.Scope = scope
		}
		if info.Permission == domainplugin.PermissionFilesRead {
			status.Scope = dev.PathGrant
			status.Granted = true
		}
		summary.Permissions = append(summary.Permissions, status)
	}
	for _, request := range manifest.Permissions.Optional {
		info, ok := domainplugin.PermissionByName(request)
		if !ok {
			continue
		}
		status := PermissionStatus{Permission: info.Permission}
		if scope, ok := manifest.Permissions.Scopes[request]; ok {
			scope.Kind = info.Scope
			status.Scope = scope
		}
		summary.Permissions = append(summary.Permissions, status)
	}
	if summary.State == domainplugin.StateFailed {
		summary.Status = StatusFailed
	}
	return summary
}

func permissionStatuses(record domainplugin.InstallRecord) []PermissionStatus {
	out := make([]PermissionStatus, 0, len(record.Required)+len(record.Optional))
	for _, request := range record.Required {
		grant, ok := record.Grant(request.Permission)
		out = append(out, PermissionStatus{
			Permission: request.Permission, Scope: grantedScope(grant, request.Scope),
			Required: true, Granted: ok, Pending: ok && grant.Pending,
		})
	}
	for _, request := range record.Optional {
		grant, ok := record.Grant(request.Permission)
		out = append(out, PermissionStatus{
			Permission: request.Permission, Scope: grantedScope(grant, request.Scope),
			Granted: ok, Pending: ok && grant.Pending,
		})
	}
	return out
}

func grantedScope(grant domainplugin.PermissionGrant, declared domainplugin.Scope) domainplugin.Scope {
	if !grant.Scope.IsZero() {
		return grant.Scope
	}
	return declared
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (m *Manager) fiberFor(id string) (*Fiber, bool) {
	return m.container.Fiber(domainplugin.NewFiberID("installed", id, 1))
}

// Detail returns one installed or development plugin.
func (m *Manager) Detail(ctx context.Context, key string) (InstalledPlugin, bool, error) {
	all, err := m.List(ctx)
	if err != nil {
		return InstalledPlugin{}, false, err
	}
	for _, plugin := range all {
		if plugin.Key == key || plugin.ID == key {
			return plugin, true, nil
		}
	}
	return InstalledPlugin{}, false, nil
}

// Review returns the install-source-labelled consent screen for an installed
// plugin.
func (m *Manager) Review(ctx context.Context, id string) (domainplugin.ConsentReview, error) {
	if err := m.builtinImmutable(id, "reviewed for consent"); err != nil {
		return domainplugin.ConsentReview{}, err
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return domainplugin.ConsentReview{}, err
	}
	if !ok {
		return domainplugin.ConsentReview{}, fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	return record.ConsentReview(), nil
}

// Enable grants the consented permissions and activates an installed plugin.
// A denied required permission keeps the plugin installed and inactive with a
// readable reason. A compiled-in plugin is enabled without a consent screen:
// its code ships with the app, so its declared permissions are already
// granted.
func (m *Manager) Enable(ctx context.Context, id string,
	decisions []domainplugin.PermissionDecision) error {
	if entry, ok := m.builtinEntryByID(id); ok {
		return m.enableBuiltin(ctx, entry)
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if record.Enabled {
		return fmt.Errorf("%w: %s", ErrAlreadyEnabled, id)
	}
	index, err := decisionIndex(record, decisions)
	if err != nil {
		return err
	}

	now := m.now().UTC()
	var grants []domainplugin.PermissionGrant
	var remembered []domainplugin.RememberedGrant
	for _, request := range record.Required {
		decision, provided := index[request.Permission]
		grant, grantErr := m.grantFor(request, decision, provided, false, now)
		if grantErr != nil || !provided || !decision.Allow {
			reason := domainplugin.PermissionReason(
				domainplugin.ReasonPermissionDenied, request.Permission)
			if grantErr != nil {
				reason = grantErr.Error()
			}
			record.Enabled = false
			record.Grants = nil
			record.Reason = reason
			if saveErr := m.store.SaveRecord(ctx, record); saveErr != nil {
				return errors.Join(grantErr, saveErr)
			}
			m.audit.Record(domainplugin.AuditEvent{
				Time: now, Event: domainplugin.AuditDeny, PluginID: record.ID,
				Source: record.Source, Version: record.Version,
				Permission: request.Permission, Scope: scopePointer(request.Scope),
				Reason: "required",
			})
			m.audit.Record(domainplugin.AuditEvent{
				Time: now, Event: domainplugin.AuditActivation, PluginID: record.ID,
				Source: record.Source, Version: record.Version,
				State: domainplugin.StatePending, Reason: reason,
			})
			if grantErr != nil {
				return grantErr
			}
			return &PermissionDeniedError{Permission: request.Permission}
		}
		grants = append(grants, grant)
	}
	for _, request := range record.Optional {
		decision, provided := index[request.Permission]
		if !provided || !decision.Allow {
			continue
		}
		grant, grantErr := m.grantFor(request, decision, provided, true, now)
		if grantErr != nil {
			return grantErr
		}
		grants = append(grants, grant)
		if decision.Remember {
			remembered = append(remembered, domainplugin.RememberedGrant{
				Permission: request.Permission, Scope: grant.Scope, GrantedAt: now,
			})
		}
	}
	for _, remember := range remembered {
		record.Remember(remember)
	}
	record.Grants = grants
	record.Enabled = true
	record.Reason = ""
	if err := m.store.SaveRecord(ctx, record); err != nil {
		return err
	}
	m.authority.SetGrants(installedSubject(record), grants)
	m.audit.Record(domainplugin.AuditEvent{
		Time: now, Event: domainplugin.AuditEnable, PluginID: record.ID,
		Source: record.Source, Version: record.Version,
	})
	for _, grant := range grants {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditGrant, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
			Permission: grant.Permission, Scope: scopePointer(grant.Scope),
			Reason: grantReason(grant),
		})
	}

	dir, err := m.store.PackageDir(ctx, id)
	if err != nil {
		return err
	}
	_, loadErr := m.container.LoadInstalled(ctx, dir, record.Source)
	if loadErr != nil {
		reason, validation := configLoadReason(loadErr)
		record.Reason = reason
		if validation != nil {
			m.setDiagnostic(record.ID, validation)
		} else {
			m.clearDiagnostic(record.ID)
		}
		_ = m.store.SaveRecord(ctx, record)
		return loadErr
	}
	m.clearDiagnostic(record.ID)
	return nil
}

func (m *Manager) grantFor(request domainplugin.PermissionRequest,
	decision domainplugin.PermissionDecision, provided, optional bool,
	now time.Time) (domainplugin.PermissionGrant, error) {
	if provided && !decision.Allow {
		return domainplugin.PermissionGrant{}, nil
	}
	scope := request.Scope
	if !decision.Scope.IsZero() {
		kind, ok := domainplugin.ScopeKindFor(string(request.Permission))
		if !ok {
			return domainplugin.PermissionGrant{}, &InvalidRequestError{
				Permission: request.Permission, Problem: "unknown permission",
			}
		}
		scope = decision.Scope
		scope.Kind = kind
		if err := scope.Validate(); err != nil {
			return domainplugin.PermissionGrant{}, &InvalidRequestError{
				Permission: request.Permission, Problem: err.Error(),
			}
		}
	}
	if domainplugin.IsResourceScoped(string(request.Permission)) && scope.IsZero() {
		return domainplugin.PermissionGrant{}, &InvalidRequestError{
			Permission: request.Permission, Problem: "requires a declared resource scope",
		}
	}
	if !scope.IsZero() && scope.Kind != request.Scope.Kind {
		if request.Scope.Kind != domainplugin.ScopeNone {
			return domainplugin.PermissionGrant{}, &InvalidRequestError{
				Permission: request.Permission, Problem: "scope does not match the declared scope",
			}
		}
	}
	return domainplugin.PermissionGrant{
		Permission: request.Permission,
		Scope:      scope,
		Optional:   optional,
		GrantedAt:  now,
	}, nil
}

// installedSubject returns the authority subject for one install record. The
// install source is part of the identity so development and installed copies
// of the same plugin id never share grants or revocation epochs.
func installedSubject(record domainplugin.InstallRecord) AuthoritySubject {
	return SubjectForInstalled(record.Source, record.ID)
}

func grantReason(domainplugin.PermissionGrant) string {
	return ""
}

func decisionIndex(record domainplugin.InstallRecord,
	decisions []domainplugin.PermissionDecision) (map[domainplugin.Permission]domainplugin.PermissionDecision, error) {
	declared := make(map[domainplugin.Permission]bool, len(record.Required)+len(record.Optional))
	for _, request := range record.Required {
		declared[request.Permission] = true
	}
	for _, request := range record.Optional {
		declared[request.Permission] = true
	}
	index := make(map[domainplugin.Permission]domainplugin.PermissionDecision, len(decisions))
	for _, decision := range decisions {
		if !declared[decision.Permission] {
			return nil, &InvalidRequestError{
				Permission: decision.Permission, Problem: "was not requested by the manifest",
			}
		}
		index[decision.Permission] = decision
	}
	return index, nil
}

func scopePointer(scope domainplugin.Scope) *domainplugin.Scope {
	if scope.IsZero() {
		return nil
	}
	copy := scope
	return &copy
}

// Disable unloads an enabled plugin and revokes every capability immediately.
// A compiled-in plugin keeps its manifest and permission list visible and can
// be enabled again.
func (m *Manager) Disable(ctx context.Context, id string) error {
	if entry, ok := m.builtinEntryByID(id); ok {
		return m.disableBuiltin(ctx, entry)
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if !record.Enabled {
		return fmt.Errorf("%w: %s", ErrNotEnabled, id)
	}
	now := m.now().UTC()
	m.authority.Reset(installedSubject(record))
	// An unload failure does not undo the revocation: persist the disabled
	// record and audit the revoke/disable events first, then report the unload
	// error. Otherwise a restart would resurrect the revoked grants.
	var unloadErr error
	if fiber, ok := m.fiberFor(id); ok {
		unloadErr = m.container.Unload(ctx, fiber.ID())
	}
	for _, grant := range record.Grants {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditRevoke, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
			Permission: grant.Permission, Scope: scopePointer(grant.Scope),
			Reason: "disable",
		})
	}
	record.Enabled = false
	record.Reason = ""
	record.Grants = nil
	saveErr := m.store.SaveRecord(ctx, record)
	if saveErr == nil {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditDisable, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
		})
	}
	return errors.Join(unloadErr, saveErr)
}

// Revoke removes one grant immediately. Every issued handle fails at once.
// Revoking a required permission unloads the plugin; an optional permission
// leaves it active with typed failures.
func (m *Manager) Revoke(ctx context.Context, id string,
	permission domainplugin.Permission) error {
	if err := m.builtinImmutable(id, "revoked"); err != nil {
		return err
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if !record.Enabled {
		return fmt.Errorf("%w: %s", ErrNotEnabled, id)
	}
	if _, exists := record.Grant(permission); !exists {
		return fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
	}
	now := m.now().UTC()
	m.authority.Revoke(installedSubject(record), permission)
	required := recordIsRequired(record, permission)
	record.RemoveGrant(permission)
	record.Reason = ""
	// A required revoke unloads the plugin, but a hostile shutdown must not
	// roll back the revocation. Save the revocation and audit it even when the
	// unload fails, then return the unload error.
	var unloadErr error
	if required {
		if fiber, ok := m.fiberFor(id); ok {
			unloadErr = m.container.Unload(ctx, fiber.ID())
		}
		record.Enabled = false
		record.Reason = domainplugin.PermissionReason(
			domainplugin.ReasonPermissionRevoked, permission)
	}
	saveErr := m.store.SaveRecord(ctx, record)
	if saveErr == nil {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditRevoke, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
			Permission: permission, Reason: revokeReason(required),
		})
	}
	if permission == domainplugin.PermissionMCPTools {
		// A required revoke unloads (and the effect disposer also reports a
		// change); an optional revoke leaves the plugin ACTIVE with its tools
		// hidden, so the client list must be refreshed either way.
		m.notifyMCPToolsChanged()
	}
	return errors.Join(unloadErr, saveErr)
}

// notifyMCPToolsChanged runs the tool-list observer without any manager lock
// held. Audit and notification failures never block a tool call.
func (m *Manager) notifyMCPToolsChanged() {
	if m == nil || m.mcpToolsChanged == nil {
		return
	}
	m.mcpToolsChanged()
}

func revokeReason(required bool) string {
	if required {
		return "required"
	}
	return "optional"
}

func recordIsRequired(record domainplugin.InstallRecord, permission domainplugin.Permission) bool {
	for _, request := range record.Required {
		if request.Permission == permission {
			return true
		}
	}
	return false
}

// RequestOptional handles a first-use optional permission prompt. A grant the
// user asked to remember for the same scope is returned without prompting.
func (m *Manager) RequestOptional(ctx context.Context, id string,
	permission domainplugin.Permission, scope domainplugin.Scope, allow, remember bool) error {
	if err := m.builtinImmutable(id, "asked for first-use consent"); err != nil {
		return err
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if !record.Enabled {
		return fmt.Errorf("%w: %s", ErrNotEnabled, id)
	}
	request, declared := optionalRequest(record, permission)
	if !declared {
		return fmt.Errorf("%w: %s", ErrOptionalNotDeclared, permission)
	}
	kind, _ := domainplugin.ScopeKindFor(string(permission))
	if scope.IsZero() {
		scope = request.Scope
	}
	if !scope.IsZero() {
		scope.Kind = kind
		if err := scope.Validate(); err != nil {
			return &InvalidRequestError{Permission: permission, Problem: err.Error()}
		}
	}
	if scope.IsZero() && domainplugin.IsResourceScoped(string(permission)) {
		return &InvalidRequestError{Permission: permission, Problem: "a first-use request requires a resource scope"}
	}
	if err := m.authority.Check(installedSubject(record), permission, scope); err == nil {
		return nil
	}
	now := m.now().UTC()
	if record.WasRemembered(permission, scope) {
		grant := domainplugin.PermissionGrant{
			Permission: permission, Scope: scope, Optional: true, GrantedAt: now,
		}
		record.SetGrant(grant)
		if err := m.store.SaveRecord(ctx, record); err != nil {
			return err
		}
		m.authority.Grant(installedSubject(record), grant)
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditGrant, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
			Permission: permission, Scope: scopePointer(scope),
			Reason: "optional remembered",
		})
		if permission == domainplugin.PermissionMCPTools {
			m.notifyMCPToolsChanged()
		}
		return nil
	}
	if !allow {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditDeny, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
			Permission: permission, Scope: scopePointer(scope), Reason: "optional first use",
		})
		return &OptionalDeniedError{Permission: permission}
	}
	grant := domainplugin.PermissionGrant{
		Permission: permission, Scope: scope, Optional: true, GrantedAt: now,
	}
	record.SetGrant(grant)
	if remember {
		record.Remember(domainplugin.RememberedGrant{
			Permission: permission, Scope: scope, GrantedAt: now,
		})
	}
	if err := m.store.SaveRecord(ctx, record); err != nil {
		return err
	}
	m.authority.Grant(installedSubject(record), grant)
	m.audit.Record(domainplugin.AuditEvent{
		Time: now, Event: domainplugin.AuditGrant, PluginID: record.ID,
		Source: record.Source, Version: record.Version,
		Permission: permission, Scope: scopePointer(scope),
		Reason: rememberReason(remember),
	})
	if permission == domainplugin.PermissionMCPTools {
		m.notifyMCPToolsChanged()
	}
	return nil
}

func rememberReason(remember bool) string {
	if remember {
		return "optional remembered"
	}
	return "optional first use"
}

func optionalRequest(record domainplugin.InstallRecord,
	permission domainplugin.Permission) (domainplugin.PermissionRequest, bool) {
	for _, request := range record.Optional {
		if request.Permission == permission {
			return request, true
		}
	}
	return domainplugin.PermissionRequest{}, false
}

// PurgeData deletes a plugin's version-independent data after a separate
// confirmation. The package and install metadata are untouched.
func (m *Manager) PurgeData(ctx context.Context, id string) error {
	if err := m.builtinImmutable(id, "purged"); err != nil {
		return err
	}
	if _, ok, err := m.store.Record(ctx, id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if err := m.store.PurgeData(ctx, id); err != nil {
		return err
	}
	if m.pluginDataPurger != nil {
		if err := m.pluginDataPurger.PurgePluginData(ctx, id); err != nil {
			return fmt.Errorf("plugin: purge provider data for %s: %w", id, err)
		}
	}
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditPurge, PluginID: id,
	})
	return nil
}

// Uninstall unloads, removes the package and install metadata, and keeps the
// data directory unless purge is true. Purging is a separate confirmation in
// the UI and a separate store operation.
func (m *Manager) Uninstall(ctx context.Context, id string, purge bool) error {
	if err := m.builtinImmutable(id, "uninstalled"); err != nil {
		return err
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	dependents, err := m.enabledDependents(ctx, id)
	if err != nil {
		return err
	}
	if len(dependents) > 0 {
		return &DependentsError{ID: id, Dependents: dependents}
	}
	// A failed or hung shutdown must not leave the package installed after the
	// user confirmed uninstall, nor skip the audit event. Remove the durable
	// record and audit first, then report the unload error.
	var unloadErr error
	if fiber, ok := m.fiberFor(id); ok {
		unloadErr = m.container.Unload(ctx, fiber.ID())
	}
	m.authority.Reset(installedSubject(record))
	// Page data is never durable, but an uninstall still clears the origin's
	// live session data. A host without a webview port has nothing to clear.
	if m.services != nil {
		_ = m.services.ClearWebviewStorage(id)
	}
	removeErr := m.store.Remove(ctx, id, purge)
	if removeErr != nil {
		return errors.Join(unloadErr, removeErr)
	}
	if purge && m.pluginDataPurger != nil {
		// Uninstall without purge deliberately keeps provider data. With the
		// separate purge confirmation, the namespaces leave the Vault too.
		if err := m.pluginDataPurger.PurgePluginData(ctx, id); err != nil {
			unloadErr = errors.Join(unloadErr,
				fmt.Errorf("plugin: purge provider data for %s: %w", id, err))
		}
	}
	now := m.now().UTC()
	m.audit.Record(domainplugin.AuditEvent{
		Time: now, Event: domainplugin.AuditUninstall, PluginID: record.ID,
		Source: record.Source, Version: record.Version,
	})
	if purge {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditPurge, PluginID: record.ID,
			Source: record.Source, Version: record.Version,
		})
	}
	return unloadErr
}

// LoadDevelopment loads an unpacked directory. The directory is recorded as a
// canonical path-scoped grant and the entry is labelled development; it never
// enters the installed store.
func (m *Manager) LoadDevelopment(ctx context.Context, directory string) (*DevelopmentPlugin, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("plugin: resolve development directory %q: %w", directory, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("plugin: resolve development directory %q: %w", directory, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("plugin: development directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plugin: development path %q is not a directory", directory)
	}
	fiber, err := m.container.LoadDevelopment(ctx, canonical)
	if err != nil {
		// The failed fiber stays visible with its reason, but the directory is
		// not listed as a usable development plugin.
		return nil, err
	}
	manifest := fiber.Manifest()
	scope := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: filepath.ToSlash(canonical)}
	pathGrant := domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionFilesRead,
		Scope:      scope,
		GrantedAt:  m.now().UTC(),
	}
	m.authority.Grant(SubjectForDevelopment(manifest.ID), pathGrant)
	dev := &DevelopmentPlugin{Directory: canonical, Fiber: fiber, PathGrant: scope}
	m.mu.Lock()
	if _, exists := m.dev[manifest.ID]; !exists {
		m.order = append(m.order, manifest.ID)
	}
	m.dev[manifest.ID] = dev
	m.mu.Unlock()
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditDevelopment, PluginID: manifest.ID,
		Source: domainplugin.SourceDevelopment, Version: manifest.Version,
		Scope: &scope, Reason: "path-scoped grant",
	})
	return dev, nil
}

// UnloadDevelopment removes a development plugin from the list and unloads
// its fiber.
func (m *Manager) UnloadDevelopment(ctx context.Context, id string) error {
	m.mu.Lock()
	dev := m.dev[id]
	delete(m.dev, id)
	for i, key := range m.order {
		if key == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	if dev == nil {
		return fmt.Errorf("%w: development plugin %s", ErrNotInstalled, id)
	}
	m.authority.Reset(SubjectForDevelopment(id))
	if err := m.container.Unload(ctx, dev.Fiber.ID()); err != nil {
		return err
	}
	return nil
}

// fiberByKey resolves a manager key to one live fiber. Installed keys are the
// plugin id or "<source>/<id>"; development keys are the plugin id or
// "development/<id>".
func (m *Manager) fiberByKey(key string) (*Fiber, bool) {
	if m == nil || m.container == nil {
		return nil, false
	}
	if fiber, ok := m.container.Fiber(domainplugin.FiberID(key)); ok {
		return fiber, true
	}
	id := key
	if strings.HasPrefix(id, string(domainplugin.SourceDevelopment)+"/") {
		id = strings.TrimPrefix(id, string(domainplugin.SourceDevelopment)+"/")
		m.mu.Lock()
		dev := m.dev[id]
		m.mu.Unlock()
		if dev != nil {
			return dev.Fiber, true
		}
		return nil, false
	}
	if index := strings.IndexByte(id, '/'); index > 0 {
		id = id[index+1:]
	}
	if fiber, ok := m.fiberFor(id); ok {
		return fiber, true
	}
	if fiber, ok := m.builtinFiber(id); ok {
		return fiber, true
	}
	m.mu.Lock()
	dev := m.dev[id]
	m.mu.Unlock()
	if dev != nil {
		return dev.Fiber, true
	}
	return nil, false
}

// Reload tears one plugin instance down and activates it again. Stored
// configuration and volatile fields survive; runtime state, effects, and
// contributions do not.
func (m *Manager) Reload(ctx context.Context, key string) error {
	fiber, ok := m.fiberByKey(key)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, key)
	}
	_, err := m.container.Reload(ctx, fiber.ID())
	return err
}

// Restart is the explicit recovery path after a plugin exhausted its one
// automatic restart. It grants a fresh automatic-restart budget.
func (m *Manager) Restart(ctx context.Context, key string) error {
	fiber, ok := m.fiberByKey(key)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, key)
	}
	_, err := m.container.Restart(ctx, fiber.ID())
	return err
}

// developmentFiber resolves a development key to a development fiber only.
// Installed keys never resolve here: a development key is the raw plugin id
// (solely from the manager's development map), "development/<id>", or the
// concrete development fiber identity "dev/<id>#instance". An installed fiber
// that shares the plugin id is a different instance and is never returned.
func (m *Manager) developmentFiber(key string) (*Fiber, bool) {
	if m == nil || m.container == nil {
		return nil, false
	}
	id := strings.TrimPrefix(key, string(domainplugin.SourceDevelopment)+"/")
	m.mu.Lock()
	dev := m.dev[id]
	m.mu.Unlock()
	if dev != nil && dev.Fiber != nil {
		return dev.Fiber, true
	}
	if fiber, ok := m.container.Fiber(domainplugin.FiberID(key)); ok && fiber.Development() {
		return fiber, true
	}
	return nil, false
}

// DevelopmentChanged performs exactly one reload of an unpacked development
// plugin after its directory changed. It never installs the directory into the
// package store, and a failed reload is not retried automatically. The key
// must name a development fiber: an installed copy with the same plugin id is
// never a fallback.
func (m *Manager) DevelopmentChanged(ctx context.Context, key string) error {
	fiber, ok := m.developmentFiber(key)
	if !ok {
		return fmt.Errorf("%w: development plugin %s", ErrNotInstalled, key)
	}
	_, err := m.container.DevelopmentChanged(ctx, fiber.ID())
	return err
}

// Restore re-enables every installed plugin whose record says enabled. Each
// activation failure is recorded and left visible as FAILED.
func (m *Manager) Restore(ctx context.Context) error {
	records, err := m.store.Records(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		m.authority.SetGrants(installedSubject(record), record.Grants)
		dir, err := m.store.PackageDir(ctx, record.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := m.container.LoadInstalled(ctx, dir, record.Source); err != nil {
			reason, validation := configLoadReason(err)
			record.Reason = reason
			if validation != nil {
				m.setDiagnostic(record.ID, validation)
			} else {
				m.clearDiagnostic(record.ID)
			}
			_ = m.store.SaveRecord(ctx, record)
			errs = append(errs, err)
			continue
		}
		record.Reason = ""
		_ = m.store.SaveRecord(ctx, record)
		m.clearDiagnostic(record.ID)
	}
	return errors.Join(errs...)
}

// Close unloads every fiber, closes the runtime, and flushes the audit sink.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	containerErr := m.container.Close(ctx)
	m.services.CloseAll()
	m.language.Close()
	m.panel.Close()
	auditErr := m.audit.Close()
	return errors.Join(containerErr, auditErr)
}

func (m *Manager) recordLifecycle(ref PluginRef, state domainplugin.State, reason string) {
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditActivation,
		PluginID: ref.ID, Source: ref.Source, Version: ref.Version,
		State: state, Reason: reason,
	})
	// Every activation change can add or remove plugin tools: ACTIVE publishes
	// them, FAILED/PENDING/DISPOSED remove them. Connected MCP clients are
	// told to re-list.
	m.notifyMCPToolsChanged()
}

func (m *Manager) recordFault(ref PluginRef, err error) {
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditCrash,
		PluginID: ref.ID, Source: ref.Source, Version: ref.Version,
		Reason: err.Error(),
	})
}

// MCPToolAudit describes one plugin-contributed external MCP tool call. It
// deliberately carries no arguments and no result content: only the tool name,
// client label, mode, access class, target label, duration, status, and result
// size. A target label is already redacted by the caller.
type MCPToolAudit struct {
	PluginID    string
	Tool        string
	Client      string
	Mode        string
	Access      string
	Target      string
	Duration    time.Duration
	Status      string
	ResultBytes int
}

// RecordMCPToolCall appends one tool-call audit row. It never blocks the call
// path: the sink queues the event and exposes dropped counts separately.
func (m *Manager) RecordMCPToolCall(event MCPToolAudit) {
	if m == nil {
		return
	}
	m.audit.Record(domainplugin.AuditEvent{
		Time:        m.now().UTC(),
		Event:       domainplugin.AuditMCPTool,
		PluginID:    event.PluginID,
		Tool:        event.Tool,
		Client:      event.Client,
		Mode:        string(event.Mode),
		Access:      event.Access,
		Target:      event.Target,
		DurationMS:  event.Duration.Milliseconds(),
		ResultBytes: event.ResultBytes,
		Status:      event.Status,
	})
}
