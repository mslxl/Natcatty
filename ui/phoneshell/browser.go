package phoneshell

// The phone Host Browser. Only the web area is a MyGo WebView; the address
// bar, the tab strip, and the navigation actions are native phone controls,
// as ADR-0007 and ADR-0014 require. Routing, the loopback gateway, the
// dedicated SSH Channel lease, and the download manager are the shared
// application/browser code the desktop browser uses; this file is the phone's
// presentation of that panel.
//
// A browser is opened either from an eligible Host's detail page or from a
// ready Session's Tools surface. Both use one browserSurface per place, so a
// browser opened from a Session never replaces the terminal: the Tools
// surface draws over the still-attached terminal, and closing it returns to
// the same Pane. The Session's own transport is untouched because the browser
// holds its own dedicated channel lease.

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// BrowserCapability reports whether this build and device may offer the Host
// Browser, and why not when they may not. It follows the same fail-closed rule
// as ProxyCommandCapability: an entry is either usable or absent with a
// reason, never a button that fails when tapped.
type BrowserCapability struct {
	Supported bool
	Reason    string
}

// BrowserProvider is the application seam the phone shell draws a Browser
// through. The application wires it to the shared application/browser panel,
// a per-Host MyGo website data store, and the dedicated Connection lease. A
// Shell without a provider hides the entry and reports that this build has no
// browser.
type BrowserProvider interface {
	// Capability reports whether this device supports the Browser. It must be
	// cheap after the first call: the page asks on every frame.
	Capability() BrowserCapability
	// Open starts one Host's browser. The window is the one the pages and
	// their WebViews belong to: the section's window, or the Sessions window
	// for a Browser opened from a Session's Tools surface.
	Open(host domainvault.Host, window *mygo.Window) (Browser, error)
}

// Browser is the shared browser panel as the phone UI sees it. It is the
// application/browser Panel's routing and state surface plus the page
// factory the UI owns; the WebView content itself stays native.
type Browser interface {
	Start()
	Retry() bool
	Close()
	State() appbrowser.State
	Subscribe(func(appbrowser.State)) func()

	Navigate(tabID, address string) (string, error)
	NewTab() (string, error)
	CloseTab(id string) bool
	Activate(id string) bool
	GatewayURL(tabID string) (string, error)
	IsGatewayURL(rawURL string) bool

	PageChanged(tabID string, page appbrowser.PageState)
	PageFailed(tabID, detail string)
	PageStopped(tabID string) bool
	SetPageFlags(tabID string, canBack, canForward, loading bool)
	StartDownload(gatewayURL, suggestedName string) (appbrowser.Download, error)

	// NewPage makes one isolated web view for a tab. The caller wires its
	// navigation guard and events before loading anything into it.
	NewPage() (BrowserPage, error)
}

// BrowserPageEventKind is one page lifecycle event the native page reports.
type BrowserPageEventKind int

const (
	BrowserPageNavigated BrowserPageEventKind = iota
	BrowserPageTitle
	BrowserPageLoading
	BrowserPageLoaded
	BrowserPageFailed
	BrowserPageDownload
)

// BrowserPageEvent is one page lifecycle event. The surface forwards it to
// the shared panel, which owns the address and the tab state.
type BrowserPageEvent struct {
	Kind          BrowserPageEventKind
	URL           string
	Title         string
	Error         string
	SuggestedName string
}

// BrowserPage is one native web page. The application implements it with a
// MyGo WebView; tests use a fake. Native returns nil when there is no real
// page surface, and the phone draws a placeholder instead of a hole.
type BrowserPage interface {
	Native() ui.NativeWebView
	Load(rawURL string) error
	Reload()
	Stop()
	Back()
	Forward()
	CanGoBack() bool
	CanGoForward() bool
	Loading() bool
	URL() string
	Destroy()
	OnEvent(func(BrowserPageEvent)) func()
	// SetNavigationGuard decides an asked navigation before it happens. It
	// returns a replacement URL to load and true to cancel the original, or
	// "" and false to allow it. The surface uses it to keep every page on a
	// gateway URL and to refuse external-app schemes.
	SetNavigationGuard(func(rawURL string) (replacement string, handled bool))
}

