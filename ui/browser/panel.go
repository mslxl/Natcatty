// Package browser renders the host-routed browser panel: its navigation
// chrome, tab strip, route status, and the isolated page views. The
// application layer owns routing; this package only shows its state and
// forwards user intent.
package browser

import (
	"errors"
	"net/url"
	"strings"
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
	PageDownload
)

// PageEvent is one page lifecycle event.
type PageEvent struct {
	Kind          PageEventKind
	URL           string
	Title         string
	Error         string
	SuggestedName string
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

	// tabItems, tabCloses, newTab, and stripView identify the tab strip's
	// controls across frames. A MyGo element belongs to one build pass; a
	// Handle is its cross-frame identity, which the panel keeps to read the
	// committed boxes and to notice when the strip's viewport narrows.
	tabItems  map[string]*ui.Handle
	tabCloses map[string]*ui.Handle
	newTab    *ui.Handle
	stripView *ui.Handle

	activeTabID string
	revealTab   bool
	stripWidth  float32

	lastLocale string
	lastWidth  float32
	lastHeight float32

	problem problemKind

	lastRoute    appbrowser.Route
	focusAddress bool
	closed       bool

	pane         polishPane
	bookmarkForm bookmarkForm
	downloadErr  error
	iconCache    map[string]*ui.Bitmap
	iconMiss     map[string]bool
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

	// A rebuilt surface at a new size or in new copy asks for one more frame.
	// A native page is a child window, and a region it used to cover can keep
	// old pixels until the panel repaints: acceptance at 960f22b1 caught
	// English empty-state copy left in a region of a window that had grown
	// into it. The copy itself is always built from this frame's locale; the
	// request is only about repainting the region. A width change also asks
	// the strip to reveal the active tab in this frame instead of after the
	// committed strip width catches up.
	if width, height := c.Size(); width > 0 && height > 0 {
		localeChanged := p.lastLocale != "" && p.lastLocale != c.Locale()
		widthChanged := p.lastWidth > 0 && p.lastWidth != width
		heightChanged := p.lastHeight > 0 && p.lastHeight != height
		if localeChanged || widthChanged || heightChanged {
			p.invalidate()
		}
		if widthChanged {
			p.revealTab = true
		}
		p.lastLocale = c.Locale()
		p.lastWidth = width
		p.lastHeight = height
	}

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
	if state.Route == appbrowser.RouteConnected && hasActive && active.URL != "" && p.pane == paneNone {
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
			switch {
			case p.pane == paneBookmarks:
				p.bookmarkPane(c, state, m, t)
			case p.pane == paneDownloads:
				p.downloadPane(c, m, t)
			case hasActive && active.URL != "":
				p.page(c, active, t)
			default:
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
		if ui.Button(c, "★").Label(m.BrowserBookmarks()).Width(30).Height(28).Clicked() {
			p.togglePane(paneBookmarks)
		}
		if ui.Button(c, "⇩").Label(m.BrowserDownloads()).Width(30).Height(28).Clicked() {
			p.togglePane(paneDownloads)
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

const (
	// browserTabStripHeight is the strip's height: one browserTabControlHeight
	// control plus vertical breathing room.
	browserTabStripHeight float32 = 32
	// browserTabWidth is one tab label's fixed width. A fixed width keeps an
	// activated tab's whole item (label and close control) stable while page
	// titles arrive, so ScrollIntoView reveals it once rather than chasing a
	// growing label.
	browserTabWidth float32 = 128
	// browserTabCloseWidth is the close control beside each label.
	browserTabCloseWidth float32 = 24
	// browserTabGap separates a label from its close control, and one tab item
	// from the next.
	browserTabGap float32 = 2
	// browserTabControlHeight is the height of a tab label and its close
	// control.
	browserTabControlHeight float32 = 26
)

// tabStrip renders the tab strip. Five tabs need more width than the 440-DIP
// content column at the minimum window (a 704x421 client with the 232-DIP
// sidebar and the shell's 16-DIP padding), so the strip scrolls sideways
// exactly like the shell's work-Tab strip and the System manager's tab strip.
// The active tab is scrolled fully into view when it changes or the strip's
// viewport narrows, so its label and its close control are never cut at
// either edge. The New tab control sits outside the scrolling area, so it is
// reachable at every width, and ScrollHorizontal gives the same hover
// affordance the other strips have.
func (p *Panel) tabStrip(c *ui.Context, state appbrowser.State, m locales.Messages, t *ui.Theme) {
	if state.ActiveID != p.activeTabID {
		p.activeTabID = state.ActiveID
		p.revealTab = true
	}
	if p.tabItems == nil {
		p.tabItems = make(map[string]*ui.Handle, len(state.Tabs))
		p.tabCloses = make(map[string]*ui.Handle, len(state.Tabs))
	}
	if p.stripView == nil {
		p.stripView = &ui.Handle{}
	}
	if p.newTab == nil {
		p.newTab = &ui.Handle{}
	}
	reveal := p.revealTab
	p.revealTab = false

	ui.Row(c).FillWidth().Height(browserTabStripHeight).Shrink(0).Padding(0, 8).Gap(4).
		AlignItems(ui.Center).Children(func() {
		scroll := ui.ScrollHorizontal(c).Grow(1).MinWidth(0).
			Height(browserTabStripHeight).Bind(p.stripView)
		// The strip's own width is the room its tabs have. A narrower window
		// (or a wider sidebar) shrinks it; reveal the active tab again so one
		// chosen at a wide window does not stay half cut.
		if width := p.stripView.Bounds(c).W; width > 0 {
			if p.stripWidth > 0 && width != p.stripWidth {
				reveal = true
			}
			p.stripWidth = width
		}
		scroll.Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				for _, tab := range state.Tabs {
					tab := tab
					label := tab.Title
					if label == "" {
						label = m.BrowserBlankTab()
					}
					active := tab.ID == state.ActiveID
					// Label and close control are one item: revealing the tab
					// reveals the close control beside it too.
					itemHandle := p.tabItems[tab.ID]
					if itemHandle == nil {
						itemHandle = &ui.Handle{}
						p.tabItems[tab.ID] = itemHandle
					}
					item := ui.Row(c).Key("browser-tab-" + tab.ID).Gap(browserTabGap).
						AlignItems(ui.Center).Bind(itemHandle)
					item.Children(func() {
						if bitmap := p.iconBitmap(tab.IconDataURL); bitmap != nil {
							ui.Image(c, bitmap).Width(14).Height(14).Shrink(0)
						}
						var button ui.Element
						if active {
							button = ui.PrimaryButton(c, label)
						} else {
							button = ui.Button(c, label)
						}
						button.Width(browserTabWidth).Height(browserTabControlHeight)
						if button.Clicked() {
							if p.core.Activate(tab.ID) {
								p.addressDirty = false
							}
							c.Invalidate()
						}
						closeHandle := p.tabCloses[tab.ID]
						if closeHandle == nil {
							closeHandle = &ui.Handle{}
							p.tabCloses[tab.ID] = closeHandle
						}
						close := ui.Button(c, "×").Label(m.BrowserCloseTab()).
							Width(browserTabCloseWidth).Height(browserTabControlHeight).Bind(closeHandle)
						if close.Clicked() {
							p.closeTab(tab.ID)
							c.Invalidate()
						}
					})
					if active && reveal {
						item.ScrollIntoView()
					}
				}
			})
		})
		// New tab stays outside the scroller, so it is reachable at every
		// width and never scrolls away with the labels. It must not be
		// compressed by a wide strip either.
		plus := ui.Button(c, "+").Label(m.BrowserNewTab()).Width(30).
			Height(browserTabControlHeight).Shrink(0).Bind(p.newTab)
		if plus.Clicked() {
			if _, err := p.core.NewTab(); err != nil {
				p.setProblem(err)
			}
			c.Invalidate()
		}
	})
	// Forget handles of tabs that no longer exist.
	live := make(map[string]bool, len(state.Tabs))
	for _, tab := range state.Tabs {
		live[tab.ID] = true
	}
	for id := range p.tabItems {
		if !live[id] {
			delete(p.tabItems, id)
			delete(p.tabCloses, id)
		}
	}
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
	previous := active.URL
	gatewayURL, err := p.core.Navigate(active.ID, p.address)
	if err != nil {
		p.setProblem(err)
		// The refused text is never submitted, and it must not stay on screen
		// either: it may carry a username and password in clear text. Put the
		// tab's real address back instead. NormalizeURL reports credentials
		// only after its scheme, host, and length checks, so a refusal for one
		// of those reasons can still contain them; inspect the text too.
		if errors.Is(err, domainbrowser.ErrAddressCredentials) || addressCarriesCredentials(p.address) {
			p.address = previous
			p.addressURL = previous
			p.addressDirty = false
		}
		c.Invalidate()
		return
	}
	p.problem = problemNone
	p.addressDirty = false
	p.addressURL = p.address
	p.loadPage(active.ID, gatewayURL)
	c.Invalidate()
}

// addressCarriesCredentials reports whether address-bar text puts a username
// or password in the URL's authority, with or without a scheme. It is used
// only to keep refused text off screen: an address can be refused for a bad
// scheme, a missing host, or its length before URL normalization looks at its
// user information, and those refusals must not leave a password visible.
func addressCarriesCredentials(address string) bool {
	authority := address
	if i := strings.Index(authority, "://"); i >= 0 {
		authority = authority[i+len("://"):]
	}
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	return strings.Contains(authority, "@")
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
	// A stop before the page committed drops the address' provisional label
	// and restores the page actually shown; the tab label follows the real
	// navigation outcome.
	p.core.PageStopped(tabID)
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
	case PageDownload:
		// The engine aborts the navigation that produced the download. Settle
		// the provisional tab so the address and loading state describe the
		// page still on screen; the download pane owns the new outcome.
		p.core.PageStopped(tabID)
		p.startDownloadFromPage(event.URL, event.SuggestedName)
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
