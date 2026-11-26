package phoneshell

// Behaviour tests for the Host Browser's isolation, reconnect, download, and
// lifetime rules. The fake Browser is the seam: it records the site origin of
// every page, the gateway it is asked to show, and every download action, so
// the surface's decisions are checked without a native WebView.

import (
	"errors"
	"strings"
	"testing"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func newIsolationSurface(t *testing.T, browser *fakeBrowser) *browserSurface {
	t.Helper()
	return newBrowserSurface(browser, func() {})
}

// TestBrowserSharesOneStorePerSiteAndKeepsSitesApart proves the page seam's
// share rule: tabs on one Host and site reuse a page and therefore one store,
// while the same Host's another site gets a page of its own.
func TestBrowserSharesOneStorePerSiteAndKeepsSitesApart(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	defer surface.close()

	first := surface.ensurePage("tab-1")
	if first == nil {
		t.Fatal("the first tab got no page")
	}
	if again := surface.ensurePage("tab-1"); again != first {
		t.Fatal("re-entering the tab replaced its page and store")
	}

	browser.mu.Lock()
	browser.state.Tabs = append(browser.state.Tabs,
		appbrowser.Tab{ID: "tab-2", URL: "https://prod.example.com/other"},
		appbrowser.Tab{ID: "tab-3", URL: "https://mail.example.com/"},
	)
	browser.nextTab = 3
	browser.mu.Unlock()

	second := surface.ensurePage("tab-2")
	third := surface.ensurePage("tab-3")
	if second == nil || third == nil {
		t.Fatal("a new tab got no page")
	}

	browser.mu.Lock()
	sites := append([]string(nil), browser.pageSites...)
	pages := len(browser.pages)
	browser.mu.Unlock()
	if pages != 3 {
		t.Fatalf("pages = %d, want one per tab", pages)
	}
	if sites[0] != "https://prod.example.com" || sites[1] != "https://prod.example.com" {
		t.Fatalf("same-site pages got sites %v, want one shared store", sites)
	}
	if sites[2] != "https://mail.example.com" {
		t.Fatalf("the second site got site %q, want its own store", sites[2])
	}
	if second == third {
		t.Fatal("two sites shared one page and store")
	}
}

// TestBrowserTabMovingToAnotherSiteGetsThatSitesStore proves a page never
// loads a navigation from another site into the store it was made for: the
// surface releases the old page and makes one of the new site's own.
func TestBrowserTabMovingToAnotherSiteGetsThatSitesStore(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	defer surface.close()

	page := surface.ensurePage("tab-1")
	if page == nil {
		t.Fatal("the tab got no page")
	}
	old := page.(*fakeBrowserPage)

	surface.address = "https://mail.example.com/inbox"
	surface.navigate()

	browser.mu.Lock()
	sites := append([]string(nil), browser.pageSites...)
	browser.mu.Unlock()
	if len(sites) != 2 || sites[0] != "https://prod.example.com" || sites[1] != "https://mail.example.com" {
		t.Fatalf("page sites = %v, want one page per site", sites)
	}
	if !old.isDestroyed() {
		t.Fatal("the old site's page was reused for another site")
	}
	if got := surface.ensurePage("tab-1"); got == nil || got == BrowserPage(old) {
		t.Fatal("the tab did not get a page of the new site's store")
	}
}

// TestBrowserSurfaceReloadsOnReconnect proves a reconnect rebinds the gateway
// on a new port and the page reloads there, while keeping the same page and
// the same per-site store.
func TestBrowserSurfaceReloadsOnReconnect(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	defer surface.close()

	page := surface.ensurePage("tab-1")
	if page == nil {
		t.Fatal("the tab got no page")
	}
	fakePage := page.(*fakeBrowserPage)
	if loaded := fakePage.loadedURLs(); len(loaded) != 1 || loaded[0] != browser.gateway {
		t.Fatalf("initial loads = %v, want the first gateway", loaded)
	}

	browser.mu.Lock()
	browser.state.Route = appbrowser.RouteDisconnected
	browser.state.Detail = "the dedicated tunnel closed"
	browser.mu.Unlock()
	browser.emit()

	browser.mu.Lock()
	browser.state.Route = appbrowser.RouteConnected
	browser.state.Detail = ""
	browser.gateway = "http://127.0.0.2:55999/"
	browser.mu.Unlock()
	browser.emit()

	again := surface.ensurePage("tab-1")
	if again != page {
		t.Fatal("a reconnect replaced the page and lost its store")
	}
	if fakePage.isDestroyed() {
		t.Fatal("a reconnect destroyed the page")
	}
	loaded := fakePage.loadedURLs()
	if len(loaded) != 2 || loaded[1] != "http://127.0.0.2:55999/" {
		t.Fatalf("loads after reconnect = %v, want the rebuilt gateway", loaded)
	}
	browser.mu.Lock()
	sites := append([]string(nil), browser.pageSites...)
	browser.mu.Unlock()
	if len(sites) != 1 || sites[0] != "https://prod.example.com" {
		t.Fatalf("page sites after reconnect = %v, want the same store", sites)
	}
}

// TestBrowserDownloadEventSettlesTheTabAndKeepsThePage proves a download
// navigation never replaces the shown page: the provisional tab is settled and
// the same page stays on screen.
func TestBrowserDownloadEventSettlesTheTabAndKeepsThePage(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	defer surface.close()

	page := surface.ensurePage("tab-1")
	fakePage := page.(*fakeBrowserPage)
	fakePage.mu.Lock()
	events := append([]func(BrowserPageEvent){}, fakePage.events...)
	fakePage.mu.Unlock()
	if len(events) == 0 {
		t.Fatal("the page has no event listener")
	}

	events[0](BrowserPageEvent{
		Kind:          BrowserPageDownload,
		URL:           browser.gateway + "files/report.pdf",
		SuggestedName: "report.pdf",
	})

	browser.mu.Lock()
	stopped := append([]string(nil), browser.stopped...)
	downloads := append([]string(nil), browser.downloads...)
	tabURL := browser.state.Tabs[0].URL
	browser.mu.Unlock()
	if len(stopped) != 1 || stopped[0] != "tab-1" {
		t.Fatalf("stopped = %v, want the download tab settled", stopped)
	}
	if len(downloads) != 1 || downloads[0] != browser.gateway+"files/report.pdf" {
		t.Fatalf("downloads = %v, want the gateway file URL", downloads)
	}
	if tabURL != "https://prod.example.com/" {
		t.Fatalf("the tab moved to %q during a download, want the original page", tabURL)
	}
	if fakePage.isDestroyed() {
		t.Fatal("a download destroyed the shown page")
	}
	if !strings.Contains(surface.downloadMessage, "Download started") {
		t.Fatalf("download message = %q", surface.downloadMessage)
	}
}

// TestBrowserDownloadCancelAndExportKeepThePage drives the surface's download
// controls: cancelling keeps the page, and export goes through the seam, with
// a cancel distinguished from a failure.
func TestBrowserDownloadCancelAndExportKeepThePage(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	defer surface.close()
	page := surface.ensurePage("tab-1").(*fakeBrowserPage)

	completed := appbrowser.Download{
		ID: "d2", Filename: "report.pdf", SavePath: "/app/downloads/report.pdf",
		Status: appbrowser.DownloadCompleted,
	}
	active := appbrowser.Download{ID: "d1", Filename: "slow.bin", Status: appbrowser.DownloadProgressing}
	browser.mu.Lock()
	browser.downloadHistory = []appbrowser.Download{active, completed}
	browser.exportPath = "/shared/report.pdf"
	browser.mu.Unlock()

	surface.exportDownload(completed)
	browser.mu.Lock()
	exported := append([]appbrowser.Download(nil), browser.exported...)
	browser.mu.Unlock()
	if len(exported) != 1 || exported[0].ID != "d2" {
		t.Fatalf("exported = %v, want the completed download", exported)
	}
	if surface.downloadProblem || !strings.Contains(surface.downloadMessage, "Exported report.pdf") {
		t.Fatalf("export message = %q problem=%v", surface.downloadMessage, surface.downloadProblem)
	}

	surface.cancelDownload("d1")
	browser.mu.Lock()
	cancelled := append([]string(nil), browser.cancelled...)
	browser.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0] != "d1" {
		t.Fatalf("cancelled = %v, want the active download", cancelled)
	}
	if surface.downloadProblem || !strings.Contains(surface.downloadMessage, "Download cancelled") {
		t.Fatalf("cancel message = %q problem=%v", surface.downloadMessage, surface.downloadProblem)
	}
	if page.isDestroyed() {
		t.Fatal("cancelling a download destroyed the page")
	}

	browser.mu.Lock()
	browser.exportPath = ""
	browser.mu.Unlock()
	surface.exportDownload(completed)
	if surface.downloadProblem || !strings.Contains(surface.downloadMessage, "Export cancelled") {
		t.Fatalf("a cancelled export message = %q problem=%v", surface.downloadMessage, surface.downloadProblem)
	}

	browser.mu.Lock()
	browser.exportErr = errors.New("the save picker is unavailable")
	browser.mu.Unlock()
	surface.exportDownload(completed)
	if !surface.downloadProblem || !strings.Contains(surface.downloadMessage, "Could not export") {
		t.Fatalf("a failed export message = %q problem=%v", surface.downloadMessage, surface.downloadProblem)
	}
	if page.isDestroyed() {
		t.Fatal("a failed export destroyed the page")
	}
}