// browserEligible reports whether a Host can carry a browser tunnel. It is
// the same rule the shared connector enforces: the Host must be remote and
// its protocol must support a forwarding channel. Telnet and Local Hosts are
// therefore never offered an entry.
func browserEligible(host domainvault.Host) bool {
	return host.IsRemote() && protocolOf(host).SupportsForwarding()
}

// browserReason is the sentence shown when the device cannot run the
// Browser. It never returns an empty string.
func browserReason(capability BrowserCapability) string {
	if reason := strings.TrimSpace(capability.Reason); reason != "" {
		return reason
	}
	return "This device cannot run the Host Browser."
}

// browserSurface owns one browser's native chrome state and its pages. A
// single instance belongs to one Host-detail page or one Pane's Tools
// surface; closing it releases the panel's gateway and channel lease.
type browserSurface struct {
	browser Browser
	redraw  func()

	unsub func()

	mu      sync.Mutex
	pages   map[string]BrowserPage
	closed  bool
	address string
	// addressTab is the tab the address field currently describes. Switching
	// tabs resets the field; page-driven navigation does not overwrite text
	// the user is editing.
	addressTab string
	problem    string
}

// newBrowserSurface starts one browser and subscribes its state changes to
// the phone's redraw. Start is asynchronous for a real panel; the shared
// tunnel reports its result through the subscription.
func newBrowserSurface(browser Browser, redraw func()) *browserSurface {
	if redraw == nil {
		redraw = invalidatePhoneWindows
	}
	s := &browserSurface{browser: browser, redraw: redraw, pages: make(map[string]BrowserPage)}
	s.unsub = browser.Subscribe(func(appbrowser.State) { redraw() })
	browser.Start()
	s.syncAddress()
	return s
}

// invalidatePhoneWindows repaints every phone window. It is the default
// redraw for a surface whose owner did not pass one, such as the Session
// Tools surface; the shared panel may call it from a tunnel goroutine.
func invalidatePhoneWindows() {
	for _, window := range mygo.Windows() {
		if window != nil {
			window.Invalidate()
		}
	}
}

// close releases every page and the browser's gateway and channel lease. It
// is safe to call more than once.
func (s *browserSurface) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	pages := s.pages
	s.pages = nil
	unsub := s.unsub
	s.unsub = nil
	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
	for _, page := range pages {
		page.Destroy()
	}
	s.browser.Close()
}

// syncAddress points the address field at the active tab when the active tab
// has changed. A page whose URL changes under the same tab keeps whatever the
// user is typing.
func (s *browserSurface) syncAddress() {
	state := s.browser.State()
	if state.ActiveID == s.addressTab {
		return
	}
	s.addressTab = state.ActiveID
	s.address = ""
	for _, tab := range state.Tabs {
		if tab.ID == state.ActiveID {
			s.address = tab.URL
			break
		}
	}
}

// ensurePage returns the tab's page, making it on first use and loading the
// tab's current gateway URL. It returns nil, with a problem recorded, when
// the platform cannot make a web view.
func (s *browserSurface) ensurePage(tabID string) BrowserPage {
	if tabID == "" {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if page := s.pages[tabID]; page != nil {
		s.mu.Unlock()
		return page
	}
	s.mu.Unlock()

	page, err := s.browser.NewPage()
	if err != nil {
		s.problem = "The web area is unavailable: " + err.Error()
		return nil
	}
	page.SetNavigationGuard(s.navigationGuard(tabID))
	page.OnEvent(func(event BrowserPageEvent) { s.pageEvent(tabID, page, event) })

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		page.Destroy()
		return nil
	}
	s.pages[tabID] = page
	s.mu.Unlock()

	if gatewayURL, err := s.browser.GatewayURL(tabID); err == nil && gatewayURL != "" {
		_ = page.Load(gatewayURL)
	}
	return page
}

