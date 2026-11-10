package plugin

import (
	"errors"
	"strings"
	"testing"
)

func validLanguageManifest() Manifest {
	manifest := validManifest()
	manifest.Permissions.Required = append(manifest.Permissions.Required,
		string(PermissionEditorLanguage))
	manifest.Contributions.Language = LanguageContributions{
		Providers: []LanguageProvider{{
			ID:        "demo",
			Languages: []string{"sql"},
			Capabilities: []LanguageCapability{
				LanguageCompletion, LanguageDiagnostics,
			},
			Priority: 10,
			Contexts: []string{"editor"},
		}},
	}
	return manifest
}

func TestManifestAcceptsLanguageProvider(t *testing.T) {
	manifest := validLanguageManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// A plugin-owned language is accepted under the contributing plugin's
	// namespace; a built-in language is add-only and needs no ownership.
	manifest = validLanguageManifest()
	manifest.Contributions.Language.Providers[0].Languages = []string{
		"sql", "acme.hello.rune",
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate with a plugin-owned language: %v", err)
	}
}

func TestLanguageProviderRequiresEditorLanguagePermission(t *testing.T) {
	manifest := validLanguageManifest()
	manifest.Permissions.Required = []string{string(PermissionCommands)}
	err := manifest.Validate()
	if err == nil {
		t.Fatal("Validate accepted a language provider without editor.language")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want ValidationError", err)
	}
	if validation.Field != "permissions.required" {
		t.Fatalf("field = %q, want permissions.required", validation.Field)
	}
	if !strings.Contains(err.Error(), string(PermissionEditorLanguage)) {
		t.Fatalf("error %q does not name editor.language", err)
	}
}

func TestLanguageProviderLanguageOwnership(t *testing.T) {
	tests := []struct {
		name      string
		pluginID  string
		languages []string
		field     string
	}{
		{
			name: "built-in", pluginID: "acme.hello", languages: []string{"go"},
		},
		{
			name: "plugin-owned", pluginID: "acme.hello", languages: []string{"acme.hello.rune"},
		},
		{
			name: "foreign prefix", pluginID: "acme.hello",
			languages: []string{"other.plugin.rune"},
			field:     "contributions.language.provider[0].languages[0]",
		},
		{
			name: "missing prefix", pluginID: "acme.hello", languages: []string{"rune"},
			field: "contributions.language.provider[0].languages[0]",
		},
		{
			name: "empty owner suffix", pluginID: "acme.hello", languages: []string{"acme.hello."},
			field: "contributions.language.provider[0].languages[0]",
		},
		{
			name: "uppercase", pluginID: "acme.hello", languages: []string{"acme.hello.Rune"},
			field: "contributions.language.provider[0].languages[0]",
		},
		{
			name: "duplicate", pluginID: "acme.hello", languages: []string{"go", "go"},
			field: "contributions.language.provider[0].languages[1]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validLanguageManifest()
			manifest.ID = test.pluginID
			manifest.Contributions.Language.Providers[0].Languages = test.languages
			err := manifest.Validate()
			if test.field == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Fatalf("field = %q, want %q", validation.Field, test.field)
			}
		})
	}
}

func TestLanguageProviderContractValidation(t *testing.T) {
	tests := []struct {
		name     string
		provider LanguageProvider
		field    string
	}{
		{
			name: "missing id",
			provider: LanguageProvider{
				Languages: []string{"go"}, Capabilities: []LanguageCapability{LanguageHover},
			},
			field: "language.provider.id",
		},
		{
			name: "no languages",
			provider: LanguageProvider{
				ID: "demo", Capabilities: []LanguageCapability{LanguageHover},
			},
			field: "language.provider.languages",
		},
		{
			name: "too many languages",
			provider: LanguageProvider{
				ID: "demo", Capabilities: []LanguageCapability{LanguageHover},
				Languages: manyLanguages(MaxLanguageProviderLanguages + 1),
			},
			field: "language.provider.languages",
		},
		{
			name: "no capabilities",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
			},
			field: "language.provider.capabilities",
		},
		{
			name: "unknown capability",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{"teleport"},
			},
			field: "language.provider.capabilities[0]",
		},
		{
			name: "duplicate capability",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{LanguageHover, LanguageHover},
			},
			field: "language.provider.capabilities[1]",
		},
		{
			name: "priority too low",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{LanguageHover},
				Priority:     MinLanguageProviderPriority - 1,
			},
			field: "language.provider.priority",
		},
		{
			name: "priority too high",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{LanguageHover},
				Priority:     MaxLanguageProviderPriority + 1,
			},
			field: "language.provider.priority",
		},
		{
			name: "too many contexts",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{LanguageHover},
				Contexts:     manyContexts(MaxLanguageContexts + 1),
			},
			field: "language.provider.contexts",
		},
		{
			name: "duplicate context",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{LanguageHover},
				Contexts:     []string{"editor", "editor"},
			},
			field: "language.provider.contexts[1]",
		},
		{
			name: "invalid context",
			provider: LanguageProvider{
				ID: "demo", Languages: []string{"go"},
				Capabilities: []LanguageCapability{LanguageHover},
				Contexts:     []string{"Not An Identifier"},
			},
			field: "language.provider.contexts[0]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.provider.Validate("acme.hello", "language.provider")
			if err == nil {
				t.Fatal("Validate accepted an invalid provider")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Fatalf("field = %q, want %q", validation.Field, test.field)
			}
		})
	}
}

