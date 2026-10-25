// Package browser renders the host-routed browser panel: its navigation
// chrome, tab strip, route status, and the isolated page views. The
// application layer owns routing; this package only shows its state and
// forwards user intent.
package browser

import (
	"errors"
	"net/url"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/ui/locales"
)

// PageEventKind is a page lifecycle event. Page views report navigation,
// title, and load results; the panel forwards them to the application layer.
type PageEventKind int

const (
	PageNavigated PageEventKind = iota
	PageTitle
	PageLoading
	PageLoaded
	PageFailed
)

// PageEvent is one page lifecycle event.
type PageEvent struct {
	Kind  PageEventKind
	URL   string
	Title string
	Error string
}

// Page is one native web page. The app implements it with MyGo; tests use a
// fake. Native returns nil when no real page surface exists, and the panel
// then renders a placeholder instead of a hole.
type Page interface {
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
	OnEvent(func(PageEvent)) func()
	// SetNavigationGuard decides an asked navigation before it happens. It
	// returns a replacement URL to load instead, and true to cancel the
	// original; "" with false allows it. The panel uses this to keep a page
	// built by script on the Host route rather than sending it directly.
	SetNavigationGuard(func(rawURL string) (replacement string, handled bool))
}

// PageFactory creates one isolated page for a tab. The app binds it to the
// owning window; a fake returns a recording page.
type PageFactory interface {
	NewPage() (Page, error)
}

// Option customizes a Panel.
type Option func(*Panel)

// WithRedraw supplies the window invalidation used after a local change.
func WithRedraw(fn func()) Option {
	return func(p *Panel) { p.redraw = fn }
}

// WithCloseRequest supplies the Session action that closes the panel. The
// panel's Close control calls it; the Session then calls Close on the panel.
func WithCloseRequest(fn func()) Option {
	return func(p *Panel) { p.closeRequest = fn }
}

// Panel is the rendered browser side panel of one Session.
type Panel struct {
	core         *appbrowser.Panel
	pages        PageFactory
	redraw       func()
	closeRequest func()

	address      string
	addressInput ui.Element
	addressTab   string
	addressURL   string
	addressDirty bool

	openTabs map[string]Page

	problem problemKind

	lastRoute    appbrowser.Route
	focusAddress bool
	closed       bool
}

type problemKind int

const (
	problemNone problemKind = iota
	problemInvalidAddress
	problemScheme
	problemCredentials
	problemNotConnected
	problemTabLimit
	problemPage
)

// New creates the panel and starts its dedicated tunnel. The session opens
// the panel from its Session menu; closing the panel releases the tunnel.
func New(core *appbrowser.Panel, pages PageFactory, options ...Option) *Panel {
	panel := &Panel{
		core:         core,
		pages:        pages,
		openTabs:     make(map[string]Page),
		focusAddress: true,
	}
	for _, option := range options {
		option(panel)
	}
	if core != nil {
		core.Start()
	}
	return panel
}

// Close releases every page and the panel's tunnel. It is idempotent.
func (p *Panel) Close() {
	if p == nil || p.closed {
		return
	}
	p.closed = true
	p.destroyPages()
	if p.core != nil {
		p.core.Close()
	}
}

