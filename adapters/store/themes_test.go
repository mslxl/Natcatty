package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	appthemes "github.com/mslxl/natcatty/application/themes"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

const storeITerm = `<plist version="1.0"><dict>
<key>Background Color</key><dict>
<key>Red Component</key><real>0.1</real>
<key>Green Component</key><real>0.2</real>
<key>Blue Component</key><real>0.3</real></dict>
<key>Foreground Color</key><dict>
<key>Red Component</key><real>0.9</real>
<key>Green Component</key><real>0.9</real>
<key>Blue Component</key><real>0.9</real></dict>
</dict></plist>`

func TestThemesStoreRoundTripsAndToleratesBadFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "themes.json")
	store := OpenThemes(path)
	ctx := context.Background()

	missing, err := store.Load(ctx)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing load = %v, %v", missing, err)
	}

	preset, err := domaintheme.ParseITermcolors([]byte(storeITerm), "Mine")
	if err != nil {
		t.Fatal(err)
	}
	preset.ID = "custom-mine"
	if err := store.Save(ctx, []domaintheme.TerminalTheme{preset}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != "custom-mine" || loaded[0].Colors.Background != "#1a334d" {
		t.Fatalf("loaded = %+v", loaded)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx); err != nil || len(loaded) != 0 {
		t.Errorf("malformed load = %v, %v", loaded, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"themes":[{"id":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx); err != nil || len(loaded) != 0 {
		t.Errorf("future version load = %v, %v", loaded, err)
	}
}

// TestImportedThemeSurvivesRestart proves the two-document contract: the
// custom scheme lives in themes.json, while preferences.json only stores its
// ID. A fresh service over the same file finds it after a restart.
func TestImportedThemeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "themes.json")
	ctx := context.Background()

	first := appthemes.NewService(OpenThemes(path))
	imported, err := first.Import(ctx, "Restart Blue.itermcolors", []byte(storeITerm))
	if err != nil {
		t.Fatal(err)
	}

	second := appthemes.NewService(OpenThemes(path))
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	found, ok := second.Find(imported.ID)
	if !ok {
		t.Fatalf("imported theme %q did not survive the restart: %+v",
			imported.ID, second.CustomThemes())
	}
	if found.Name != "Restart Blue" || found.Colors.Background != "#1a334d" {
		t.Errorf("restored theme = %+v", found)
	}
	if got := len(second.VisibleThemes()); got != 185 {
		t.Errorf("visible themes = %d, want 185", got)
	}
}
