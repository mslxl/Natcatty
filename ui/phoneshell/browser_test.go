package phoneshell

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeBrowserProvider is the shell's Browser seam in tests. It answers the
// capability the test wants and records every Host a Browser was opened for.
type fakeBrowserProvider struct {
	capability BrowserCapability
	openErr    error

	mu       sync.Mutex
	opens    []domainvault.Host
	windows  []*mygo.Window
	browsers []*fakeBrowser
}

var _ BrowserProvider = (*fakeBrowserProvider)(nil)

func newFakeBrowserProvider() *fakeBrowserProvider {
	return &fakeBrowserProvider{capability: BrowserCapability{Supported: true}}
}

func (p *fakeBrowserProvider) Capability() BrowserCapability { return p.capability }

func (p *fakeBrowserProvider) Open(host domainvault.Host, window *mygo.Window) (Browser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens = append(p.opens, host)
	p.windows = append(p.windows, window)
	if p.openErr != nil {
		return nil, p.openErr
	}
	browser := newFakeBrowser(host)
	p.browsers = append(p.browsers, browser)
	return browser, nil
}

func (p *fakeBrowserProvider) opened() []domainvault.Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domainvault.Host(nil), p.opens...)
}

func (p *fakeBrowserProvider) lastBrowser(t *testing.T) *fakeBrowser {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.browsers) == 0 {
		t.Fatal("no browser was opened")
	}
	return p.browsers[len(p.browsers)-1]
}

// fakeBrowser is the shared panel as the phone UI drives it. The gateway URL
// is a loopback origin, so the guard's decisions can be checked directly.
type fakeBrowser struct {
	host domainvault.Host

	mu              sync.Mutex
	state           appbrowser.State
	subs            []func(appbrowser.State)
	started         bool
	closed          bool
	gateway         string
	pages           []*fakeBrowserPage
	pageSites       []string
	navigations     []string
	downloads       []string
	downloadHistory []appbrowser.Download
	cancelled       []string
	exported        []appbrowser.Download
	exportPath      string
	exportErr       error
	stopped         []string
	nextTab         int
}

var _ Browser = (*fakeBrowser)(nil)

func newFakeBrowser(host domainvault.Host) *fakeBrowser {
	gateway := "http://127.0.0.2:55123/"
	id := "tab-1"
	return &fakeBrowser{
		host:    host,
		gateway: gateway,
		nextTab: 1,
		state: appbrowser.State{
			HostID:   host.ID,
			Label:    host.Label,
			Route:    appbrowser.RouteConnected,
			ActiveID: id,
			Tabs: []appbrowser.Tab{{
				ID:  id,
				URL: "https://" + host.Hostname + "/",
			}},
		},
	}
}

func (b *fakeBrowser) Start() {
	b.mu.Lock()
	b.started = true
	b.mu.Unlock()
}

func (b *fakeBrowser) Retry() bool { return true }

func (b *fakeBrowser) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *fakeBrowser) State() appbrowser.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *fakeBrowser) Subscribe(fn func(appbrowser.State)) func() {
	b.mu.Lock()
	index := len(b.subs)
	b.subs = append(b.subs, fn)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if index < len(b.subs) {
			b.subs[index] = nil
		}
	}
}

func (b *fakeBrowser) emit() {
	b.mu.Lock()
	state := b.state
	subs := append([]func(appbrowser.State){}, b.subs...)
	b.mu.Unlock()
	for _, fn := range subs {
		if fn != nil {
			fn(state)
		}
	}
}

func (b *fakeBrowser) Navigate(tabID, address string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.state.Tabs {
		if b.state.Tabs[i].ID == tabID {
			b.navigations = append(b.navigations, address)
			b.state.Tabs[i].URL = address
			b.state.ActiveID = tabID
			break
		}
	}
	return b.gateway, nil
}

func (b *fakeBrowser) NewTab() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextTab++
	id := fmt.Sprintf("tab-%d", b.nextTab)
	b.state.Tabs = append(b.state.Tabs, appbrowser.Tab{ID: id})
	b.state.ActiveID = id
	return id, nil
}

func (b *fakeBrowser) CloseTab(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.state.Tabs {
		if b.state.Tabs[i].ID != id {
			continue
		}
		b.state.Tabs = append(b.state.Tabs[:i], b.state.Tabs[i+1:]...)
		if len(b.state.Tabs) > 0 {
			b.state.ActiveID = b.state.Tabs[0].ID
		} else {
			b.state.ActiveID = ""
		}
		return true
	}
	return false
}

