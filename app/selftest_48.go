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
//   - the long bookmark list is rendered inside the real shell at 704x421 and
//     1084x661 in English and zh-CN; the wheel and End bring the last row
//     wholly inside the scrolled viewport and the client, above the pinned
//     action bar, while the pane stays inside the client;
//   - a hostile page is fetched through the gateway and its CSP blocks
//     cross-origin fetch, images, frames, objects, forms, window.open, and
//     eval; the foreign origin records zero requests, and the page options
//     carry no preload or trusted origin while the gateway binds a loopback
//     address outside MyGo's development trust where the platform assigns
//     one (127.0.0.2; macOS falls back to 127.0.0.1);
//   - the bookmark, editor, favicon, download, and typed-address-error
//     states are captured at content sizes for 1100x720 and 720x480 in
//     English and zh-CN.
//
// Every wait observes panel, service, file, or HTTP state with a hard
// deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-48.json and
// %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-48-shots\*.png.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appbrowser "github.com/mslxl/natcatty/application/browser"
	appsession "github.com/mslxl/natcatty/application/session"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	browserui "github.com/mslxl/natcatty/ui/browser"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
	uishell "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
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
			{Name: "bookmarks-long-list-shell", Run: t.shellLongListStep},
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

// shellLongListStep renders the persisted long list in the real shell, where
// the browser panel is a flex child of the Pane: that is where the panel used
// to grow with the list and leave the last rows below the client however the
// user scrolled. At 704x421 and 1084x661, in English and zh-CN, the wheel and
// End must bring the last row whole into the client, inside the scrolled
// viewport and above the pinned action bar, while the pane stays inside the
// client. The measured rects are the pass condition, not the row's text.
func (t *selftest48) shellLongListStep(*ui.Context) selftest.Outcome {
	variants := []struct {
		width, height int
		locale        string
	}{
		{704, 421, "en"},
		{704, 421, "zh-CN"},
		{1084, 661, "en"},
		{1084, 661, "zh-CN"},
	}
	details := make([]string, 0, len(variants))
	for _, variant := range variants {
		detail, err := t.checkShellLongList(variant.width, variant.height, variant.locale)
		if err != nil {
			return selftest.Failed(fmt.Errorf("%dx%d %s: %w",
				variant.width, variant.height, variant.locale, err))
		}
		details = append(details, detail)
	}
	return selftest.Passed(
		"the long bookmark list stayed in a bounded viewport in the real shell: "+strings.Join(details, "; "),
		"uishell + browserui bookmark pane + ui.Tester")
}

