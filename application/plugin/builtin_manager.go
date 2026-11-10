package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ErrBuiltinImmutable reports a runtime install-class action on a compiled-in
// first-party plugin. A built-in ships with the app, so it cannot be
// installed, updated, rolled back, uninstalled, purged, revoked, or put
// through the consent flow. It can still be enabled and disabled.
var ErrBuiltinImmutable = errors.New(
	"plugin: built-in plugins cannot be installed, updated, or uninstalled")

// BuiltinImmutableError names the built-in plugin and the refused action. It
// matches ErrBuiltinImmutable with errors.Is.
type BuiltinImmutableError struct {
	ID     string
	Action string
}

func (e *BuiltinImmutableError) Error() string {
	return fmt.Sprintf("plugin %s: built-in plugins cannot be %s", e.ID, e.Action)
}

// Is reports the shared sentinel so callers can branch without matching the
// action text.
func (e *BuiltinImmutableError) Is(target error) bool { return target == ErrBuiltinImmutable }

// BuiltinStateStore persists whether each compiled-in plugin is enabled.
// known is false when the store has no opinion, in which case the plugin is
// enabled by default. The manager uses it only for built-ins; installed
// plugins have their own install record.
type BuiltinStateStore interface {
	BuiltinEnabled(ctx context.Context, id string) (enabled bool, known bool, err error)
	SaveBuiltinEnabled(ctx context.Context, id string, enabled bool) error
}

// MemoryBuiltinStateStore is a deterministic BuiltinStateStore for tests and
// for a host without persistent state. It records only disabled overrides.
type MemoryBuiltinStateStore struct {
	mu       sync.Mutex
	disabled map[string]bool
}

// NewMemoryBuiltinStateStore returns an empty state store.
func NewMemoryBuiltinStateStore() *MemoryBuiltinStateStore {
	return &MemoryBuiltinStateStore{disabled: make(map[string]bool)}
}

