package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ErrConfigStoreNotConfigured reports a settings write without a host-side
// configuration store. Reads still return the declared defaults.
var ErrConfigStoreNotConfigured = errors.New("plugin: configuration store is not configured")

// resolveSettings is the container's SettingsResolver. Precedence is stored
// value, then the manifest's declared default, then the group default; a
// reloaded fiber additionally keeps the previous activation's volatile values.
// A validation error leaves the fiber PENDING.
func (m *Manager) resolveSettings(ctx context.Context, manifest domainplugin.Manifest,
	inherited SettingsInheritance) (map[string]any, error) {
	values := make(map[string]any)
	for key, value := range inherited.Group {
		if _, declared := manifest.Settings.Field(key); declared {
			values[key] = domainplugin.CloneSettingValue(value)
		}
	}
	for key, value := range manifest.Settings.Defaults() {
		values[key] = value
	}
	if m.configStore != nil {
		stored, err := m.configStore.Load(ctx, manifest.ID)
		if err != nil {
			return nil, err
		}
		for key, value := range manifest.Settings.DropVolatile(stored) {
			values[key] = value
		}
	}
	for key, value := range inherited.Volatile {
		if field, declared := manifest.Settings.Field(key); declared && field.Volatile {
			values[key] = domainplugin.CloneSettingValue(value)
		}
	}
	normalized, validation := domainplugin.NormalizeSettings(manifest.Settings, values)
	if validation != nil {
		return normalized, validation
	}
	return normalized, nil
}

// checkRequiredPermission is the container's activation-time permission
// gate. A development directory carries its own path-scoped grant and does
// not pass through the installed consent flow, so its activation is not
// gated here. An installed plugin with an ungranted required permission stays
// PENDING with no instance.
func (m *Manager) checkRequiredPermission(ref PluginRef,
	request domainplugin.PermissionRequest) error {
	if ref.Development {
		return nil
	}
	if m == nil || m.authority == nil {
		return ErrPermissionNotGranted
	}
	// The install record, not a package manifest, is host-authoritative for
	// consent. An update that turns a required permission optional keeps the
	// running package active even though the manifest still names it; the
	// record already reflects the reviewed permission set.
	if m.store != nil {
		record, ok, err := m.store.Record(context.Background(), ref.ID)
		if err == nil && ok && !recordIsRequired(record, request.Permission) {
			return nil
		}
	}
	return m.authority.Check(SubjectForRef(ref), request.Permission, request.Scope)
}

// loadManifest returns the validated manifest for one installed plugin. A
// live fiber's manifest wins; otherwise the manifest is decoded from the
// package directory so settings still work while the plugin is PENDING or
// disabled.
func (m *Manager) loadManifest(ctx context.Context, id string) (domainplugin.Manifest, error) {
	if fiber, ok := m.fiberFor(id); ok {
		return fiber.Manifest(), nil
	}
	if manifest, ok := m.builtinManifest(id); ok {
		return manifest, nil
	}
	if m.manifests == nil || m.store == nil {
		return domainplugin.Manifest{}, errors.New("plugin: manifest source is not configured")
	}
	dir, err := m.store.PackageDir(ctx, id)
	if err != nil {
		return domainplugin.Manifest{}, err
	}
	return m.manifests.Load(dir)
}

