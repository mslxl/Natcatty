package plugininstall_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/plugininstall"
)

// TestBuiltinStatePersistsOnlyDisabledOverrides proves the store defaults a
// new built-in to enabled, records an explicit disable across reopen, and
// removes the override on enable.
func TestBuiltinStatePersistsOnlyDisabledOverrides(t *testing.T) {
	root := t.TempDir()
	store := plugininstall.Open(root)
	ctx := context.Background()
	enabled, known, err := store.BuiltinEnabled(ctx, "acme.first-party")
	if err != nil || !enabled || known {
		t.Fatalf("initial built-in state = %v %v %v", enabled, known, err)
	}
	if err := store.SaveBuiltinEnabled(ctx, "acme.first-party", false); err != nil {
		t.Fatalf("SaveBuiltinEnabled(false): %v", err)
	}
	reopened := plugininstall.Open(root)
	enabled, known, err = reopened.BuiltinEnabled(ctx, "acme.first-party")
	if err != nil || enabled || !known {
		t.Fatalf("persisted disabled state = %v %v %v", enabled, known, err)
	}
	if err := reopened.SaveBuiltinEnabled(ctx, "acme.first-party", true); err != nil {
		t.Fatalf("SaveBuiltinEnabled(true): %v", err)
	}
	enabled, known, err = reopened.BuiltinEnabled(ctx, "acme.first-party")
	if err != nil || !enabled || known {
		t.Fatalf("cleared built-in state = %v %v %v", enabled, known, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "plugins", plugininstall.BuiltinStateFile))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if strings.Contains(string(data), "acme.first-party") {
		t.Fatalf("an enabled built-in is still recorded: %s", data)
	}
}

// TestBuiltinStateSortsAndRejectsUnknownIDs covers deterministic output for
// the diagnostics diff and the same id validation as the package coordinates.
func TestBuiltinStateSortsAndRejectsUnknownIDs(t *testing.T) {
	root := t.TempDir()
	store := plugininstall.Open(root)
	ctx := context.Background()
	for _, id := range []string{"zeta.plugin", "alpha.plugin"} {
		if err := store.SaveBuiltinEnabled(ctx, id, false); err != nil {
			t.Fatalf("SaveBuiltinEnabled(%s): %v", id, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "plugins", plugininstall.BuiltinStateFile))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	alpha := strings.Index(string(data), "alpha.plugin")
	zeta := strings.Index(string(data), "zeta.plugin")
	if alpha < 0 || zeta < 0 || alpha > zeta {
		t.Fatalf("disabled ids are not sorted: %s", data)
	}
	if _, _, err := store.BuiltinEnabled(ctx, "not-an-id"); err == nil {
		t.Fatal("BuiltinEnabled accepted an invalid id")
	}
	if err := store.SaveBuiltinEnabled(ctx, "not-an-id", false); err == nil {
		t.Fatal("SaveBuiltinEnabled accepted an invalid id")
	}
}

// TestBuiltinStateReportsACorruptDocument proves a damaged file is an error,
// not a silent reset that could re-enable a user-disabled plugin.
func TestBuiltinStateReportsACorruptDocument(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "plugins", plugininstall.BuiltinStateFile)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := plugininstall.Open(root)
	ctx := context.Background()
	if _, _, err := store.BuiltinEnabled(ctx, "acme.first-party"); err == nil {
		t.Fatal("BuiltinEnabled ignored a corrupt state file")
	}
	if err := store.SaveBuiltinEnabled(ctx, "acme.first-party", false); err == nil {
		t.Fatal("SaveBuiltinEnabled ignored a corrupt state file")
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", plugininstall.BuiltinStateFile)); err != nil {
		t.Fatalf("state file was removed: %v", err)
	}
}