func TestLanguageProviderPrioritiesAndBounds(t *testing.T) {
	for _, priority := range []int{
		MinLanguageProviderPriority, 0, MaxLanguageProviderPriority,
	} {
		provider := LanguageProvider{
			ID: "demo", Languages: []string{"go"},
			Capabilities: []LanguageCapability{LanguageHighlight},
			Priority:     priority,
		}
		if err := provider.Validate("acme.hello", "language.provider"); err != nil {
			t.Fatalf("Validate priority %d: %v", priority, err)
		}
	}
}

func TestDuplicateLanguageProviderIDsAreRejected(t *testing.T) {
	manifest := validLanguageManifest()
	manifest.Contributions.Language.Providers = append(
		manifest.Contributions.Language.Providers,
		manifest.Contributions.Language.Providers[0])
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate language provider") {
		t.Fatalf("Validate error = %v, want duplicate provider", err)
	}
}

func TestLanguageCapabilitySetIsClosed(t *testing.T) {
	want := []LanguageCapability{
		LanguageHighlight, LanguageCompletion, LanguageDiagnostics, LanguageFormatting,
		LanguageHover, LanguageSignatureHelp, LanguageNavigation, LanguageCodeActions,
	}
	got := LanguageCapabilities()
	if len(got) != len(want) {
		t.Fatalf("capabilities = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("capability %d = %q, want %q", i, got[i], want[i])
		}
		if !IsLanguageCapability(string(want[i])) {
			t.Fatalf("IsLanguageCapability(%q) = false", want[i])
		}
	}
	if IsLanguageCapability("teleport") {
		t.Fatal("the capability set accepted an unknown name")
	}
	got[0] = "mutated"
	if LanguageCapabilities()[0] != LanguageHighlight {
		t.Fatal("LanguageCapabilities leaks its backing array")
	}
}

func TestBuiltinLanguageIDsAreStable(t *testing.T) {
	for _, id := range []string{
		"c", "cpp", "css", "go", "html", "javascript", "json", "markdown",
		"python", "rust", "shell", "sql", "toml", "typescript", "xml", "yaml",
	} {
		if !IsBuiltinLanguageID(id) {
			t.Fatalf("IsBuiltinLanguageID(%q) = false", id)
		}
	}
	if IsBuiltinLanguageID("rune") || IsBuiltinLanguageID("") {
		t.Fatal("an unknown language is reported as built-in")
	}
	if got := BuiltinLanguageIDs(); len(got) != 16 || got[0] != "c" || got[15] != "yaml" {
		t.Fatalf("BuiltinLanguageIDs = %v", got)
	}
}

func TestLanguageProviderMatching(t *testing.T) {
	provider := LanguageProvider{
		ID: "demo", Languages: []string{"sql", "acme.hello.rune"},
		Capabilities: []LanguageCapability{LanguageCompletion, LanguageDiagnostics},
		Contexts:     []string{"editor"},
	}
	if !provider.SupportsLanguage("sql", "editor") {
		t.Fatal("the provider does not serve sql in the editor")
	}
	if !provider.SupportsLanguage("acme.hello.rune", "editor") {
		t.Fatal("the provider does not serve its own custom language")
	}
	if provider.SupportsLanguage("go", "editor") {
		t.Fatal("the provider serves a language it did not declare")
	}
	if provider.SupportsLanguage("sql", "grid") {
		t.Fatal("the provider ignored its context allowlist")
	}
	if !provider.Supports(LanguageCompletion) || !provider.Supports(LanguageDiagnostics) {
		t.Fatal("the provider does not declare its capabilities")
	}
	if provider.Supports(LanguageFormatting) {
		t.Fatal("the provider declares a capability it did not name")
	}

	open := LanguageProvider{
		ID: "open", Languages: []string{"go"},
		Capabilities: []LanguageCapability{LanguageHover},
	}
	if !open.SupportsLanguage("go", "anything") {
		t.Fatal("a provider without contexts must serve every context")
	}
}

func TestIsPluginOwnedLanguageID(t *testing.T) {
	if !IsPluginOwnedLanguageID("acme.hello", "acme.hello.rune") {
		t.Fatal("a namespaced language was rejected")
	}
	for _, id := range []string{
		"rune", "other.plugin.rune", "acme.hello.", "acme.hello.Rune",
		"acme.hello/run e", "",
	} {
		if IsPluginOwnedLanguageID("acme.hello", id) {
			t.Fatalf("IsPluginOwnedLanguageID(%q) = true", id)
		}
	}
}

func manyLanguages(count int) []string {
	languages := make([]string, count)
	for i := range languages {
		languages[i] = "acme.hello.language" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	return languages
}

func manyContexts(count int) []string {
	contexts := make([]string, count)
	for i := range contexts {
		contexts[i] = "context" + string(rune('a'+i))
	}
	return contexts
}