// View renders the panel.
func (p *Panel) View(c *ui.Context) ui.Element {
	if p.core == nil {
		return ui.Column(c)
	}
	// Ctrl+L is the browser address shortcut. Declared here, before the
	// panel's own elements, it is a window shortcut when the panel is the
	// whole surface and follows the host surface's focus when embedded. A
	// focused native page keeps the OS keyboard to itself (WebView2 only
	// forwards Tab out), so the shortcut applies while Natcatty's own chrome
	// has focus; clicking the page moves keyboard focus into the page.
	c.OnShortcut(ui.Cmd, ui.KeyL, func() {
		p.addressDirty = false
		p.addressInput.Focus()
		// Ctrl+L selects the whole address, so typing replaces it as in a
		// browser rather than appending to the previous page.
		p.addressInput.SetTextSelection(0, utf8.RuneCountInString(p.address))
	})
	state := p.core.State()
	m := locales.In(c.Locale())
	t := c.Theme()

	if state.Route != appbrowser.RouteConnected {
		p.destroyPages()
	}
	if p.lastRoute == appbrowser.RouteDisconnected && state.Route == appbrowser.RouteConnected {
		if tab, ok := activeTab(state); ok && tab.URL != "" {
			p.loadTab(tab.ID)
		}
	}
	p.lastRoute = state.Route
	p.syncAddress(state)
	active, hasActive := activeTab(state)
	if state.Route == appbrowser.RouteConnected && hasActive && active.URL != "" {
		// A page is created lazily and kept while its tab exists. This also
		// recreates it after a retry, without touching other tabs.
		if _, exists := p.openTabs[active.ID]; !exists {
			p.loadTab(active.ID)
		}
	}

	return ui.Column(c).Fill().MinWidth(0).Background(t.Background).Children(func() {
		p.toolbar(c, state, active, hasActive, m, t)
		p.tabStrip(c, state, m, t)
		if text := p.problemText(m); text != "" {
			p.banner(c, text, t)
		} else if state.Notice == appbrowser.FailurePage {
			p.banner(c, m.BrowserPageFailed(), t)
		}
		switch state.Route {
		case appbrowser.RouteConnecting:
			p.status(c, m.BrowserConnecting(), m.BrowserConnectingBody(), "", m, t)
		case appbrowser.RouteDisconnected:
			title, body := m.BrowserDisconnected(), m.BrowserDisconnectedBody()
			if state.Failure == appbrowser.FailureConnect {
				title, body = m.BrowserConnectFailed(), m.BrowserConnectFailedBody()
			}
			p.status(c, title, body, m.BrowserRetry(), m, t)
		default:
			if hasActive && active.URL != "" {
				p.page(c, active, t)
			} else {
				p.status(c, m.BrowserEmptyTitle(), m.BrowserEmptyBody(), "", m, t)
			}
		}
	})
}

func (p *Panel) toolbar(c *ui.Context, state appbrowser.State, active appbrowser.Tab,
	hasActive bool, m locales.Messages, t *ui.Theme) {
	connected := state.Route == appbrowser.RouteConnected
	ui.Row(c).FillWidth().Padding(6, 8).Gap(6).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "‹").Label(m.BrowserBack()).Width(30).Height(28).
			Disabled(!connected || !hasActive || !active.CanGoBack).Clicked() {
			p.pageCommand(active.ID, func(page Page) { page.Back() })
		}
		if ui.Button(c, "›").Label(m.BrowserForward()).Width(30).Height(28).
			Disabled(!connected || !hasActive || !active.CanGoForward).Clicked() {
			p.pageCommand(active.ID, func(page Page) { page.Forward() })
		}
		if ui.Button(c, "⟳").Label(m.BrowserReload()).Width(30).Height(28).
			Disabled(!connected || !hasActive || active.URL == "").Clicked() {
			p.pageCommand(active.ID, func(page Page) { page.Reload() })
		}
		if ui.Button(c, "✕").Label(m.BrowserStop()).Width(30).Height(28).
			Disabled(!connected || !hasActive || !active.Loading).Clicked() {
			p.stopPage(active.ID)
		}
		input := ui.TextInput(c, &p.address).Grow(1).MinWidth(0).
			Label(m.BrowserAddress())
		p.addressInput = input
		// Opening the browser puts the keyboard in the address bar: the panel
		// is replacing a focused terminal, and its own shortcuts live in this
		// surface's focus scope.
		if p.focusAddress && connected {
			input.Focus()
			p.focusAddress = false
		}
		if input.Changed() {
			p.addressDirty = true
		}
		if input.Submitted() {
			p.submit(c)
		}
		if ui.Button(c, "×").Label(m.BrowserClose()).Width(30).Height(28).Clicked() {
			p.requestClose()
		}
	})
}

func (p *Panel) tabStrip(c *ui.Context, state appbrowser.State, m locales.Messages, t *ui.Theme) {
	ui.ScrollHorizontal(c).FillWidth().Height(32).Shrink(0).Children(func() {
		ui.Row(c).Gap(4).Padding(0, 8).AlignItems(ui.Center).Children(func() {
			for _, tab := range state.Tabs {
				tab := tab
				label := tab.Title
				if label == "" {
					label = m.BrowserBlankTab()
				}
				active := tab.ID == state.ActiveID
				var button ui.Element
				if active {
					button = ui.PrimaryButton(c, label)
				} else {
					button = ui.Button(c, label)
				}
				if button.MaxWidth(180).Clicked() {
					if p.core.Activate(tab.ID) {
						p.addressDirty = false
					}
					c.Invalidate()
				}
				if ui.Button(c, "×").Label(m.BrowserCloseTab()).Width(24).Height(26).Clicked() {
					p.closeTab(tab.ID)
					c.Invalidate()
				}
			}
			if ui.Button(c, "+").Label(m.BrowserNewTab()).Width(30).Height(26).Clicked() {
				if _, err := p.core.NewTab(); err != nil {
					p.setProblem(err)
				}
				c.Invalidate()
			}
		})
	})
	// Keep a hairline under the chrome without stealing room from the page.
	ui.Row(c).FillWidth().Height(1).Background(t.Border)
}

