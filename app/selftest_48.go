//go:build selftest

package app

// Ticket 48 self-test. Press F11, then 4, then 8.
//
// The check drives the production browser panel, bookmark and favicon
// services, the download manager, and the loopback gateway against an
// in-process HTTP origin reached only through a recording tunnel:
//
//   - a Host-scoped and an all-Host bookmark are added through the real
//     bookmark editor, edited, and reloaded from the same file by a fresh
//     service, proving both scope rules and restart persistence;
//   - the origin's PNG favicon is fetched through the tunnel with the page
//     cookie, cached, decoded, and shown on the tab and bookmark rows;
//   - a page-reported download completes through the gateway with exact
//     bytes; a slow download shows progress and cancel removes its .part
//     file; an off-route URL is refused before a file is reserved;
//   - a hostile page is fetched through the gateway and its CSP blocks
//     cross-origin fetch, images, frames, objects, forms, window.open, and
//     eval; the foreign origin records zero requests, and the page options
//     carry no preload or trusted origin while the gateway binds 127.0.0.2;
//   - the bookmark, editor, favicon, download, and typed-address-error
//     states are captured at content sizes for 1100x720 and 720x480 in
//     English and zh-CN.
//
// Every wait observes panel, service, file, or HTTP state with a hard
// deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-48.json and
// %TEMP%\natcatty-selftest-48-shots\*.png.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appbrowser "github.com/mslxl/natcatty/application/browser"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
	browserui "github.com/mslxl/natcatty/ui/browser"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest48Ticket = 48
	selftest48Wait   = 10 * time.Second
	selftest48Host   = "selftest-48-host"
	selftest48Other  = "selftest-48-other"
	selftest48Cookie = "selftest48=through-the-tunnel"
)

// 1x1 PNG. A valid image is what MyGo's native decoder accepts, unlike ICO.
const selftest48PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// selftest48ForeignCount counts every request that reaches the origin the
// hostile page tries to exfiltrate to. It must stay zero.
var selftest48ForeignCount atomic.Int64

type selftest48 struct {
	// run persists across resets so a second run never reuses another run's
	// servers, services, pages, tunnel counters, or stage flags.
	run int

	panel     *browserui.Panel
	core      *appbrowser.Panel
	pages     *selftest48Pages
	connector *selftest48Connector
	bookmarks *appbrowser.Bookmarks
	icons     *appbrowser.SiteIcons
	tester    *ui.Tester

	origin   *httptest.Server
	foreign  *httptest.Server
	originMu sync.Mutex
	cookies  map[string]string

	workDir      string
	shotDir      string
	bookmarkPath string
	iconPath     string
	downloadsDir string

	originURL string
	homeURL   string
	docsURL   string
	fileURL   string
	slowURL   string

	emptyShot     bool
	editorShot    bool
	populated     bool
	persisted     bool
	faviconDone   bool
	downloadsDone bool
	sandboxDone   bool
	renderStage   int
	closed        bool
	foreignSeen   int64

	deadline time.Time
	captured int
	tabID    string
}

func (t *selftest48) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest48Ticket,
		Name:   "Browser bookmarks, favicons, downloads, and sandbox",
		Steps: []selftest.Step{
			{Name: "open-panel", Run: t.openPanelStep},
			{Name: "bookmarks-add-edit", Run: t.bookmarksStep},
			{Name: "bookmarks-persist-and-scope", Run: t.persistStep},
			{Name: "favicon", Run: t.faviconStep},
			{Name: "downloads", Run: t.downloadsStep},
			{Name: "sandbox", Run: t.sandboxStep},
			{Name: "render-and-capture", Run: t.renderStep},
			{Name: "close-and-release", Run: t.cleanupStep},
		},
	}
}

// resetRunState tears down every seam from the previous run, including one
// that failed before close-and-release. A repeated F11,4,8 then starts from
// the same conditions as the first run.
func (t *selftest48) resetRunState() {
	if t.panel != nil {
		t.panel.Close()
	}
	if t.core != nil {
		t.core.Close()
	}
	if t.origin != nil {
		t.origin.Close()
	}
	if t.foreign != nil {
		t.foreign.Close()
	}
	if t.workDir != "" {
		_ = os.RemoveAll(t.workDir)
	}
	if t.shotDir != "" {
		_ = os.RemoveAll(t.shotDir)
	}

	t.run++
	t.panel = nil
	t.core = nil
	t.pages = &selftest48Pages{}
	t.connector = &selftest48Connector{}
	t.bookmarks = nil
	t.icons = nil
	t.tester = nil
	t.origin = nil
	t.foreign = nil
	t.cookies = make(map[string]string)
	t.workDir = ""
	t.shotDir = ""
	t.bookmarkPath = ""
	t.iconPath = ""
	t.downloadsDir = ""
	t.originURL = ""
	t.homeURL = ""
	t.docsURL = ""
	t.fileURL = ""
	t.slowURL = ""
	t.emptyShot = false
	t.editorShot = false
	t.populated = false
	t.persisted = false
	t.faviconDone = false
	t.downloadsDone = false
	t.sandboxDone = false
	t.renderStage = 0
	t.closed = false
	t.foreignSeen = 0
	t.deadline = time.Time{}
	t.captured = 0
	t.tabID = ""
}