// checkShellLongList mounts the real shell at width×height, opens the
// production browser panel and the bookmark pane, and measures the last row
// after scrolling by wheel and keyboard. It returns the measured rects.
func (t *selftest48) checkShellLongList(width, height int, locale string) (string, error) {
	h, err := t.openShellHarness(width, height, locale)
	if err != nil {
		return "", err
	}
	defer h.Close()

	m := locales.In(locale)
	if err := h.tester.Click(m.BrowserBookmarks()); err != nil {
		return "", fmt.Errorf("open the bookmark pane: %w", err)
	}
	h.tester.Frame()
	if !h.tester.HasText(m.BrowserBookmarkAdd()) {
		return "", fmt.Errorf("the bookmark pane did not open: %v", h.tester.Texts())
	}
	items := h.core.VisibleBookmarks()
	if len(items) < 20 {
		return "", fmt.Errorf("visible bookmarks = %d, want the long list", len(items))
	}
	first, last := items[0], items[len(items)-1]
	firstAddress := selftest48BookmarkAddress(first, m)
	lastAddress := selftest48BookmarkAddress(last, m)
	// Measure only after the pane has stopped resizing: the shell applies
	// session state asynchronously, and a rect read mid-resize says nothing
	// about the layout under test.
	if err := h.waitSettled(m, m.BrowserBookmarkAdd(), first.Title, firstAddress); err != nil {
		return "", err
	}
	firstTitle, ok := h.tester.Find(first.Title)
	if !ok || firstTitle.H <= 0 {
		return "", fmt.Errorf("the first row's title is missing: %v", h.tester.Texts())
	}
	firstURL, ok := h.tester.Find(firstAddress)
	if !ok || firstURL.H <= 0 {
		return "", fmt.Errorf("the first row's address is missing: %v", h.tester.Texts())
	}
	lastTitle, _ := h.tester.Find(last.Title)
	lastURL, _ := h.tester.Find(lastAddress)
	if selftest48FullLabel(lastTitle, firstTitle) && selftest48FullLabel(lastURL, firstURL) &&
		selftest48InsideClient(lastTitle, width, height) {
		return "", fmt.Errorf("the %d-row list fits without scrolling; the viewport was not exercised",
			len(items))
	}
	bar, ok := h.tester.Find(m.BrowserBookmarkAdd())
	if !ok {
		return "", fmt.Errorf("the pinned action bar is missing: %v", h.tester.Texts())
	}
	// A point over the list's favicon column, left of the title and address.
	listX := firstURL.X - 14
	listY := firstTitle.Y + firstTitle.H/2

	assertBottom := func(how string) error {
		title, _ := h.tester.Find(last.Title)
		address, _ := h.tester.Find(lastAddress)
		if !selftest48FullLabel(title, firstTitle) {
			return fmt.Errorf("after %s the last title is clipped: %+v (whole is %+v)",
				how, title, firstTitle)
		}
		if !selftest48FullLabel(address, firstURL) {
			return fmt.Errorf("after %s the last address is clipped: %+v (whole is %+v)",
				how, address, firstURL)
		}
		if !selftest48InsideClient(title, width, height) {
			return fmt.Errorf("after %s the last title is outside the %dx%d client: %+v",
				how, width, height, title)
		}
		if !selftest48InsideClient(address, width, height) {
			return fmt.Errorf("after %s the last address is outside the %dx%d client: %+v",
				how, width, height, address)
		}
		if title.Y < bar.Y+bar.H-0.5 {
			return fmt.Errorf("after %s the last row runs under the pinned bar %+v: %+v",
				how, bar, title)
		}
		pane, ok := h.tester.Find(m.PaneRegion(1))
		if !ok {
			return fmt.Errorf("the shell's content pane is missing: %v", h.tester.Texts())
		}
		if pane.Y < -0.5 || pane.Y+pane.H > float32(height)+0.5 {
			return fmt.Errorf("the browser pane %+v exceeds the %dx%d client", pane, width, height)
		}
		// Measure the Scroll widget itself, not only the pane and client:
		// the acceptance defect was an unbounded viewport that the pane grew
		// with. The row must be whole inside this viewport, below the pinned
		// bar.
		if h.context == nil {
			return fmt.Errorf("the shell has not been built yet")
		}
		viewport := h.panel.BookmarkViewportBounds(h.context)
		if viewport.W <= 0 || viewport.H <= 0 {
			return fmt.Errorf("the bookmark scroll viewport has no size: %+v", viewport)
		}
		if !selftest48InsideClient(viewport, width, height) {
			return fmt.Errorf("the bookmark scroll viewport is outside the %dx%d client: %+v",
				width, height, viewport)
		}
		if title.Y < viewport.Y-0.5 || title.Y+title.H > viewport.Y+viewport.H+0.5 ||
			address.Y < viewport.Y-0.5 || address.Y+address.H > viewport.Y+viewport.H+0.5 {
			return fmt.Errorf("after %s the last row is not inside the scroll viewport %+v: title %+v address %+v",
				how, viewport, title, address)
		}
		return nil
	}

	deadline := time.Now().Add(selftest48Wait)
	for {
		title, _ := h.tester.Find(last.Title)
		address, _ := h.tester.Find(lastAddress)
		if selftest48FullLabel(title, firstTitle) && selftest48FullLabel(address, firstURL) &&
			selftest48InsideClient(title, width, height) &&
			selftest48InsideClient(address, width, height) {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the wheel never brought the last row into the viewport: title %+v address %+v",
				title, address)
		}
		h.tester.Scroll(listX, listY, 0, 300)
		h.tester.Frame()
	}
	if err := assertBottom("the wheel"); err != nil {
		return "", err
	}
	if firstTitle, ok := h.tester.Find(first.Title); ok && selftest48InsideClient(firstTitle, width, height) {
		return "", fmt.Errorf("the first row is still whole after scrolling to the last: %+v", firstTitle)
	}
	shot := fmt.Sprintf("bookmarks-shell-%dx%d-%s-bottom.png", width, height, locale)
	if err := capture(h.tester, filepath.Join(t.shotDir, shot)); err != nil {
		return "", err
	}
	t.captured++

	// Keyboard: the pointer is over the list and no text field takes the
	// keyboard, so Home and End scroll the list under it.
	h.tester.Move(listX, listY)
	h.tester.Key(0, ui.KeyHome)
	deadline = time.Now().Add(selftest48Wait)
	for {
		title, _ := h.tester.Find(first.Title)
		address, _ := h.tester.Find(firstAddress)
		if selftest48FullLabel(title, firstTitle) && selftest48FullLabel(address, firstURL) {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("Home never brought the first row back: title %+v address %+v",
				title, address)
		}
		h.tester.Frame()
	}
	h.tester.Key(0, ui.KeyEnd)
	deadline = time.Now().Add(selftest48Wait)
	for {
		title, _ := h.tester.Find(last.Title)
		address, _ := h.tester.Find(lastAddress)
		if selftest48FullLabel(title, firstTitle) && selftest48FullLabel(address, firstURL) &&
			selftest48InsideClient(title, width, height) {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("End never brought the last row into the viewport: title %+v address %+v",
				title, address)
		}
		h.tester.Frame()
	}
	if err := assertBottom("End"); err != nil {
		return "", err
	}
	pane, _ := h.tester.Find(m.PaneRegion(1))
	viewport := h.panel.BookmarkViewportBounds(h.context)
	title, _ := h.tester.Find(last.Title)
	address, _ := h.tester.Find(lastAddress)
	return fmt.Sprintf("%dx%d %s last title y %.1f..%.1f, address y %.1f..%.1f, client %d, pane bottom %.1f, viewport bottom %.1f, bar bottom %.1f",
		width, height, locale, title.Y, title.Y+title.H, address.Y, address.Y+address.H,
		height, pane.Y+pane.H, viewport.Y+viewport.H, bar.Y+bar.H), nil
}

