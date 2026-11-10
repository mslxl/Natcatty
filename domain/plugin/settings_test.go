package plugin

import (
	"errors"
	"strings"
	"testing"
)

func sampleSchema() SettingsSchema {
	min := 1.0
	max := 60.0
	return SettingsSchema{
		Version: 1,
		Fields: []SettingsField{
			{
				Key: "greeting", Type: SettingTypeString,
				Label: "Greeting", Default: "hello", Required: true,
			},
			{
				Key: "mode", Type: SettingTypeString,
				Default: "safe", Options: []string{"safe", "fast"},
			},
			{
				Key: "retries", Type: SettingTypeInt,
				Default: int64(3), Min: &min, Max: &max,
			},
			{
				Key: "enabled", Type: SettingTypeBool, Default: true,
			},
			{
				Key: "session_token", Type: SettingTypeString,
				Default: "ephemeral", Volatile: true,
			},
		},
	}
}

func TestSettingsSchemaValidatesAndAppliesDefaults(t *testing.T) {
	schema := sampleSchema()
	if err := schema.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	effective, err := NormalizeSettings(schema, nil)
	if err != nil {
		t.Fatalf("NormalizeSettings: %v", err)
	}
	if effective["greeting"] != "hello" || effective["retries"] != int64(3) ||
		effective["enabled"] != true || effective["session_token"] != "ephemeral" {
		t.Fatalf("defaults = %#v", effective)
	}
}

func TestSettingsSchemaRejectsInvalidDeclarations(t *testing.T) {
	tests := map[string]SettingsSchema{
		"unknown type": {
			Version: 1,
			Fields:  []SettingsField{{Key: "a", Type: "secret"}},
		},
		"duplicate key": {
			Version: 1,
			Fields: []SettingsField{
				{Key: "a", Type: SettingTypeString},
				{Key: "a", Type: SettingTypeBool},
			},
		},
		"bad default": {
			Version: 1,
			Fields:  []SettingsField{{Key: "a", Type: SettingTypeInt, Default: "x"}},
		},
		"bad option default": {
			Version: 1,
			Fields: []SettingsField{{
				Key: "a", Type: SettingTypeString,
				Default: "c", Options: []string{"a", "b"},
			}},
		},
		"bad pattern": {
			Version: 1,
			Fields: []SettingsField{{
				Key: "a", Type: SettingTypeString, Pattern: "[",
			}},
		},
		"out of range declaration": {
			Version: 1,
			Fields: []SettingsField{{
				Key: "a", Type: SettingTypeBool, Min: floatPtr(0),
			}},
		},
		"inverted range": {
			Version: 1,
			Fields: []SettingsField{{
				Key: "a", Type: SettingTypeInt,
				Min: floatPtr(10), Max: floatPtr(1),
			}},
		},
		"unknown schema version": {
			Version: 2,
			Fields:  []SettingsField{{Key: "a", Type: SettingTypeString}},
		},
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid schema")
			}
		})
	}
}

func TestNormalizeSettingsRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
		field  string
		match  string
	}{
		{"wrong type", map[string]any{"retries": "many"}, "retries", "want an integer"},
		{"below minimum", map[string]any{"retries": int64(0)}, "retries", "at least"},
		{"above maximum", map[string]any{"retries": int64(61)}, "retries", "at most"},
		{"unknown option", map[string]any{"mode": "turbo"}, "mode", "declared options"},
		{"unknown key", map[string]any{"mystery": true}, "mystery", "unknown setting"},
		{"over long string", map[string]any{"greeting": strings.Repeat("x", MaxSettingStringBytes+1)},
			"greeting", "at most"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeSettings(sampleSchema(), test.values)
			if err == nil {
				t.Fatal("NormalizeSettings accepted an invalid value")
			}
			var validation *SettingsValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %T, want SettingsValidationError", err)
			}
			if got := validation.FieldErrors()[test.field]; !strings.Contains(got, test.match) {
				t.Fatalf("field %q error = %q, want %q", test.field, got, test.match)
			}
		})
	}
}