func (b *fakeBrowser) Activate(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tab := range b.state.Tabs {
		if tab.ID == id {
			b.state.ActiveID = id
			return true
		}
	}
	return false
}

func (b *fakeBrowser) GatewayURL(tabID string) (string, error) {
	return b.gateway, nil
}

// IsGatewayURL matches the real panel's exact-origin rule: the scheme, host,
// and port must all be this browser's gateway, not merely another loopback
// address.
func (b *fakeBrowser) IsGatewayURL(rawURL string) bool {
	b.mu.Lock()
	gateway := b.gateway
	b.mu.Unlock()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	want, err := url.Parse(gateway)
	if err != nil {
		return false
	}
	return parsed.Scheme == want.Scheme && parsed.Host == want.Host
}

func (b *fakeBrowser) PageChanged(tabID string, page appbrowser.PageState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.state.Tabs {
		if b.state.Tabs[i].ID != tabID {
			continue
		}
		if page.URL != "" {
			b.state.Tabs[i].URL = page.URL
		}
		if page.Title != "" {
			b.state.Tabs[i].Title = page.Title
		}
		b.state.Tabs[i].Loading = page.Loading
		b.state.Tabs[i].CanGoBack = page.CanGoBack
		b.state.Tabs[i].CanGoForward = page.CanGoForward
	}
}

func (b *fakeBrowser) PageFailed(tabID, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Route = appbrowser.RouteDisconnected
	b.state.Failure = appbrowser.FailurePage
	b.state.Detail = detail
}

func (b *fakeBrowser) PageStopped(tabID string) bool {
	b.mu.Lock()
	b.stopped = append(b.stopped, tabID)
	b.mu.Unlock()
	return true
}

func (b *fakeBrowser) SetPageFlags(tabID string, canBack, canForward, loading bool) {
}

func (b *fakeBrowser) StartDownload(gatewayURL, suggestedName string) (appbrowser.Download, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.downloads = append(b.downloads, gatewayURL)
	return appbrowser.Download{URL: gatewayURL, Filename: suggestedName}, nil
}

func (b *fakeBrowser) Downloads() []appbrowser.Download {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]appbrowser.Download(nil), b.downloadHistory...)
}

func (b *fakeBrowser) CancelDownload(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, item := range b.downloadHistory {
		if item.ID == id {
			b.cancelled = append(b.cancelled, id)
			return true
		}
	}
	return false
}

func (b *fakeBrowser) ExportDownload(download appbrowser.Download) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.exported = append(b.exported, download)
	return b.exportPath, b.exportErr
}

func (b *fakeBrowser) NewPage(siteOrigin string) (BrowserPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	page := &fakeBrowserPage{}
	b.pages = append(b.pages, page)
	b.pageSites = append(b.pageSites, siteOrigin)
	return page, nil
}

func (b *fakeBrowser) lastPage(t *testing.T) *fakeBrowserPage {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pages) == 0 {
		t.Fatal("no browser page was made")
	}
	return b.pages[len(b.pages)-1]
}

func (b *fakeBrowser) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *fakeBrowser) wasStarted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

// fakeBrowserPage records what the surface loaded and the guard it installed.
type fakeBrowserPage struct {
	mu        sync.Mutex
	loaded    []string
	guard     func(string) (string, bool)
	events    []func(BrowserPageEvent)
	destroyed bool
}

var _ BrowserPage = (*fakeBrowserPage)(nil)

func (p *fakeBrowserPage) Native() ui.NativeWebView { return nil }

func (p *fakeBrowserPage) Load(rawURL string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loaded = append(p.loaded, rawURL)
	return nil
}

func (p *fakeBrowserPage) Reload()            {}
func (p *fakeBrowserPage) Stop()              {}
func (p *fakeBrowserPage) Back()              {}
func (p *fakeBrowserPage) Forward()           {}
func (p *fakeBrowserPage) CanGoBack() bool    { return false }
func (p *fakeBrowserPage) CanGoForward() bool { return false }
func (p *fakeBrowserPage) Loading() bool      { return false }
func (p *fakeBrowserPage) URL() string        { return "" }

