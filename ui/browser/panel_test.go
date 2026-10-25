package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeTunnel stands in for one dedicated Host transport. Closing done is a
// tunnel disconnect; Discard counts the panel's teardown.
type fakeTunnel struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	disc int
}

func newFakeTunnel() *fakeTunnel { return &fakeTunnel{done: make(chan struct{})} }

func (t *fakeTunnel) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("the UI tests do not route HTTP")
}

func (t *fakeTunnel) Done() <-chan struct{} { return t.done }

func (t *fakeTunnel) Discard() {
	t.mu.Lock()
	t.disc++
	t.mu.Unlock()
	t.disconnect()
}

func (t *fakeTunnel) disconnect() { t.once.Do(func() { close(t.done) }) }

// fakeConnector opens one fake tunnel per Connect, or returns queued errors.
type fakeConnector struct {
	mu       sync.Mutex
	failures []error
	tunnels  []*fakeTunnel
	calls    int
	hosts    []model.HostID
}

func (c *fakeConnector) Connect(_ context.Context, hostID model.HostID) (appbrowser.Tunnel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if len(c.failures) > 0 {
		err := c.failures[0]
		c.failures = c.failures[1:]
		return nil, err
	}
	tunnel := newFakeTunnel()
	c.tunnels = append(c.tunnels, tunnel)
	c.hosts = append(c.hosts, hostID)
	return tunnel, nil
}

func (c *fakeConnector) last() *fakeTunnel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tunnels) == 0 {
		return nil
	}
	return c.tunnels[len(c.tunnels)-1]
}

func (c *fakeConnector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// fakePage records every command and reports events the test triggers.
type fakePage struct {
	mu         sync.Mutex
	pages      *fakePages
	listeners  []func(PageEvent)
	loads      []string
	guards     func(string) (string, bool)
	url        string
	canBack    bool
	canForward bool
	loading    bool
	backs      int
	forwards   int
	reloads    int
	stops      int
	destroyed  bool
}

func (p *fakePage) Native() ui.NativeWebView { return nil }

func (p *fakePage) Load(rawURL string) error {
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return nil
	}
	p.loads = append(p.loads, rawURL)
	p.url = rawURL
	p.loading = true
	p.mu.Unlock()
	p.emit(PageEvent{Kind: PageNavigated, URL: rawURL})
	return nil
}

func (p *fakePage) Reload() {
	p.mu.Lock()
	p.reloads++
	p.mu.Unlock()
}

func (p *fakePage) Stop() {
	p.mu.Lock()
	p.stops++
	// Native WebView2 reports a stopped load as an aborted navigation and
	// leaves IsLoading true until something clears it, so the fake must not
	// hide that from the panel.
	p.mu.Unlock()
}

func (p *fakePage) Back() {
	p.mu.Lock()
	p.backs++
	p.mu.Unlock()
}

func (p *fakePage) Forward() {
	p.mu.Lock()
	p.forwards++
	p.mu.Unlock()
}

func (p *fakePage) CanGoBack() bool    { p.mu.Lock(); defer p.mu.Unlock(); return p.canBack }
func (p *fakePage) CanGoForward() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.canForward }
func (p *fakePage) Loading() bool      { p.mu.Lock(); defer p.mu.Unlock(); return p.loading }
func (p *fakePage) URL() string        { p.mu.Lock(); defer p.mu.Unlock(); return p.url }

func (p *fakePage) Destroy() {
	p.mu.Lock()
	p.destroyed = true
	p.mu.Unlock()
}

func (p *fakePage) OnEvent(fn func(PageEvent)) func() {
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

func (p *fakePage) SetNavigationGuard(fn func(string) (string, bool)) {
	p.mu.Lock()
	p.guards = fn
	p.mu.Unlock()
}

func (p *fakePage) guard(rawURL string) (string, bool) {
	p.mu.Lock()
	guard := p.guards
	p.mu.Unlock()
	if guard == nil {
		return "", false
	}
	return guard(rawURL)
}

func (p *fakePage) emit(event PageEvent) {
	p.mu.Lock()
	listeners := append([]func(PageEvent){}, p.listeners...)
	p.mu.Unlock()
	for _, fn := range listeners {
		if fn != nil {
			fn(event)
		}
	}
}

func (p *fakePage) setFlags(canBack, canForward, loading bool, emit bool) {
	p.mu.Lock()
	p.canBack = canBack
	p.canForward = canForward
	p.loading = loading
	p.mu.Unlock()
	if emit {
		p.emit(PageEvent{Kind: PageLoading})
	}
}

func (p *fakePage) loadsCopy() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.loads...)
}

