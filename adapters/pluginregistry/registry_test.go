package pluginregistry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/pluginregistry"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

const packageBytes = "natplug-package-body"

type fixture struct {
	root    string
	archive []byte
	sum     string
}

func writeFixture(t *testing.T, mutate func(*domainplugin.RegistryPlugin, map[string][]byte)) fixture {
	t.Helper()
	root := t.TempDir()
	archive := []byte(packageBytes)
	sum := sha256Hex(archive)
	version := domainplugin.RegistryVersion{
		Version: "1.0.0", Channel: domainplugin.ChannelStable,
		PluginAPI: "^0.1", ABI: 1,
		Package: "packages/acme.notes/1.0.0.zip",
		SHA256:  sum, Size: int64(len(archive)),
		Required: []string{"commands"},
	}
	plugin := domainplugin.RegistryPlugin{
		ID: "acme.notes", Name: "Acme Notes", Description: "Markdown notebook",
		Versions: []domainplugin.RegistryVersion{version},
	}
	files := map[string][]byte{}
	if mutate != nil {
		mutate(&plugin, files)
	}
	index := domainplugin.RegistryIndex{
		Schema: 1, Name: "Test Registry",
		Plugins: []domainplugin.RegistryPluginSummary{{
			ID: plugin.ID, Name: plugin.Name, Description: plugin.Description,
			Keywords: []string{"writing"},
		}},
	}
	writeJSON(t, filepath.Join(root, "index.json"), index)
	writeJSON(t, filepath.Join(root, "plugins", plugin.ID+".json"), plugin)
	for name, data := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), data)
	}
	if mutate == nil {
		writeFile(t, filepath.Join(root, filepath.FromSlash(version.Package)), archive)
	}
	return fixture{root: root, archive: archive, sum: sum}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, data)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryRegistryServesIndexPluginAndPackage(t *testing.T) {
	fixture := writeFixture(t, nil)
	registry := pluginregistry.OpenDirectory(fixture.root)
	ctx := context.Background()

	index, err := registry.Index(ctx)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if index.Name != "Test Registry" || len(index.Plugins) != 1 ||
		index.Plugins[0].ID != "acme.notes" {
		t.Fatalf("index = %+v", index)
	}

	plugin, err := registry.Plugin(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("Plugin: %v", err)
	}
	if len(plugin.Versions) != 1 || plugin.Versions[0].SHA256 != fixture.sum {
		t.Fatalf("plugin = %+v", plugin)
	}

	body, err := registry.FetchPackage(ctx, "acme.notes", "1.0.0")
	if err != nil {
		t.Fatalf("FetchPackage: %v", err)
	}
	if string(body) != packageBytes {
		t.Fatalf("package = %q", body)
	}
	if registry.Identity() == "" {
		t.Fatal("registry identity is empty")
	}
}

func TestDirectoryRegistryRejectsInvalidMetadataAndUnknownIDs(t *testing.T) {
	fixture := writeFixture(t, nil)
	registry := pluginregistry.OpenDirectory(fixture.root)
	ctx := context.Background()

	if _, err := registry.Plugin(ctx, "acme.missing"); err == nil {
		t.Fatal("unknown plugin id was accepted")
	}
	if _, err := registry.Plugin(ctx, "../escape"); err == nil {
		t.Fatal("unsafe plugin id was accepted")
	}
	if _, err := registry.FetchPackage(ctx, "acme.notes", "9.9.9"); err == nil {
		t.Fatal("unknown version was fetched")
	}
}

func TestDirectoryRegistryRejectsAPathEscapingPackageEntry(t *testing.T) {
	root := t.TempDir()
	plugin := domainplugin.RegistryPlugin{
		ID: "acme.notes",
		Versions: []domainplugin.RegistryVersion{{
			Version: "1.0.0", Channel: domainplugin.ChannelStable,
			PluginAPI: "^0.1", ABI: 1,
			Package: "../outside.zip", SHA256: strings.Repeat("a", 64), Size: 1,
		}},
	}
	writeJSON(t, filepath.Join(root, "plugins", "acme.notes.json"), plugin)
	registry := pluginregistry.OpenDirectory(root)
	if _, err := registry.Plugin(context.Background(), "acme.notes"); err == nil {
		t.Fatal("a package path escaping the registry root was accepted")
	}
}

func TestDirectoryRegistryRejectsAMalformedIndex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.json"), []byte("{not json"))
	registry := pluginregistry.OpenDirectory(root)
	if _, err := registry.Index(context.Background()); err == nil {
		t.Fatal("malformed index was accepted")
	}
}

func TestHTTPRegistryServesTheSameLayout(t *testing.T) {
	fixture := writeFixture(t, nil)
	server := httptest.NewServer(http.FileServer(http.Dir(fixture.root)))
	defer server.Close()
	registry := pluginregistry.OpenHTTP(server.URL)
	ctx := context.Background()

	index, err := registry.Index(ctx)
	if err != nil || len(index.Plugins) != 1 {
		t.Fatalf("Index = %+v, %v", index, err)
	}
	plugin, err := registry.Plugin(ctx, "acme.notes")
	if err != nil || len(plugin.Versions) != 1 {
		t.Fatalf("Plugin = %+v, %v", plugin, err)
	}
	body, err := registry.FetchPackage(ctx, "acme.notes", "1.0.0")
	if err != nil || string(body) != packageBytes {
		t.Fatalf("FetchPackage = %q, %v", body, err)
	}
	if got := registry.Identity(); got != server.URL {
		t.Fatalf("identity = %q, want %q", got, server.URL)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