// selftest48BookmarkAddress is the address line the bookmark row renders.
func selftest48BookmarkAddress(item appbrowser.BookmarkView, m locales.Messages) string {
	if item.Scope == domainbrowser.ScopeAllHosts {
		return m.BrowserBookmarkAllHosts() + " · " + item.URL
	}
	return item.URL
}

// selftest48FullLabel reports whether r shows its whole built height: a
// label clipped by the scroll viewport keeps only the intersection with it.
// Width is not compared because the first and last rows carry different text;
// the viewport band is checked separately against the pane.
func selftest48FullLabel(r, reference ui.Rect) bool {
	return r.W > 0 && r.H > 0 && r.H >= reference.H-0.5
}

func selftest48InsideClient(r ui.Rect, width, height int) bool {
	return r.W > 0 && r.H > 0 &&
		r.X >= -0.5 && r.Y >= -0.5 &&
		r.X+r.W <= float32(width)+0.5 && r.Y+r.H <= float32(height)+0.5
}

// selftest48ShellHarness mounts the real shell with the production Session
// surface and browser panel, using its own quiet transport and page factory so
// it never disturbs the panel-alone harness of the other steps.
type selftest48ShellHarness struct {
	service *appsession.Service
	factory *selftest48ShellFactory
	shell   *shellstate.State
	tester  *ui.Tester
	context *ui.Context
	pages   *selftest48Pages
	core    *appbrowser.Panel
	panel   *browserui.Panel
}