func (p *fakePage) isDestroyed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.destroyed
}

// fakePages hands out recording pages.
type fakePages struct {
	mu    sync.Mutex
	pages []*fakePage
	err   error
}

func (f *fakePages) NewPage() (Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	page := &fakePage{pages: f}
	f.pages = append(f.pages, page)
	return page, nil
}

func (f *fakePages) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pages)
}

func (f *fakePages) at(index int) *fakePage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.pages) {
		return nil
	}
	return f.pages[index]
}

// harness owns one mounted panel and its recording collaborators.
type harness struct {
	panel     *Panel
	core      *appbrowser.Panel
	connector *fakeConnector
	pages     *fakePages
	tester    *ui.Tester
	address   string
}

func newHarness(t *testing.T, locale string, width, height int) *harness {
	t.Helper()
	connector := &fakeConnector{}
	pages := &fakePages{}
	core := appbrowser.New(appbrowser.Config{
		HostID:    "host-1",
		Label:     "web-1",
		Connector: connector,
	})
	panel := New(core, pages)
	t.Cleanup(panel.Close)
	tester := ui.NewTester(func(c *ui.Context) { panel.View(c) }, width, height)
	tester.SetLocale(locale)
	tester.Frame()
	return &harness{
		panel: panel, core: core, connector: connector, pages: pages,
		tester: tester, address: locales.In(locale).BrowserAddress(),
	}
}

func (h *harness) waitRoute(t *testing.T, route appbrowser.Route) {
	t.Helper()
	waitFor(t, "the browser route to become "+string(route), func() bool {
		return h.core.State().Route == route
	})
}

func (h *harness) frame() { h.tester.Frame() }

func (h *harness) state() appbrowser.State { return h.core.State() }

func (h *harness) navigate(t *testing.T, address string) {
	t.Helper()
	h.tester.Key(ui.Cmd, ui.KeyL)
	h.tester.Frame()
	if !h.tester.Focused(h.address) {
		t.Fatalf("Ctrl+L did not focus the address bar; texts %q", h.tester.Texts())
	}
	h.tester.Type(address)
	if h.panel.address != address {
		t.Fatalf("typed address = %q, want %q", h.panel.address, address)
	}
	h.tester.Key(0, ui.KeyEnter)
}