func TestNormalizeSettingsRequiresAValueForRequiredFields(t *testing.T) {
	schema := SettingsSchema{
		Version: 1,
		Fields: []SettingsField{{
			Key: "host", Type: SettingTypeString, Required: true,
		}},
	}
	if err := schema.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	_, err := NormalizeSettings(schema, map[string]any{})
	var validation *SettingsValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want SettingsValidationError", err)
	}
	if validation.FieldErrors()["host"] == "" {
		t.Fatalf("missing required field error: %#v", validation.Errors)
	}
	effective, err := NormalizeSettings(schema, map[string]any{"host": "example"})
	if err != nil || effective["host"] != "example" {
		t.Fatalf("valid value = %#v, %v", effective, err)
	}
}

func TestNormalizeSettingsTreatsNilAsAbsent(t *testing.T) {
	schema := SettingsSchema{
		Version: 1,
		Fields: []SettingsField{
			{Key: "mode", Type: SettingTypeString, Default: "safe"},
			{Key: "optional_note", Type: SettingTypeString},
			{Key: "host", Type: SettingTypeString, Required: true},
		},
	}
	if err := schema.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	effective, err := NormalizeSettings(schema, map[string]any{
		"mode": "fast",
		// The settings surface renders an unset optional field as nil.
		"optional_note": nil,
		"host":          "example.com",
	})
	if err != nil {
		t.Fatalf("NormalizeSettings with a nil optional field: %v", err)
	}
	if _, ok := effective["optional_note"]; ok {
		t.Fatalf("nil optional field was stored: %#v", effective)
	}
	if effective["mode"] != "fast" || effective["host"] != "example.com" {
		t.Fatalf("effective values = %#v", effective)
	}

	// A nil value never satisfies a required field.
	_, err = NormalizeSettings(schema, map[string]any{"host": nil})
	var validation *SettingsValidationError
	if !errors.As(err, &validation) || validation.FieldErrors()["host"] == "" {
		t.Fatalf("required nil field error = %v, want a host field error", err)
	}
}

func TestVolatileFieldsAreDroppedFromStoredValues(t *testing.T) {
	schema := sampleSchema()
	stored := map[string]any{
		"greeting":      "stored",
		"session_token": "must-not-survive",
	}
	dropped := schema.DropVolatile(stored)
	if _, ok := dropped["session_token"]; ok {
		t.Fatalf("DropVolatile kept a volatile value: %#v", dropped)
	}
	if dropped["greeting"] != "stored" {
		t.Fatalf("DropVolatile dropped a normal value: %#v", dropped)
	}
	effective, err := NormalizeSettings(schema, dropped)
	if err != nil {
		t.Fatalf("NormalizeSettings: %v", err)
	}
	if effective["session_token"] != "ephemeral" {
		t.Fatalf("volatile field did not reset to its default: %#v", effective)
	}
}

func TestSettingsErrorsAreReadable(t *testing.T) {
	err := &SettingsValidationError{Errors: []SettingsError{
		{Field: "retries", Problem: "must be at least 1"},
		{Field: "mode", Problem: "unknown setting"},
	}}
	message := err.Error()
	for _, want := range []string{"retries", "must be at least 1", "mode", "unknown setting"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q lacks %q", message, want)
		}
	}
	if ConfigReason(message) != "config-invalid: "+message {
		t.Fatalf("ConfigReason = %q", ConfigReason(message))
	}
}

func TestEventVocabularyIsClosed(t *testing.T) {
	for _, name := range []string{
		"host.changed", "session.state", "terminal.exit", "vault.changed",
		"plugin.lifecycle", "notification.action", "settings.changed",
		"locale_changed", "events.gap",
	} {
		if !IsKnownEventType(name) {
			t.Errorf("event type %q is not known", name)
		}
	}
	if IsKnownEventType("host.deleted") {
		t.Fatal("an undeclared event type was accepted")
	}
}

func floatPtr(value float64) *float64 { return &value }
