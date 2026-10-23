package sftp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

func TestBookmarksRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sftp-bookmarks.json")
	repository := NewBookmarks(path, "")
	items := []domainsftp.Bookmark{
		{Label: "Home", Path: "/home/me"},
		{Label: "Test:/srv", Path: "/srv", HostID: "h1", HostLabel: "Test"},
	}
	if err := repository.SaveBookmarks(context.Background(), items); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := repository.LoadBookmarks(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 || loaded[0] != items[0] || loaded[1] != items[1] {
		t.Fatalf("round trip changed the list: %+v", loaded)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("saved bookmarks are %04o, want 0600", mode)
		}
	}
}

func TestLocalBookmarksRoundTripSeparately(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	localPath := filepath.Join(dir, "local.json")
	repository := NewBookmarks(globalPath, localPath)
	global := []domainsftp.Bookmark{{Label: "Test:/srv", Path: "/srv", HostID: "h1", HostLabel: "Test"}}
	local := []domainsftp.Bookmark{{Label: "/home/me", Path: "/home/me"}}
	if err := repository.SaveBookmarks(context.Background(), global); err != nil {
		t.Fatalf("save global: %v", err)
	}
	if err := repository.SaveLocalBookmarks(context.Background(), local); err != nil {
		t.Fatalf("save local: %v", err)
	}
	loadedGlobal, err := repository.LoadBookmarks(context.Background())
	if err != nil || len(loadedGlobal) != 1 || loadedGlobal[0] != global[0] {
		t.Fatalf("global = %+v, %v", loadedGlobal, err)
	}
	loadedLocal, err := repository.LoadLocalBookmarks(context.Background())
	if err != nil || len(loadedLocal) != 1 || loadedLocal[0] != local[0] {
		t.Fatalf("local = %+v, %v", loadedLocal, err)
	}
}

func TestBookmarksMissingFileIsEmpty(t *testing.T) {
	repository := NewBookmarks(filepath.Join(t.TempDir(), "missing.json"), "")
	loaded, err := repository.LoadBookmarks(context.Background())
	if err != nil {
		t.Fatalf("a missing file is not an error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("missing file returned %+v", loaded)
	}
}

func TestBookmarksMalformedFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte(`{"not":"a list"`), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := NewBookmarks(path, "")
	if _, err := repository.LoadBookmarks(context.Background()); err == nil {
		t.Fatal("a malformed file was accepted")
	}
}

func TestBookmarksSaveReplacesAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.json")
	repository := NewBookmarks(path, "")
	if err := repository.SaveBookmarks(context.Background(), []domainsftp.Bookmark{{Path: "/one"}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveBookmarks(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.LoadBookmarks(context.Background())
	if err != nil || len(loaded) != 0 {
		t.Fatalf("empty save left %+v, %v", loaded, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repository.SaveBookmarks(ctx, []domainsftp.Bookmark{{Path: "/two"}}); err == nil {
		t.Fatal("a cancelled save was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a cancelled save removed the existing file: %v", err)
	}
}
