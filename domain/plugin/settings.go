package plugin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// CurrentSettingsSchemaVersion is the manifest settings schema this host
// understands. An omitted version (zero) is treated as the current version so
// a simple schema does not need to repeat it.
const CurrentSettingsSchemaVersion = 1

// MaxSettingStringBytes bounds one string setting. Configuration is small
// host-side state; a plugin cannot use it as a data store.
const MaxSettingStringBytes = 4096

// SettingType is one scalar setting type. The set is closed: the host renders
// and validates only these kinds of field.
type SettingType string

const (
	SettingTypeString SettingType = "string"
	SettingTypeBool   SettingType = "bool"
	SettingTypeInt    SettingType = "int"
	SettingTypeFloat  SettingType = "float"
)

// Valid reports whether t is a supported setting type.
func (t SettingType) Valid() bool {
	switch t {
	case SettingTypeString, SettingTypeBool, SettingTypeInt, SettingTypeFloat:
		return true
	default:
		return false
	}
}

// SettingsField is one declared setting. Default is the value used when the
// host has no stored configuration. Volatile fields are never persisted; they
// reset to Default on every activation.
type SettingsField struct {
	Key   string      `toml:"key" json:"key"`
	Type  SettingType `toml:"type" json:"type"`
	Label string      `toml:"label" json:"label,omitempty"`
	// LabelKey and DescriptionKey name catalog messages resolved at display
	// time in the host locale. The literal fields stay the fallbacks.
	LabelKey       string   `toml:"label_key" json:"label_key,omitempty"`
	Description    string   `toml:"description" json:"description,omitempty"`
	DescriptionKey string   `toml:"description_key" json:"description_key,omitempty"`
	Default        any      `toml:"default" json:"default,omitempty"`
	Volatile       bool     `toml:"volatile" json:"volatile,omitempty"`
	Required       bool     `toml:"required" json:"required,omitempty"`
	Options        []string `toml:"options" json:"options,omitempty"`
	Min            *float64 `toml:"min" json:"min,omitempty"`
	Max            *float64 `toml:"max" json:"max,omitempty"`
	Pattern        string   `toml:"pattern" json:"pattern,omitempty"`
}

// SettingsSchema is the manifest's declarative settings page.
type SettingsSchema struct {
	Version int             `toml:"version" json:"version"`
	Fields  []SettingsField `toml:"fields" json:"fields,omitempty"`
}

var settingKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Validate checks the schema itself. Configuration values are checked later
// against the schema, because an invalid value must leave a plugin inactive
// rather than reject the whole package.
func (s SettingsSchema) Validate() error {
	if s.Version != 0 && s.Version != CurrentSettingsSchemaVersion {
		return invalid("settings.version", "want %d, got %d",
			CurrentSettingsSchemaVersion, s.Version)
	}
	seen := make(map[string]bool, len(s.Fields))
	for i, field := range s.Fields {
		where := fmt.Sprintf("settings.fields[%d]", i)
		if err := field.validate(where); err != nil {
			return err
		}
		if seen[field.Key] {
			return invalid(where+".key", "duplicate setting %q", field.Key)
		}
		seen[field.Key] = true
	}
	return nil
}

func (f SettingsField) validate(where string) error {
	if !settingKeyPattern.MatchString(f.Key) {
		return invalid(where+".key",
			"%q must be lowercase letters, digits, dot, underscore, or hyphen, at most 64 characters", f.Key)
	}
	if !f.Type.Valid() {
		return invalid(where+".type", "%q is not one of string, bool, int, float", f.Type)
	}
	if err := validateMessageKey(where+".label_key", f.LabelKey); err != nil {
		return err
	}
	if err := validateMessageKey(where+".description_key", f.DescriptionKey); err != nil {
		return err
	}
	if f.Default != nil {
		if _, err := f.coerceDefault(); err != nil {
			return invalid(where+".default", "%v", err)
		}
	}
	if len(f.Options) > 0 {
		if f.Type != SettingTypeString {
			return invalid(where+".options", "options are only valid for string settings")
		}
		seen := make(map[string]bool, len(f.Options))
		for i, option := range f.Options {
			if option == "" {
				return invalid(fmt.Sprintf("%s.options[%d]", where, i), "must not be empty")
			}
			if seen[option] {
				return invalid(fmt.Sprintf("%s.options[%d]", where, i), "duplicate option %q", option)
			}
			seen[option] = true
		}
		if text, ok := f.Default.(string); ok && !seen[text] {
			return invalid(where+".default", "%q is not a declared option", text)
		}
	}
	if f.Pattern != "" {
		if f.Type != SettingTypeString {
			return invalid(where+".pattern", "a pattern is only valid for string settings")
		}
		if _, err := regexp.Compile(f.Pattern); err != nil {
			return invalid(where+".pattern", "invalid regular expression: %v", err)
		}
	}
	if f.Min != nil || f.Max != nil {
		if f.Type != SettingTypeInt && f.Type != SettingTypeFloat {
			return invalid(where, "minimum and maximum are only valid for numeric settings")
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return invalid(where, "minimum %v is above maximum %v", *f.Min, *f.Max)
		}
	}
	return nil
}

// Defaults returns the declared defaults, cloned so callers cannot mutate the
// manifest.
func (s SettingsSchema) Defaults() map[string]any {
	out := make(map[string]any, len(s.Fields))
	for _, field := range s.Fields {
		if field.Default != nil {
			out[field.Key] = cloneSettingValue(field.Default)
		}
	}
	return out
}

// Field returns one declared field.
func (s SettingsSchema) Field(key string) (SettingsField, bool) {
	for _, field := range s.Fields {
		if field.Key == key {
			return field, true
		}
	}
	return SettingsField{}, false
}