// navigationGuard keeps every page on the shared gateway. A gateway URL is
// allowed as it is; an HTTP(S) address is mapped to the panel's loopback
// gateway and the original navigation is cancelled; every other scheme, such
// as an external-app link, is cancelled with no replacement. A page can
// therefore never reach the product's bridge entry points or a destination
// the panel did not route.
func (s *browserSurface) navigationGuard(tabID string) func(string) (string, bool) {
	return func(rawURL string) (string, bool) {
		if s.browser.IsGatewayURL(rawURL) {
			return "", false
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", true
		}
		gatewayURL, err := s.browser.Navigate(tabID, rawURL)
		if err != nil {
			return "", true
		}
		return gatewayURL, true
	}
}

// pageEvent forwards one native page event to the shared panel.
func (s *browserSurface) pageEvent(tabID string, page BrowserPage, event BrowserPageEvent) {
	switch event.Kind {
	case BrowserPageNavigated:
		s.browser.PageChanged(tabID, appbrowser.PageState{
			URL:          event.URL,
			Loading:      true,
			CanGoBack:    page.CanGoBack(),
			CanGoForward: page.CanGoForward(),
		})
	case BrowserPageTitle:
		s.browser.PageChanged(tabID, appbrowser.PageState{Title: event.Title})
	case BrowserPageLoading:
		s.browser.SetPageFlags(tabID, page.CanGoBack(), page.CanGoForward(), page.Loading())
	case BrowserPageLoaded:
		s.browser.PageChanged(tabID, appbrowser.PageState{
			URL:          page.URL(),
			Loading:      false,
			CanGoBack:    page.CanGoBack(),
			CanGoForward: page.CanGoForward(),
		})
	case BrowserPageFailed:
		s.browser.PageFailed(tabID, event.Error)
	case BrowserPageDownload:
		// The page's own download navigation is aborted by the engine. Settle
		// the provisional tab, then hand the gateway URL to the shared
		// download manager, which keeps the file inside the configured app
		// directory and uses the Host's own route.
		s.browser.PageStopped(tabID)
		if _, err := s.browser.StartDownload(event.URL, event.SuggestedName); err != nil {
			s.problem = "Could not start the download: " + err.Error()
		} else {
			s.problem = "Download started."
		}
	}
	s.syncAddress()
	if s.redraw != nil {
		s.redraw()
	}
}

// navigate loads the address in the active tab through the panel's gateway.
func (s *browserSurface) navigate() {
	state := s.browser.State()
	if state.ActiveID == "" {
		s.problem = "There is no tab to load the address in."
		return
	}
	address := strings.TrimSpace(s.address)
	if address == "" {
		s.problem = "Enter an address first."
		return
	}
	gatewayURL, err := s.browser.Navigate(state.ActiveID, address)
	if err != nil {
		s.problem = err.Error()
		return
	}
	s.problem = ""
	if page := s.ensurePage(state.ActiveID); page != nil {
		_ = page.Load(gatewayURL)
	}
	s.syncAddress()
	if s.redraw != nil {
		s.redraw()
	}
}

// command applies a history or reload action to the active tab.
func (s *browserSurface) command(apply func(BrowserPage)) {
	state := s.browser.State()
	if state.ActiveID == "" {
		return
	}
	page := s.ensurePage(state.ActiveID)
	if page == nil {
		return
	}
	apply(page)
	s.browser.SetPageFlags(state.ActiveID, page.CanGoBack(), page.CanGoForward(), page.Loading())
	if s.redraw != nil {
		s.redraw()
	}
}

// newTab adds and activates a blank tab.
func (s *browserSurface) newTab() {
	if _, err := s.browser.NewTab(); err != nil {
		s.problem = err.Error()
		return
	}
	s.syncAddress()
	if s.redraw != nil {
		s.redraw()
	}
}

// closeTab closes one tab and forgets its page.
func (s *browserSurface) closeTab(tabID string) {
	s.mu.Lock()
	page := s.pages[tabID]
	delete(s.pages, tabID)
	s.mu.Unlock()
	if page != nil {
		page.Destroy()
	}
	if !s.browser.CloseTab(tabID) {
		return
	}
	s.syncAddress()
	if s.redraw != nil {
		s.redraw()
	}
}