// Settings returns one plugin settings page with effective values, defaults,
// volatile markers, and per-field validation errors.
func (m *Manager) Settings(ctx context.Context, id string) (PluginSettingsView, error) {
	if _, ok := m.builtinSnapshot(id); ok {
		return m.builtinSettings(ctx, id)
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return PluginSettingsView{}, err
	}
	if !ok {
		return PluginSettingsView{}, fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	manifest, err := m.loadManifest(ctx, id)
	if err != nil {
		return PluginSettingsView{}, err
	}
	stored := map[string]any{}
	if m.configStore != nil {
		stored, err = m.configStore.Load(ctx, id)
		if err != nil {
			return PluginSettingsView{}, err
		}
	}
	values, validation := EffectiveSettings(manifest.Settings, stored)
	return settingsView(record.ID, record.Name, manifest.Settings, values, validation,
		m.localizerFor(ctx, record.ID)), nil
}

// SaveSettings validates and persists one plugin configuration. Volatile
// values are accepted but never written. A live plugin receives the new
// effective configuration immediately; a PENDING plugin is retried so fixing
// an invalid configuration activates it.
func (m *Manager) SaveSettings(ctx context.Context, id string, values map[string]any) error {
	if _, ok := m.builtinSnapshot(id); ok {
		return m.saveBuiltinSettings(ctx, id, values)
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	manifest, err := m.loadManifest(ctx, id)
	if err != nil {
		return err
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
	// Volatile fields are accepted for this activation but never persisted.
	// The live session and the fiber's reload snapshot must still see the
	// submitted value, otherwise a reload would silently reset it to Default.
	for _, field := range manifest.Settings.Fields {
		if !field.Volatile {
			continue
		}
		if value, ok := normalized[field.Key]; ok {
			effective[field.Key] = domainplugin.CloneSettingValue(value)
		}
	}
	// A PENDING or FAILED fiber has no service session. It is unloaded before
	// the retry so activation starts from a clean fiber identity.
	if fiber, live := m.fiberFor(id); live {
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
	if !record.Enabled {
		m.clearDiagnostic(id)
		return nil
	}
	// The plugin is enabled but has no live fiber, usually because the stored
	// configuration was invalid at activation. Retry activation now.
	m.authority.SetGrants(installedSubject(record), record.Grants)
	dir, err := m.store.PackageDir(ctx, id)
	if err != nil {
		return err
	}
	if _, err := m.container.LoadInstalled(ctx, dir, record.Source); err != nil {
		reason, settingsErr := configLoadReason(err)
		record.Reason = reason
		if settingsErr != nil {
			m.setDiagnostic(record.ID, settingsErr)
		}
		_ = m.store.SaveRecord(ctx, record)
		return err
	}
	record.Reason = ""
	if err := m.store.SaveRecord(ctx, record); err != nil {
		return err
	}
	m.clearDiagnostic(id)
	return nil
}

// ResetSettings restores the declared defaults and drops every stored value.
func (m *Manager) ResetSettings(ctx context.Context, id string) error {
	return m.SaveSettings(ctx, id, map[string]any{})
}

// Diagnostics returns configuration diagnostics in plugin-id order. It is the
// first-party diagnostics view's configuration slice, not a plugin service.
func (m *Manager) Diagnostics() []DiagnosticsEntry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DiagnosticsEntry, 0, len(m.diagnostics))
	for _, entry := range m.diagnostics {
		entry.Settings = append([]domainplugin.SettingsError(nil), entry.Settings...)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PluginID == out[j].PluginID {
			return out[i].Kind < out[j].Kind
		}
		return out[i].PluginID < out[j].PluginID
	})
	return out
}

func (m *Manager) setDiagnostic(id string, validation *domainplugin.SettingsValidationError) {
	if m == nil || validation == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.diagnostics[id] = DiagnosticsEntry{
		PluginID: id,
		Kind:     domainplugin.ReasonConfigInvalid,
		Reason:   validation.Error(),
		Settings: append([]domainplugin.SettingsError(nil), validation.Errors...),
	}
}

func (m *Manager) clearDiagnostic(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.diagnostics, id)
}

// configLoadReason converts an activation failure into the stable reason stored
// in the install record. Only a settings validation error carries diagnostics.
func configLoadReason(err error) (string, *domainplugin.SettingsValidationError) {
	var validation *domainplugin.SettingsValidationError
	if errors.As(err, &validation) {
		return domainplugin.ConfigReason(validation.Error()), validation
	}
	return err.Error(), nil
}

// NotificationAction forwards the user's notification choice to the host
// service layer, which publishes the notification.action event to the plugin.
func (m *Manager) NotificationAction(notificationID, actionID string) error {
	if m == nil || m.services == nil {
		return ErrServiceUnavailable
	}
	return m.services.NotificationAction(notificationID, actionID)
}

// PublishEvent broadcasts one host event to subscribed plugins. Host-owned
// components call it; plugins cannot publish host events.
func (m *Manager) PublishEvent(eventType domainplugin.EventType, data any) {
	if m == nil {
		return
	}
	m.services.Publish(eventType, data)
}