func (p *Panel) banner(c *ui.Context, text string, t *ui.Theme) {
	ui.Row(c).FillWidth().Padding(6, 8).Background(t.Surface).Children(func() {
		ui.Text(c, text).FontSize(11).Grow(1).MinWidth(0).SingleLine().TextColor(t.Danger)
	})
}

func (p *Panel) status(c *ui.Context, title, body, retry string, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Padding(24).Gap(10).
		Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(16).Bold().TextAlign(ui.Center).TextColor(t.Text)
		ui.Text(c, body).FontSize(11).Wrap().TextAlign(ui.Center).
			MaxWidth(420).TextColor(t.TextMuted)
		if retry != "" {
			if ui.PrimaryButton(c, retry).Clicked() {
				p.problem = problemNone
				if p.core.Retry() {
					c.Invalidate()
				}
			}
		}
	})
}

func (p *Panel) page(c *ui.Context, tab appbrowser.Tab, t *ui.Theme) {
	page := p.openTabs[tab.ID]
	if page != nil {
		if native := page.Native(); native != nil {
			ui.WebView(c, native).Grow(1).Key("browser-page-" + tab.ID)
			return
		}
	}
	ui.Column(c).Grow(1).FillWidth().Padding(24).Gap(8).
		Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		title := tab.Title
		if title == "" {
			title = tab.URL
		}
		ui.Text(c, title).FontSize(14).Bold().TextAlign(ui.Center).
			MaxWidth(420).SingleLine().TextColor(t.Text)
		ui.Text(c, tab.URL).FontSize(11).TextAlign(ui.Center).
			MaxWidth(420).SingleLine().TextColor(t.TextMuted)
	})
}

// syncAddress follows the active tab's real address unless the user is
// editing the field.
func (p *Panel) syncAddress(state appbrowser.State) {
	active, ok := activeTab(state)
	if !ok {
		return
	}
	switch {
	case p.addressTab != active.ID:
		p.addressTab = active.ID
		p.address = active.URL
		p.addressURL = active.URL
		p.addressDirty = false
	case !p.addressDirty && p.addressURL != active.URL:
		p.address = active.URL
		p.addressURL = active.URL
	}
}

func (p *Panel) submit(c *ui.Context) {
	state := p.core.State()
	active, ok := activeTab(state)
	if !ok {
		return
	}
	gatewayURL, err := p.core.Navigate(active.ID, p.address)
	if err != nil {
		p.setProblem(err)
		c.Invalidate()
		return
	}
	p.problem = problemNone
	p.addressDirty = false
	p.addressURL = p.address
	p.loadPage(active.ID, gatewayURL)
	c.Invalidate()
}

func (p *Panel) loadTab(tabID string) {
	gatewayURL, err := p.core.GatewayURL(tabID)
	if err != nil {
		return
	}
	p.loadPage(tabID, gatewayURL)
}

func (p *Panel) loadPage(tabID, gatewayURL string) {
	page := p.ensurePage(tabID)
	if page == nil {
		return
	}
	if err := page.Load(gatewayURL); err != nil {
		p.core.PageFailed(tabID, err.Error())
	}
}

// stopPage stops a load without waiting for a finish event. WebView2 reports a
// stopped load as an aborted navigation and runs neither its finish nor its
// failure handler, so the loading flag must be cleared here; otherwise Stop
// stays enabled forever.
func (p *Panel) stopPage(tabID string) {
	page := p.openTabs[tabID]
	if page == nil {
		return
	}
	page.Stop()
	p.core.SetPageFlags(tabID, page.CanGoBack(), page.CanGoForward(), false)
	p.invalidate()
}

