package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/autocomplete"
)

func TestCommandHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "command-history.json")
	store := OpenCommandHistory(path)
	want := []autocomplete.Entry{
		{Command: "git status", Scope: "local", Count: 3,
			LastUsed: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)},
		{Command: "ls -la", Scope: "host:abc", Count: 1,
			LastUsed: time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)},
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded = %+v, want %+v", got, want)
	}
}

func TestCommandHistoryMissingFileIsEmpty(t *testing.T) {
	store := OpenCommandHistory(filepath.Join(t.TempDir(), "missing.json"))
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("loaded = %+v, want no entries", got)
	}
}

func TestCommandHistoryMalformedFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "command-history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := OpenCommandHistory(path).Load(context.Background())
	if err != nil {
		t.Fatalf("malformed history must not fail loading: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("loaded = %+v, want no entries", got)
	}
}

func TestCommandHistoryHonorsContextCancellation(t *testing.T) {
	store := OpenCommandHistory(filepath.Join(t.TempDir(), "command-history.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, nil); err == nil {
		t.Fatal("save with a cancelled context succeeded")
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("load with a cancelled context succeeded")
	}
}

func TestCommandHistorySaveLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	store := OpenCommandHistory(filepath.Join(dir, "command-history.json"))
	if err := store.Save(context.Background(), []autocomplete.Entry{
		{Command: "pwd", Scope: "local", Count: 1},
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "command-history.json" {
		t.Fatalf("directory entries = %v", entries)
	}
}
