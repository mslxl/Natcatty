package app

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adapterplugininstall "github.com/mslxl/natcatty/adapters/plugininstall"
	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterpluginpackage "github.com/mslxl/natcatty/adapters/pluginpackage"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	appfileopen "github.com/mslxl/natcatty/application/fileopen"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
	editorui "github.com/mslxl/natcatty/ui/editor"
)

// sampleRegistrar satisfies the container's contribution port. The language
// sample contributes only a language provider.
type sampleRegistrar struct{}

func (sampleRegistrar) RegisterCommand(appplugin.PluginRef, domainplugin.Command,
	appplugin.Handler) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (sampleRegistrar) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (sampleRegistrar) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

// sampleEditorService is the editor's file seam. The render test never saves.
type sampleEditorService struct{}

func (sampleEditorService) Save(context.Context, appfileopen.EditID, []byte, bool) error {
	return nil
}
func (sampleEditorService) Close(appfileopen.EditID) {}
func (sampleEditorService) ResolveExternal(context.Context, appfileopen.EditID, bool) error {
	return nil
}
func (sampleEditorService) RetryExternal(context.Context, appfileopen.EditID) error { return nil }

// sampleArchive packages the committed language-provider sample as a .natplug.
func sampleArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "language-provider")
	manifest, err := os.ReadFile(filepath.Join(root, "plugin.toml"))
	if err != nil {
		t.Fatalf("read sample manifest: %v", err)
	}
	module, err := os.ReadFile(filepath.Join(root, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read sample module: %v", err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "i18n", "en.toml"))
	if err != nil {
		t.Fatalf("read sample catalog: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "acme.language-provider.natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"i18n/en.toml", catalog},
		{"plugin.toml", manifest},
		{"plugin.wasm", module},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetModTime(fixed)
		header.SetMode(0o644)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create %s: %v", entry.name, err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatalf("write %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
	return archivePath
}

// startLanguageSample installs and enables the committed sample through the
// real validator, install store, manager, container, and wazero runtime.
func startLanguageSample(t *testing.T) *appplugin.Manager {
	t.Helper()
	ctx := context.Background()
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		sampleRegistrar{},
		adapterpluginpackage.New(adapterwasm.Validator{}),
		adapterplugininstall.Open(t.TempDir()),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	record, err := manager.InstallFile(ctx, sampleArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if _, err := manager.Review(ctx, record.ID); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if err := manager.Enable(ctx, record.ID, []domainplugin.PermissionDecision{{
		Permission: domainplugin.PermissionEditorLanguage, Allow: true,
	}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return manager
}

// TestLanguageSampleRendersInTheEditorView proves the committed sample's
// completion and diagnostics reach the rendered editor view, not just the
// editor state. The sdk sample test cannot import MyGo under the architecture
// rule, so the render assertion lives at the app layer where MyGo is allowed.
func TestLanguageSampleRendersInTheEditorView(t *testing.T) {
	manager := startLanguageSample(t)
	state := editorui.NewState(sampleEditorService{},
		editorui.WithLanguage(manager.Language()))
	t.Cleanup(func() {
		state.Close()
		state.Discard()
	})

	content := "select 1 -- TODO: fix the query\n"
	path := filepath.Join(t.TempDir(), "demo.sql")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.OpenFile(context.Background(), appfileopen.File{
		Session: "s1", Name: "demo.sql", Path: path,
		Save: func(context.Context, []byte, bool) error { return nil },
	}); err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	state.Flush()
	state.SyncLanguage()

	deadline := time.Now().Add(5 * time.Second)
	for {
		state.SyncLanguage()
		if len(state.Completions()) > 0 && len(state.Diagnostics()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sample provider did not answer: completions=%+v diagnostics=%+v",
				state.Completions(), state.Diagnostics())
		}
		time.Sleep(10 * time.Millisecond)
	}

	tester := ui.NewTester(editorui.View(state), 800, 600)
	tester.SetLocale("en")
	tester.Frame()
	if _, ok := tester.Find("sample_keyword"); !ok {
		t.Fatal("the sample completion label is not rendered in the editor view")
	}
	if _, ok := tester.Find("TODO marker found by acme.language-provider"); !ok {
		t.Fatal("the sample diagnostic message is not rendered in the editor view")
	}
}
