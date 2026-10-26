package browser

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
)

type polishBookmarkStore struct {
	mu    sync.Mutex
	items []domainbrowser.Bookmark
}

func (s *polishBookmarkStore) Load() ([]domainbrowser.Bookmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domainbrowser.Bookmark(nil), s.items...), nil
}

func (s *polishBookmarkStore) Save(items []domainbrowser.Bookmark) error {
	s.mu.Lock()
	s.items = append([]domainbrowser.Bookmark(nil), items...)
	s.mu.Unlock()
	return nil
}

type polishIconStore struct {
	mu    sync.Mutex
	items []domainbrowser.SiteIcon
}

func (s *polishIconStore) Load() ([]domainbrowser.SiteIcon, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domainbrowser.SiteIcon(nil), s.items...), nil
}

func (s *polishIconStore) Save(items []domainbrowser.SiteIcon) error {
	s.mu.Lock()
	s.items = append([]domainbrowser.SiteIcon(nil), items...)
	s.mu.Unlock()
	return nil
}

type polishTunnel struct{}

func (polishTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}
func (polishTunnel) Done() <-chan struct{} { return nil }
func (polishTunnel) Discard()              {}

type polishConnector struct{}

func (polishConnector) Connect(context.Context, model.HostID) (appbrowser.Tunnel, error) {
	return polishTunnel{}, nil
}

func newPolishHarness(t *testing.T, width, height int, bookmarks *appbrowser.Bookmarks,
	icons *appbrowser.SiteIcons, downloads appbrowser.DownloadOptions) *harness {
	t.Helper()
	connector := &fakeConnector{}
	pages := &fakePages{}
	core := appbrowser.New(appbrowser.Config{
		HostID: "host-1", Label: "web-1",
		Connector: polishConnector{}, Bookmarks: bookmarks, Icons: icons, Downloads: downloads,
	})
	h := &harness{
		core: core, connector: connector, pages: pages,
		address: locales.In("en").BrowserAddress(),
	}
	panel := New(core, pages, WithRedraw(func() { h.redraws++ }))
	t.Cleanup(panel.Close)
	h.panel = panel
	h.tester = ui.NewTester(func(c *ui.Context) {
		h.ctx = c
		panel.View(c)
	}, width, height)
	h.tester.SetLocale("en")
	h.tester.Frame()
	waitFor(t, "the panel route", func() bool {
		return core.State().Route == appbrowser.RouteConnected
	})
	h.frame()
	return h
}

func TestBookmarkPaneReachableAtBothSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{440, 380},
		{868, 688},
	} {
		t.Run(strings.ReplaceAll(strings.Join([]string{
			locales.In("en").BrowserBookmarks(),
			itoa(size.width), itoa(size.height),
		}, "-"), " ", "_"), func(t *testing.T) {
			next := 0
			bookmarks, _ := appbrowser.NewBookmarks(&polishBookmarkStore{}, time.Now, func() string {
				next++
				return "id-" + itoa(next)
			})
			if _, err := bookmarks.Upsert("host-1", domainbrowser.BookmarkInput{
				URL: "http://127.0.0.1:8080/", Scope: domainbrowser.ScopeHost, Title: "Console",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := bookmarks.Upsert("host-1", domainbrowser.BookmarkInput{
				URL: "https://shared.example/", Scope: domainbrowser.ScopeAllHosts, Title: "Shared",
			}); err != nil {
				t.Fatal(err)
			}
			h := newPolishHarness(t, size.width, size.height, bookmarks, nil, appbrowser.DownloadOptions{})
			if err := h.tester.Click(locales.In("en").BrowserBookmarks()); err != nil {
				t.Fatalf("click Bookmarks: %v", err)
			}
			h.frame()
			if !h.tester.HasText("Console") || !h.tester.HasText("Shared") {
				t.Fatalf("bookmark pane missing rows: %q", h.tester.Texts())
			}
			if !h.tester.HasText("All Hosts · https://shared.example/") {
				t.Fatalf("all-Host bookmark has no scope marker: %q", h.tester.Texts())
			}
			// Open the editor and prove every control is reachable.
			if err := h.tester.Click(locales.In("en").BrowserBookmarkAdd()); err != nil {
				t.Fatalf("click Add bookmark: %v", err)
			}
			h.frame()
			for _, label := range []string{"Title", "Address", "This Host", "All Hosts", "Save", "Cancel"} {
				if _, ok := h.tester.Find(label); !ok {
					t.Fatalf("editor missing %q at %dx%d: %q", label, size.width, size.height, h.tester.Texts())
				}
			}
		})
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

func TestBookmarkPaneAddEditDelete(t *testing.T) {
	bookmarks, _ := appbrowser.NewBookmarks(&polishBookmarkStore{}, time.Now, func() string { return "generated-id" })
	h := newPolishHarness(t, 868, 688, bookmarks, nil, appbrowser.DownloadOptions{})
	if err := h.tester.Click("Bookmarks"); err != nil {
		t.Fatal(err)
	}
	h.frame()
	if !h.tester.HasText("No bookmarks on this Host yet.") {
		t.Fatalf("empty state missing: %q", h.tester.Texts())
	}
	if err := h.tester.Click("Add bookmark"); err != nil {
		t.Fatal(err)
	}
	h.frame()
	focusField(t, h, "Title")
	h.tester.Type("Console")
	h.frame()
	focusField(t, h, "Address")
	h.tester.Type("http://127.0.0.1:8080/")
	if err := h.tester.Click("This Host"); err != nil {
		t.Fatalf("choose scope: %v", err)
	}
	if err := h.tester.Click("Save"); err != nil {
		t.Fatalf("save: %v", err)
	}
	h.frame()
	if !h.tester.HasText("Console") {
		t.Fatalf("saved bookmark missing: %q", h.tester.Texts())
	}
	if got := h.panel.core.VisibleBookmarks(); len(got) != 1 || got[0].HostID != "host-1" {
		t.Fatalf("saved bookmarks = %+v", got)
	}
	if err := h.tester.Click("Edit"); err != nil {
		t.Fatal(err)
	}
	h.frame()
	if err := h.tester.Click("Delete"); err != nil {
		t.Fatalf("delete from editor: %v", err)
	}
	h.frame()
	if len(h.panel.core.VisibleBookmarks()) != 0 {
		t.Fatal("delete did not remove the bookmark")
	}
}

func TestBookmarkPaneLongListScrolls(t *testing.T) {
	store := &polishBookmarkStore{}
	next := 0
	bookmarks, _ := appbrowser.NewBookmarks(store, time.Now, func() string {
		next++
		return "generated-" + itoa(next)
	})
	for i := 0; i < 60; i++ {
		if _, err := bookmarks.Upsert("host-1", domainbrowser.BookmarkInput{
			URL: "https://host.example/" + itoa(i), Scope: domainbrowser.ScopeAllHosts,
			Title: "Bookmark " + itoa(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	h := newPolishHarness(t, 440, 380, bookmarks, nil, appbrowser.DownloadOptions{})
	if err := h.tester.Click("Bookmarks"); err != nil {
		t.Fatal(err)
	}
	h.frame()
	first, ok := h.tester.Find("Bookmark 0")
	if !ok {
		t.Fatalf("first bookmark outside the visible pane: %q", h.tester.Texts())
	}
	if first.W <= 0 || first.H <= 0 {
		t.Fatalf("first bookmark has no box: %+v", first)
	}
	if _, ok := h.tester.Find("Add bookmark"); !ok {
		t.Fatal("Add bookmark scrolled away with the list")
	}
}

func focusField(t *testing.T, h *harness, label string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		if h.tester.Focused(label) {
			return
		}
		h.tester.Key(0, ui.KeyTab)
		h.frame()
	}
	t.Fatalf("%q never took focus; texts %q", label, h.tester.Texts())
}

func TestDownloadPaneShowsProgressAndCompletion(t *testing.T) {
	payload := []byte(strings.Repeat("download-through-gateway-", 2048))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer origin.Close()
	dir := t.TempDir()
	h := newPolishHarness(t, 868, 688, nil, nil, appbrowser.DownloadOptions{
		Dir: func() (string, error) { return dir, nil },
	})
	tab := h.activeTab(t)
	gatewayURL, err := h.core.Navigate(tab.ID, origin.URL+"/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	item, err := h.core.StartDownload(gatewayURL, "file.bin")
	if err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	waitFor(t, "the download to complete", func() bool {
		got := h.core.Downloads()
		return len(got) > 0 && got[0].Status == appbrowser.DownloadCompleted
	})
	if err := h.tester.Click("Downloads"); err != nil {
		t.Fatal(err)
	}
	h.frame()
	if !h.tester.HasText("file.bin") || !h.tester.HasText("Completed") {
		t.Fatalf("download pane missing completion: %q", h.tester.Texts())
	}
	if item.Filename != "file.bin" {
		t.Fatalf("item = %+v", item)
	}
}
