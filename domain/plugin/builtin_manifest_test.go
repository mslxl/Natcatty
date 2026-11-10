package plugin

import (
	"errors"
	"testing"
)

// builtinManifest returns a valid package manifest with the module removed, as
// a compiled-in first-party plugin declares it.
func builtinManifest() Manifest {
	manifest := validManifest()
	manifest.ABI = 0
	manifest.Wasm = Wasm{}
	return manifest
}

// TestValidateBuiltinKeepsTheManifestShapeWithoutAModule proves a built-in may
// omit the wire ABI and module, while every other rule stays identical to
// Manifest.Validate.
func TestValidateBuiltinKeepsTheManifestShapeWithoutAModule(t *testing.T) {
	manifest := builtinManifest()
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate accepted a manifest without a module")
	}
	if err := manifest.ValidateBuiltin(); err != nil {
		t.Fatalf("ValidateBuiltin: %v", err)
	}
}

// TestValidateBuiltinRejectsBadFields reuses the third-party field rules on the
// built-in shape.
func TestValidateBuiltinRejectsBadFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*Manifest)
	}{
		{"missing id", "id", func(m *Manifest) { m.ID = "" }},
		{"id dot count", "id", func(m *Manifest) { m.ID = "acme.hello.extra" }},
		{"bad version", "version", func(m *Manifest) { m.Version = "1.0" }},
		{"incompatible api", "plugin_api", func(m *Manifest) { m.PluginAPI = "^1.0" }},
		{"bad locale", "default_locale", func(m *Manifest) { m.DefaultLocale = "not a locale" }},
		{"unknown permission", "permissions.required[0]", func(m *Manifest) {
			m.Permissions.Required = append(m.Permissions.Required, "not.a.permission")
		}},
		{"duplicate command", "contributions.commands[1].id", func(m *Manifest) {
			m.Contributions.Commands = append(m.Contributions.Commands,
				Command{ID: "greet", Title: "Again"})
		}},
		{"bad dependency id", "dependencies.id", func(m *Manifest) {
			m.Dependencies = []Dependency{{ID: "nodot", Version: "^1.0"}}
		}},
		{"bad injection", "inject", func(m *Manifest) {
			m.Inject = []Injection{{Service: "bad"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := builtinManifest()
			test.edit(&manifest)
			err := manifest.ValidateBuiltin()
			if err == nil {
				t.Fatal("ValidateBuiltin accepted an invalid manifest")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Fatalf("field = %q, want %q (%v)", validation.Field, test.field, err)
			}
		})
	}
}

// TestValidateBuiltinChecksADeclaredModule proves a module left in a built-in
// manifest cannot hide a traversal or a bad hash.
func TestValidateBuiltinChecksADeclaredModule(t *testing.T) {
	tests := []struct {
		name  string
		field string
		wasm  Wasm
	}{
		{"escaping path", "wasm.path", Wasm{Path: "../plugin.wasm"}},
		{"bad hash", "wasm.sha256", Wasm{Path: "plugin.wasm", SHA256: "xyz"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := builtinManifest()
			manifest.Wasm = test.wasm
			err := manifest.ValidateBuiltin()
			if err == nil {
				t.Fatal("ValidateBuiltin accepted a bad module")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Fatalf("field = %q, want %q (%v)", validation.Field, test.field, err)
			}
		})
	}
}