// BuiltinEnabled implements BuiltinStateStore.
func (s *MemoryBuiltinStateStore) BuiltinEnabled(_ context.Context,
	id string) (bool, bool, error) {
	if s == nil {
		return true, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled[id] {
		return false, true, nil
	}
	return true, false, nil
}

// SaveBuiltinEnabled implements BuiltinStateStore.
func (s *MemoryBuiltinStateStore) SaveBuiltinEnabled(_ context.Context,
	id string, enabled bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled == nil {
		s.disabled = make(map[string]bool)
	}
	if enabled {
		delete(s.disabled, id)
	} else {
		s.disabled[id] = true
	}
	return nil
}

// builtinEntry is one registered compiled-in plugin. enabled and reason are
// guarded by Manager.mu; the Builtin value itself is immutable after
// registration.
type builtinEntry struct {
	builtin Builtin
	enabled bool
	reason  string
}

// RegisterBuiltin records one compiled-in first-party plugin and activates it
// when enabled. The manifest is validated with ValidateBuiltin and its version
// is replaced by the app version when that is a valid semantic version, so a
// built-in tracks the app it ships with. Dependencies, permission display, and
// fiber lifecycle are identical to an installed plugin. An activation failure
// is recorded on the fiber and returned; sibling fibers are untouched.
func (m *Manager) RegisterBuiltin(ctx context.Context, builtin Builtin) error {
	if m == nil || m.container == nil {
		return errors.New("plugin: manager is not configured")
	}
	m.builtinMu.Lock()
	defer m.builtinMu.Unlock()
	if builtin.Setup == nil {
		return fmt.Errorf("plugin %s: built-in setup is required", builtin.Manifest.ID)
	}
	manifest := builtin.Manifest
	// An app version that is not semantic (a developer build) cannot replace a
	// valid manifest version; the declared version stays as the honest value.
	if version := strings.TrimSpace(m.appVersion); version != "" {
		if _, ok := domainplugin.ParseSemver(version); ok {
			manifest.Version = version
		}
	}
	if err := manifest.ValidateBuiltin(); err != nil {
		return err
	}
	entry := &builtinEntry{
		builtin: Builtin{Manifest: manifest, Setup: builtin.Setup},
		enabled: true,
	}
	m.mu.Lock()
	if _, exists := m.builtins[manifest.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("%w: built-in %s", ErrAlreadyInstalled, manifest.ID)
	}
	m.builtins[manifest.ID] = entry
	m.builtinOrder = append(m.builtinOrder, manifest.ID)
	m.mu.Unlock()

	if m.builtinState != nil {
		enabled, known, err := m.builtinState.BuiltinEnabled(ctx, manifest.ID)
		if err != nil {
			// A failed state read cannot tell an enabled built-in from one the
			// user disabled, so registration rolls back instead of leaving a
			// PENDING entry that defaults to enabled.
			m.mu.Lock()
			delete(m.builtins, manifest.ID)
			for i, id := range m.builtinOrder {
				if id == manifest.ID {
					m.builtinOrder = append(m.builtinOrder[:i], m.builtinOrder[i+1:]...)
					break
				}
			}
			m.mu.Unlock()
			return err
		}
		if known {
			m.mu.Lock()
			entry.enabled = enabled
			m.mu.Unlock()
		}
	}
	if !entry.enabled {
		return nil
	}
	m.grantBuiltinPermissions(entry.builtin)
	return m.loadBuiltin(ctx, entry)
}

// loadBuiltin activates one registered entry through the shared container
// path. The entry keeps the last activation error for a missing-fiber summary;
// a created fiber carries its own FAILED or PENDING reason.
func (m *Manager) loadBuiltin(ctx context.Context, entry *builtinEntry) error {
	builtin := entry.builtin
	_, err := m.container.LoadBuiltin(ctx, builtin)
	m.mu.Lock()
	if current := m.builtins[builtin.Manifest.ID]; current == entry {
		current.reason = ""
		if err != nil {
			current.reason = err.Error()
		}
	}
	m.mu.Unlock()
	return err
}

// grantBuiltinPermissions consents to every permission the compiled-in
// manifest declares. The code ships with the app, so there is no user prompt;
// the grants are still recorded in the same Authority, which makes the
// permission list visible and keeps the runtime checks identical to a
// third-party plugin.
func (m *Manager) grantBuiltinPermissions(builtin Builtin) {
	if m == nil || m.authority == nil {
		return
	}
	manifest := builtin.Manifest
	required, optional, err := manifest.Permissions.Requests()
	if err != nil {
		return
	}
	now := m.now().UTC()
	grants := make([]domainplugin.PermissionGrant, 0, len(required)+len(optional))
	for _, request := range required {
		grants = append(grants, domainplugin.PermissionGrant{
			Permission: request.Permission, Scope: request.Scope, GrantedAt: now,
		})
	}
	for _, request := range optional {
		grants = append(grants, domainplugin.PermissionGrant{
			Permission: request.Permission, Scope: request.Scope,
			Optional: true, GrantedAt: now,
		})
	}
	m.authority.SetGrants(SubjectForInstalled(domainplugin.SourceBuiltIn, manifest.ID), grants)
	for _, grant := range grants {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditGrant, PluginID: manifest.ID,
			Source: domainplugin.SourceBuiltIn, Version: manifest.Version,
			Permission: grant.Permission, Scope: scopePointer(grant.Scope),
			Reason: "built-in",
		})
	}
}