// TestClosingBrowserSurfaceReleasesItsPages is the lifetime rule: closing the
// surface destroys every page and the panel's gateway and lease before the
// Surface is forgotten. A closed surface never makes another page.
func TestClosingBrowserSurfaceReleasesItsPages(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	page := surface.ensurePage("tab-1").(*fakeBrowserPage)

	surface.close()
	surface.close()

	if !page.isDestroyed() {
		t.Fatal("closing the surface left a page alive")
	}
	if !browser.isClosed() {
		t.Fatal("closing the surface did not release the panel's gateway and lease")
	}
	if got := surface.ensurePage("tab-1"); got != nil {
		t.Fatal("a closed surface made another page")
	}
}

// TestClosingTheShellReleasesHostBrowsers proves the app-lifetime rule: the
// root's Close gives up every Host-detail Browser, not only the Session's
// Tools surfaces.
func TestClosingTheShellReleasesHostBrowsers(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)
	openHostDetail(t, fixture, "prod box")
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v", err)
	}
	fixture.settle()
	browser := provider.lastBrowser(t)

	fixture.shell.Close()

	if !browser.isClosed() {
		t.Fatal("the shell kept a Host Browser's gateway and lease after Close")
	}
}

// TestBrowserSurvivesBackgroundAndClosesWithTheShell records the lifetime
// rule: backgrounding suspends the app but keeps the Browser's gateway and
// dedicated lease so the page resumes, exactly as it keeps a Session's
// transport; only closing the Browser or the shell releases them.
func TestBrowserSurvivesBackgroundAndClosesWithTheShell(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)
	openHostDetail(t, fixture, "prod box")
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v", err)
	}
	fixture.settle()
	browser := provider.lastBrowser(t)

	fixture.shell.Background()
	if browser.isClosed() {
		t.Fatal("backgrounding released the Browser's gateway and lease")
	}
	fixture.shell.Foreground()
	if browser.isClosed() {
		t.Fatal("returning to the foreground lost the Browser")
	}

	fixture.shell.Close()
	if !browser.isClosed() {
		t.Fatal("closing the shell kept the Browser's gateway and lease")
	}
}

