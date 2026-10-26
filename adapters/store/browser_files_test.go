package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
)

func TestBrowserBookmarkStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "browser-bookmarks.json")
	store := OpenBrowserBookmarks(path)
	items := []domainbrowser.Bookmark{
		{ID: "a", Title: "Console", URL: "http://127.0.0.1/", Scope: domainbrowser.ScopeHost, HostID: "host-a", Icon: "C", IconMode: domainbrowser.IconModeAuto, Color: "#247969", Order: 1},
		{ID: "b", Title: "Shared", URL: "https://shared.example/", Scope: domainbrowser.ScopeAllHosts, Icon: "S", IconMode: domainbrowser.IconModeAuto, Color: "#247969", Order: 2},
	}
	if err := store.Save(items); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode = %o, want owner-only", info.Mode().Perm())
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].HostID != "" {
		t.Fatalf("loaded = %+v", got)
	}
}

func TestBrowserBookmarkStoreMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	store := OpenBrowserBookmarks(filepath.Join(dir, "missing.json"))
	got, err := store.Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("missing Load = %v, %v", got, err)
	}
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = OpenBrowserBookmarks(path).Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("malformed Load = %v, %v; want empty", got, err)
	}
}

func TestBrowserSiteIconStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "browser-site-icons.json")
	store := OpenBrowserSiteIcons(path)
	items := []domainbrowser.SiteIcon{
		{HostID: "host-a", Origin: "https://example.com", Mode: domainbrowser.IconModeAuto, DataURL: "data:image/png;base64,AAAA", UpdatedAt: 5},
	}
	if err := store.Save(items); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].HostID != "host-a" || got[0].Origin != "https://example.com" {
		t.Fatalf("loaded = %+v", got)
	}
}