// builtinSnapshot returns one entry value under the manager lock.
func (m *Manager) builtinSnapshot(id string) (builtinEntry, bool) {
	if m == nil {
		return builtinEntry{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.builtins[id]
	if entry == nil {
		return builtinEntry{}, false
	}
	return *entry, true
}

// builtinEntryByID returns the live entry for mutation under the manager lock.
func (m *Manager) builtinEntryByID(id string) (*builtinEntry, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.builtins[id]
	return entry, entry != nil
}

// isBuiltin reports whether id names a registered compiled-in plugin.
func (m *Manager) isBuiltin(id string) bool {
	_, ok := m.builtinSnapshot(id)
	return ok
}

// builtinVersion returns the app-versioned manifest version of one built-in.
func (m *Manager) builtinVersion(id string) (string, bool) {
	entry, ok := m.builtinSnapshot(id)
	if !ok {
		return "", false
	}
	return entry.builtin.Manifest.Version, true
}

// builtinFiber returns the live fiber of one compiled-in plugin.
func (m *Manager) builtinFiber(id string) (*Fiber, bool) {
	if m == nil || m.container == nil {
		return nil, false
	}
	return m.container.Fiber(domainplugin.NewFiberID(BuiltinGroup, id, 1))
}

// builtinSummary is the List read model for one built-in. Fiber state wins
// over the recorded enable flag, exactly like an installed record.
func (m *Manager) builtinSummary(entry builtinEntry) InstalledPlugin {
	manifest := entry.builtin.Manifest
	summary := InstalledPlugin{
		ID:          manifest.ID,
		Key:         string(domainplugin.SourceBuiltIn) + "/" + manifest.ID,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Source:      domainplugin.SourceBuiltIn,
		Status:      StatusDisabled,
		Enabled:     entry.enabled,
		Reason:      entry.reason,
		Permissions: builtinPermissionStatuses(manifest),
	}
	if fiber, ok := m.builtinFiber(manifest.ID); ok {
		summary.State = fiber.State()
		summary.Reason = fiber.Reason()
		if summary.Reason == "" && fiber.State() != domainplugin.StateActive {
			summary.Reason = entry.reason
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
	if entry.enabled && summary.Status == StatusDisabled {
		summary.Status = StatusPending
	}
	if !entry.enabled && entry.reason != "" {
		summary.Status = StatusPending
	}
	return summary
}

// builtinPermissionStatuses renders the declared permissions as granted. A
// built-in's code ships with the app, so there is no consent state to read and
// no revocation to display; the rows exist so the detail surface is identical
// to an installed plugin.
func builtinPermissionStatuses(manifest domainplugin.Manifest) []PermissionStatus {
	required, optional, err := manifest.Permissions.Requests()
	if err != nil {
		return nil
	}
	out := make([]PermissionStatus, 0, len(required)+len(optional))
	for _, request := range required {
		out = append(out, PermissionStatus{
			Permission: request.Permission, Scope: request.Scope,
			Required: true, Granted: true,
		})
	}
	for _, request := range optional {
		out = append(out, PermissionStatus{
			Permission: request.Permission, Scope: request.Scope, Granted: true,
		})
	}
	return out
}

// enableBuiltin activates a registered compiled-in plugin without a consent
// review. The host grants the declared permissions from the shipped code.
func (m *Manager) enableBuiltin(ctx context.Context, entry *builtinEntry) error {
	m.builtinMu.Lock()
	defer m.builtinMu.Unlock()
	m.mu.Lock()
	if entry.enabled {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAlreadyEnabled, entry.builtin.Manifest.ID)
	}
	entry.enabled = true
	entry.reason = ""
	m.mu.Unlock()

	manifest := entry.builtin.Manifest
	m.grantBuiltinPermissions(entry.builtin)
	if err := m.saveBuiltinEnabled(ctx, manifest.ID, true); err != nil {
		return err
	}
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditEnable, PluginID: manifest.ID,
		Source: domainplugin.SourceBuiltIn, Version: manifest.Version,
	})
	return m.loadBuiltin(ctx, entry)
}

// disableBuiltin unloads a compiled-in plugin and clears its live grants. The
// manifest, permissions, and disabled state stay visible in the plugin list.
func (m *Manager) disableBuiltin(ctx context.Context, entry *builtinEntry) error {
	m.builtinMu.Lock()
	defer m.builtinMu.Unlock()
	m.mu.Lock()
	if !entry.enabled {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotEnabled, entry.builtin.Manifest.ID)
	}
	entry.enabled = false
	entry.reason = ""
	m.mu.Unlock()

	manifest := entry.builtin.Manifest
	m.authority.Reset(SubjectForInstalled(domainplugin.SourceBuiltIn, manifest.ID))
	var unloadErr error
	if fiber, ok := m.builtinFiber(manifest.ID); ok {
		unloadErr = m.container.Unload(ctx, fiber.ID())
	}
	saveErr := m.saveBuiltinEnabled(ctx, manifest.ID, false)
	if saveErr == nil {
		m.audit.Record(domainplugin.AuditEvent{
			Time: m.now().UTC(), Event: domainplugin.AuditDisable, PluginID: manifest.ID,
			Source: domainplugin.SourceBuiltIn, Version: manifest.Version,
		})
	}
	return errors.Join(unloadErr, saveErr)
}

func (m *Manager) saveBuiltinEnabled(ctx context.Context, id string, enabled bool) error {
	if m == nil || m.builtinState == nil {
		return nil
	}
	return m.builtinState.SaveBuiltinEnabled(ctx, id, enabled)
}

// builtinImmutable returns the typed refusal for an install-class action on a
// built-in, or nil when id is not built in.
func (m *Manager) builtinImmutable(id, action string) error {
	if !m.isBuiltin(id) {
		return nil
	}
	return &BuiltinImmutableError{ID: id, Action: action}
}

