package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mslxl/natcatty/application/desktop"
)

func TestDesktopStoreLoadMissingAndMalformedUseDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-settings.json")
	store := OpenDesktop(path)

	settings, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 0 {
		t.Fatalf("missing file = %+v", settings)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 0 {
		t.Fatalf("malformed file = %+v", settings)
	}
}

func TestDesktopStoreSaveIsAtomicAndReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "desktop-settings.json")
	store := OpenDesktop(path)
	settings := desktop.Defaults("windows")
	settings.ShowTrayIcon = false
	settings.ToggleWindowHotkey = "Ctrl+Shift+Space"
	if err := store.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loaded = desktop.Normalize(loaded, "windows")
	if loaded != settings {
		t.Fatalf("loaded = %+v, want %+v", loaded, settings)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
