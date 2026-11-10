package languageprovider_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

const sampleID = "acme.language-provider"

// noopRegistrar satisfies the container's contribution port. The sample
// contributes only a language provider, which the container registers with the
// language host directly.
type noopRegistrar struct{}

func (noopRegistrar) RegisterCommand(appplugin.PluginRef, domainplugin.Command,
	appplugin.Handler) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (noopRegistrar) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (noopRegistrar) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

// editorService is the editor's file seam. The sample test never saves, so
// every operation succeeds without touching a remote.
type editorService struct{}

func (editorService) Save(context.Context, appfileopen.EditID, []byte, bool) error { return nil }
func (editorService) Close(appfileopen.EditID)                                     {}
func (editorService) ResolveExternal(context.Context, appfileopen.EditID, bool) error {
	return nil
}
func (editorService) RetryExternal(context.Context, appfileopen.EditID) error { return nil }

// sampleArchive packages the committed manifest and module as a .natplug.
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
	archivePath := filepath.Join(t.TempDir(), sampleID+".natplug")
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

// startSample installs, reviews, enables, and activates the real sample
// through the real archive validator, install store, manager, container, and
// wazero runtime.
func startSample(t *testing.T) *appplugin.Manager {
	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		noopRegistrar{},
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, sampleArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if record.ID != sampleID {
		t.Fatalf("installed id = %q", record.ID)
	}
	review, err := manager.Review(ctx, sampleID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(review.Required) != 1 ||
		review.Required[0].Permission != domainplugin.PermissionEditorLanguage {
		t.Fatalf("required consent rows = %+v", review.Required)
	}
	if len(review.Optional) != 0 {
		t.Fatalf("optional consent rows = %+v", review.Optional)
	}
	if err := manager.Enable(ctx, sampleID, []domainplugin.PermissionDecision{{
		Permission: domainplugin.PermissionEditorLanguage, Allow: true,
	}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	list, err := manager.List(ctx)
	if err != nil || len(list) != 1 || list[0].Status != appplugin.StatusActive {
		t.Fatalf("List after Enable = %+v, %v", list, err)
	}
	return manager
}

// TestLanguageProviderSampleDrivesTheEditor packages and runs the committed
// sample and drives the real editor state through the real manager, container,
// wazero runtime, and language router.
func TestLanguageProviderSampleDrivesTheEditor(t *testing.T) {
	manager := startSample(t)
	state := editorui.NewState(editorService{}, editorui.WithLanguage(manager.Language()))

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

	if state.LanguageID() != "sql" || state.LanguageDocumentID() != "editor:s1" {
		t.Fatalf("language binding = %q %q", state.LanguageID(), state.LanguageDocumentID())
	}

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

	labels := make(map[string]bool)
	for _, item := range state.Completions() {
		labels[item.Label] = true
		if item.Detail != "provided by acme.language-provider" {
			t.Fatalf("completion item = %+v", item)
		}
	}
	if !labels["sample_keyword"] || !labels["sample_function"] {
		t.Fatalf("completion labels = %v", labels)
	}
	diagnostics := state.Diagnostics()
	if len(diagnostics) != 1 ||
		!strings.Contains(diagnostics[0].Message, "TODO marker found") ||
		diagnostics[0].Source != sampleID || diagnostics[0].Severity != 2 ||
		diagnostics[0].Range.Start != (appplugin.LanguagePosition{Line: 0, Column: 12}) {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	results, ok := manager.Language().Results(state.LanguageDocumentID(),
		domainplugin.LanguageCompletion)
	if !ok || results.Version != state.LanguageVersion() || results.Providers != 1 {
		t.Fatalf("stored completion results = %+v, %v", results, ok)
	}

	// Base highlighting is host-owned and cannot be replaced: the SQL
	// keywords and comment come from the host layer while the sample's empty
	// highlight result stacks on top.
	highlight, ok := state.RequestLanguage(context.Background(),
		domainplugin.LanguageHighlight)
	if !ok {
		t.Fatal("the highlight request failed")
	}
	kinds := make(map[string]bool)
	for _, token := range highlight.Highlights {
		kinds[token.Kind] = true
	}
	if !kinds["keyword"] || !kinds["comment"] {
		t.Fatalf("base highlight kinds = %v", kinds)
	}

	hover, ok := state.RequestLanguage(context.Background(), domainplugin.LanguageHover)
	if !ok || hover.Hover == nil ||
		hover.Hover.Contents != "Language-provider sample hover" ||
		hover.Hover.Format != "plain" {
		t.Fatalf("hover = %+v, %v", hover, ok)
	}

	// The full snapshot reached the guest: the diagnostics line/column proves
	// the sample parsed the document content, and the host routed by the
	// editor's document id and version.
	if state.LanguageVersion() != 1 {
		t.Fatalf("document version = %d, want 1", state.LanguageVersion())
	}
}
