package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func settingBound(value float64) *float64 { return &value }

func sampleSettingsSchema() domainplugin.SettingsSchema {
	return domainplugin.SettingsSchema{
		Version: domainplugin.CurrentSettingsSchemaVersion,
		Fields: []domainplugin.SettingsField{
			{
				Key: "mode", Type: domainplugin.SettingTypeString, Label: "Mode",
				Default: "fast", Options: []string{"fast", "slow"}, Required: true,
			},
			{
				Key: "retries", Type: domainplugin.SettingTypeInt, Label: "Retries",
				Default: int64(3), Min: settingBound(1), Max: settingBound(9),
			},
			{
				Key: "ratio", Type: domainplugin.SettingTypeFloat, Label: "Ratio",
				Default: 0.5, Min: settingBound(0), Max: settingBound(1),
			},
			{
				Key: "debug", Type: domainplugin.SettingTypeBool, Label: "Debug",
				Default: true, Volatile: true,
			},
			{
				// Optional with no default: the settings surface renders it
				// as nil until the user sets it.
				Key: "optional_note", Type: domainplugin.SettingTypeString, Label: "Note",
			},
		},
	}
}

type settingsManagerFixture struct {
	manager *appplugin.Manager
	store   *memoryStore
	config  *appplugin.MemoryConfigStore
	audit   *recordingAudit
	id      string
}

func newSettingsManagerFixture(t *testing.T, withConfigStore bool) *settingsManagerFixture {
	t.Helper()
	manifest := managerManifest("acme.settings", []string{"commands"}, nil, nil)
	manifest.Settings = sampleSettingsSchema()
	validator := &fixedValidator{pkg: appplugin.Package{
		Manifest: manifest,
		Files: map[string][]byte{
			"plugin.toml": []byte("manifest"),
			"plugin.wasm": fixtureModule,
		},
		SHA256: "package-hash",
	}}
	store := newMemoryStore(t)
	config := appplugin.NewMemoryConfigStore()
	audit := &recordingAudit{}
	options := []appplugin.ManagerOption{
		appplugin.WithAudit(audit),
		appplugin.WithServiceDataDir(store.DataDir),
		appplugin.WithManagerClock(func() time.Time {
			return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		}),
	}
	if withConfigStore {
		options = append(options, appplugin.WithConfigStore(config))
	}
	manager := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store, options...)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	record, err := manager.InstallFile(context.Background(), "plugin.natplug")
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	return &settingsManagerFixture{
		manager: manager, store: store, config: config, audit: audit, id: record.ID,
	}
}

func (f *settingsManagerFixture) enable(t *testing.T) error {
	t.Helper()
	return f.manager.Enable(context.Background(), f.id, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
	})
}