// activate shows one tab.
func (s *browserSurface) activate(tabID string) {
	if !s.browser.Activate(tabID) {
		return
	}
	s.syncAddress()
	if s.redraw != nil {
		s.redraw()
	}
}

// drawBrowserSurface renders the native browser chrome and the WebView. The
// web area is the only WebView on the phone; every control around it is
// native. The caller supplies the Done action and label for the surface it is
// embedded in.
func drawBrowserSurface(c *ui.Context, surface *browserSurface, doneLabel string, done func()) {
	if surface == nil {
		kit.EmptyHint(c, "The Browser is not ready.")
		return
	}
	state := surface.browser.State()
	surface.syncAddress()

	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		if kit.IconButton(c, icons.ArrowLeft, "Browser back").Clicked() {
			surface.command(func(page BrowserPage) { page.Back() })
		}
		if kit.IconButton(c, icons.ArrowRight, "Browser forward").Clicked() {
			surface.command(func(page BrowserPage) { page.Forward() })
		}
		if kit.IconButton(c, icons.RefreshCw, "Browser reload").Clicked() {
			surface.command(func(page BrowserPage) { page.Reload() })
		}
		input := ui.TextInput(c, &surface.address).Placeholder("address").Label("Browser address").
			Grow(1).MinWidth(0)
		if input.Submitted() {
			surface.navigate()
		}
	})
	ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
		for _, tab := range state.Tabs {
			tab := tab
			label := browserTabLabel(tab)
			marker := ""
			if tab.ID == state.ActiveID {
				marker = "• "
			}
			if kit.GhostButton(c, icons.Globe, marker+label).Clicked() {
				surface.activate(tab.ID)
			}
			if kit.IconButton(c, icons.X, "Close browser tab "+label).Clicked() {
				surface.closeTab(tab.ID)
			}
		}
		if kit.GhostButton(c, icons.Plus, "New browser tab").Clicked() {
			surface.newTab()
		}
		if done != nil {
			if kit.GhostButton(c, icons.X, doneLabel).Clicked() {
				done()
				return
			}
		}
	})

	if surface.problem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, surface.problem, nil)
	}
	if state.NoticeMsg != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning, state.NoticeMsg, nil)
	}

	switch state.Route {
	case appbrowser.RouteConnecting:
		kit.EmptyHint(c, "Connecting "+state.Label+"'s browser…")
		return
	case appbrowser.RouteDisconnected:
		detail := state.Detail
		if detail == "" {
			detail = "The browser's dedicated connection is closed."
		}
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, detail, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				surface.browser.Retry()
			}
		})
		return
	}

	active := activeBrowserTab(state)
	if active == nil {
		kit.EmptyHint(c, "Open an address to start browsing.")
		return
	}
	page := surface.ensurePage(active.ID)
	if page == nil {
		kit.EmptyHint(c, "The web area is not available.")
		return
	}
	ui.Box(c).FillWidth().Grow(1).MinHeight(220).Radius(12).Children(func() {
		if native := page.Native(); native != nil {
			ui.WebView(c, native).Fill()
			return
		}
		kit.EmptyHint(c, "The web page renders in the platform WebView.")
	})
}

// activeBrowserTab returns the shown tab, if any.
func activeBrowserTab(state appbrowser.State) *appbrowser.Tab {
	for i := range state.Tabs {
		if state.Tabs[i].ID == state.ActiveID {
			return &state.Tabs[i]
		}
	}
	return nil
}

// browserTabLabel names a tab in the strip: its page title, its host, or a
// blank-tab label.
func browserTabLabel(tab appbrowser.Tab) string {
	if title := strings.TrimSpace(tab.Title); title != "" {
		return title
	}
	if raw := strings.TrimSpace(tab.URL); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
			return parsed.Host
		}
		return raw
	}
	return "New tab"
}

// browserHostKey names one Host-detail browser by its section and Host, so
// the Hosts tab and the Vault section cannot share one panel's state.
func browserHostKey(sec Section, hostID model.HostID) string {
	return fmt.Sprintf("%d/%s", sec, hostID)
}