func (t *selftest48) openShellHarness(width, height int, locale string) (*selftest48ShellHarness, error) {
	h := &selftest48ShellHarness{
		factory: &selftest48ShellFactory{},
		pages:   &selftest48Pages{},
	}
	// Every error path below must release what it started: a leaked Session
	// or service would keep the next variant's shell from settling.
	complete := false
	defer func() {
		if !complete {
			h.Close()
		}
	}()
	h.service = appsession.NewService(h.factory)
	connector := &selftest48Connector{}
	opener := func(session *appsession.Session) (shellstate.Surface, error) {
		return sessionui.New(session, sessionui.WithBrowser(
			func(onClose func()) (sessionui.BrowserPanel, error) {
				core := appbrowser.New(appbrowser.Config{
					HostID:    session.HostID(),
					Label:     session.Label(),
					Connector: connector,
					Bookmarks: t.bookmarks,
				})
				panel := browserui.New(core, h.pages, browserui.WithCloseRequest(onClose))
				h.core = core
				h.panel = panel
				return panel, nil
			}))
	}
	h.shell = shellstate.NewState(shellstate.WithSessions(h.service, opener))
	h.tester = ui.NewTester(func(c *ui.Context) {
		h.context = c
		uishell.View(h.shell)(c)
	}, width, height)
	h.tester.SetLocale(locale)
	h.tester.Frame()

	host := domainvault.Host{
		ID:       model.HostID(selftest48Host),
		Label:    "48",
		Hostname: "selftest-48.shell",
		Port:     22,
		Username: "root",
		Protocol: domainvault.ProtocolSSH,
	}
	if err := h.shell.OpenHost(host); err != nil {
		return nil, fmt.Errorf("open the shell Session: %w", err)
	}
	deadline := time.Now().Add(selftest48Wait)
	for h.shell.TabCount() != 1 {
		h.shell.Flush()
		h.tester.Frame()
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for the shell Session: %v", h.tester.Texts())
		}
	}
	m := locales.In(locale)
	if err := h.tester.Click(m.SessionMenuFor(1)); err != nil {
		return nil, fmt.Errorf("open the Session menu: %w", err)
	}
	h.tester.Frame()
	if err := h.tester.ChooseMenuItem(m.SessionMenuOpenBrowser()); err != nil {
		return nil, fmt.Errorf("choose Open Browser: %w", err)
	}
	deadline = time.Now().Add(selftest48Wait)
	for {
		h.tester.Frame()
		if h.core != nil && h.core.State().Route == appbrowser.RouteConnected {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for the shell browser route: %v", h.tester.Texts())
		}
	}
	if err := h.waitSettled(m, m.BrowserNewTab(), m.BrowserAddress()); err != nil {
		return nil, err
	}
	complete = true
	return h, nil
}

// waitSettled frames until the shell's content pane and the named labels
// report the same positive geometry on two consecutive frames. The shell
// applies session state asynchronously, so a measurement taken right after
// the browser opens can land on a pane that is still taking its final size.
func (h *selftest48ShellHarness) waitSettled(m locales.Messages, labels ...string) error {
	deadline := time.Now().Add(selftest48Wait)
	var previous []ui.Rect
	stable := 0
	for {
		h.shell.Flush()
		h.tester.Frame()
		rects := make([]ui.Rect, 0, len(labels)+1)
		pane, ok := h.tester.Find(m.PaneRegion(1))
		good := ok && pane.W > 0 && pane.H > 0
		rects = append(rects, pane)
		for _, label := range labels {
			rect, found := h.tester.Find(label)
			if !found || rect.W <= 0 || rect.H <= 0 {
				good = false
			}
			rects = append(rects, rect)
		}
		if good && slices.Equal(rects, previous) {
			stable++
			if stable >= 2 {
				return nil
			}
		} else {
			stable = 0
		}
		previous = rects
		if time.Now().After(deadline) {
			return fmt.Errorf("the shell's pane never settled: pane %+v, labels %v; texts %v",
				pane, labels, h.tester.Texts())
		}
	}
}

