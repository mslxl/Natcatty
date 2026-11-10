package plugin

import (
	"errors"
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		ManifestVersion: 1,
		ID:              "acme.hello",
		Name:            "Acme Hello",
		Version:         "1.2.3",
		PluginAPI:       "^0.1",
		ABI:             1,
		DefaultLocale:   "en",
		Wasm: Wasm{
			Path:         "plugin.wasm",
			SHA256:       strings.Repeat("a", 64),
			MemoryMaxMiB: 64,
		},
		Contributions: Contributions{
			Commands: []Command{
				{ID: "greet", Title: "Say Hello", Keywords: []string{"hello"}},
			},
			Menus:       []Menu{{Command: "greet", Label: "Say Hello"}},
			Keybindings: []Keybinding{{Command: "greet", Accelerator: "CmdOrCtrl+Shift+G"}},
		},
	}
}

func TestManifestValidationAcceptsTheWalkingSkeleton(t *testing.T) {
	manifest := validManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := QualifiedCommandID(manifest.ID, "greet"); got != "plugin.acme.hello.greet" {
		t.Fatalf("qualified command = %q", got)
	}
}

func TestManifestValidationRejectsBadFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*Manifest)
	}{
		{"manifest version", "manifest_version", func(m *Manifest) { m.ManifestVersion = 2 }},
		{"missing id", "id", func(m *Manifest) { m.ID = "" }},
		{"uppercase id", "id", func(m *Manifest) { m.ID = "Acme.Hello" }},
		{"id dot count", "id", func(m *Manifest) { m.ID = "acme.hello.extra" }},
		{"missing name", "name", func(m *Manifest) { m.Name = " " }},
		{"bad version", "version", func(m *Manifest) { m.Version = "1.0" }},
		{"incompatible api", "plugin_api", func(m *Manifest) { m.PluginAPI = "^1.0" }},
		{"unsupported abi", "abi", func(m *Manifest) { m.ABI = 2 }},
		{"missing locale", "default_locale", func(m *Manifest) { m.DefaultLocale = "" }},
		{"bad locale", "default_locale", func(m *Manifest) { m.DefaultLocale = "not a locale" }},
		{"absolute path", "wasm.path", func(m *Manifest) { m.Wasm.Path = "/plugin.wasm" }},
		{"escaping path", "wasm.path", func(m *Manifest) { m.Wasm.Path = "../plugin.wasm" }},
		{"non wasm path", "wasm.path", func(m *Manifest) { m.Wasm.Path = "plugin.txt" }},
		{"bad hash", "wasm.sha256", func(m *Manifest) { m.Wasm.SHA256 = "xyz" }},
		{"negative memory", "wasm.memory_max_mib", func(m *Manifest) { m.Wasm.MemoryMaxMiB = -1 }},
		{"too much memory", "wasm.memory_max_mib", func(m *Manifest) { m.Wasm.MemoryMaxMiB = 257 }},
		{"duplicate command", "contributions.commands[1].id", func(m *Manifest) {
			m.Contributions.Commands = append(m.Contributions.Commands,
				Command{ID: "greet", Title: "Again"})
		}},
		{"menu without command", "contributions.menus[0].command", func(m *Manifest) {
			m.Contributions.Menus[0].Command = "missing"
		}},
		{"empty menu label", "contributions.menus[0].label", func(m *Manifest) {
			m.Contributions.Menus[0].Label = ""
		}},
		{"keybinding without command", "contributions.keybindings[0].command", func(m *Manifest) {
			m.Contributions.Keybindings[0].Command = "missing"
		}},
		{"empty accelerator", "contributions.keybindings[0].accelerator", func(m *Manifest) {
			m.Contributions.Keybindings[0].Accelerator = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.edit(&manifest)
			err := manifest.Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid manifest")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Fatalf("field = %q, want %q (%v)", validation.Field, test.field, err)
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("error %q does not name %q", err, test.field)
			}
		})
	}
}

func TestManifestMemoryMaximumDefaultsAndBounds(t *testing.T) {
	manifest := validManifest()
	manifest.Wasm.MemoryMaxMiB = 0
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate with an omitted maximum: %v", err)
	}
	if got, want := manifest.Wasm.EffectiveMemoryMaxMiB(), DefaultMemoryMaxMiB; got != want {
		t.Fatalf("effective maximum = %d MiB, want %d MiB", got, want)
	}
	if got, want := manifest.Wasm.EffectiveMemoryMaxBytes(), int64(DefaultMemoryMaxMiB)<<20; got != want {
		t.Fatalf("effective bytes = %d, want %d", got, want)
	}

	for _, mib := range []int{MinMemoryMaxMiB, MaxMemoryMaxMiB} {
		manifest := validManifest()
		manifest.Wasm.MemoryMaxMiB = mib
		if err := manifest.Validate(); err != nil {
			t.Fatalf("Validate %d MiB: %v", mib, err)
		}
		if got := manifest.Wasm.EffectiveMemoryMaxMiB(); got != mib {
			t.Fatalf("effective maximum = %d MiB, want %d MiB", got, mib)
		}
	}
}

func TestManifestContributionsMayBeEmpty(t *testing.T) {
	manifest := validManifest()
	manifest.Contributions = Contributions{}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
