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
	// redraws counts the panel's requests to repaint the window. A stale
	// native child region can only be replaced by a repaint, so a locale or
	// size change must ask for one.
	redraws int
	// ctx is the context of the last build pass. Handle.Bounds reads committed
	// boxes through it between frames, which is how the minimum-window tests
	// measure controls that share a label, as the five close buttons do.
	ctx *ui.Context
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
	h := &harness{
		core: core, connector: connector, pages: pages,
		address: locales.In(locale).BrowserAddress(),
	}
	panel := New(core, pages, WithRedraw(func() { h.redraws++ }))
	t.Cleanup(panel.Close)
	h.panel = panel
	h.tester = ui.NewTester(func(c *ui.Context) {
		h.ctx = c
		panel.View(c)
	}, width, height)
	h.tester.SetLocale(locale)
	h.tester.Frame()
	return h
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

// isLoopbackGatewayURL reports whether raw names a loopback address a
// gateway may bind. The gateway prefers 127.0.0.2 and falls back to
// 127.0.0.1 where the platform assigns only the latter, so tests ask the
// application layer for the shared shape instead of assuming either address.
func isLoopbackGatewayURL(raw string) bool {
	return appbrowser.IsGatewayURL(raw)
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
	if len(loads) != 1 || !h.core.IsGatewayURL(loads[0]) {
		t.Fatalf("local page loads = %q, want a loopback gateway URL", loads)
	}

	h.navigate(t, "example.com/docs")
	h.frame()
	waitFor(t, "the public page load", func() bool { return len(h.pages.at(0).loadsCopy()) == 2 })
	tab = h.activeTab(t)
	if tab.URL != "https://example.com/docs" {
		t.Fatalf("public tab URL = %q, want HTTPS", tab.URL)
	}
	loads = h.pages.at(0).loadsCopy()
	if !isLoopbackGatewayURL(loads[1]) || !h.core.IsGatewayURL(loads[1]) {
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
	if len(loads) != 1 || !isLoopbackGatewayURL(loads[0]) {
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
	if !handled || !isLoopbackGatewayURL(replacement) {
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

// boundsInside reports whether box lies wholly inside outer.
func boundsInside(box, outer ui.Rect) bool {
	return box.W > 0 && box.H > 0 &&
		box.X >= outer.X-0.5 && box.Y >= outer.Y-0.5 &&
		box.X+box.W <= outer.X+outer.W+0.5 && box.Y+box.H <= outer.Y+outer.H+0.5
}

// The app enforces a 720x480 window. At 96 DPI that is a 704x421 client, and
// the shell's 232-DIP sidebar plus its 16-DIP padding leave the browser a
// 440-DIP content column. The panel-level cases below render the panel alone,
// so the content-column case uses that width and the client case the whole
// 704-DIP client. The shell-mounted test in minsize_shell_test.go measures the
// real window coordinates.
const (
	browserMinimumClientWidth  = 704
	browserMinimumClientHeight = 421
	browserMinimumColumnWidth  = 440
)

func (h *harness) openTabs(t *testing.T, count int) []appbrowser.Tab {
	t.Helper()
	for len(h.state().Tabs) < count {
		if _, err := h.core.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	h.frame()
	tabs := h.state().Tabs
	if len(tabs) != count {
		t.Fatalf("tabs = %d, want %d", len(tabs), count)
	}
	return append([]appbrowser.Tab(nil), tabs...)
}

// waitTabRevealed frames until the active tab and its close control are whole
// inside the scrolled viewport. It reports their boxes and the viewport's.
// Every iteration renders a frame and checks the committed boxes, so it waits
// on the real condition with a hard deadline rather than on a delay.
func (h *harness) waitTabRevealed(t *testing.T, tabID string) (ui.Rect, ui.Rect, ui.Rect) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.frame()
		itemNode, okItem := h.panel.tabItems[tabID]
		closeNode, okClose := h.panel.tabCloses[tabID]
		if okItem && okClose && itemNode != nil && closeNode != nil && h.ctx != nil {
			item := itemNode.Bounds(h.ctx)
			closing := closeNode.Bounds(h.ctx)
			view := h.panel.stripView.Bounds(h.ctx)
			if boundsInside(item, view) && boundsInside(closing, view) {
				return item, closing, view
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the active tab %q never became whole in the strip", tabID)
		}
	}
}

// stripBounds reads the committed box of one of the tab strip's handles.
func (h *harness) stripBounds(handle *ui.Handle) ui.Rect {
	if h.ctx == nil {
		return ui.Rect{}
	}
	return handle.Bounds(h.ctx)
}

func hasTab(state appbrowser.State, id string) bool {
	for _, tab := range state.Tabs {
		if tab.ID == id {
			return true
		}
	}
	return false
}

// TestPanelTabStripKeepsFiveTabsReachableAtTheMinimumWindow is the acceptance
// regression at 960f22b1. With five tabs the strip overflows, so it must
// scroll: each activated tab's whole item and its close control stay inside
// the strip's viewport, and New tab stays outside the scroller and clickable
// at the minimum size. It runs in English and zh-CN because the chrome widths
// differ by locale.
func TestPanelTabStripKeepsFiveTabsReachableAtTheMinimumWindow(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range []struct {
			name          string
			width, height int
		}{
			{"content-column", browserMinimumColumnWidth, browserMinimumClientHeight},
			{"whole-client", browserMinimumClientWidth, browserMinimumClientHeight},
		} {
			t.Run(locale+"-"+size.name, func(t *testing.T) {
				h := newHarness(t, locale, size.width, size.height)
				h.waitRoute(t, appbrowser.RouteConnected)
				h.frame()
				tabs := h.openTabs(t, 5)

				panelBox := ui.Rect{X: 0, Y: 0, W: float32(size.width), H: float32(size.height)}
				plus := h.stripBounds(h.panel.newTab)
				if !boundsInside(plus, panelBox) {
					t.Fatalf("New tab is outside %dx%d with five tabs: %+v",
						size.width, size.height, plus)
				}
				t.Logf("%s %dx%d: New tab at %.1f,%.1f %.0fx%.0f",
					locale, size.width, size.height, plus.X, plus.Y, plus.W, plus.H)

				// With all five tabs present, every one of them, including the
				// last, must become whole inside the strip when activated.
				for index, tab := range tabs {
					if !h.core.Activate(tab.ID) {
						t.Fatalf("Activate tab %d = false", index)
					}
					item, closing, view := h.waitTabRevealed(t, tab.ID)
					if !boundsInside(item, panelBox) {
						t.Fatalf("the active tab %d is outside the %d-wide panel: item %+v, view %+v",
							index, size.width, item, view)
					}
					if !boundsInside(closing, panelBox) {
						t.Fatalf("tab %d's close control is outside the %d-wide panel: %+v",
							index, size.width, closing)
					}
					t.Logf("%s %dx%d: active tab %d item %.1f,%.1f %.0fx%.0f; close %.1f,%.1f %.0fx%.0f; view %.1f..%.1f",
						locale, size.width, size.height, index,
						item.X, item.Y, item.W, item.H,
						closing.X, closing.Y, closing.W, closing.H,
						view.X, view.X+view.W)
				}
				// Five fixed-width items are wider than the strip's viewport at
				// this size, and the last activation above still showed the
				// whole item: the strip had to scroll. An item compressed to
				// fit would prove nothing, so its full width is checked too.
				view := h.stripBounds(h.panel.stripView)
				lastItem := h.stripBounds(h.panel.tabItems[tabs[len(tabs)-1].ID])
				fullItemWidth := browserTabWidth + browserTabGap + browserTabCloseWidth
				if lastItem.W < fullItemWidth-0.5 {
					t.Fatalf("%s %dx%d: the last tab's item is compressed to %.1f DIPs, want %.0f: %+v",
						locale, size.width, size.height, lastItem.W, fullItemWidth, lastItem)
				}
				if view.W >= lastItem.W*float32(len(tabs)) {
					t.Fatalf("%s %dx%d: five items (%.0f each) fit the %.0f-DIP viewport; the test did not exercise scrolling",
						locale, size.width, size.height, lastItem.W, view.W)
				}

				// New tab is reachable with five tabs open and makes a sixth,
				// which is revealed and whose close control works.
				before := len(h.state().Tabs)
				h.tester.ClickAt(plus.X+plus.W/2, plus.Y+plus.H/2)
				h.frame()
				if got := len(h.state().Tabs); got != before+1 {
					t.Fatalf("New tab made %d tabs, want %d", got, before+1)
				}
				extra := h.state().ActiveID
				_, extraClose, _ := h.waitTabRevealed(t, extra)
				h.tester.ClickAt(extraClose.X+extraClose.W/2, extraClose.Y+extraClose.H/2)
				h.frame()
				if hasTab(h.state(), extra) || len(h.state().Tabs) != before {
					t.Fatalf("the extra tab was not closed: %+v", h.state().Tabs)
				}

				// Each surviving tab's close control is its hit target: click
				// it while that tab is active, which also activates the next.
				for index, tab := range tabs {
					if hasTab(h.state(), tab.ID) {
						if !h.core.Activate(tab.ID) {
							t.Fatalf("Activate tab %d before close = false", index)
						}
					}
					_, closing, _ := h.waitTabRevealed(t, tab.ID)
					h.tester.ClickAt(closing.X+closing.W/2, closing.Y+closing.H/2)
					h.frame()
					if hasTab(h.state(), tab.ID) {
						t.Fatalf("clicking tab %d's close control did not close it: %+v",
							index, h.state().Tabs)
					}
				}
			})
		}
	}
}

// TestPanelTabStripRevealsTheActiveTabWhenTheContentNarrows activates the last
// of five tabs at a wide panel, then shrinks it to the minimum content column:
// the active tab and its close control must be whole again, and New tab must
// stay inside the column.
func TestPanelTabStripRevealsTheActiveTabWhenTheContentNarrows(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		t.Run(locale, func(t *testing.T) {
			h := newHarness(t, locale, 868, 688)
			h.waitRoute(t, appbrowser.RouteConnected)
			h.frame()
			tabs := h.openTabs(t, 5)
			last := tabs[len(tabs)-1].ID
			if !h.core.Activate(last) {
				t.Fatal("Activate the last tab = false")
			}
			h.waitTabRevealed(t, last)

			h.tester.SetSize(browserMinimumColumnWidth, browserMinimumClientHeight)
			h.frame()
			item, closing, view := h.waitTabRevealed(t, last)
			column := ui.Rect{X: 0, Y: 0, W: browserMinimumColumnWidth, H: browserMinimumClientHeight}
			if view.X+view.W > float32(browserMinimumColumnWidth)+0.5 {
				t.Fatalf("the strip viewport is wider than the %d-DIP column after the resize: %+v",
					browserMinimumColumnWidth, view)
			}
			if !boundsInside(item, column) {
				t.Fatalf("the active tab is cut after the resize: %+v", item)
			}
			if !boundsInside(closing, column) {
				t.Fatalf("the active tab's close control is cut after the resize: %+v", closing)
			}
			plus := h.stripBounds(h.panel.newTab)
			if !boundsInside(plus, column) {
				t.Fatalf("New tab is outside the column after the resize: %+v", plus)
			}
			t.Logf("%s after 868 -> %d: active item %.1f,%.1f %.0fx%.0f; close %.1f,%.1f %.0fx%.0f; view %.1f..%.1f; New tab %.1f,%.1f %.0fx%.0f",
				locale, browserMinimumColumnWidth,
				item.X, item.Y, item.W, item.H, closing.X, closing.Y, closing.W, closing.H,
				view.X, view.X+view.W, plus.X, plus.Y, plus.W, plus.H)
		})
	}
}

// TestPanelEmptyStateFollowsTheLocaleAfterAResize guards the acceptance
// finding of an English "Browse through this Host" in a zh-CN window after a
// resize: the empty state is built from the frame's locale every time, and a
// size (either dimension) or language change asks for the repaint that
// replaces old copy. The redraw count is the observable the panel controls;
// without the request a native page's old pixels can survive in a region the
// panel has grown into.
func TestPanelEmptyStateFollowsTheLocaleAfterAResize(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	if !h.tester.HasText(locales.In("en").BrowserEmptyTitle()) {
		t.Fatalf("the English empty state is missing: %q", h.tester.Texts())
	}

	zh := locales.In("zh-CN").BrowserEmptyTitle()

	// A language change alone asks for a repaint and builds the new copy.
	before := h.redraws
	h.tester.SetLocale("zh-CN")
	h.frame()
	if h.redraws == before {
		t.Fatalf("a locale change did not ask for a repaint")
	}
	waitFor(t, "the zh-CN empty state", func() bool { return h.tester.HasText(zh) })

	// A height-only resize asks for a repaint too: a native page can cover a
	// region the panel gains vertically.
	before = h.redraws
	h.tester.SetSize(868, browserMinimumClientHeight)
	h.frame()
	if h.redraws == before {
		t.Fatalf("a height-only resize did not ask for a repaint")
	}

	// A width-only resize asks for a repaint and reveals the active tab.
	before = h.redraws
	h.tester.SetSize(browserMinimumClientWidth, browserMinimumClientHeight)
	h.frame()
	if h.redraws == before {
		t.Fatalf("a width-only resize did not ask for a repaint")
	}

	if !h.tester.HasText(zh) {
		t.Fatalf("the zh-CN empty state is missing after the resize: %q", h.tester.Texts())
	}
	if h.tester.HasText(locales.In("en").BrowserEmptyTitle()) {
		t.Fatalf("English empty-state copy survived in the zh-CN frame: %q", h.tester.Texts())
	}
}

// TestPanelRefusedCredentialedAddressLeavesNoCredential checks the address bar
// after a refused credentialed submission: the refusal is visible, but the
// unsubmitted username and password do not stay on screen. NormalizeURL
// reports credentials only after its scheme, host, and length checks, so the
// table includes forms refused for one of those reasons first; none may keep
// the clear-text credential.
func TestPanelRefusedCredentialedAddressLeavesNoCredential(t *testing.T) {
	h := newHarness(t, "en", browserMinimumColumnWidth, browserMinimumClientHeight)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "example.com/docs")
	h.frame()
	waitFor(t, "the committed page", func() bool {
		tab, ok := h.core.ActiveTab()
		return ok && tab.URL == "https://example.com/docs"
	})
	loads := len(h.pages.at(0).loadsCopy())
	refused := []struct {
		address     string
		credentials bool // NormalizeURL reports ErrAddressCredentials
	}{
		{"http://alice:" + "pw" + "@example.com/", true},
		{"ftp://alice:" + "pw" + "@example.com/", false},
		{"http://alice:" + "pw" + "@", false},
		{"alice:" + "pw" + "@example.com", false},
	}
	for _, test := range refused {
		h.tester.Key(ui.Cmd, ui.KeyL)
		h.frame()
		h.tester.Type(test.address)
		h.tester.Key(0, ui.KeyEnter)
		h.frame()

		if test.credentials && !h.tester.HasText(locales.In("en").BrowserCredentials()) {
			t.Fatalf("%q: the credential refusal is not visible: %q", test.address, h.tester.Texts())
		}
		if strings.Contains(h.panel.address, "alice") || strings.Contains(h.panel.address, "pw") {
			t.Fatalf("%q: the refused credentialed address stayed on screen: %q", test.address, h.panel.address)
		}
		if h.panel.address != "https://example.com/docs" {
			t.Fatalf("%q: the address bar = %q, want the tab's real address", test.address, h.panel.address)
		}
		if got := len(h.pages.at(0).loadsCopy()); got != loads {
			t.Fatalf("%q: the refused address caused a page load: %d -> %d", test.address, loads, got)
		}
	}
}

// TestPanelStopDropsAProvisionalTabLabel stops a navigation that never
// committed: the tab keeps the page it actually shows instead of the host
// label it guessed from the typed address.
func TestPanelStopDropsAProvisionalTabLabel(t *testing.T) {
	h := newHarness(t, "en", browserMinimumColumnWidth, browserMinimumClientHeight)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "docs.example.com/a")
	h.frame()
	tab := h.activeTab(t)
	h.core.PageChanged(tab.ID, appbrowser.PageState{Title: "Docs"})
	h.frame()

	if _, err := h.core.Navigate(tab.ID, "other.example/slow"); err != nil {
		t.Fatalf("Navigate the slow address: %v", err)
	}
	h.frame()
	if active := h.activeTab(t); active.Title != "other.example" || active.URL != "https://other.example/slow" {
		t.Fatalf("the provisional tab = %+v", active)
	}
	if err := h.tester.Click(locales.In("en").BrowserStop()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.frame()

	active := h.activeTab(t)
	if active.Title != "Docs" || active.URL != "https://docs.example.com/a" {
		t.Fatalf("the stopped tab kept %+v, want the committed Docs page", active)
	}
	if !h.tester.HasText("Docs") {
		t.Fatalf("the restored tab label is missing: %q", h.tester.Texts())
	}
	if h.panel.address != "https://docs.example.com/a" {
		t.Fatalf("the address bar = %q, want the restored page", h.panel.address)
	}
}