// builtinSummaries renders every built-in entry in stable id order. It is used
// by List so the same read model carries installed, development, and built-in
// entries.
func (m *Manager) builtinSummaries() []InstalledPlugin {
	m.mu.Lock()
	ids := append([]string(nil), m.builtinOrder...)
	entries := make([]builtinEntry, 0, len(ids))
	for _, id := range ids {
		if entry := m.builtins[id]; entry != nil {
			entries = append(entries, *entry)
		}
	}
	m.mu.Unlock()
	out := make([]InstalledPlugin, 0, len(entries))
	for _, entry := range entries {
		out = append(out, m.builtinSummary(entry))
	}
	sortBuiltinSummaries(out)
	return out
}

func sortBuiltinSummaries(summaries []InstalledPlugin) {
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].ID == summaries[j].ID {
			return summaries[i].Key < summaries[j].Key
		}
		return summaries[i].ID < summaries[j].ID
	})
}

// builtinManifest returns the manifest to use for settings. A live fiber's
// manifest wins; otherwise the registered manifest is the source of truth.
func (m *Manager) builtinManifest(id string) (domainplugin.Manifest, bool) {
	if fiber, ok := m.builtinFiber(id); ok {
		return fiber.Manifest(), true
	}
	entry, ok := m.builtinSnapshot(id)
	if !ok {
		return domainplugin.Manifest{}, false
	}
	return entry.builtin.Manifest, true
}

// builtinSettings is the Settings branch for a compiled-in plugin. It reads
// the same configuration store and schema validation as an installed plugin,
// so a built-in with declared settings behaves identically.
func (m *Manager) builtinSettings(ctx context.Context, id string) (PluginSettingsView, error) {
	manifest, ok := m.builtinManifest(id)
	if !ok {
		return PluginSettingsView{}, fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	stored := map[string]any{}
	if m.configStore != nil {
		var err error
		stored, err = m.configStore.Load(ctx, id)
		if err != nil {
			return PluginSettingsView{}, err
		}
	}
	values, validation := EffectiveSettings(manifest.Settings, stored)
	return settingsView(manifest.ID, manifest.Name, manifest.Settings, values, validation,
		m.localizerFor(ctx, manifest.ID)), nil
}

// saveBuiltinSettings persists a compiled-in plugin's configuration and
// retries activation when the plugin is enabled but not live.
func (m *Manager) saveBuiltinSettings(ctx context.Context, id string,
	values map[string]any) error {
	m.builtinMu.Lock()
	defer m.builtinMu.Unlock()
	manifest, ok := m.builtinManifest(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	normalized, validation := domainplugin.NormalizeSettings(manifest.Settings, values)
	if validation != nil {
		return validation
	}
	if m.configStore == nil {
		return ErrConfigStoreNotConfigured
	}
	persisted := manifest.Settings.Persisted(normalized)
	if err := m.configStore.Save(ctx, id, persisted); err != nil {
		return err
	}
	effective, _ := EffectiveSettings(manifest.Settings, persisted)
	for _, field := range manifest.Settings.Fields {
		if !field.Volatile {
			continue
		}
		if value, ok := normalized[field.Key]; ok {
			effective[field.Key] = domainplugin.CloneSettingValue(value)
		}
	}
	if fiber, live := m.builtinFiber(id); live {
		if session, ok := m.services.Session(fiber.ID()); ok {
			session.UpdateConfig(effective)
			m.container.UpdateSettings(fiber.ID(), effective)
			m.clearDiagnostic(id)
			return nil
		}
		if err := m.container.Unload(ctx, fiber.ID()); err != nil {
			return err
		}
	}
	snapshot, ok := m.builtinSnapshot(id)
	if !ok || !snapshot.enabled {
		m.clearDiagnostic(id)
		return nil
	}
	entry, _ := m.builtinEntryByID(id)
	if err := m.loadBuiltin(ctx, entry); err != nil {
		reason, settingsErr := configLoadReason(err)
		m.mu.Lock()
		if current := m.builtins[id]; current != nil {
			current.reason = reason
		}
		m.mu.Unlock()
		if settingsErr != nil {
			m.setDiagnostic(id, settingsErr)
		}
		return err
	}
	m.clearDiagnostic(id)
	return nil
}