func (h *harness) activeTab(t *testing.T) appbrowser.Tab {
	t.Helper()
	tab, ok := h.core.ActiveTab()
	if !ok {
		t.Fatalf("no active tab in %+v", h.state())
	}
	return tab
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func assertVisible(t *testing.T, tester *ui.Tester, text string, width, height int) {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok {
		t.Fatalf("%q is not rendered; texts %q", text, tester.Texts())
	}
	if rect.X < -0.5 || rect.Y < -0.5 ||
		rect.X+rect.W > float32(width)+0.5 || rect.Y+rect.H > float32(height)+0.5 {
		t.Fatalf("%q is outside the %dx%d panel: %+v", text, width, height, rect)
	}
}

func TestPanelOpensFromASessionOnItsDedicatedTunnel(t *testing.T) {
	h := newHarness(t, "en", 448, 421)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()

	state := h.state()
	if len(state.Tabs) != 1 || state.ActiveID == "" {
		t.Fatalf("connected panel tabs = %+v", state)
	}
	if h.connector.callCount() != 1 {
		t.Fatalf("connector calls = %d, want one dedicated tunnel", h.connector.callCount())
	}
	if len(h.connector.hosts) != 1 || h.connector.hosts[0] != "host-1" {
		t.Fatalf("connector hosts = %v, want host-1", h.connector.hosts)
	}
	if !h.tester.HasText("Browse through this Host") {
		t.Fatalf("the empty panel is not shown; texts %q", h.tester.Texts())
	}
	for _, label := range []string{"Back", "Forward", "Reload", "Stop", "Address", "New tab", "Close browser"} {
		if _, ok := h.tester.Find(label); !ok {
			t.Fatalf("%q control is missing; texts %q", label, h.tester.Texts())
		}
	}
	if h.pages.count() != 0 {
		t.Fatalf("a blank tab created %d pages", h.pages.count())
	}
}

func TestPanelNavigationUsesHTTPForLocalAndHTTPSForPublicHosts(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()

	h.navigate(t, "localhost:8080/status")
	h.frame()
	waitFor(t, "the local page load", func() bool { return h.pages.count() == 1 })
	tab := h.activeTab(t)
	if tab.URL != "http://localhost:8080/status" {
		t.Fatalf("local tab URL = %q, want HTTP", tab.URL)
	}
	loads := h.pages.at(0).loadsCopy()
	if len(loads) != 1 || !strings.HasPrefix(loads[0], "http://127.0.0.1:") {
		t.Fatalf("local page loads = %q, want a loopback gateway URL", loads)
	}
	if !h.core.IsGatewayURL(loads[0]) {
		t.Fatalf("page load %q is not this panel's gateway URL", loads[0])
	}

	h.navigate(t, "example.com/docs")
	h.frame()
	waitFor(t, "the public page load", func() bool { return len(h.pages.at(0).loadsCopy()) == 2 })
	tab = h.activeTab(t)
	if tab.URL != "https://example.com/docs" {
		t.Fatalf("public tab URL = %q, want HTTPS", tab.URL)
	}
	loads = h.pages.at(0).loadsCopy()
	if !strings.HasPrefix(loads[1], "http://127.0.0.1:") || !h.core.IsGatewayURL(loads[1]) {
		t.Fatalf("public page load = %q, want a loopback gateway URL", loads[1])
	}
	if loads[1] == loads[0] {
		t.Fatal("two origins reused one gateway address")
	}
}

func TestPanelRefusesAnAddressOutsideHTTPAndHTTPS(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "localhost:8080")
	h.frame()
	waitFor(t, "the first page load", func() bool { return h.pages.count() == 1 })
	before := h.activeTab(t)

	h.navigate(t, "javascript:alert(1)")
	h.frame()
	if !h.tester.HasText("Only HTTP and HTTPS pages can open here.") {
		t.Fatalf("the scheme error is not shown; texts %q", h.tester.Texts())
	}
	if after := h.activeTab(t); after.URL != before.URL {
		t.Fatalf("a refused address changed the tab: %q -> %q", before.URL, after.URL)
	}
	if loads := h.pages.at(0).loadsCopy(); len(loads) != 1 {
		t.Fatalf("the refused address caused a page load: %q", loads)
	}

	h.navigate(t, "https://user"+":pw@"+"example.com/")
	h.frame()
	if !h.tester.HasText("Enter an address without a username or password.") {
		t.Fatalf("the credential error is not shown; texts %q", h.tester.Texts())
	}
}

func TestPanelControlsDriveThePage(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "localhost:8080")
	h.frame()
	waitFor(t, "the page", func() bool { return h.pages.count() == 1 })
	page := h.pages.at(0)
	page.setFlags(true, true, true, true)
	h.frame()

	if err := h.tester.Click("Back"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	if err := h.tester.Click("Forward"); err != nil {
		t.Fatalf("click Forward: %v", err)
	}
	if err := h.tester.Click("Reload"); err != nil {
		t.Fatalf("click Reload: %v", err)
	}
	if err := h.tester.Click("Stop"); err != nil {
		t.Fatalf("click Stop: %v", err)
	}
	page.mu.Lock()
	backs, forwards, reloads, stops := page.backs, page.forwards, page.reloads, page.stops
	page.mu.Unlock()
	if backs != 1 || forwards != 1 || reloads != 1 || stops != 1 {
		t.Fatalf("page commands = back %d forward %d reload %d stop %d", backs, forwards, reloads, stops)
	}
	h.frame()
	if h.activeTab(t).Loading {
		t.Fatal("Stop left the tab loading")
	}
	if !h.tester.HasText("http://localhost:8080") {
		t.Fatalf("the address is not shown; texts %q", h.tester.Texts())
	}
}

