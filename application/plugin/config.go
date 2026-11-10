package plugin

import (
	"context"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ConfigStore owns the host-side, schema-validated configuration for one
// plugin. Values are TOML scalars; the adapter encrypts nothing because plugin
// configuration is not a secret store. Implementations must write atomically.
type ConfigStore interface {
	// Load returns the stored values, or an empty map when none exist.
	Load(ctx context.Context, pluginID string) (map[string]any, error)
	// Save replaces the stored values.
	Save(ctx context.Context, pluginID string, values map[string]any) error
}

// MemoryConfigStore is a deterministic ConfigStore for tests.
type MemoryConfigStore struct {
	mu     sync.Mutex
	values map[string]map[string]any
}

// NewMemoryConfigStore returns an empty in-memory configuration store.
func NewMemoryConfigStore() *MemoryConfigStore {
	return &MemoryConfigStore{values: make(map[string]map[string]any)}
}

// Load implements ConfigStore.
func (s *MemoryConfigStore) Load(_ context.Context, pluginID string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettings(s.values[pluginID]), nil
}

// Save implements ConfigStore.
func (s *MemoryConfigStore) Save(_ context.Context, pluginID string, values map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[pluginID] = cloneSettings(values)
	return nil
}

// Set stores values directly, for tests that simulate a hand-edited or
// schema-invalid configuration file.
func (s *MemoryConfigStore) Set(pluginID string, values map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[pluginID] = cloneSettings(values)
}

// EffectiveSettings merges the declared defaults with stored values, dropping
// volatile fields and rejecting unknown keys or invalid values. The returned
// values are valid even when validation failed, so a settings page can show
// the user's input beside per-field errors.
func EffectiveSettings(schema domainplugin.SettingsSchema,
	stored map[string]any) (map[string]any, *domainplugin.SettingsValidationError) {
	return domainplugin.NormalizeSettings(schema, schema.DropVolatile(stored))
}

// SettingsFieldView is one rendered settings row. Values are the effective
// host-side values, never a guest view.
type SettingsFieldView struct {
	Key         string                   `json:"key"`
	Type        domainplugin.SettingType `json:"type"`
	Label       string                   `json:"label,omitempty"`
	Description string                   `json:"description,omitempty"`
	Value       any                      `json:"value,omitempty"`
	Default     any                      `json:"default,omitempty"`
	Volatile    bool                     `json:"volatile,omitempty"`
	Required    bool                     `json:"required,omitempty"`
	Options     []string                 `json:"options,omitempty"`
	Pattern     string                   `json:"pattern,omitempty"`
	Min         *float64                 `json:"min,omitempty"`
	Max         *float64                 `json:"max,omitempty"`
	Error       string                   `json:"error,omitempty"`
}

// PluginSettingsView is one plugin settings page: the declared fields in
// manifest order, their effective values, and any per-field validation errors.
type PluginSettingsView struct {
	PluginID string                       `json:"plugin_id"`
	Name     string                       `json:"name,omitempty"`
	Version  int                          `json:"version"`
	Fields   []SettingsFieldView          `json:"fields"`
	Errors   []domainplugin.SettingsError `json:"errors,omitempty"`
	Valid    bool                         `json:"valid"`
}

// DiagnosticsEntry is one operator-facing configuration or activation
// diagnostic. It carries the full validation error so the Plugins surface can
// show every invalid field, not just the first reason in the plugin list.
type DiagnosticsEntry struct {
	PluginID string                       `json:"plugin_id"`
	Kind     string                       `json:"kind"`
	Reason   string                       `json:"reason"`
	Settings []domainplugin.SettingsError `json:"settings,omitempty"`
}

// settingsView renders a settings page from a schema and effective values.
// localize resolves a *_key against the plugin's catalogs; nil keeps the
// manifest literals.
func settingsView(pluginID, name string, schema domainplugin.SettingsSchema,
	values map[string]any, validation *domainplugin.SettingsValidationError,
	localize func(key, literal string) string) PluginSettingsView {
	fieldErrors := map[string]string{}
	errorsOut := []domainplugin.SettingsError(nil)
	if validation != nil {
		fieldErrors = validation.FieldErrors()
		errorsOut = append(errorsOut, validation.Errors...)
	}
	view := PluginSettingsView{
		PluginID: pluginID,
		Name:     name,
		Version:  schema.Version,
		Fields:   make([]SettingsFieldView, 0, len(schema.Fields)),
		Errors:   errorsOut,
		Valid:    validation == nil,
	}
	for _, field := range schema.Fields {
		label, description := field.Label, field.Description
		if localize != nil {
			label = localize(field.LabelKey, label)
			description = localize(field.DescriptionKey, description)
		}
		view.Fields = append(view.Fields, SettingsFieldView{
			Key:         field.Key,
			Type:        field.Type,
			Label:       label,
			Description: description,
			Value:       cloneSetting(fieldValue(values, field.Key, field.Default)),
			Default:     cloneSetting(field.Default),
			Volatile:    field.Volatile,
			Required:    field.Required,
			Options:     append([]string(nil), field.Options...),
			Pattern:     field.Pattern,
			Min:         field.Min,
			Max:         field.Max,
			Error:       fieldErrors[field.Key],
		})
	}
	return view
}

func fieldValue(values map[string]any, key string, fallback any) any {
	if values != nil {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return fallback
}

func cloneSetting(value any) any {
	return domainplugin.CloneSettingValue(value)
}
