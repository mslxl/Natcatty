package browser

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

type directConnector struct {
	connector fakeConnector
}

func (c *directConnector) Connect(ctx context.Context, hostID model.HostID) (Tunnel, error) {
	return &testTunnel{}, nil
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(testPNGDataURL, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func startPolishPanel(t *testing.T, cfg Config) (*Panel, <-chan State) {
	t.Helper()
	cfg.HostID = "host-a"
	cfg.Label = "web-1"
	cfg.Connector = &directConnector{}
	panel := New(cfg)
	updates := subscribePanel(t, panel)
	t.Cleanup(panel.Close)
	panel.Start()
	waitState(t, updates, "the panel route", func(state State) bool {
		return state.Route == RouteConnected
	})
	return panel, updates
}

func waitDownload(t *testing.T, panel *Panel, want DownloadStatus) Download {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		items := panel.Downloads()
		if len(items) > 0 && items[0].Status == want {
			return items[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("download never reached %q: %+v", want, items)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPanelFetchesAndCachesFaviconThroughGateway(t *testing.T) {
	png := testPNGBytes(t)
	var faviconCalls atomic.Int32
	var cookieOK atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/favicon.ico":
			faviconCalls.Add(1)
			if r.Header.Get("Cookie") == "session=abc" {
				cookieOK.Store(true)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		case "/page":
			_, _ = w.Write([]byte("<title>Console</title><p>page</p>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	icons, err := NewSiteIcons(&memoryIconStore{})
	if err != nil {
		t.Fatal(err)
	}
	panel, updates := startPolishPanel(t, Config{
		Icons:   icons,
		Cookies: func(string) string { return "session=abc" },
	})
	tabID := panel.State().ActiveID
	gatewayURL, err := panel.Navigate(tabID, origin.URL+"/page")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	panel.PageChanged(tabID, PageState{URL: gatewayURL, Title: "Console", Loading: false})
	state := waitState(t, updates, "the cached favicon", func(state State) bool {
		return state.Tabs[0].IconDataURL == testPNGDataURL
	})
	if state.Tabs[0].IconDataURL != testPNGDataURL {
		t.Fatalf("tab icon = %q", state.Tabs[0].IconDataURL)
	}
	if !cookieOK.Load() {
		t.Fatal("favicon request did not carry the WebView cookie")
	}
	if faviconCalls.Load() != 1 {
		t.Fatalf("favicon calls = %d, want 1", faviconCalls.Load())
	}
	if _, ok := icons.Get("host-a", origin.URL+"/page"); !ok {
		t.Fatal("icon not stored under the Host and origin")
	}

	second, err := panel.NewTab()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Navigate(second, origin.URL+"/second"); err != nil {
		t.Fatal(err)
	}
	state = panel.State()
	for _, tab := range state.Tabs {
		if tab.ID == second && tab.IconDataURL != testPNGDataURL {
			t.Fatalf("cached icon missing on second tab: %+v", tab)
		}
	}
	if faviconCalls.Load() != 1 {
		t.Fatalf("cached favicon refetched: %d calls", faviconCalls.Load())
	}
}

func TestPanelFaviconFailureLeavesFallback(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	defer origin.Close()
	icons, _ := NewSiteIcons(&memoryIconStore{})
	panel, updates := startPolishPanel(t, Config{Icons: icons})
	tabID := panel.State().ActiveID
	gatewayURL, err := panel.Navigate(tabID, origin.URL+"/page")
	if err != nil {
		t.Fatal(err)
	}
	panel.PageChanged(tabID, PageState{URL: gatewayURL, Title: "No icon", Loading: false})
	waitState(t, updates, "the completed favicon attempt", func(state State) bool {
		return calls.Load() > 0
	})
	state := panel.State()
	if state.Tabs[0].IconDataURL != "" {
		t.Fatalf("failed favicon produced data: %q", state.Tabs[0].IconDataURL)
	}
	if _, ok := icons.Get("host-a", origin.URL+"/page"); ok {
		t.Fatal("failed favicon was cached")
	}
}

func TestPanelDownloadUsesGatewayAndWritesExactBytes(t *testing.T) {
	payload := []byte(strings.Repeat("download-bytes-", 512))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/image.bin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer origin.Close()
	dir := t.TempDir()
	panel, _ := startPolishPanel(t, Config{
		Downloads: DownloadOptions{Dir: func() (string, error) { return dir, nil }},
	})
	tabID := panel.State().ActiveID
	gatewayURL, err := panel.Navigate(tabID, origin.URL+"/files/image.bin")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	item, err := panel.StartDownload(gatewayURL, "image.bin")
	if err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	final := waitDownload(t, panel, DownloadCompleted)
	if final.ID != item.ID || final.Received != int64(len(payload)) {
		t.Fatalf("download = %+v, want %d bytes", final, len(payload))
	}
	if !strings.HasPrefix(final.URL, origin.URL) {
		t.Fatalf("recorded URL = %q, want remote address", final.URL)
	}
	if filepath.Dir(final.SavePath) != filepath.Clean(dir) {
		t.Fatalf("SavePath %q escaped %q", final.SavePath, dir)
	}
	got, err := os.ReadFile(final.SavePath)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(final.SavePath + ".part"); !os.IsNotExist(err) {
		t.Fatalf("part marker remains: %v", err)
	}
	if _, err := panel.StartDownload(origin.URL+"/files/direct.bin", "direct.bin"); err != ErrDownloadURL {
		t.Fatalf("direct URL error = %v, want ErrDownloadURL", err)
	}
}

func TestPanelDownloadCancelFromPanel(t *testing.T) {
	started := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("first"))
		flusher.Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer origin.Close()
	dir := t.TempDir()
	panel, _ := startPolishPanel(t, Config{
		Downloads: DownloadOptions{Dir: func() (string, error) { return dir, nil }},
	})
	tabID := panel.State().ActiveID
	gatewayURL, err := panel.Navigate(tabID, origin.URL+"/slow.bin")
	if err != nil {
		t.Fatal(err)
	}
	item, err := panel.StartDownload(gatewayURL, "slow.bin")
	if err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download never started")
	}
	if !panel.CancelDownload(item.ID) {
		t.Fatal("CancelDownload = false")
	}
	final := waitDownload(t, panel, DownloadCancelled)
	if final.Failure != DownloadFailureNone {
		t.Fatalf("cancel failure = %q", final.Failure)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("cancelled download left files: %v", entries)
	}
}

func TestPanelBookmarksPerHostAndAllHosts(t *testing.T) {
	bookmarks, err := NewBookmarks(&memoryBookmarkStore{}, time.Now, func() string { return "generated" })
	if err != nil {
		t.Fatal(err)
	}
	panel, _ := startPolishPanel(t, Config{Bookmarks: bookmarks})
	if _, err := panel.UpsertBookmark(domainbrowser.BookmarkInput{
		URL: "http://127.0.0.1:8080/", Scope: domainbrowser.ScopeHost, Title: "Console",
	}); err != nil {
		t.Fatalf("host bookmark: %v", err)
	}
	if _, err := panel.UpsertBookmark(domainbrowser.BookmarkInput{
		URL: "https://shared.example/", Scope: domainbrowser.ScopeAllHosts, Title: "Shared",
	}); err != nil {
		t.Fatalf("all-Host bookmark: %v", err)
	}
	views := panel.VisibleBookmarks()
	if len(views) != 2 || views[0].HostID != "host-a" || views[1].HostID != "" {
		t.Fatalf("bookmarks = %+v", views)
	}
	if removed, err := panel.RemoveBookmark(views[0].ID); err != nil || !removed {
		t.Fatalf("RemoveBookmark = %v, %v", removed, err)
	}
	if len(panel.VisibleBookmarks()) != 1 {
		t.Fatal("bookmark removal not reflected")
	}
}

func TestGatewaySandboxPolicyAndUntrustedLoopbackAddress(t *testing.T) {
	gateway := newGateway(&testTunnel{})
	defer gateway.Close()
	origin := "https://host.internal:8443"
	gatewayURL, err := gateway.URLFor(origin + "/")
	if err != nil {
		t.Fatalf("URLFor: %v", err)
	}
	parsed, _ := http.NewRequest("GET", gatewayURL, nil)
	host := parsed.URL.Hostname()
	if !IsGatewayHost(host) {
		t.Fatalf("gateway host = %q, want a loopback address a gateway binds", host)
	}
	// The preferred address must stay outside MyGo's development trust set.
	// A platform that cannot bind it (macOS assigns only 127.0.0.1) uses the
	// documented fallback, which is inside that set; the sandbox relies on
	// the app registering no MyGo bindings there.
	if GatewayHostIsDevTrusted(GatewayLoopbackHost) {
		t.Fatalf("preferred gateway host %q is inside MyGo's dev-trusted set", GatewayLoopbackHost)
	}
	if !GatewayHostIsDevTrusted(GatewayFallbackLoopbackHost) {
		t.Fatalf("fallback gateway host %q is not the documented dev-trusted fallback",
			GatewayFallbackLoopbackHost)
	}
	if gatewayPageCSP == "" || !strings.Contains(gatewayPageCSP, "connect-src 'self'") ||
		!strings.Contains(gatewayPageCSP, "object-src 'none'") ||
		!strings.Contains(gatewayPageCSP, "frame-src 'self'") ||
		!strings.Contains(gatewayPageCSP, "form-action 'self'") {
		t.Fatalf("CSP does not confine the page: %s", gatewayPageCSP)
	}
	if strings.Contains(gatewayPageCSP, "unsafe-eval") {
		t.Fatal("CSP allows eval")
	}
}