func TestPanelTabsActivateAndCloseTheirOwnPage(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "localhost:8080")
	h.frame()
	waitFor(t, "the first tab page", func() bool { return h.pages.count() == 1 })
	first := h.pages.at(0)
	first.emit(PageEvent{Kind: PageTitle, Title: "Docs"})
	h.frame()

	if err := h.tester.Click("+"); err != nil {
		t.Fatalf("click New tab: %v", err)
	}
	h.frame()
	state := h.state()
	if len(state.Tabs) != 2 {
		t.Fatalf("tabs after New tab = %+v", state.Tabs)
	}
	firstTabID := state.Tabs[0].ID
	if state.ActiveID == firstTabID {
		t.Fatal("a new tab did not become active")
	}

	h.navigate(t, "example.com")
	h.frame()
	waitFor(t, "the second tab page", func() bool { return h.pages.count() == 2 })
	secondTabID := h.activeTab(t).ID
	if secondTabID == firstTabID {
		t.Fatal("navigation switched back to the first tab")
	}

	if err := h.tester.Click("Docs"); err != nil {
		t.Fatalf("click the first tab: %v", err)
	}
	h.frame()
	if got := h.activeTab(t).ID; got != firstTabID {
		t.Fatalf("active tab = %q, want the Docs tab %q", got, firstTabID)
	}
	if !strings.Contains(h.tester.Texts()[0], "Address") {
		// The address input keeps a label even when it shows a value.
		t.Logf("texts %q", h.tester.Texts())
	}

	if err := h.tester.Click("Close tab"); err != nil {
		t.Fatalf("click Close tab: %v", err)
	}
	h.frame()
	state = h.state()
	if len(state.Tabs) != 1 || state.ActiveID != secondTabID {
		t.Fatalf("tabs after closing the first = %+v", state)
	}
	if !first.isDestroyed() {
		t.Fatal("closing a tab did not destroy its page")
	}
	if h.pages.at(1).isDestroyed() {
		t.Fatal("closing one tab destroyed another tab's page")
	}
}

func TestPanelDisconnectIsVisibleAndRetryOpensANewTunnelAndPage(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "localhost:8080")
	h.frame()
	waitFor(t, "the first page", func() bool { return h.pages.count() == 1 })
	first := h.pages.at(0)
	first.emit(PageEvent{Kind: PageTitle, Title: "Console"})
	tunnel := h.connector.last()
	if tunnel == nil {
		t.Fatal("no dedicated tunnel was recorded")
	}
	tunnel.disconnect()
	h.waitRoute(t, appbrowser.RouteDisconnected)
	h.frame()

	if !h.tester.HasText("Browser connection lost") {
		t.Fatalf("the disconnect is not shown; texts %q", h.tester.Texts())
	}
	if _, ok := h.tester.Find("Retry"); !ok {
		t.Fatalf("Retry is missing; texts %q", h.tester.Texts())
	}
	if !first.isDestroyed() {
		t.Fatal("a disconnect left the page alive on a dead tunnel")
	}
	if tab := h.activeTab(t); tab.Loading {
		t.Fatalf("a disconnected tab is still loading: %+v", tab)
	}
	if tab := h.activeTab(t); tab.URL != "http://localhost:8080/" {
		t.Fatalf("a disconnect lost the tab address: %+v", tab)
	}

	if err := h.tester.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	waitFor(t, "the retry page", func() bool { return h.pages.count() == 2 })
	if h.connector.callCount() != 2 {
		t.Fatalf("connector calls = %d, want a fresh tunnel on retry", h.connector.callCount())
	}
	loads := h.pages.at(1).loadsCopy()
	if len(loads) != 1 || !strings.HasPrefix(loads[0], "http://127.0.0.1:") {
		t.Fatalf("the retried page did not load through the new gateway: %q", loads)
	}
	if state := h.state(); len(state.Tabs) != 1 || state.Tabs[0].Title != "Console" {
		t.Fatalf("retry did not keep the tab: %+v", state)
	}
}