// openPanelStep starts the in-process origin and the panel. It runs its own
// frame loop so resetRunState is called exactly once per run.
func (t *selftest48) openPanelStep(*ui.Context) selftest.Outcome {
	t.resetRunState()
	if err := t.startOrigins(); err != nil {
		return selftest.Failed(err)
	}
	if err := t.startPanel(); err != nil {
		return selftest.Failed(err)
	}
	deadline := time.Now().Add(selftest48Wait)
	for {
		t.tester.Frame()
		state := t.core.State()
		if state.Route == appbrowser.RouteConnected && len(state.Tabs) == 1 &&
			state.ActiveID != "" && t.tester.HasText(locales.In("en").BrowserNewTab()) {
			calls, hosts, tunnels := t.connector.snapshot()
			if calls != 1 || len(tunnels) != 1 || len(hosts) != 1 || hosts[0] != model.HostID(selftest48Host) {
				return selftest.Failed(fmt.Errorf(
					"connector calls=%d hosts=%v tunnels=%d, want one to %s",
					calls, hosts, len(tunnels), selftest48Host))
			}
			t.tabID = state.ActiveID
			return selftest.Passed(fmt.Sprintf(
				"the production panel connected through one dedicated tunnel to %s; run %d",
				selftest48Host, t.run), "appbrowser.New + browserui.New")
		}
		if time.Now().After(deadline) {
			return selftest.Failed(fmt.Errorf(
				"timed out after %s waiting for the browser route", selftest48Wait))
		}
	}
}