// TestBrowserDownloadControlsExportAndCancel drives the native controls the
// user actually taps: a completed file exports through the system path and an
// active one cancels, each with its own notice.
func TestBrowserDownloadControlsExportAndCancel(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)
	openHostDetail(t, fixture, "prod box")
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v", err)
	}
	fixture.settle()

	browser := provider.lastBrowser(t)
	browser.mu.Lock()
	browser.downloadHistory = []appbrowser.Download{
		{ID: "d1", Filename: "slow.bin", Status: appbrowser.DownloadProgressing},
		{ID: "d2", Filename: "report.pdf", SavePath: "/app/downloads/report.pdf",
			Status: appbrowser.DownloadCompleted},
	}
	browser.exportPath = "/shared/report.pdf"
	browser.mu.Unlock()
	fixture.tester.Frame()

	if err := fixture.tester.Click("Export download report.pdf"); err != nil {
		t.Fatalf("export the download: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	browser.mu.Lock()
	exported := append([]appbrowser.Download(nil), browser.exported...)
	browser.mu.Unlock()
	if len(exported) != 1 || exported[0].ID != "d2" {
		t.Fatalf("exported = %v, want the completed download", exported)
	}
	if !fixture.tester.HasText("Exported report.pdf.") {
		t.Fatalf("the export notice is missing; texts: %q", fixture.tester.Texts())
	}

	if err := fixture.tester.Click("Cancel download slow.bin"); err != nil {
		t.Fatalf("cancel the download: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	browser.mu.Lock()
	cancelled := append([]string(nil), browser.cancelled...)
	browser.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0] != "d1" {
		t.Fatalf("cancelled = %v, want the active download", cancelled)
	}
	if !fixture.tester.HasText("Download cancelled.") {
		t.Fatalf("the cancel notice is missing; texts: %q", fixture.tester.Texts())
	}
}

// TestBrowserBlankTabReleasesItsPage proves a page never outlives the site it
// was made for: a tab that loses its address also loses the page, so a later
// site never writes into the old site's store.
func TestBrowserBlankTabReleasesItsPage(t *testing.T) {
	browser := newFakeBrowser(domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	surface := newIsolationSurface(t, browser)
	defer surface.close()

	page := surface.ensurePage("tab-1").(*fakeBrowserPage)
	browser.mu.Lock()
	browser.state.Tabs[0].URL = ""
	browser.mu.Unlock()

	if got := surface.ensurePage("tab-1"); got != nil {
		t.Fatal("a blank tab got a page")
	}
	if !page.isDestroyed() {
		t.Fatal("a blank tab kept the old site's page")
	}
}