// TestSettingsLocalizesAnInactivePluginsFields proves the settings surface
// reads the installed package's catalogs even before activation, so a pending
// plugin renders translated labels.
func TestSettingsLocalizesAnInactivePluginsFields(t *testing.T) {
	manifest := managerManifest("acme.settings-i18n", []string{"commands"}, nil, nil)
	manifest.Settings = domainplugin.SettingsSchema{
		Version: domainplugin.CurrentSettingsSchemaVersion,
		Fields: []domainplugin.SettingsField{{
			Key: "mode", Type: domainplugin.SettingTypeString,
			Label: "Mode", LabelKey: "field_mode",
			Description: "Execution mode", DescriptionKey: "field_mode_description",
			Default: "fast",
		}},
	}
	validator := &fixedValidator{pkg: appplugin.Package{
		Manifest: manifest,
		Files: map[string][]byte{
			"plugin.toml": []byte("manifest"),
			"plugin.wasm": fixtureModule,
		},
		SHA256: "package-hash",
	}}
	source := &stubCatalogSource{catalogs: domainplugin.NewCatalogs("en", map[string]domainplugin.Catalog{
		"en": {"field_mode": "Mode", "field_mode_description": "Execution mode"},
		"es": {"field_mode": "Modo", "field_mode_description": "Modo de ejecución"},
	})}
	store := newMemoryStore(t)
	manager := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store,
		appplugin.WithServiceDataDir(store.DataDir),
		appplugin.WithPluginCatalogSource(source),
		appplugin.WithLocale("es-MX"),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	record, err := manager.InstallFile(context.Background(), "plugin.natplug")
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	view, err := manager.Settings(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if len(view.Fields) != 1 {
		t.Fatalf("fields = %+v", view.Fields)
	}
	field := view.Fields[0]
	if field.Label != "Modo" || field.Description != "Modo de ejecución" {
		t.Fatalf("localized field = %+v", field)
	}

	// Changing the host locale re-renders the same settings page.
	manager.SetLocale("en")
	view, err = manager.Settings(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Settings after locale change: %v", err)
	}
	if view.Fields[0].Label != "Mode" || view.Fields[0].Description != "Execution mode" {
		t.Fatalf("field after locale change = %+v", view.Fields[0])
	}
}

func TestInvalidStoredSettingsLeaveThePluginPendingWithDiagnostics(t *testing.T) {
	fixture := newSettingsManagerFixture(t, true)
	fixture.config.Set(fixture.id, map[string]any{
		"mode": "turbo", "retries": int64(3), "ratio": 0.5,
	})
	err := fixture.enable(t)
	var validation *domainplugin.SettingsValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Enable error = %v, want SettingsValidationError", err)
	}
	if !strings.Contains(validation.Error(), "mode") {
		t.Fatalf("validation error = %v", validation)
	}

	list, err := fixture.manager.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list[0].Status != appplugin.StatusPending || !list[0].Enabled {
		t.Fatalf("plugin after invalid config = %+v", list[0])
	}
	if !strings.HasPrefix(list[0].Reason, domainplugin.ReasonConfigInvalid) {
		t.Fatalf("reason = %q, want config-invalid", list[0].Reason)
	}

	diagnostics := fixture.manager.Diagnostics()
	if len(diagnostics) != 1 || diagnostics[0].PluginID != fixture.id ||
		diagnostics[0].Kind != domainplugin.ReasonConfigInvalid {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(diagnostics[0].Settings) == 0 ||
		diagnostics[0].Settings[0].Field != "mode" {
		t.Fatalf("diagnostic settings = %+v", diagnostics[0].Settings)
	}

	view, err := fixture.manager.Settings(context.Background(), fixture.id)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if view.Valid || len(view.Errors) == 0 {
		t.Fatalf("settings view = %+v, want invalid", view)
	}
	fieldError := ""
	for _, field := range view.Fields {
		if field.Key == "mode" {
			fieldError = field.Error
		}
	}
	if fieldError == "" {
		t.Fatalf("settings view has no mode error: %+v", view.Fields)
	}
	if !fixture.audit.has(domainplugin.AuditActivation) {
		t.Fatal("the pending activation was not audited")
	}
}

func TestSaveSettingsActivatesAPendingPluginAndUpdatesTheLiveSession(t *testing.T) {
	fixture := newSettingsManagerFixture(t, true)
	fixture.config.Set(fixture.id, map[string]any{"mode": "turbo"})
	if err := fixture.enable(t); err == nil {
		t.Fatal("Enable accepted the invalid stored configuration")
	}
	if err := fixture.manager.SaveSettings(context.Background(), fixture.id, map[string]any{
		"mode":    "slow",
		"retries": int64(5),
		"ratio":   0.25,
		"debug":   false,
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	list, err := fixture.manager.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Status != appplugin.StatusActive {
		t.Fatalf("List after save = %+v, %v", list, err)
	}
	if list[0].Reason != "" {
		t.Fatalf("active reason = %q", list[0].Reason)
	}
	if diagnostics := fixture.manager.Diagnostics(); len(diagnostics) != 0 {
		t.Fatalf("diagnostics after fix = %+v", diagnostics)
	}
	view, err := fixture.manager.Settings(context.Background(), fixture.id)
	if err != nil || !view.Valid {
		t.Fatalf("Settings = %+v, %v", view, err)
	}
	if got := settingViewValue(view, "mode"); got != "slow" {
		t.Fatalf("mode = %v", got)
	}
	// Volatile values are never persisted, so the guest sees the default.
	if got := settingViewValue(view, "debug"); got != true {
		t.Fatalf("volatile debug = %v, want the default true", got)
	}
	stored, err := fixture.config.Load(context.Background(), fixture.id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := stored["debug"]; ok {
		t.Fatalf("volatile value was persisted: %v", stored)
	}

	fibers := fixture.manager.Container().Fibers()
	if len(fibers) != 1 {
		t.Fatalf("fibers = %d, want 1", len(fibers))
	}
	session, ok := fixture.manager.Services().Session(fibers[0].ID())
	if !ok {
		t.Fatal("active plugin has no host-service session")
	}
	effective := session.Config()
	if effective["mode"] != "slow" || effective["retries"] != int64(5) ||
		effective["debug"] != true {
		t.Fatalf("effective config = %+v", effective)
	}

	// An invalid save is rejected and leaves the persisted values untouched.
	err = fixture.manager.SaveSettings(context.Background(), fixture.id,
		map[string]any{"mode": "turbo"})
	var validation *domainplugin.SettingsValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("invalid SaveSettings error = %v", err)
	}
	after, _ := fixture.config.Load(context.Background(), fixture.id)
	if after["mode"] != "slow" {
		t.Fatalf("stored mode after rejected save = %v", after["mode"])
	}

	// Reset restores every declared default.
	if err := fixture.manager.ResetSettings(context.Background(), fixture.id); err != nil {
		t.Fatalf("ResetSettings: %v", err)
	}
	view, err = fixture.manager.Settings(context.Background(), fixture.id)
	if err != nil || !view.Valid {
		t.Fatalf("Settings after reset = %+v, %v", view, err)
	}
	if settingViewValue(view, "mode") != "fast" ||
		settingViewValue(view, "retries") != int64(3) ||
		settingViewValue(view, "debug") != true {
		t.Fatalf("defaults after reset = %+v", view.Fields)
	}
}

func TestSettingsWithoutAConfigStoreFallBackToDefaults(t *testing.T) {
	fixture := newSettingsManagerFixture(t, false)
	view, err := fixture.manager.Settings(context.Background(), fixture.id)
	if err != nil || !view.Valid || settingViewValue(view, "mode") != "fast" {
		t.Fatalf("Settings = %+v, %v", view, err)
	}
	err = fixture.manager.SaveSettings(context.Background(), fixture.id,
		map[string]any{"mode": "slow"})
	if !errors.Is(err, appplugin.ErrConfigStoreNotConfigured) {
		t.Fatalf("SaveSettings error = %v", err)
	}
}

// TestSaveSettingsAcceptsAnUnsetOptionalField is the manager-side regression
// for the settings surface submitting nil for a declared optional field with
// no stored value and no default.
func TestSaveSettingsAcceptsAnUnsetOptionalField(t *testing.T) {
	fixture := newSettingsManagerFixture(t, true)
	if err := fixture.enable(t); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	view, err := fixture.manager.Settings(context.Background(), fixture.id)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if value := settingViewValue(view, "optional_note"); value != nil || !view.Valid {
		t.Fatalf("unset optional field = %#v, valid = %v", value, view.Valid)
	}
	if err := fixture.manager.SaveSettings(context.Background(), fixture.id, map[string]any{
		"mode":          "slow",
		"retries":       int64(3),
		"ratio":         0.5,
		"debug":         true,
		"optional_note": nil,
	}); err != nil {
		t.Fatalf("SaveSettings with a nil optional field: %v", err)
	}
	stored, err := fixture.config.Load(context.Background(), fixture.id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := stored["optional_note"]; ok {
		t.Fatalf("nil optional field was persisted: %#v", stored)
	}
	view, err = fixture.manager.Settings(context.Background(), fixture.id)
	if err != nil || !view.Valid || settingViewValue(view, "mode") != "slow" {
		t.Fatalf("Settings after save = %+v, %v", view, err)
	}
}

func settingViewValue(view appplugin.PluginSettingsView, key string) any {
	for _, field := range view.Fields {
		if field.Key == key {
			return field.Value
		}
	}
	return nil
}