// WithBrowserProvider gives the shell the shared browser panel and the
// device capability probe. Without it the Browser entry is absent and its
// pages say this build has no browser.
func WithBrowserProvider(provider BrowserProvider) Option {
	return func(s *Shell) { s.browser = provider }
}

// browserCapability is the capability every Browser entry reads. A shell with
// no provider fails closed with a stated reason.
func (s *Shell) browserCapability() BrowserCapability {
	if s.browser == nil {
		return BrowserCapability{Reason: "The Host Browser is not available in this build."}
	}
	capability := s.browser.Capability()
	if !capability.Supported {
		capability.Reason = browserReason(capability)
	}
	return capability
}

// openHostBrowser pushes the Browser page for one Host in the section that
// showed its detail page. The Host's own Session is not mounted: the Browser
// holds a dedicated channel lease, so it never disturbs a terminal.
func (s *Shell) openHostBrowser(host domainvault.Host) {
	if !browserEligible(host) {
		return
	}
	id := string(host.ID)
	// The same detail page is drawn in both sections. The selected section is
	// the usual answer; when a page is driven directly (a test, or a section
	// window not yet selected), the Vault's own stack decides.
	if s.selected == Vault ||
		(s.selected != Hosts && strings.HasPrefix(s.sections[Vault].router.Path(), "/hosts/")) {
		s.sections[Vault].router.Push("/hosts/" + id + "/browser")
		return
	}
	s.sections[Hosts].router.Push("/host/" + id + "/browser")
}

// drawHostBrowser renders one Host's Browser page. Closing it releases the
// panel and returns to the Host detail page.
func (s *Shell) drawHostBrowser(c *ui.Context, sec Section, id string) {
	host, ok := s.hosts.host(id)
	if !ok {
		kit.EmptyHint(c, "This Host is no longer in the Vault. Refresh the Hosts list.")
		return
	}
	if !browserEligible(host) {
		kit.EmptyHint(c, "This Host cannot carry a Browser.")
		return
	}
	surface, err := s.hostBrowserSurface(sec, host)
	if err != nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, err.Error(), nil)
		return
	}
	ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
		kit.SectionBar(c, hostName(host), nil)
		drawBrowserSurface(c, surface, "Close Browser", func() {
			s.closeHostBrowser(sec, host.ID)
			s.sections[sec].router.Back()
		})
	})
}

// hostBrowserSurface returns the section's browser for one Host, making it on
// first use. One Host detail page has one browser, so re-entering the page
// does not open a second channel lease.
func (s *Shell) hostBrowserSurface(sec Section, host domainvault.Host) (*browserSurface, error) {
	if s.browser == nil {
		return nil, fmt.Errorf("The Host Browser is not available in this build.")
	}
	key := browserHostKey(sec, host.ID)
	if surface := s.hostBrowsers[key]; surface != nil {
		return surface, nil
	}
	browser, err := s.browser.Open(host, s.Window(sec))
	if err != nil {
		return nil, fmt.Errorf("Could not open the Browser: %w", err)
	}
	surface := newBrowserSurface(browser, s.invalidateBrowser)
	s.hostBrowsers[key] = surface
	return surface, nil
}

// closeHostBrowser releases one Host-detail browser.
func (s *Shell) closeHostBrowser(sec Section, hostID model.HostID) {
	key := browserHostKey(sec, hostID)
	surface := s.hostBrowsers[key]
	delete(s.hostBrowsers, key)
	if surface != nil {
		surface.close()
	}
}

// invalidateBrowser repaints every phone window after a browser state change.
// It is safe from the shared panel's goroutine, like MyGo's own invalidation.
func (s *Shell) invalidateBrowser() {
	for _, window := range s.windows {
		if window != nil {
			window.Invalidate()
		}
	}
}

// Window returns the window one section is shown in, or nil before Show.
func (s *Shell) Window(sec Section) *mygo.Window {
	if sec < 0 || sec >= sectionCount {
		return nil
	}
	return s.windows[sec]
}
