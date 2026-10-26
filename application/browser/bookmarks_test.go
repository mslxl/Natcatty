package browser

import (
	"errors"
	"reflect"
	"testing"
	"time"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

type memoryBookmarkStore struct {
	items   []domainbrowser.Bookmark
	saves   int
	failOn  int
	loadErr error
}

func (m *memoryBookmarkStore) Load() ([]domainbrowser.Bookmark, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return append([]domainbrowser.Bookmark(nil), m.items...), nil
}

func (m *memoryBookmarkStore) Save(items []domainbrowser.Bookmark) error {
	m.saves++
	if m.failOn > 0 && m.saves >= m.failOn {
		return errors.New("disk full")
	}
	m.items = append([]domainbrowser.Bookmark(nil), items...)
	return nil
}

func newTestBookmarks(t *testing.T, store BookmarkStore) *Bookmarks {
	t.Helper()
	now := time.UnixMilli(1_000)
	next := 0
	service, err := NewBookmarks(store, func() time.Time { return now }, func() string {
		next++
		return "generated-" + string(rune('a'+next-1))
	})
	if err != nil {
		t.Fatalf("NewBookmarks: %v", err)
	}
	return service
}

func TestBookmarkStoreReadErrorBlocksSaving(t *testing.T) {
	store := &memoryBookmarkStore{loadErr: errors.New("access denied")}
	service := newTestBookmarks(t, store)
	if _, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://example.com/", Scope: domainbrowser.ScopeHost, Title: "Example",
	}); err == nil {
		t.Fatal("Upsert overwrote a bookmark file that could not be read")
	}
	if store.saves != 0 {
		t.Fatalf("Save calls = %d, want 0", store.saves)
	}
}

func TestBookmarksHostAndAllHostScopes(t *testing.T) {
	store := &memoryBookmarkStore{}
	service := newTestBookmarks(t, store)

	hostA, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "http://127.0.0.1:8080/", Scope: domainbrowser.ScopeHost, Title: "Console",
	})
	if err != nil {
		t.Fatalf("Upsert host: %v", err)
	}
	if hostA.HostID != "host-a" {
		t.Fatalf("host scoped HostID = %q, want host-a", hostA.HostID)
	}
	shared, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://status.example/", Scope: domainbrowser.ScopeAllHosts, Title: "Status",
	})
	if err != nil {
		t.Fatalf("Upsert all hosts: %v", err)
	}
	if shared.HostID != "" {
		t.Fatalf("all-Host HostID = %q, want empty", shared.HostID)
	}
	if _, err := service.Upsert("host-b", domainbrowser.BookmarkInput{
		URL: "https://b.example/", Scope: domainbrowser.ScopeHost, Title: "B",
	}); err != nil {
		t.Fatalf("Upsert host-b: %v", err)
	}

	listA := service.List("host-a")
	if len(listA) != 2 || listA[0].ID != hostA.ID || listA[1].ID != shared.ID {
		t.Fatalf("List(host-a) = %+v, want host then shared", listA)
	}
	listB := service.List("host-b")
	if len(listB) != 2 || listB[0].ID != shared.ID || listB[1].HostID != "host-b" {
		t.Fatalf("List(host-b) = %+v, want shared plus own", listB)
	}
	if len(service.All()) != 3 {
		t.Fatalf("All = %d, want 3", len(service.All()))
	}
	if store.saves != 3 {
		t.Fatalf("saves = %d, want 3", store.saves)
	}
}