func TestPanelShowsConnectFailureAndRetryRecovers(t *testing.T) {
	connector := &fakeConnector{failures: []error{errors.New("host unreachable")}}
	pages := &fakePages{}
	core := appbrowser.New(appbrowser.Config{HostID: "host-1", Label: "web-1", Connector: connector})
	panel := New(core, pages)
	t.Cleanup(panel.Close)
	tester := ui.NewTester(func(c *ui.Context) { panel.View(c) }, 448, 421)
	tester.SetLocale("en")
	tester.Frame()
	waitFor(t, "the failed connect", func() bool {
		state := core.State()
		return state.Route == appbrowser.RouteDisconnected && state.Failure == appbrowser.FailureConnect
	})
	tester.Frame()
	if !tester.HasText("Could not connect through this Host") {
		t.Fatalf("the connect failure is not shown; texts %q", tester.Texts())
	}
	if err := tester.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	waitFor(t, "the recovered tunnel", func() bool { return core.State().Route == appbrowser.RouteConnected })
	tester.Frame()
	if !tester.HasText("Browse through this Host") {
		t.Fatalf("the recovered panel has no empty tab; texts %q", tester.Texts())
	}
	if got := connector.callCount(); got != 2 {
		t.Fatalf("connector calls = %d, want the failure plus a retry", got)
	}
}

func TestPanelSendsPageInitiatedNavigationBackThroughTheTunnel(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "localhost:8080")
	h.frame()
	waitFor(t, "the page", func() bool { return h.pages.count() == 1 })
	page := h.pages.at(0)

	gatewayURL := page.loadsCopy()[0]
	if replacement, handled := page.guard(gatewayURL); handled {
		t.Fatalf("a gateway navigation was blocked: replacement %q", replacement)
	}
	replacement, handled := page.guard("https://other.example/page")
	if !handled || !strings.HasPrefix(replacement, "http://127.0.0.1:") {
		t.Fatalf("page-initiated navigation = (%q, %t), want a gateway URL", replacement, handled)
	}
	if tab := h.activeTab(t); tab.URL != "https://other.example/page" {
		t.Fatalf("the guarded navigation did not update the tab: %+v", tab)
	}
	if _, handled := page.guard("file:///etc/passwd"); !handled {
		t.Fatal("a non-HTTP navigation was allowed")
	}
}

func TestPanelLayoutKeepsEveryControlReachable(t *testing.T) {
	m := locales.In("en")
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range []struct{ width, height int }{{448, 421}, {868, 688}} {
			t.Run(fmt.Sprintf("%s-%dx%d", locale, size.width, size.height), func(t *testing.T) {
				h := newHarness(t, locale, size.width, size.height)
				h.waitRoute(t, appbrowser.RouteConnected)
				h.frame()
				h.navigate(t, "example.com/docs")
				h.frame()
				waitFor(t, "the layout page", func() bool { return h.pages.count() == 1 })
				h.frame()

				labels := locales.In(locale)
				for _, text := range []string{
					labels.BrowserBack(), labels.BrowserForward(),
					labels.BrowserReload(), labels.BrowserStop(),
					labels.BrowserAddress(), labels.BrowserNewTab(),
					labels.BrowserClose(),
				} {
					assertVisible(t, h.tester, text, size.width, size.height)
				}
				if locale == "en" && m.BrowserBack() != labels.BrowserBack() {
					t.Fatalf("the English baseline is stale")
				}
				for _, text := range h.tester.Texts() {
					rect, ok := h.tester.Find(text)
					if !ok || rect.W <= 0 || rect.H <= 0 {
						continue
					}
					if rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 {
						t.Fatalf("%q overflows horizontally at %dx%d: %+v",
							text, size.width, size.height, rect)
					}
					if rect.Y < -0.5 || rect.Y+rect.H > float32(size.height)+0.5 {
						t.Fatalf("%q overflows vertically at %dx%d: %+v",
							text, size.width, size.height, rect)
					}
				}
				if err := h.tester.Click(labels.BrowserReload()); err != nil {
					t.Fatalf("the Reload control is not clickable at %dx%d: %v",
						size.width, size.height, err)
				}
			})
		}
	}
}

// TestPanelTabStripRendersTheActiveTabOnce guards the active tab's single
// primary button: building a normal button first and replacing the variable
// with a primary one would paint two overlapping tab names.
func TestPanelTabStripRendersTheActiveTabOnce(t *testing.T) {
	h := newHarness(t, "en", 720, 480)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	tab := h.activeTab(t)
	h.core.PageChanged(tab.ID, appbrowser.PageState{Title: "Solo tab"})
	h.frame()

	seen := 0
	for _, text := range h.tester.Texts() {
		if text == "Solo tab" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the active tab rendered %d times, want once: %q", seen, h.tester.Texts())
	}
}
