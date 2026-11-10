package pluginmanifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/pluginmanifest"
)

func TestDecodeDevelopmentManifest(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "wasm", "testdata", "devplugin", "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := pluginmanifest.Decode(file)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if manifest.ID != "acme.hello" {
		t.Fatalf("id = %q", manifest.ID)
	}
	if len(manifest.Contributions.Commands) != 1 ||
		manifest.Contributions.Commands[0].ID != "greet" {
		t.Fatalf("commands = %+v", manifest.Contributions.Commands)
	}
	if len(manifest.Contributions.Menus) != 1 ||
		len(manifest.Contributions.Keybindings) != 1 {
		t.Fatalf("contributions = %+v", manifest.Contributions)
	}
}

func TestDecodeRejectsMalformedTOML(t *testing.T) {
	_, err := pluginmanifest.Decode(strings.NewReader("manifest_version = "))
	if err == nil {
		t.Fatal("Decode accepted malformed TOML")
	}
}

// TestDecodeLanguageProviderContribution proves the dotted contribution type
// `language.provider` decodes from its nested TOML table form and validates
// with the editor.language permission.
func TestDecodeLanguageProviderContribution(t *testing.T) {
	manifest, err := pluginmanifest.Decode(strings.NewReader(`
manifest_version = 1
id = "acme.hello"
name = "Hello"
version = "1.0.0"
plugin_api = "^0.1"
abi = 1
default_locale = "en"

[wasm]
path = "plugin.wasm"
sha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
memory_max_mib = 64

[permissions]
required = ["editor.language"]

[[contributions.language.provider]]
id = "demo"
languages = ["sql", "acme.hello.rune"]
capabilities = ["completion", "diagnostics"]
priority = 10
contexts = ["editor"]
`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(manifest.Contributions.Language.Providers) != 1 {
		t.Fatalf("language providers = %+v", manifest.Contributions.Language.Providers)
	}
	provider := manifest.Contributions.Language.Providers[0]
	if provider.ID != "demo" || provider.Priority != 10 ||
		len(provider.Languages) != 2 || provider.Languages[0] != "sql" ||
		len(provider.Capabilities) != 2 ||
		provider.Capabilities[0] != "completion" ||
		provider.Capabilities[1] != "diagnostics" ||
		len(provider.Contexts) != 1 || provider.Contexts[0] != "editor" {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestDecodeIgnoresUnknownKeys(t *testing.T) {
	manifest, err := pluginmanifest.Decode(strings.NewReader(`
manifest_version = 1
id = "acme.hello"
name = "Hello"
version = "1.0.0"
plugin_api = "^0.1"
abi = 1
default_locale = "en"
future_field = "ignored"

[wasm]
path = "plugin.wasm"
sha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
memory_max_mib = 64

[[future_contributions]]
whatever = true
`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if manifest.ID != "acme.hello" {
		t.Fatalf("id = %q", manifest.ID)
	}
}