func (p *Panel) ensurePage(tabID string) Page {
	if page := p.openTabs[tabID]; page != nil {
		return page
	}
	if p.pages == nil {
		return nil
	}
	page, err := p.pages.NewPage()
	if err != nil {
		p.problem = problemPage
		return nil
	}
	page.OnEvent(func(event PageEvent) {
		p.onPageEvent(tabID, page, event)
	})
	page.SetNavigationGuard(func(rawURL string) (string, bool) {
		if p.core.IsGatewayURL(rawURL) {
			return "", false
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", true
		}
		gatewayURL, err := p.core.Navigate(tabID, rawURL)
		if err != nil {
			return "", true
		}
		return gatewayURL, true
	})
	p.openTabs[tabID] = page
	return page
}

func (p *Panel) onPageEvent(tabID string, page Page, event PageEvent) {
	if p.closed {
		return
	}
	switch event.Kind {
	case PageNavigated:
		p.core.PageChanged(tabID, appbrowser.PageState{
			URL:          event.URL,
			Loading:      true,
			CanGoBack:    page.CanGoBack(),
			CanGoForward: page.CanGoForward(),
		})
	case PageTitle:
		p.core.PageChanged(tabID, appbrowser.PageState{Title: event.Title})
	case PageLoading:
		p.core.SetPageFlags(tabID, page.CanGoBack(), page.CanGoForward(), page.Loading())
	case PageLoaded:
		p.core.PageChanged(tabID, appbrowser.PageState{
			URL:          page.URL(),
			Loading:      false,
			CanGoBack:    page.CanGoBack(),
			CanGoForward: page.CanGoForward(),
		})
	case PageFailed:
		p.core.PageFailed(tabID, event.Error)
	}
	p.invalidate()
}

func (p *Panel) pageCommand(tabID string, command func(Page)) {
	page := p.openTabs[tabID]
	if page == nil {
		// Reload and Back after a retry start from a fresh page.
		p.loadTab(tabID)
		page = p.openTabs[tabID]
	}
	if page == nil {
		return
	}
	command(page)
	p.core.SetPageFlags(tabID, page.CanGoBack(), page.CanGoForward(), page.Loading())
	p.invalidate()
}

func (p *Panel) closeTab(tabID string) {
	if page := p.openTabs[tabID]; page != nil {
		page.Destroy()
		delete(p.openTabs, tabID)
	}
	p.core.CloseTab(tabID)
}

func (p *Panel) requestClose() {
	if p.closeRequest != nil {
		p.closeRequest()
		return
	}
	p.Close()
}

func (p *Panel) destroyPages() {
	for tabID, page := range p.openTabs {
		page.Destroy()
		delete(p.openTabs, tabID)
	}
}

func (p *Panel) invalidate() {
	if p.redraw != nil {
		p.redraw()
	}
}

func (p *Panel) setProblem(err error) {
	switch {
	case errors.Is(err, domainbrowser.ErrAddressEmpty),
		errors.Is(err, domainbrowser.ErrAddressInvalid):
		p.problem = problemInvalidAddress
	case errors.Is(err, domainbrowser.ErrAddressScheme):
		p.problem = problemScheme
	case errors.Is(err, domainbrowser.ErrAddressCredentials):
		p.problem = problemCredentials
	case errors.Is(err, appbrowser.ErrNotConnected):
		p.problem = problemNotConnected
	case errors.Is(err, appbrowser.ErrTabLimit):
		p.problem = problemTabLimit
	case errors.Is(err, appbrowser.ErrNoTab):
		p.problem = problemPage
	default:
		p.problem = problemPage
	}
}

func (p *Panel) problemText(m locales.Messages) string {
	switch p.problem {
	case problemInvalidAddress:
		return m.BrowserInvalidAddress()
	case problemScheme:
		return m.BrowserInvalidScheme()
	case problemCredentials:
		return m.BrowserCredentials()
	case problemNotConnected:
		return m.BrowserNotConnected()
	case problemTabLimit:
		return m.BrowserTabLimit()
	case problemPage:
		return m.BrowserPageFailed()
	default:
		return ""
	}
}

func activeTab(state appbrowser.State) (appbrowser.Tab, bool) {
	for _, tab := range state.Tabs {
		if tab.ID == state.ActiveID {
			return tab, true
		}
	}
	return appbrowser.Tab{}, false
}
