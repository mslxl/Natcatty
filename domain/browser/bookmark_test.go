package browser

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBookmarkDefaultsAndScopes(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	got, err := NormalizeBookmark(BookmarkInput{
		ID:    "bookmark-1",
		URL:   "https://Example.COM:8443/console",
		Scope: ScopeAllHosts,
	}, nil, now, func() string { return "generated" })
	if err != nil {
		t.Fatalf("NormalizeBookmark: %v", err)
	}
	if got.URL != "https://example.com:8443/console" {
		t.Fatalf("URL = %q, want normalized HTTPS address", got.URL)
	}
	if got.HostID != "" {
		t.Fatalf("all-Host bookmark HostID = %q, want empty", got.HostID)
	}
	if got.Title != "example.com" {
		t.Fatalf("Title = %q, want hostname fallback", got.Title)
	}
	if got.Icon != "e" {
		t.Fatalf("Icon = %q, want first title rune", got.Icon)
	}
	if got.Color != DefaultBookmarkColor {
		t.Fatalf("Color = %q, want %q", got.Color, DefaultBookmarkColor)
	}
	if got.IconMode != IconModeAuto {
		t.Fatalf("IconMode = %q, want auto", got.IconMode)
	}
	if got.CreatedAt != now.UnixMilli() || got.UpdatedAt != now.UnixMilli() {
		t.Fatalf("timestamps = %d/%d, want %d", got.CreatedAt, got.UpdatedAt, now.UnixMilli())
	}
	if got.ID != "bookmark-1" {
		t.Fatalf("ID = %q, want preserved bookmark-1", got.ID)
	}

	host, err := NormalizeBookmark(BookmarkInput{
		URL:    "http://127.0.0.1:9090/",
		Scope:  ScopeHost,
		HostID: "host-a",
		Title:  "  Local console  ",
		Icon:   "  🖥️  ",
		Color:  "#ABCDEF",
	}, nil, now, func() string { return "generated" })
	if err != nil {
		t.Fatalf("NormalizeBookmark host: %v", err)
	}
	if host.HostID != "host-a" {
		t.Fatalf("host bookmark HostID = %q", host.HostID)
	}
	if host.Title != "Local console" {
		t.Fatalf("Title = %q, want trimmed", host.Title)
	}
	if host.Icon != "🖥️" {
		t.Fatalf("Icon = %q, want trimmed four-rune icon", host.Icon)
	}
	if host.Color != "#ABCDEF" {
		t.Fatalf("Color = %q, want preserved valid color", host.Color)
	}
}

func TestNormalizeBookmarkRejectsInvalidValues(t *testing.T) {
	now := time.UnixMilli(10)
	nextID := func() string { return "generated" }
	cases := []struct {
		name string
		in   BookmarkInput
		want error
	}{
		{"empty url", BookmarkInput{Scope: ScopeAllHosts}, ErrAddressEmpty},
		{"ftp", BookmarkInput{URL: "ftp://example.com/file", Scope: ScopeAllHosts}, ErrAddressScheme},
		{"credentials", BookmarkInput{URL: "https://user:" + "pw" + "@example.com/", Scope: ScopeAllHosts}, ErrAddressCredentials},
		{"host scope without host", BookmarkInput{URL: "https://example.com/", Scope: ScopeHost}, ErrBookmarkHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeBookmark(tc.in, nil, now, nextID)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNormalizeBookmarkCapsTitleIconAndPreservesIdentity(t *testing.T) {
	previous := &Bookmark{
		ID:        "fixed",
		Scope:     ScopeHost,
		HostID:    "host-a",
		Order:     7,
		CreatedAt: 100,
		IconMode:  IconModeCustom,
		IconDataURL: "data:image/png;base64," +
			"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAFgwJ/lZ1cvwAAAABJRU5ErkJggg==",
	}
	got, err := NormalizeBookmark(BookmarkInput{
		ID: "fixed", URL: "https://example.com/", Scope: ScopeHost, HostID: "host-a",
		Title: strings.Repeat("t", 400), Icon: "abcdEFGH", IconMode: IconModeCustom,
	}, previous, time.UnixMilli(50), func() string { return "other" })
	if err != nil {
		t.Fatalf("NormalizeBookmark: %v", err)
	}
	if len([]rune(got.Title)) != MaxBookmarkTitle {
		t.Fatalf("title length = %d, want %d", len([]rune(got.Title)), MaxBookmarkTitle)
	}
	if got.Icon != "abcd" {
		t.Fatalf("icon = %q, want four runes", got.Icon)
	}
	if got.ID != "fixed" || got.Order != 7 || got.CreatedAt != 100 {
		t.Fatalf("identity changed: %+v", got)
	}
	if got.UpdatedAt != 50 {
		t.Fatalf("UpdatedAt = %d, want now", got.UpdatedAt)
	}
	if got.IconDataURL != previous.IconDataURL {
		t.Fatalf("custom icon data lost")
	}
}

func TestBookmarkListRules(t *testing.T) {
	all := Bookmark{ID: "all", Scope: ScopeAllHosts, Order: 2, Title: "Shared", URL: "https://shared.example/"}
	host := Bookmark{ID: "host", Scope: ScopeHost, HostID: "host-a", Order: 1, Title: "Console", URL: "http://127.0.0.1/"}
	foreign := Bookmark{ID: "foreign", Scope: ScopeHost, HostID: "host-b", Order: 3, Title: "Other", URL: "https://other.example/"}
	got := VisibleBookmarks([]Bookmark{all, host, foreign}, "host-a")
	if len(got) != 2 || got[0].ID != "host" || got[1].ID != "all" {
		t.Fatalf("VisibleBookmarks = %+v, want host then all-hosts by order", got)
	}
}

func TestValidImageDataURL(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAFgwJ/lZ1cvwAAAABJRU5ErkJggg=="
	if !ValidImageDataURL(png) {
		t.Fatal("known 1x1 PNG data URL rejected")
	}
	for _, raw := range []string{"http://example.com/a.png", "data:text/html;base64,PGgxPg==", "data:image/png;base64,!!!", ""} {
		if ValidImageDataURL(raw) {
			t.Fatalf("ValidImageDataURL(%q) = true", raw)
		}
	}
	oversized := "data:image/png;base64," + strings.Repeat("A", MaxIconBytes*2)
	if ValidImageDataURL(oversized) {
		t.Fatal("oversized image accepted")
	}
}