func (p *fakeBrowserPage) Destroy() {
	p.mu.Lock()
	p.destroyed = true
	p.mu.Unlock()
}

func (p *fakeBrowserPage) OnEvent(fn func(BrowserPageEvent)) func() {
	p.mu.Lock()
	p.events = append(p.events, fn)
	p.mu.Unlock()
	return func() {}
}

func (p *fakeBrowserPage) SetNavigationGuard(fn func(string) (string, bool)) {
	p.mu.Lock()
	p.guard = fn
	p.mu.Unlock()
}

func (p *fakeBrowserPage) loadedURLs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.loaded...)
}

func (p *fakeBrowserPage) isDestroyed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.destroyed
}

// Note: Native must return ui.NativeWebView. It is declared on the concrete
// type below so the fake stays readable.

// openHostDetail clicks the Hosts list's info button and settles the push.
func openHostDetail(t *testing.T, fixture *phoneFixture, label string) {
	t.Helper()
	if err := fixture.tester.Click("Details for " + label); err != nil {
		t.Fatalf("open %s details: %v; texts: %q", label, err, fixture.tester.Texts())
	}
	fixture.settle()
}

// newBrowserFixture wires a fake Browser provider into the standard phone
// fixture.
func newBrowserFixture(t *testing.T, provider BrowserProvider) (*phoneFixture, *fakeOpener) {
	t.Helper()
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener, WithBrowserProvider(provider))
	return fixture, opener
}

// TestVaultHostBrowserStaysOnTheVaultStack proves the shared Host detail page
// opens the Browser in the section that owns the page: from Vault the push is
// /hosts/{id}/browser, so the Hosts tab's stack is untouched.
func TestVaultHostBrowserStaysOnTheVaultStack(t *testing.T) {
	provider := newFakeBrowserProvider()
	store := &fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}
	shell, tt := openVaultHosts(t, store, WithBrowserProvider(provider))

	if err := tt.Click("Details for prod box"); err != nil {
		t.Fatalf("open prod details: %v; texts: %q", err, tt.Texts())
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v; texts: %q", err, tt.Texts())
	}
	tt.Advance(600 * time.Millisecond)
	if got := shell.Router(Vault).Path(); got != "/hosts/prod/browser" {
		t.Fatalf("Vault path = %q, want the Vault Browser page", got)
	}
	if got := shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("the Hosts stack moved to %q, want its root", got)
	}
	if opened := provider.opened(); len(opened) != 1 || opened[0].ID != "prod" {
		t.Fatalf("Browser opened for %+v, want only prod", opened)
	}
}

// TestHostBrowserEntryOpensTheHostsOwnGateway is the entry acceptance: an
// eligible SSH Host's detail page offers the Browser, tapping it pushes the
// Browser page, the provider is opened for that Host, and the web area loads
// the loopback gateway URL rather than the remote page.
func TestHostBrowserEntryOpensTheHostsOwnGateway(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)

	openHostDetail(t, fixture, "prod box")
	if !fixture.tester.HasText("Open Browser for prod box") {
		t.Fatalf("an eligible SSH Host has no Browser entry; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.settle()

	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/browser" {
		t.Fatalf("Hosts path = %q, want the pushed Browser page", got)
	}
	opened := provider.opened()
	if len(opened) != 1 || opened[0].ID != "prod" {
		t.Fatalf("Browser opened for %+v, want only prod", opened)
	}
	browser := provider.lastBrowser(t)
	if !browser.wasStarted() {
		t.Fatal("the Browser was not started")
	}
	loaded := browser.lastPage(t).loadedURLs()
	if len(loaded) == 0 || loaded[0] != browser.gateway {
		t.Fatalf("web area loaded %v, want the loopback gateway %q", loaded, browser.gateway)
	}
	if strings.Contains(strings.Join(loaded, " "), "prod.example.com") {
		t.Fatalf("the web area loaded the remote page directly: %v", loaded)
	}
}

