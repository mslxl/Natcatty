package pluginsettings

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripsScalarsAndCreatesMissingAsEmpty(t *testing.T) {
	root := t.TempDir()
	store := Open(func(id string) string { return filepath.Join(root, id) })
	values, err := store.Load(context.Background(), "acme.settings")
	if err != nil || len(values) != 0 {
		t.Fatalf("Load missing = %v, %v", values, err)
	}
	want := map[string]any{
		"mode":    "slow",
		"retries": int64(5),
		"ratio":   0.25,
		"enabled": true,
	}
	if err := store.Save(context.Background(), "acme.settings", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(root, "acme.settings", FileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat settings: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
	got, err := store.Load(context.Background(), "acme.settings")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %#v, want %#v", key, got[key], value)
		}
	}

	// Replacing values leaves no stale key behind.
	if err := store.Save(context.Background(), "acme.settings",
		map[string]any{"mode": "fast"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, err = store.Load(context.Background(), "acme.settings")
	if err != nil || len(got) != 1 || got["mode"] != "fast" {
		t.Fatalf("Load after replace = %v, %v", got, err)
	}
}

func TestStoreRejectsUnsafePluginIDs(t *testing.T) {
	root := t.TempDir()
	store := Open(func(id string) string { return filepath.Join(root, id) })
	if _, err := store.Load(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe id was accepted")
	}
	if err := store.Save(context.Background(), "Acme..Bad", map[string]any{}); err == nil {
		t.Fatal("unsafe id was accepted by Save")
	}
}