func TestBookmarksEditAndOwnership(t *testing.T) {
	store := &memoryBookmarkStore{}
	service := newTestBookmarks(t, store)
	hostA, _ := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://a.example/", Scope: domainbrowser.ScopeHost, Title: "A",
	})
	shared, _ := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://shared.example/", Scope: domainbrowser.ScopeAllHosts, Title: "Shared",
	})

	edited, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		ID: hostA.ID, URL: "https://a.example/new", Scope: domainbrowser.ScopeHost, Title: "A2",
	})
	if err != nil {
		t.Fatalf("edit own: %v", err)
	}
	if edited.Title != "A2" || edited.CreatedAt != hostA.CreatedAt {
		t.Fatalf("edited = %+v", edited)
	}
	// A different Host may edit an all-Host bookmark.
	if _, err := service.Upsert("host-b", domainbrowser.BookmarkInput{
		ID: shared.ID, URL: shared.URL, Scope: domainbrowser.ScopeAllHosts, Title: "Shared 2",
	}); err != nil {
		t.Fatalf("edit all-Hosts from host-b: %v", err)
	}
	// It may not edit another Host's scoped bookmark, even while naming its own.
	if _, err := service.Upsert("host-b", domainbrowser.BookmarkInput{
		ID: hostA.ID, URL: hostA.URL, Scope: domainbrowser.ScopeHost, Title: "stolen",
	}); !errors.Is(err, domainbrowser.ErrBookmarkOwner) {
		t.Fatalf("foreign edit error = %v, want owner refusal", err)
	}
	if removed, err := service.Remove("host-b", hostA.ID); err != nil || removed {
		t.Fatalf("foreign remove = %v, %v; want false, nil", removed, err)
	}
	if removed, err := service.Remove("host-a", hostA.ID); err != nil || !removed {
		t.Fatalf("own remove = %v, %v; want true, nil", removed, err)
	}
	if store.saves != 5 {
		t.Fatalf("saves = %d, want 5", store.saves)
	}
}

func TestBookmarksRollbackOnStoreFailure(t *testing.T) {
	store := &memoryBookmarkStore{}
	service := newTestBookmarks(t, store)
	first, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://a.example/", Scope: domainbrowser.ScopeAllHosts,
	})
	if err != nil {
		t.Fatalf("initial add: %v", err)
	}
	store.failOn = store.saves + 1
	if _, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://b.example/", Scope: domainbrowser.ScopeAllHosts,
	}); err == nil {
		t.Fatal("expected save failure")
	}
	if got := service.All(); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("state changed after failed save: %+v", got)
	}
}

func TestBookmarksLoadSkipsMalformedAndEnforcesLimit(t *testing.T) {
	store := &memoryBookmarkStore{items: []domainbrowser.Bookmark{
		{ID: "ok", Title: "OK", URL: "https://ok.example/", Scope: domainbrowser.ScopeAllHosts},
		{ID: "bad", URL: "ftp://bad.example/", Scope: domainbrowser.ScopeAllHosts},
	}}
	service := newTestBookmarks(t, store)
	if got := service.All(); len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("loaded = %+v, want only valid", got)
	}
	store.items = make([]domainbrowser.Bookmark, domainbrowser.MaxBookmarks)
	for i := range store.items {
		store.items[i] = domainbrowser.Bookmark{
			ID: string(rune(i + 1)), URL: "https://example.com/", Scope: domainbrowser.ScopeAllHosts,
		}
	}
	full := newTestBookmarks(t, store)
	if _, err := full.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://one-too-many.example/", Scope: domainbrowser.ScopeAllHosts,
	}); !errors.Is(err, domainbrowser.ErrBookmarkLimit) {
		t.Fatalf("limit error = %v, want ErrBookmarkLimit", err)
	}
}

func TestBookmarksPersistReload(t *testing.T) {
	store := &memoryBookmarkStore{}
	first := newTestBookmarks(t, store)
	if _, err := first.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://persisted.example/", Scope: domainbrowser.ScopeHost, Title: "Persisted",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	second := newTestBookmarks(t, store)
	want := first.List("host-a")
	got := second.List("host-a")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded list = %+v, want %+v", got, want)
	}
	if len(got) != 1 || got[0].HostID != model.HostID("host-a") {
		t.Fatalf("reloaded item lost host scope: %+v", got)
	}
}

func TestBookmarksObserveDisconnect(t *testing.T) {
	store := &memoryBookmarkStore{}
	service := newTestBookmarks(t, store)
	var observed int
	unsubscribe := service.Subscribe(func() { observed++ })
	if _, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://notify.example/", Scope: domainbrowser.ScopeAllHosts,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if observed != 1 {
		t.Fatalf("observed = %d, want 1", observed)
	}
	unsubscribe()
	if _, err := service.Upsert("host-a", domainbrowser.BookmarkInput{
		URL: "https://again.example/", Scope: domainbrowser.ScopeAllHosts,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if observed != 1 {
		t.Fatalf("observed after unsubscribe = %d, want 1", observed)
	}
}