// TestHostBrowserHiddenWithoutCapability proves the fail-closed rule: a device
// whose WebView cannot keep per-Host profiles states MyGo's reason, and neither
// the Host detail page nor a Session's Tools surface offers the entry.
func TestHostBrowserHiddenWithoutCapability(t *testing.T) {
	const reason = "mygo: this device's WebView has no profiles"
	provider := &fakeBrowserProvider{capability: BrowserCapability{Reason: reason}}
	fixture, _ := newBrowserFixture(t, provider)

	openHostDetail(t, fixture, "prod box")
	if fixture.tester.HasText("Open Browser for prod box") {
		t.Fatal("a Browser entry was offered without the device capability")
	}
	if !fixture.tester.HasText(reason) {
		t.Fatalf("the capability reason is not stated; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Back"); err != nil {
		t.Fatalf("leave the Host detail page: %v", err)
	}
	fixture.settle()

	fixture.openSession(t, "prod box")
	fixture.openToolsPanel(t, "prod box")
	if fixture.tester.HasText("Open Browser") {
		t.Fatal("the Tools surface offered a Browser the device cannot run")
	}
	if !fixture.tester.HasText(reason) {
		t.Fatalf("the Tools catalog does not state the reason; texts: %q", fixture.tester.Texts())
	}
}

// TestHostBrowserAbsentForNonForwardingHosts keeps the entry off Telnet and
// Local Hosts: they have no channel a browser could tunnel through.
func TestHostBrowserAbsentForNonForwardingHosts(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)

	openHostDetail(t, fixture, "console")
	if fixture.tester.HasText("Open Browser for console") {
		t.Fatalf("a Telnet Host offered the Browser; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Back"); err != nil {
		t.Fatalf("leave the Host detail page: %v", err)
	}
	fixture.settle()

	fixture.openSession(t, "console")
	fixture.openToolsPanel(t, "console")
	if fixture.tester.HasText("Open Browser") {
		t.Fatalf("a Telnet Session's Tools offered the Browser; texts: %q", fixture.tester.Texts())
	}
}

// TestBrowserNavigationGuardRefusesEveryNonGatewayDestination is the
// destination acceptance at the page seam: a gateway URL is allowed, another
// HTTP(S) address is remapped through the panel, and an external-app or
// script scheme is refused with no replacement. No remote URL ever reaches the
// WebView, and the page has no route to the product's bridge.
func TestBrowserNavigationGuardRefusesEveryNonGatewayDestination(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)
	openHostDetail(t, fixture, "prod box")
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v", err)
	}
	fixture.settle()

	browser := provider.lastBrowser(t)
	page := browser.lastPage(t)
	page.mu.Lock()
	guard := page.guard
	page.mu.Unlock()
	if guard == nil {
		t.Fatal("the page has no navigation guard")
	}

	if replacement, handled := guard(browser.gateway + "index"); handled || replacement != "" {
		t.Fatalf("a gateway URL was refused: replacement=%q handled=%v", replacement, handled)
	}
	// A navigation within the page's own site maps to the gateway and loads
	// there.
	replacement, handled := guard("https://prod.example.com/login")
	if !handled || replacement != browser.gateway {
		t.Fatalf("same-site URL mapped to %q handled=%v, want the gateway", replacement, handled)
	}
	// A navigation to another origin is cancelled here: the surface swaps in
	// a page of that site's own store rather than loading it into the
	// current site's cookies.
	if replacement, handled := guard("https://example.com/login"); !handled || replacement != "" {
		t.Fatalf("cross-site URL = %q handled=%v, want a cancel for the surface", replacement, handled)
	}
	browser.mu.Lock()
	navigations := append([]string(nil), browser.navigations...)
	browser.mu.Unlock()
	if len(navigations) != 2 ||
		navigations[0] != "https://prod.example.com/login" ||
		navigations[1] != "https://example.com/login" {
		t.Fatalf("panel navigations = %v, want each HTTP(S) link routed once", navigations)
	}
	// A lookalike loopback origin (another port, scheme, or host) is not this
	// gateway either, so it goes through the same exact-origin decision.
	for _, lookalike := range []string{
		"http://127.0.0.2:1/",
		"https://127.0.0.2:55123/",
		"http://127.0.0.1:55123/",
	} {
		if replacement, handled := guard(lookalike); !handled || replacement != "" {
			t.Fatalf("lookalike gateway %q = %q handled=%v, want a cancel", lookalike, replacement, handled)
		}
	}
	for _, refused := range []string{"ssh://host.example", "javascript:alert(1)", "file:///etc/passwd", "mailto:a@example.com"} {
		if replacement, handled := guard(refused); !handled || replacement != "" {
			t.Fatalf("%q was allowed: replacement=%q handled=%v", refused, replacement, handled)
		}
	}
	browser.mu.Lock()
	after := len(browser.navigations)
	browser.mu.Unlock()
	if after != 5 {
		t.Fatalf("a non-HTTP navigation reached the panel: %d navigations", after)
	}
}