func (h *selftest48ShellHarness) Close() {
	if h.panel != nil {
		h.panel.Close()
	}
	if h.shell != nil {
		h.shell.CloseAllSessions()
		h.shell.Shutdown()
	}
	if h.service != nil {
		h.service.CloseAll()
	}
}

// selftest48ShellFactory opens a quiet in-process SSH transport for the
// shell-mounted check; no bytes flow.
type selftest48ShellFactory struct {
	mu         sync.Mutex
	transports []*selftest48ShellTransport
}

func (f *selftest48ShellFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.HostID != model.HostID(selftest48Host) {
		return nil, fmt.Errorf("shell self-test transport got Host %q", spec.HostID)
	}
	transport := &selftest48ShellTransport{done: make(chan struct{})}
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

type selftest48ShellTransport struct {
	done chan struct{}
	once sync.Once
}

func (t *selftest48ShellTransport) Read([]byte) (int, error) {
	<-t.done
	return 0, io.EOF
}

func (t *selftest48ShellTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *selftest48ShellTransport) Resize(int, int) error       { return nil }

func (t *selftest48ShellTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
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
			if !selftestLoopbackGatewayURL(gatewayURL) {
				return selftest.Failed(fmt.Errorf("page loaded %q, want a loopback gateway URL", gatewayURL))
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
	if !selftestLoopbackGatewayURL(gatewayURL) {
		return selftest.Failed(fmt.Errorf("hostile page gateway URL = %q, want a loopback gateway address",
			gatewayURL))
	}
	parsedGateway, parseErr := url.Parse(gatewayURL)
	if parseErr != nil {
		return selftest.Failed(fmt.Errorf("parse the hostile gateway URL %q: %w", gatewayURL, parseErr))
	}
	gatewayHost := parsedGateway.Hostname()
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
	if appbrowser.GatewayLoopbackHost != "127.0.0.2" ||
		appbrowser.GatewayHostIsDevTrusted(appbrowser.GatewayLoopbackHost) {
		return selftest.Failed(fmt.Errorf("preferred gateway host = %q, want 127.0.0.2 outside MyGo dev trust",
			appbrowser.GatewayLoopbackHost))
	}
	if !appbrowser.GatewayHostIsDevTrusted(appbrowser.GatewayFallbackLoopbackHost) {
		return selftest.Failed(fmt.Errorf("fallback gateway host = %q, want the documented loopback fallback",
			appbrowser.GatewayFallbackLoopbackHost))
	}
	// The run used the preferred address where the platform assigns it. On
	// macOS, which assigns only 127.0.0.1, the gateway used the documented
	// fallback; the app registers no MyGo bindings, so the bridge still
	// reaches no product API there.
	if !appbrowser.IsGatewayHost(gatewayHost) {
		return selftest.Failed(fmt.Errorf("this run's gateway host = %q, want a gateway address", gatewayHost))
	}
	entry, ok := t.tester.Find(m.BrowserInvalidScheme())
	if !ok || entry.W <= 0 || entry.H <= 0 {
		return selftest.Failed(errors.New("the typed error is not rendered"))
	}
	t.sandboxDone = true
	return selftest.Passed(
		fmt.Sprintf("the hostile page was pinned to 'self' CSP on the loopback gateway %s (preferred %s is outside MyGo dev trust); its body carried no bridge, the page options had no preload or trusted origin, and the foreign origin recorded 0 requests",
			gatewayHost, appbrowser.GatewayLoopbackHost),
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
	if t.captured < 14 {
		return selftest.Failed(fmt.Errorf("captured %d screenshots, want at least 14", t.captured))
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