// DropVolatile removes stored values for volatile fields. A stale config file
// therefore cannot override a volatile field: it always starts at Default.
func (s SettingsSchema) DropVolatile(values map[string]any) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		field, ok := s.Field(key)
		if ok && field.Volatile {
			continue
		}
		out[key] = cloneSettingValue(value)
	}
	return out
}

// Persisted returns the values that may be written to disk: every normalised
// value except volatile fields.
func (s SettingsSchema) Persisted(values map[string]any) map[string]any {
	return s.DropVolatile(values)
}

// SettingsError is one invalid field with a readable reason.
type SettingsError struct {
	Field   string `json:"field"`
	Problem string `json:"problem"`
}

func (e SettingsError) Error() string {
	if e.Field == "" {
		return e.Problem
	}
	return e.Field + ": " + e.Problem
}

// SettingsValidationError reports every invalid configuration value. The host
// stores it as the plugin's PENDING reason and shows it in the settings page.
type SettingsValidationError struct {
	Errors []SettingsError `json:"errors"`
}

func (e *SettingsValidationError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "plugin settings: invalid configuration"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		parts = append(parts, item.Error())
	}
	return "plugin settings: " + strings.Join(parts, "; ")
}

// FieldErrors returns the problems keyed by field for the settings page.
func (e *SettingsValidationError) FieldErrors() map[string]string {
	out := make(map[string]string)
	if e == nil {
		return out
	}
	for _, item := range e.Errors {
		if out[item.Field] == "" {
			out[item.Field] = item.Problem
		}
	}
	return out
}

// NormalizeSettings merges defaults and supplied values, coerces them to the
// declared types, and validates every field. A nil value means the field is
// absent, not invalid: a declared optional field with no default is simply
// left unset. It returns the normalised values that are valid even when some
// fields failed, so the settings page can keep showing the user's input with
// per-field errors.
func NormalizeSettings(schema SettingsSchema, values map[string]any) (map[string]any, *SettingsValidationError) {
	out := schema.Defaults()
	fieldErrors := make([]SettingsError, 0)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field, ok := schema.Field(key)
		if !ok {
			fieldErrors = append(fieldErrors, SettingsError{Field: key, Problem: "unknown setting"})
			continue
		}
		if values[key] == nil {
			// The settings surface submits every rendered field; an unset
			// field with no default is nil. Treat it as absent so the value
			// map can be saved, and let the required-field check below report
			// a required field that still has no value.
			continue
		}
		normalized, err := field.normalizeValue(values[key])
		if err != nil {
			fieldErrors = append(fieldErrors, SettingsError{Field: key, Problem: err.Error()})
			continue
		}
		out[key] = normalized
	}
	for _, field := range schema.Fields {
		if !field.Required {
			continue
		}
		if _, ok := out[field.Key]; !ok {
			fieldErrors = append(fieldErrors, SettingsError{
				Field: field.Key, Problem: "a value is required",
			})
		}
	}
	if len(fieldErrors) > 0 {
		return out, &SettingsValidationError{Errors: fieldErrors}
	}
	return out, nil
}

// normalizeValue coerces one supplied value and checks type, bounds, options,
// and pattern.
func (f SettingsField) normalizeValue(value any) (any, error) {
	switch f.Type {
	case SettingTypeString:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("want a string, got %T", value)
		}
		if len(text) > MaxSettingStringBytes {
			return nil, fmt.Errorf("must be at most %d bytes", MaxSettingStringBytes)
		}
		if len(f.Options) > 0 {
			allowed := false
			for _, option := range f.Options {
				if option == text {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, fmt.Errorf("%q is not one of the declared options", text)
			}
		}
		if f.Pattern != "" {
			matched, err := regexp.MatchString(f.Pattern, text)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern")
			}
			if !matched {
				return nil, fmt.Errorf("%q does not match %s", text, f.Pattern)
			}
		}
		return text, nil
	case SettingTypeBool:
		flag, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("want a bool, got %T", value)
		}
		return flag, nil
	case SettingTypeInt:
		number, ok := settingInt(value)
		if !ok {
			return nil, fmt.Errorf("want an integer, got %T", value)
		}
		if err := f.checkBounds(float64(number)); err != nil {
			return nil, err
		}
		return number, nil
	case SettingTypeFloat:
		number, ok := settingFloat(value)
		if !ok {
			return nil, fmt.Errorf("want a number, got %T", value)
		}
		if err := f.checkBounds(number); err != nil {
			return nil, err
		}
		return number, nil
	default:
		return nil, fmt.Errorf("unknown setting type %q", f.Type)
	}
}

func (f SettingsField) checkBounds(number float64) error {
	if f.Min != nil && number < *f.Min {
		return fmt.Errorf("must be at least %v", *f.Min)
	}
	if f.Max != nil && number > *f.Max {
		return fmt.Errorf("must be at most %v", *f.Max)
	}
	return nil
}

func (f SettingsField) coerceDefault() (any, error) {
	return f.normalizeValue(f.Default)
}

func settingInt(value any) (int64, bool) {
	switch number := value.(type) {
	case int64:
		return number, true
	case int:
		return int64(number), true
	case int32:
		return int64(number), true
	case uint32:
		return int64(number), true
	default:
		return 0, false
	}
}

func settingFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int64:
		return float64(number), true
	case int:
		return float64(number), true
	default:
		return 0, false
	}
}

// CloneSettingValue copies a settings value so callers cannot mutate the
// manifest or a stored map through a shared slice.
func CloneSettingValue(value any) any {
	return cloneSettingValue(value)
}

func cloneSettingValue(value any) any {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		return append([]any(nil), typed...)
	default:
		return typed
	}
}