// TestBrowserDownloadStaysInsideTheGateway hands a download navigation to the
// shared manager only after the provisional tab is settled, and the URL it
// forwards is the gateway URL.
func TestBrowserDownloadStaysInsideTheGateway(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)
	openHostDetail(t, fixture, "prod box")
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v", err)
	}
	fixture.settle()

	browser := provider.lastBrowser(t)
	page := browser.lastPage(t)
	page.mu.Lock()
	events := append([]func(BrowserPageEvent){}, page.events...)
	page.mu.Unlock()
	if len(events) == 0 {
		t.Fatal("the page has no event listener")
	}
	events[0](BrowserPageEvent{
		Kind:          BrowserPageDownload,
		URL:           browser.gateway + "file",
		SuggestedName: "report.pdf",
	})
	browser.mu.Lock()
	downloads := append([]string(nil), browser.downloads...)
	browser.mu.Unlock()
	if len(downloads) != 1 || downloads[0] != browser.gateway+"file" {
		t.Fatalf("downloads = %v, want the gateway URL", downloads)
	}
}

// TestClosingBrowserLeavesTheSessionUsable is the close acceptance: the
// Browser is opened from a ready Session's Tools surface, the terminal stays
// attached throughout, and closing the Browser returns to the same Pane with
// its terminal still live and its scroll position intact.
func TestClosingBrowserLeavesTheSessionUsable(t *testing.T) {
	provider := newFakeBrowserProvider()
	fixture, _ := newBrowserFixture(t, provider)
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})
	pane := fixture.focusedPane()
	screen := fixture.focusedScreen(t)
	screen.scrolled = true
	path := fixture.shell.Router(Sessions).Path()

	fixture.openToolsPanel(t, "prod box")
	if !fixture.tester.HasText("Open Browser") {
		t.Fatalf("the Tools catalog lacks the Browser; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Open Browser"); err != nil {
		t.Fatalf("open the Browser tool: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	browser := provider.lastBrowser(t)
	if !browser.wasStarted() {
		t.Fatal("the Browser was not started")
	}
	if !handle.isAttached() {
		t.Fatal("opening the Browser detached the Session")
	}

	if err := fixture.tester.Click("Close Browser"); err != nil {
		t.Fatalf("close the Browser: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if !browser.isClosed() {
		t.Fatal("closing the Browser did not release its panel and lease")
	}
	if got := fixture.shell.Router(Sessions).Path(); got != path {
		t.Fatalf("closing the Browser changed the route to %q, want %q", got, path)
	}
	if !fixture.tester.HasText("remote prompt $") {
		t.Fatalf("the terminal did not return after closing the Browser; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.focusedPane(); got == nil || got.id != pane.id {
		t.Fatalf("focused Pane changed after closing the Browser: %v", got)
	}
	if !handle.isAttached() {
		t.Fatal("closing the Browser ended or detached the Session")
	}
	if !screen.scrolled {
		t.Fatal("closing the Browser lost the terminal's scroll position")
	}
}

// TestBrowserOpenFailureIsReported keeps a failed panel construction local to
// the Browser page: the Host detail page reports it and stays usable.
func TestBrowserOpenFailureIsReported(t *testing.T) {
	provider := newFakeBrowserProvider()
	provider.openErr = errors.New("no connection manager")
	fixture, _ := newBrowserFixture(t, provider)

	openHostDetail(t, fixture, "prod box")
	if err := fixture.tester.Click("Open Browser for prod box"); err != nil {
		t.Fatalf("open the Browser: %v", err)
	}
	fixture.settle()
	if !fixture.tester.HasText("Could not open the Browser") {
		t.Fatalf("the open failure is not shown; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Back"); err != nil {
		t.Fatalf("Back from the failed Browser page: %v", err)
	}
	fixture.settle()
	if !fixture.tester.HasText("Open Browser for prod box") {
		t.Fatalf("the Host detail page is no longer usable; texts: %q", fixture.tester.Texts())
	}
}