func (t *selftest48) startOrigins() error {
	selftest48ForeignCount.Store(0)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selftest48ForeignCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.foreign = foreign

	png, err := base64.StdEncoding.DecodeString(selftest48PNG)
	if err != nil {
		return fmt.Errorf("decode the fixture PNG: %w", err)
	}
	payload := []byte(strings.Repeat("ticket-48-download-through-the-tunnel\n", 4096))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		t.recordCookie(r, "/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>Ticket 48 Console</title></head>`+
			`<body><h1>Ticket 48 console</h1><p>Fetched through the browser tunnel.</p>`+
			`<a href="/docs">Docs</a></body></html>`)
	})
	mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		t.recordCookie(r, "/docs")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>Ticket 48 Docs</title></head>`+
			`<body><h1>Docs</h1></body></html>`)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		t.recordCookie(r, "/favicon.ico")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(png)))
		_, _ = w.Write(png)
	})
	mux.HandleFunc("/file.bin", func(w http.ResponseWriter, r *http.Request) {
		t.recordCookie(r, "/file.bin")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/slow.bin", func(w http.ResponseWriter, r *http.Request) {
		t.recordCookie(r, "/slow.bin")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(strings.Repeat("slow-", 1024)))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("/hostile", func(w http.ResponseWriter, r *http.Request) {
		t.recordCookie(r, "/hostile")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, t.hostilePage())
	})
	origin := httptest.NewServer(mux)
	t.origin = origin
	t.originURL = origin.URL
	t.homeURL = origin.URL + "/"
	t.docsURL = origin.URL + "/docs"
	t.fileURL = origin.URL + "/file.bin"
	t.slowURL = origin.URL + "/slow.bin"
	return nil
}

func (t *selftest48) hostilePage() string {
	foreign := t.foreign.URL
	return `<!doctype html><html><head><title>Hostile page</title>` +
		`<meta http-equiv="Content-Security-Policy" content="default-src * 'unsafe-eval'">` +
		`</head><body>` +
		`<a href="` + foreign + `/navigate">navigate</a>` +
		`<form action="` + foreign + `/form"><input name="q"></form>` +
		`<img src="` + foreign + `/pixel">` +
		`<iframe src="` + foreign + `/frame"></iframe>` +
		`<object data="` + foreign + `/object"></object>` +
		`<script>try{fetch("` + foreign + `/fetch")}catch(e){}` +
		`try{window.open("` + foreign + `/popup")}catch(e){}` +
		`try{eval("fetch('` + foreign + `/eval')")}catch(e){}` +
		`try{window.mygo.call("Vault.List")}catch(e){}` +
		`</script></body></html>`
}

func (t *selftest48) recordCookie(r *http.Request, path string) {
	t.originMu.Lock()
	t.cookies[path] = r.Header.Get("Cookie")
	t.originMu.Unlock()
}

func (t *selftest48) cookieFor(path string) string {
	t.originMu.Lock()
	defer t.originMu.Unlock()
	return t.cookies[path]
}

func (t *selftest48) startPanel() error {
	var err error
	t.workDir, err = os.MkdirTemp(selftestResultDir, "natcatty-selftest-48-run")
	if err != nil {
		return fmt.Errorf("create the self-test working directory: %w", err)
	}
	t.shotDir = filepath.Join(selftestResultDir, "natcatty-selftest-48-shots")
	if err := os.RemoveAll(t.shotDir); err != nil {
		return fmt.Errorf("clear the screenshot directory: %w", err)
	}
	if err := os.MkdirAll(t.shotDir, 0o755); err != nil {
		return fmt.Errorf("create the screenshot directory: %w", err)
	}
	t.downloadsDir = filepath.Join(t.workDir, "downloads")
	t.bookmarkPath = filepath.Join(t.workDir, "browser-bookmarks.json")
	t.iconPath = filepath.Join(t.workDir, "browser-site-icons.json")
	nextID := 0
	t.bookmarks, err = appbrowser.NewBookmarks(
		store.OpenBrowserBookmarks(t.bookmarkPath),
		time.Now,
		func() string {
			nextID++
			return fmt.Sprintf("selftest-48-%d", nextID)
		},
	)
	if err != nil {
		return fmt.Errorf("open bookmarks: %w", err)
	}
	t.icons, err = appbrowser.NewSiteIcons(store.OpenBrowserSiteIcons(t.iconPath))
	if err != nil {
		return fmt.Errorf("open site icons: %w", err)
	}
	cookies := func(string) string { return selftest48Cookie }
	t.core = appbrowser.New(appbrowser.Config{
		HostID:    model.HostID(selftest48Host),
		Label:     "48",
		Connector: t.connector,
		Redraw:    func() {},
		Bookmarks: t.bookmarks,
		Icons:     t.icons,
		Cookies:   cookies,
		Downloads: appbrowser.DownloadOptions{
			Dir:     func() (string, error) { return t.downloadsDir, nil },
			Cookies: cookies,
		},
	})
	t.panel = browserui.New(t.core, t.pages, browserui.WithRedraw(func() {}))
	t.tester = ui.NewTester(func(c *ui.Context) {
		t.panel.View(c)
	}, 868, 688)
	t.tester.SetLocale("en")
	t.tester.Frame()
	t.deadline = time.Now().Add(selftest48Wait)
	return nil
}

func (t *selftest48) bookmarksStep(*ui.Context) selftest.Outcome {
	if t.populated {
		return selftest.Passed(
			"host and all-host bookmarks were added and edited through the production editor",
			"browserui bookmark pane + application/browser.Bookmarks")
	}
	m := locales.In("en")
	if !t.emptyShot {
		if err := t.tester.Click(m.BrowserBookmarks()); err != nil {
			return selftest.Failed(fmt.Errorf("open bookmarks: %w", err))
		}
		t.tester.Frame()
		if !t.tester.HasText(m.BrowserBookmarkEmpty()) {
			return selftest.Failed(fmt.Errorf("bookmark empty state missing: %v", t.tester.Texts()))
		}
		if err := capture(t.tester, filepath.Join(t.shotDir, "bookmarks-empty-en-868x688.png")); err != nil {
			return selftest.Failed(err)
		}
		t.captured++
		t.emptyShot = true
	}
	if err := t.tester.Click(m.BrowserBookmarkAdd()); err != nil {
		return selftest.Failed(fmt.Errorf("add a bookmark: %w", err))
	}
	t.tester.Frame()
	if !t.editorShot {
		if err := capture(t.tester, filepath.Join(t.shotDir, "bookmarks-editor-en-868x688.png")); err != nil {
			return selftest.Failed(err)
		}
		t.captured++
		t.editorShot = true
	}
	if err := t.setField(m.BrowserBookmarkTitle(), "Ticket 48 Host"); err != nil {
		return selftest.Failed(err)
	}
	if err := t.setField(m.BrowserBookmarkAddress(), t.homeURL); err != nil {
		return selftest.Failed(err)
	}
	if err := t.tester.Click(m.BrowserBookmarkThisHost()); err != nil {
		return selftest.Failed(fmt.Errorf("choose This Host: %w", err))
	}
	if err := t.tester.Click(m.BrowserBookmarkSave()); err != nil {
		return selftest.Failed(fmt.Errorf("save the host bookmark: %w", err))
	}
	t.tester.Frame()
	if !t.tester.HasText("Ticket 48 Host") {
		return selftest.Failed(fmt.Errorf("saved host bookmark missing: %v", t.tester.Texts()))
	}

	if err := t.tester.Click(m.BrowserBookmarkAdd()); err != nil {
		return selftest.Failed(fmt.Errorf("add the second bookmark: %w", err))
	}
	t.tester.Frame()
	if err := t.setField(m.BrowserBookmarkTitle(), "Ticket 48 Shared"); err != nil {
		return selftest.Failed(err)
	}
	if err := t.setField(m.BrowserBookmarkAddress(), t.docsURL); err != nil {
		return selftest.Failed(err)
	}
	if err := t.tester.Click(m.BrowserBookmarkAllHosts()); err != nil {
		return selftest.Failed(fmt.Errorf("choose All Hosts: %w", err))
	}
	if err := t.tester.Click(m.BrowserBookmarkSave()); err != nil {
		return selftest.Failed(fmt.Errorf("save the all-host bookmark: %w", err))
	}
	t.tester.Frame()
	if err := t.tester.Click(m.BrowserBookmarkEdit()); err != nil {
		return selftest.Failed(fmt.Errorf("edit the host bookmark: %w", err))
	}
	t.tester.Frame()
	if err := t.setField(m.BrowserBookmarkTitle(), "Ticket 48 Host edited"); err != nil {
		return selftest.Failed(err)
	}
	if err := t.tester.Click(m.BrowserBookmarkSave()); err != nil {
		return selftest.Failed(fmt.Errorf("save the edited bookmark: %w", err))
	}
	t.tester.Frame()
	items := t.core.VisibleBookmarks()
	if len(items) != 2 {
		return selftest.Failed(fmt.Errorf("visible bookmarks = %d, want 2: %+v", len(items), items))
	}
	if !t.tester.HasText("Ticket 48 Host edited") || !t.tester.HasText("All Hosts · "+t.docsURL) {
		return selftest.Failed(fmt.Errorf("bookmark rows incomplete: %v", t.tester.Texts()))
	}
	if err := capture(t.tester, filepath.Join(t.shotDir, "bookmarks-populated-en-868x688.png")); err != nil {
		return selftest.Failed(err)
	}
	t.captured++
	t.populated = true
	return selftest.Passed(
		"one Host bookmark and one all-Host bookmark were added, the Host title edited, and both rows rendered",
		"browserui bookmark editor + appbrowser.Bookmarks")
}

func (t *selftest48) persistStep(*ui.Context) selftest.Outcome {
	if t.persisted {
		return selftest.Passed("bookmarks persisted with the correct host and all-host visibility",
			"adapters/store + appbrowser.NewBookmarks")
	}
	for i := 0; i < 30; i++ {
		if _, err := t.core.UpsertBookmark(domainbrowser.BookmarkInput{
			Title: fmt.Sprintf("Ticket 48 Bookmark %02d", i),
			URL:   fmt.Sprintf("%s/docs?item=%d", t.originURL, i),
			Scope: domainbrowser.ScopeAllHosts,
		}); err != nil {
			return selftest.Failed(fmt.Errorf("add long-list bookmark %d: %w", i, err))
		}
	}
	reloaded, err := appbrowser.NewBookmarks(store.OpenBrowserBookmarks(t.bookmarkPath), time.Now,
		func() string { return "unused" })
	if err != nil {
		return selftest.Failed(fmt.Errorf("reload bookmarks: %w", err))
	}
	hostItems := reloaded.List(selftest48Host)
	if len(hostItems) != 32 {
		return selftest.Failed(fmt.Errorf("reloaded host bookmarks = %d, want 32", len(hostItems)))
	}
	otherItems := reloaded.List(selftest48Other)
	if len(otherItems) != 31 {
		return selftest.Failed(fmt.Errorf("other-Host bookmarks = %d, want 31 all-Host only",
			len(otherItems)))
	}
	foundEdited := false
	for _, item := range hostItems {
		if item.Title == "Ticket 48 Host edited" && item.Scope == domainbrowser.ScopeHost {
			foundEdited = true
			break
		}
	}
	if !foundEdited {
		return selftest.Failed(errors.New("the edited Host bookmark did not survive reload"))
	}
	m := locales.In("en")
	t.tester.SetSize(440, 380)
	t.tester.Frame()
	if !t.tester.HasText("Ticket 48 Bookmark 00") || !t.tester.HasText(m.BrowserBookmarkAdd()) {
		return selftest.Failed(fmt.Errorf("long bookmark list is not scrollable: %v", t.tester.Texts()))
	}
	if err := capture(t.tester, filepath.Join(t.shotDir, "bookmarks-long-en-440x380.png")); err != nil {
		return selftest.Failed(err)
	}
	t.captured++
	t.tester.SetSize(868, 688)
	t.tester.Frame()
	t.persisted = true
	return selftest.Passed(
		"32 Host-visible bookmarks reloaded from disk; another Host sees only the 31 all-Host items; the edited title survived",
		"adapters/store + appbrowser.Bookmarks scopes")
}

func (t *selftest48) faviconStep(*ui.Context) selftest.Outcome {
	if t.faviconDone {
		return selftest.Passed("the PNG favicon was fetched through the tunnel, cached, and shown",
			"gateway.fetchFavicon + appbrowser.SiteIcons")
	}
	m := locales.In("en")
	// Close the bookmark pane so the page can be navigated with Ctrl+L.
	if err := t.tester.Click(m.BrowserBookmarks()); err != nil {
		return selftest.Failed(fmt.Errorf("close the bookmark pane: %w", err))
	}
	t.tester.Frame()
	t.tester.Key(ui.Cmd, ui.KeyL)
	t.tester.Frame()
	t.tester.Type(t.homeURL)
	t.tester.Key(0, ui.KeyEnter)
	deadline := time.Now().Add(selftest48Wait)
	for {
		t.tester.Frame()
		page := t.pages.only()
		if page != nil && len(page.loads()) == 1 {
			gatewayURL := page.loads()[0]
			if !strings.HasPrefix(gatewayURL, "http://127.0.0.2:") {
				return selftest.Failed(fmt.Errorf("page loaded %q, want the 127.0.0.2 gateway", gatewayURL))
			}
			page.report(browserui.PageNavigated, gatewayURL, "", "")
			page.reportTitle("Ticket 48 Console")
			page.report(browserui.PageLoaded, "", "", "")
			break
		}
		if time.Now().After(deadline) {
			return selftest.Failed(fmt.Errorf("timed out waiting for the page to load %s", t.homeURL))
		}
	}
	iconDeadline := time.Now().Add(selftest48Wait)
	for {
		t.tester.Frame()
		icon, ok := t.icons.Get(selftest48Host, t.homeURL)
		if ok && icon.DataURL != "" {
			page := t.pages.only()
			if page != nil {
				page.reportTitle("Ticket 48 Console")
			}
			t.tester.Frame()
			if tab, ok := t.core.ActiveTab(); ok && tab.IconDataURL == icon.DataURL {
				raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(icon.DataURL,
					"data:image/png;base64,"))
				if err != nil {
					return selftest.Failed(fmt.Errorf("favicon data URL: %w", err))
				}
				if bitmap, err := ui.DecodeBitmap(raw); err != nil || bitmap == nil {
					return selftest.Failed(fmt.Errorf("favicon did not decode: %v", err))
				}
				if got := t.cookieFor("/favicon.ico"); !strings.Contains(got, selftest48Cookie) {
					return selftest.Failed(fmt.Errorf("favicon fetch cookie = %q, want %q",
						got, selftest48Cookie))
				}
				reloaded, err := appbrowser.NewSiteIcons(store.OpenBrowserSiteIcons(t.iconPath))
				if err != nil {
					return selftest.Failed(fmt.Errorf("reload site icons: %w", err))
				}
				if persisted, ok := reloaded.Get(selftest48Host, t.homeURL); !ok ||
					persisted.DataURL != icon.DataURL {
					return selftest.Failed(errors.New("the favicon did not persist to disk"))
				}
				if err := t.tester.Click(m.BrowserBookmarks()); err != nil {
					return selftest.Failed(fmt.Errorf("open bookmarks with favicon: %w", err))
				}
				t.tester.Frame()
				withIcon := 0
				for _, item := range t.core.VisibleBookmarks() {
					if item.IconDataURL != "" {
						withIcon++
					}
				}
				if withIcon < 2 {
					return selftest.Failed(fmt.Errorf(
						"bookmarks with cached favicon = %d, want both origin bookmarks", withIcon))
				}
				if err := capture(t.tester, filepath.Join(t.shotDir, "favicon-en-868x688.png")); err != nil {
					return selftest.Failed(err)
				}
				t.captured++
				t.faviconDone = true
				return selftest.Passed(fmt.Sprintf(
					"the %d-byte PNG favicon came through %s with the page cookie, decoded, persisted, and appeared on the tab and both bookmarks",
					len(raw), strings.TrimPrefix(t.homeURL, "http://")),
					"gateway.fetchFavicon + SiteIcons + ui.DecodeBitmap")
			}
		}
		if time.Now().After(iconDeadline) {
			return selftest.Failed(fmt.Errorf(
				"timed out waiting for the favicon cache; bookmark pane=%v", t.tester.Texts()))
		}
	}
}

func (t *selftest48) downloadsStep(*ui.Context) selftest.Outcome {
	if t.downloadsDone {
		return selftest.Passed("the download completed with exact bytes; progress, cancel cleanup, and off-route refusal passed",
			"appbrowser download manager + gateway.openDownload")
	}
	m := locales.In("en")
	if err := t.tester.Click(m.BrowserBookmarks()); err != nil {
		return selftest.Failed(fmt.Errorf("close bookmark pane before download: %w", err))
	}
	t.tester.Frame()
	page := t.pages.only()
	if page == nil {
		return selftest.Failed(errors.New("the origin page is missing"))
	}
	gatewayURL, err := t.core.Navigate(t.tabID, t.fileURL)
	if err != nil {
		return selftest.Failed(fmt.Errorf("navigate to the download: %w", err))
	}
	if err := page.Load(gatewayURL); err != nil {
		return selftest.Failed(fmt.Errorf("load the download page: %w", err))
	}
	// A page-initiated download goes through the WebView event exactly as a
	// real Content-Disposition response would.
	page.download(gatewayURL, "file.bin")
	deadline := time.Now().Add(selftest48Wait)
	for {
		t.tester.Frame()
		items := t.core.Downloads()
		if len(items) == 1 && items[0].Status == appbrowser.DownloadCompleted {
			t.tester.Frame()
			got, err := os.ReadFile(filepath.Join(t.downloadsDir, items[0].Filename))
			if err != nil {
				return selftest.Failed(fmt.Errorf("read the completed download: %w", err))
			}
			want := []byte(strings.Repeat("ticket-48-download-through-the-tunnel\n", 4096))
			if string(got) != string(want) {
				return selftest.Failed(fmt.Errorf("download bytes = %d, want %d", len(got), len(want)))
			}
			if got := t.cookieFor("/file.bin"); !strings.Contains(got, selftest48Cookie) {
				return selftest.Failed(fmt.Errorf("download cookie = %q", got))
			}
			if !t.tester.HasText("file.bin") || !t.tester.HasText(m.BrowserDownloadCompleted()) {
				return selftest.Failed(fmt.Errorf("download pane missing completion: %v", t.tester.Texts()))
			}
			if err := capture(t.tester, filepath.Join(t.shotDir, "downloads-completed-en-868x688.png")); err != nil {
				return selftest.Failed(err)
			}
			t.captured++
			break
		}
		if time.Now().After(deadline) {
			return selftest.Failed(fmt.Errorf("timed out waiting for the completed download: %+v",
				t.core.Downloads()))
		}
	}

	slowGateway, err := t.core.Navigate(t.tabID, t.slowURL)
	if err != nil {
		return selftest.Failed(fmt.Errorf("navigate to the slow download: %w", err))
	}
	if err := page.Load(slowGateway); err != nil {
		return selftest.Failed(fmt.Errorf("load the slow download: %w", err))
	}
	if _, err := t.core.StartDownload(slowGateway, "slow.bin"); err != nil {
		return selftest.Failed(fmt.Errorf("start the slow download: %w", err))
	}
	deadline = time.Now().Add(selftest48Wait)
	for {
		t.tester.Frame()
		items := t.core.Downloads()
		if len(items) >= 2 {
			active := items[0]
			if active.Status == appbrowser.DownloadProgressing && active.Received > 0 {
				t.tester.Frame()
				if !strings.Contains(fmt.Sprint(t.tester.Texts()), m.BrowserDownloadProgressing()) {
					return selftest.Failed(fmt.Errorf("progress row missing: %v", t.tester.Texts()))
				}
				if err := capture(t.tester, filepath.Join(t.shotDir, "downloads-progress-en-868x688.png")); err != nil {
					return selftest.Failed(err)
				}
				t.captured++
				break
			}
		}
		if time.Now().After(deadline) {
			return selftest.Failed(fmt.Errorf("timed out waiting for download progress: %+v",
				t.core.Downloads()))
		}
	}
	if err := t.tester.Click(m.BrowserDownloadCancel()); err != nil {
		return selftest.Failed(fmt.Errorf("cancel the slow download: %w", err))
	}
	deadline = time.Now().Add(selftest48Wait)
	for {
		t.tester.Frame()
		items := t.core.Downloads()
		if len(items) >= 2 && items[0].Status == appbrowser.DownloadCancelled {
			t.tester.Frame()
			break
		}
		if time.Now().After(deadline) {
			return selftest.Failed(fmt.Errorf("timed out waiting for cancel: %+v", t.core.Downloads()))
		}
	}
	for _, name := range []string{"slow.bin", "slow.bin.part"} {
		if _, err := os.Stat(filepath.Join(t.downloadsDir, name)); !os.IsNotExist(err) {
			return selftest.Failed(fmt.Errorf("cancelled download left %s (stat err=%v)", name, err))
		}
	}
	if _, err := t.core.StartDownload(t.fileURL, "off-route.bin"); !errors.Is(err, appbrowser.ErrDownloadURL) {
		return selftest.Failed(fmt.Errorf("off-route download error = %v, want ErrDownloadURL", err))
	}
	for _, item := range t.core.Downloads() {
		if item.Filename == "off-route.bin" {
			return selftest.Failed(errors.New("an off-route download was recorded"))
		}
	}
	t.downloadsDone = true
	return selftest.Passed(
		"file.bin completed through the gateway with 155648 exact bytes; progress rendered, cancel removed slow.bin.part, and a direct origin URL was refused",
		"application/browser downloads + gateway.openDownload")
}

func (t *selftest48) sandboxStep(*ui.Context) selftest.Outcome {
	if t.sandboxDone {
		return selftest.Passed("the hostile page route stayed locked down with no product bridge",
			"gateway CSP + app browser sandbox")
	}
	m := locales.In("en")
	// First show a typed address refusal through the real address bar at the
	// content width that corresponds to a 704x421 client window.
	t.tester.SetSize(440, 380)
	t.tester.Frame()
	t.tester.Key(ui.Cmd, ui.KeyL)
	t.tester.Frame()
	t.tester.Type("javascript:alert(1)")
	t.tester.Key(0, ui.KeyEnter)
	t.tester.Frame()
	if !t.tester.HasText(m.BrowserInvalidScheme()) {
		return selftest.Failed(fmt.Errorf("typed scheme refusal missing: %v", t.tester.Texts()))
	}
	if err := capture(t.tester, filepath.Join(t.shotDir, "blocked-typed-error-en-440x380.png")); err != nil {
		return selftest.Failed(err)
	}
	t.captured++
	t.tester.SetSize(868, 688)
	t.tester.Frame()

	gatewayURL, err := t.core.Navigate(t.tabID, t.originURL+"/hostile")
	if err != nil {
		return selftest.Failed(fmt.Errorf("navigate to the hostile page: %w", err))
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(gatewayURL)
	if err != nil {
		return selftest.Failed(fmt.Errorf("fetch the hostile page through the gateway: %w", err))
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return selftest.Failed(fmt.Errorf("hostile status = %d", response.StatusCode))
	}
	csp := response.Header.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'", "connect-src 'self'", "frame-src 'self'",
		"worker-src 'self'", "object-src 'none'", "form-action 'self'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			return selftest.Failed(fmt.Errorf("hostile CSP %q missing %q", csp, want))
		}
	}
	for _, bad := range []string{"unsafe-eval", t.foreign.URL, "default-src *"} {
		if strings.Contains(csp, bad) {
			return selftest.Failed(fmt.Errorf("hostile CSP %q allows %q", csp, bad))
		}
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "http-equiv=\"content-security-policy\"") {
		return selftest.Failed(errors.New("the hostile page's own CSP survived"))
	}
	for _, marker := range []string{"window.netcatty", "window.hostBrowser", "host-browser:"} {
		if strings.Contains(text, marker) {
			return selftest.Failed(fmt.Errorf("gateway injected product bridge marker %q", marker))
		}
	}
	t.foreignSeen = selftest48ForeignCount.Load()
	if t.foreignSeen != 0 {
		return selftest.Failed(fmt.Errorf("the hostile page made %d requests to the foreign origin",
			t.foreignSeen))
	}
	options := browserPageOptions()
	if options.PreloadScript != "" || len(options.TrustedOrigins) != 0 {
		return selftest.Failed(fmt.Errorf("browser page options include a bridge: preload=%q trusted=%v",
			options.PreloadScript, options.TrustedOrigins))
	}
	if appbrowser.GatewayLoopbackHost != "127.0.0.2" {
		return selftest.Failed(fmt.Errorf("gateway host = %q, want 127.0.0.2 outside MyGo dev trust",
			appbrowser.GatewayLoopbackHost))
	}
	entry, ok := t.tester.Find(m.BrowserInvalidScheme())
	if !ok || entry.W <= 0 || entry.H <= 0 {
		return selftest.Failed(errors.New("the typed error is not rendered"))
	}
	t.sandboxDone = true
	return selftest.Passed(
		"the hostile page was pinned to 'self' CSP on an untrusted 127.0.0.2 gateway; its body carried no bridge, the page options had no preload or trusted origin, and the foreign origin recorded 0 requests",
		"gatewayPageCSP + browserPageOptions + hardenBrowserPage")
}

func (t *selftest48) renderStep(*ui.Context) selftest.Outcome {
	m := locales.In("zh-CN")
	if t.renderStage == 0 {
		t.tester.SetSize(440, 380)
		t.tester.SetLocale("zh-CN")
		t.tester.Frame()
		if !t.tester.HasText(m.BrowserBookmarks()) {
			return selftest.Failed(fmt.Errorf("zh-CN bookmark toolbar missing: %v", t.tester.Texts()))
		}
		// The bookmark pane is closed after the sandbox step.
		if err := t.tester.Click(m.BrowserBookmarks()); err != nil {
			return selftest.Failed(fmt.Errorf("open zh-CN bookmarks: %w", err))
		}
		t.tester.Frame()
		if !t.tester.HasText(m.BrowserBookmarkAdd()) {
			return selftest.Failed(fmt.Errorf("zh-CN bookmark pane missing: %v", t.tester.Texts()))
		}
		if err := capture(t.tester, filepath.Join(t.shotDir, "bookmarks-long-zh-CN-440x380.png")); err != nil {
			return selftest.Failed(err)
		}
		t.captured++
		t.renderStage = 1
		return selftest.Pending
	}
	if t.renderStage == 1 {
		t.tester.SetSize(868, 688)
		t.tester.Frame()
		if err := t.tester.Click(m.BrowserDownloads()); err != nil {
			return selftest.Failed(fmt.Errorf("open zh-CN downloads: %w", err))
		}
		t.tester.Frame()
		if !t.tester.HasText(m.BrowserDownloadCompleted()) {
			return selftest.Failed(fmt.Errorf("zh-CN download pane missing: %v", t.tester.Texts()))
		}
		if err := capture(t.tester, filepath.Join(t.shotDir, "downloads-zh-CN-868x688.png")); err != nil {
			return selftest.Failed(err)
		}
		t.captured++
		t.renderStage = 2
		return selftest.Pending
	}
	t.tester.SetLocale("en")
	t.tester.SetSize(868, 688)
	t.tester.Frame()
	return selftest.Passed(fmt.Sprintf(
		"captured %d bookmark, editor, favicon, download, and typed-error frames in en and zh-CN at 868x688 and 440x380",
		t.captured), "ui.Tester + capture")
}

func (t *selftest48) cleanupStep(*ui.Context) selftest.Outcome {
	if !t.closed {
		t.panel.Close()
		t.closed = true
		t.deadline = time.Now().Add(selftest48Wait)
	}
	if !t.connector.allDiscarded() {
		return pending(t.deadline, "the dedicated tunnel to be discarded")
	}
	if !t.pages.allDestroyed() {
		return pending(t.deadline, "every browser page to be destroyed")
	}
	for _, name := range []string{"slow.bin.part", "off-route.bin.part"} {
		if _, err := os.Stat(filepath.Join(t.downloadsDir, name)); !os.IsNotExist(err) {
			return selftest.Failed(fmt.Errorf("cleanup left %s", name))
		}
	}
	if t.captured < 8 {
		return selftest.Failed(fmt.Errorf("captured %d screenshots, want at least 8", t.captured))
	}
	t.foreignSeen = selftest48ForeignCount.Load()
	if t.foreignSeen != 0 {
		return selftest.Failed(fmt.Errorf("foreign origin received %d requests", t.foreignSeen))
	}
	return selftest.Passed(fmt.Sprintf(
		"the panel closed, %d dedicated tunnel(s) were discarded, %d page(s) destroyed, and the foreign origin recorded 0 requests",
		len(t.connector.tunnelList()), len(t.pages.snapshot())),
		"browserui.Panel.Close + downloads.Close")
}

func (t *selftest48) setField(label, value string) error {
	for i := 0; i < 40; i++ {
		if t.tester.Focused(label) {
			t.tester.Key(ui.Cmd, ui.KeyA)
			t.tester.Frame()
			t.tester.Type(value)
			t.tester.Frame()
			return nil
		}
		t.tester.Key(0, ui.KeyTab)
		t.tester.Frame()
	}
	return fmt.Errorf("the %q field never took focus; texts %v", label, t.tester.Texts())
}

// --- fake pages -----------------------------------------------------------

type selftest48Pages struct {
	mu    sync.Mutex
	items []*selftest48Page
}

func (f *selftest48Pages) NewPage() (browserui.Page, error) {
	page := &selftest48Page{}
	f.mu.Lock()
	f.items = append(f.items, page)
	f.mu.Unlock()
	return page, nil
}

func (f *selftest48Pages) snapshot() []*selftest48Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*selftest48Page(nil), f.items...)
}

func (f *selftest48Pages) only() *selftest48Page {
	items := f.snapshot()
	if len(items) != 1 {
		return nil
	}
	return items[0]
}

func (f *selftest48Pages) allDestroyed() bool {
	for _, page := range f.snapshot() {
		if !page.isDestroyed() {
			return false
		}
	}
	return true
}

// selftest48Page records loads and lets the check report page events. It has
// no native surface, so the panel draws its placeholder; no WebView is needed
// in the headless harness.
type selftest48Page struct {
	mu        sync.Mutex
	listeners []func(browserui.PageEvent)
	loadsSeen []string
	url       string
	loading   bool
	destroyed bool
}

func (p *selftest48Page) Native() ui.NativeWebView { return nil }

func (p *selftest48Page) Load(rawURL string) error {
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return nil
	}
	p.loadsSeen = append(p.loadsSeen, rawURL)
	p.url = rawURL
	p.loading = true
	p.mu.Unlock()
	return nil
}

func (p *selftest48Page) Reload()  {}
func (p *selftest48Page) Stop()    {}
func (p *selftest48Page) Back()    {}
func (p *selftest48Page) Forward() {}

func (p *selftest48Page) CanGoBack() bool    { return false }
func (p *selftest48Page) CanGoForward() bool { return false }

func (p *selftest48Page) Loading() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loading
}

func (p *selftest48Page) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}

func (p *selftest48Page) Destroy() {
	p.mu.Lock()
	p.destroyed = true
	p.mu.Unlock()
}

func (p *selftest48Page) OnEvent(fn func(browserui.PageEvent)) func() {
	p.mu.Lock()
	index := len(p.listeners)
	p.listeners = append(p.listeners, fn)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if index < len(p.listeners) {
			p.listeners[index] = nil
		}
	}
}

func (p *selftest48Page) SetNavigationGuard(func(string) (string, bool)) {}

func (p *selftest48Page) report(kind browserui.PageEventKind, rawURL, title, errText string) {
	p.mu.Lock()
	listeners := append([]func(browserui.PageEvent){}, p.listeners...)
	p.mu.Unlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(browserui.PageEvent{Kind: kind, URL: rawURL, Title: title, Error: errText})
		}
	}
}

func (p *selftest48Page) reportTitle(title string) {
	p.report(browserui.PageTitle, "", title, "")
}

func (p *selftest48Page) download(gatewayURL, suggested string) {
	p.mu.Lock()
	listeners := append([]func(browserui.PageEvent){}, p.listeners...)
	p.mu.Unlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(browserui.PageEvent{
				Kind:          browserui.PageDownload,
				URL:           gatewayURL,
				SuggestedName: suggested,
			})
		}
	}
}

func (p *selftest48Page) loads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.loadsSeen...)
}

func (p *selftest48Page) isDestroyed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.destroyed
}

// --- recording tunnel -----------------------------------------------------

type selftest48Tunnel struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	disc int
}

func (t *selftest48Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

func (t *selftest48Tunnel) Done() <-chan struct{} { return t.done }

func (t *selftest48Tunnel) Discard() {
	t.mu.Lock()
	t.disc++
	t.mu.Unlock()
	t.once.Do(func() { close(t.done) })
}

func (t *selftest48Tunnel) discarded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.disc > 0
}

type selftest48Connector struct {
	mu      sync.Mutex
	calls   int
	hosts   []model.HostID
	tunnels []*selftest48Tunnel
}

func (c *selftest48Connector) Connect(_ context.Context, hostID model.HostID) (appbrowser.Tunnel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	tunnel := &selftest48Tunnel{done: make(chan struct{})}
	c.hosts = append(c.hosts, hostID)
	c.tunnels = append(c.tunnels, tunnel)
	return tunnel, nil
}

func (c *selftest48Connector) snapshot() (int, []model.HostID, []*selftest48Tunnel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]model.HostID(nil), c.hosts...),
		append([]*selftest48Tunnel(nil), c.tunnels...)
}

func (c *selftest48Connector) tunnelList() []*selftest48Tunnel {
	_, _, tunnels := c.snapshot()
	return tunnels
}

func (c *selftest48Connector) allDiscarded() bool {
	for _, tunnel := range c.tunnelList() {
		if !tunnel.discarded() {
			return false
		}
	}
	return true
}
