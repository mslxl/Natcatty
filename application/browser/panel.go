// Package browser is the host-routed browser's application layer. It owns
// the panel's route, tabs, and navigation rules and keeps every page request
// on one dedicated per-Host tunnel. It knows nothing about MyGo, the UI, or
// the Vault: the app supplies a Connector and a redraw request, and the UI
// supplies the page views.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

// Route is the state of the panel's dedicated tunnel.
type Route string

const (
	// RouteConnecting means the tunnel is being opened. Pages are not shown
	// yet and navigation is refused.
	RouteConnecting Route = "connecting"
	// RouteConnected means the tunnel is open and navigation is allowed.
	RouteConnected Route = "connected"
	// RouteDisconnected means the tunnel failed or was closed. Browsing is
	// stopped until a successful Retry opens a new tunnel.
	RouteDisconnected Route = "disconnected"
)

// Failure classifies the panel's problem without carrying user-visible copy.
// The UI maps each kind to a localized message.
type Failure string

const (
	FailureNone         Failure = ""
	FailureConnect      Failure = "connect"
	FailureTunnel       Failure = "tunnel"
	FailurePage         Failure = "page"
	FailureAddress      Failure = "address"
	FailureNotConnected Failure = "not_connected"
	FailureTabLimit     Failure = "tab_limit"
	FailureNoTab        Failure = "no_tab"
)

// MaxTabs is the original panel's tab limit.
const MaxTabs = 32

// ErrNotConnected reports navigation attempted while the tunnel is not
// connected. The page stays closed rather than falling back to a direct
// local request.
var ErrNotConnected = errors.New("browser: navigation without a connected tunnel")

// ErrTabLimit reports that the panel already holds MaxTabs tabs.
var ErrTabLimit = errors.New("browser: tab limit reached")

// ErrNoTab reports an operation on a tab id the panel does not know.
var ErrNoTab = errors.New("browser: unknown tab")

// Tunnel is one dedicated transport to the Host. DialContext must resolve and
// connect on the Host, exactly as a dynamic forwarding rule does. Done closes
// when the transport dies. The panel always Discards its tunnel because a
// browser connection is never reusable.
type Tunnel interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Done() <-chan struct{}
	Discard()
}

// Connector opens the panel's dedicated tunnel. The app implements it on the
// shared Connection Manager with a non-reusable channel lease.
type Connector interface {
	Connect(ctx context.Context, hostID model.HostID) (Tunnel, error)
}

// Config is what the app supplies to a panel.
type Config struct {
	HostID    model.HostID
	Label     string
	Connector Connector
	// Redraw requests a frame after a state change. It may run on the
	// tunnel's goroutine; the app's window invalidation is safe there.
	Redraw func()
	// Bookmarks and Icons are the shared process-wide stores. Both may be
	// nil in tests that only exercise routing.
	Bookmarks *Bookmarks
	Icons     *SiteIcons
	// Downloads is the page download policy. A nil Dir keeps downloads
	// disabled.
	Downloads DownloadOptions
	// Cookies returns the Cookie header for a gateway URL. The app reads it
	// from the WebView cookie store for favicon fetches.
	Cookies func(gatewayURL string) string
}

// Tab is one panel tab. URL is the real page address; the WebView loads the
// panel's loopback gateway URL for it.
type Tab struct {
	ID           string
	URL          string
	Title        string
	IconDataURL  string
	Loading      bool
	CanGoBack    bool
	CanGoForward bool

	// provisional marks URL, Title, and the navigation flags as the address'
	// guess before the page reports anything. A Stop that aborts the
	// navigation restores the last committed page, so the tab never outlives
	// the navigation it described.
	provisional bool
	previous    pageSnapshot
}

// pageSnapshot is the last committed page of a tab, held while a navigation is
// provisional so a stop can put the tab back exactly as it was.
type pageSnapshot struct {
	url          string
	title        string
	icon         string
	canGoBack    bool
	canGoForward bool
}

// State is an immutable snapshot for the UI and tests.
type State struct {
	HostID    model.HostID
	Label     string
	Route     Route
	Failure   Failure
	Detail    string
	Notice    Failure
	NoticeMsg string
	ActiveID  string
	Tabs      []Tab
}

// PageState is what a page reports after a navigation, title, or load event.
type PageState struct {
	URL          string
	Title        string
	Loading      bool
	CanGoBack    bool
	CanGoForward bool
}

// Panel owns one Session's browser tabs and its one dedicated tunnel.
type Panel struct {
	cfg Config

	mu         sync.Mutex
	life       context.Context
	cancel     context.CancelFunc
	closed     bool
	generation uint64
	route      Route
	failure    Failure
	detail     string
	notice     Failure
	noticeMsg  string
	tabs       []Tab
	active     string
	nextTab    int
	tunnel     Tunnel
	gateway    *gateway
	listeners  map[int]func(State)
	nextListen int
	downloads  *downloadManager
	iconTried  map[string]bool
	iconActive map[string]bool
}

// New creates a closed panel. Start opens its dedicated tunnel; Close
// releases everything.
func New(cfg Config) *Panel {
	life, cancel := context.WithCancel(context.Background())
	if cfg.Downloads.Redraw == nil {
		cfg.Downloads.Redraw = cfg.Redraw
	}
	panel := &Panel{
		cfg:        cfg,
		life:       life,
		cancel:     cancel,
		route:      RouteDisconnected,
		listeners:  make(map[int]func(State)),
		iconTried:  make(map[string]bool),
		iconActive: make(map[string]bool),
	}
	downloadOptions := cfg.Downloads
	downloadOptions.Validate = func(gatewayURL string) error {
		panel.mu.Lock()
		gateway := panel.gateway
		panel.mu.Unlock()
		if gateway == nil {
			return ErrNotConnected
		}
		if _, ok := gateway.RemoteURL(gatewayURL); !ok {
			return ErrDownloadURL
		}
		return nil
	}
	panel.downloads = newDownloadManager(downloadOptions, func(ctx context.Context, gatewayURL, cookies string) (*http.Response, error) {
		panel.mu.Lock()
		gateway := panel.gateway
		panel.mu.Unlock()
		if gateway == nil {
			return nil, ErrDownloadURL
		}
		return gateway.openDownload(ctx, gatewayURL, cookies)
	})
	return panel
}

// Start opens the dedicated tunnel. It is idempotent while connecting or
// connected. The call returns immediately; Subscribe or Redraw observes the
// result.
func (p *Panel) Start() {
	p.mu.Lock()
	if p.closed || p.route == RouteConnecting || p.route == RouteConnected {
		p.mu.Unlock()
		return
	}
	p.generation++
	generation := p.generation
	p.route = RouteConnecting
	p.failure = FailureNone
	p.detail = ""
	p.notice = FailureNone
	p.noticeMsg = ""
	p.mu.Unlock()
	p.changed()
	go p.connect(generation)
}

func (p *Panel) connect(generation uint64) {
	if p.cfg.Connector == nil {
		p.failRoute(generation, FailureConnect, "no browser connector")
		return
	}
	tunnel, err := p.cfg.Connector.Connect(p.life, p.cfg.HostID)
	if err != nil {
		p.failRoute(generation, FailureConnect, err.Error())
		return
	}
	gateway := newGateway(tunnel)

	p.mu.Lock()
	if p.closed || generation != p.generation || p.life.Err() != nil {
		p.mu.Unlock()
		tunnel.Discard()
		return
	}
	p.tunnel = tunnel
	p.gateway = gateway
	p.route = RouteConnected
	p.failure = FailureNone
	p.detail = ""
	if len(p.tabs) == 0 {
		p.appendBlankTabLocked()
	}
	p.mu.Unlock()
	p.changed()

	if done := tunnel.Done(); done != nil {
		go func() {
			select {
			case <-done:
				p.failRoute(generation, FailureTunnel, "the dedicated tunnel closed")
			case <-p.life.Done():
			}
		}()
	}
}

// failRoute stops browsing and releases the tunnel. It is safe to call more
// than once; only the current generation takes effect.
func (p *Panel) failRoute(generation uint64, failure Failure, detail string) {
	p.mu.Lock()
	if p.closed || generation != p.generation || p.route == RouteDisconnected {
		p.mu.Unlock()
		return
	}
	// Bumping the generation makes late connect results and repeated Done
	// notifications harmless.
	p.generation++
	gateway := p.gateway
	tunnel := p.tunnel
	p.gateway = nil
	p.tunnel = nil
	p.route = RouteDisconnected
	p.failure = failure
	p.detail = detail
	p.iconTried = make(map[string]bool)
	p.iconActive = make(map[string]bool)
	for i := range p.tabs {
		p.tabs[i].Loading = false
		p.tabs[i].CanGoBack = false
		p.tabs[i].CanGoForward = false
	}
	p.mu.Unlock()

	p.downloads.cancelAll()
	if gateway != nil {
		gateway.Close()
	}
	if tunnel != nil {
		tunnel.Discard()
	}
	p.changed()
}

// Retry opens a fresh dedicated tunnel after a disconnect. It reports false
// when the panel was not disconnected and therefore has nothing to retry.
func (p *Panel) Retry() bool {
	p.mu.Lock()
	if p.closed || p.route != RouteDisconnected {
		p.mu.Unlock()
		return false
	}
	p.generation++
	generation := p.generation
	p.route = RouteConnecting
	p.failure = FailureNone
	p.detail = ""
	p.notice = FailureNone
	p.noticeMsg = ""
	p.mu.Unlock()
	p.changed()
	go p.connect(generation)
	return true
}

// Close releases the tunnel, the gateway, and every page's route. It is safe
// to call more than once.
func (p *Panel) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.generation++
	gateway := p.gateway
	tunnel := p.tunnel
	p.gateway = nil
	p.tunnel = nil
	p.route = RouteDisconnected
	p.listeners = make(map[int]func(State))
	p.mu.Unlock()

	p.cancel()
	p.downloads.Close()
	if gateway != nil {
		gateway.Close()
	}
	if tunnel != nil {
		tunnel.Discard()
	}
}

// State returns a deep copy of the panel state.
func (p *Panel) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := State{
		HostID:    p.cfg.HostID,
		Label:     p.cfg.Label,
		Route:     p.route,
		Failure:   p.failure,
		Detail:    p.detail,
		Notice:    p.notice,
		NoticeMsg: p.noticeMsg,
		ActiveID:  p.active,
		Tabs:      append([]Tab(nil), p.tabs...),
	}
	return state
}

// Subscribe observes state changes. The listener runs after the change and
// may call back into the panel. The returned function unsubscribes.
func (p *Panel) Subscribe(fn func(State)) func() {
	if fn == nil {
		return func() {}
	}
	p.mu.Lock()
	id := p.nextListen
	p.nextListen++
	p.listeners[id] = fn
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.listeners, id)
		p.mu.Unlock()
	}
}

func (p *Panel) changed() {
	state := p.State()
	p.mu.Lock()
	listeners := make([]func(State), 0, len(p.listeners))
	for _, fn := range p.listeners {
		listeners = append(listeners, fn)
	}
	p.mu.Unlock()
	if p.cfg.Redraw != nil {
		p.cfg.Redraw()
	}
	for _, fn := range listeners {
		fn(state)
	}
}

// NewTab appends a blank tab and makes it active.
func (p *Panel) NewTab() (string, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", ErrNoTab
	}
	if len(p.tabs) >= MaxTabs {
		p.mu.Unlock()
		return "", ErrTabLimit
	}
	id := p.appendBlankTabLocked()
	p.mu.Unlock()
	p.changed()
	return id, nil
}

func (p *Panel) appendBlankTabLocked() string {
	p.nextTab++
	id := fmt.Sprintf("tab-%d", p.nextTab)
	p.tabs = append(p.tabs, Tab{ID: id})
	p.active = id
	return id
}

// CloseTab closes id and activates a neighbor. The panel always keeps at
// least one blank tab, as the original does.
func (p *Panel) CloseTab(id string) bool {
	p.mu.Lock()
	index := p.tabIndexLocked(id)
	if index < 0 || p.closed {
		p.mu.Unlock()
		return false
	}
	p.tabs = append(p.tabs[:index], p.tabs[index+1:]...)
	if p.active == id {
		p.active = ""
		if index < len(p.tabs) {
			p.active = p.tabs[index].ID
		} else if len(p.tabs) > 0 {
			p.active = p.tabs[len(p.tabs)-1].ID
		}
	}
	if len(p.tabs) == 0 {
		p.appendBlankTabLocked()
	}
	p.mu.Unlock()
	p.changed()
	return true
}

// Activate makes id the shown tab.
func (p *Panel) Activate(id string) bool {
	p.mu.Lock()
	if p.closed || p.tabIndexLocked(id) < 0 {
		p.mu.Unlock()
		return false
	}
	p.active = id
	p.mu.Unlock()
	p.changed()
	return true
}

// ActiveTab returns the active tab, if any.
func (p *Panel) ActiveTab() (Tab, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index := p.tabIndexLocked(p.active); index >= 0 {
		return p.tabs[index], true
	}
	return Tab{}, false
}

func (p *Panel) tabIndexLocked(id string) int {
	for i := range p.tabs {
		if p.tabs[i].ID == id {
			return i
		}
	}
	return -1
}

// Navigate validates address and prepares tabID to load it. It returns the
// loopback gateway URL the page must load; the real address stays in the
// panel state so the address bar never shows a loopback port. It refuses to
// navigate while the tunnel is not connected.
func (p *Panel) Navigate(tabID, address string) (string, error) {
	normalized, err := domainbrowser.NormalizeURL(address)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", ErrNoTab
	}
	index := p.tabIndexLocked(tabID)
	if index < 0 {
		p.mu.Unlock()
		return "", ErrNoTab
	}
	if p.route != RouteConnected || p.gateway == nil {
		p.mu.Unlock()
		return "", ErrNotConnected
	}
	gatewayURL, err := p.gateway.URLFor(normalized)
	if err != nil {
		p.mu.Unlock()
		return "", err
	}
	tab := &p.tabs[index]
	// The address is a guess until the page commits or reports a title. Keep
	// the last committed page so Stop can put the tab back instead of leaving
	// a label for a page that never arrived.
	if !tab.provisional {
		tab.previous = pageSnapshot{
			url:          tab.URL,
			title:        tab.Title,
			icon:         tab.IconDataURL,
			canGoBack:    tab.CanGoBack,
			canGoForward: tab.CanGoForward,
		}
	}
	tab.URL = normalized
	tab.Title = hostLabel(normalized)
	tab.IconDataURL = p.cachedIconLocked(normalized)
	tab.provisional = true
	tab.Loading = true
	tab.CanGoBack = false
	tab.CanGoForward = false
	p.active = tabID
	p.notice = FailureNone
	p.noticeMsg = ""
	p.mu.Unlock()
	// The caller loads the page after this returns; the address bar updates
	// from the state change now, even if the page load is slow.
	p.changed()
	return gatewayURL, nil
}

// PageChanged applies a navigation, title, or load event from a page view.
// A gateway URL is mapped back to the real page address.
func (p *Panel) PageChanged(tabID string, page PageState) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	index := p.tabIndexLocked(tabID)
	if index < 0 {
		p.mu.Unlock()
		return
	}
	changed := false
	committed := false
	iconURL := ""
	if page.URL != "" && p.gateway != nil {
		if remote, ok := p.gateway.RemoteURL(page.URL); ok {
			if p.tabs[index].URL != remote {
				p.tabs[index].URL = remote
				changed = true
			}
			if p.tabs[index].Title == "" || p.tabs[index].provisional {
				// A provisional host label follows the committed URL (a
				// redirect may land somewhere else) until a title arrives.
				if label := hostLabel(remote); p.tabs[index].Title != label {
					p.tabs[index].Title = label
					changed = true
				}
			}
			// Reaching a gateway page commits the navigation.
			committed = true
		}
	}
	if page.Title != "" {
		// A title is reported for the page that committed, so it settles the
		// navigation even when it happens to equal the provisional label.
		if p.tabs[index].Title != page.Title {
			p.tabs[index].Title = page.Title
			changed = true
		}
		committed = true
	}
	// A finished load is a committed navigation even when the page never
	// reports a title: the host label now describes a page that exists.
	if page.URL != "" && !page.Loading {
		committed = true
	}
	if committed && p.tabs[index].provisional {
		p.tabs[index].provisional = false
		changed = true
	}
	if committed && p.tabs[index].URL != "" {
		if cached := p.cachedIconLocked(p.tabs[index].URL); cached != "" {
			if p.tabs[index].IconDataURL != cached {
				p.tabs[index].IconDataURL = cached
				changed = true
			}
		} else if p.cfg.Icons != nil {
			iconURL = p.tabs[index].URL
		}
	}
	if p.tabs[index].Loading != page.Loading {
		p.tabs[index].Loading = page.Loading
		changed = true
	}
	if p.tabs[index].CanGoBack != page.CanGoBack {
		p.tabs[index].CanGoBack = page.CanGoBack
		changed = true
	}
	if p.tabs[index].CanGoForward != page.CanGoForward {
		p.tabs[index].CanGoForward = page.CanGoForward
		changed = true
	}
	if !page.Loading && p.notice == FailurePage {
		p.notice = FailureNone
		p.noticeMsg = ""
		changed = true
	}
	p.mu.Unlock()
	if changed {
		p.changed()
	}
	if iconURL != "" {
		p.ensureFavicon(tabID)
	}
}

// PageFailed records a page load failure without tearing down the tunnel. The
// page's own error body stays visible; the UI shows a dismissible notice.
func (p *Panel) PageFailed(tabID, detail string) {
	p.mu.Lock()
	index := p.tabIndexLocked(tabID)
	if index < 0 || p.closed {
		p.mu.Unlock()
		return
	}
	p.tabs[index].Loading = false
	p.tabs[index].provisional = false
	p.notice = FailurePage
	p.noticeMsg = detail
	p.mu.Unlock()
	p.changed()
}

// PageStopped records a stopped load. WebView2 reports a stopped load as an
// aborted navigation and fires no finish event, so the panel must settle the
// tab itself. A stop before the page committed drops the provisional address,
// title, and navigation flags and restores the last committed page; the tab
// then describes the page that is actually shown rather than the one that was
// asked for. It reports whether the tab was provisional.
func (p *Panel) PageStopped(tabID string) bool {
	p.mu.Lock()
	index := p.tabIndexLocked(tabID)
	if index < 0 || p.closed {
		p.mu.Unlock()
		return false
	}
	tab := &p.tabs[index]
	tab.Loading = false
	restored := tab.provisional
	if restored {
		tab.URL = tab.previous.url
		tab.Title = tab.previous.title
		tab.IconDataURL = tab.previous.icon
		tab.CanGoBack = tab.previous.canGoBack
		tab.CanGoForward = tab.previous.canGoForward
		tab.provisional = false
	}
	p.mu.Unlock()
	p.changed()
	return restored
}

// cachedIconLocked looks up the Host's cached favicon for pageURL.
func (p *Panel) cachedIconLocked(pageURL string) string {
	if p.cfg.Icons == nil {
		return ""
	}
	if icon, ok := p.cfg.Icons.Get(p.cfg.HostID, pageURL); ok {
		return icon.DataURL
	}
	return ""
}

// ensureFavicon fetches the origin's favicon at most once per route, through
// the panel's tunnel. The fetch runs off the UI goroutine; a missing or
// undecodable icon leaves the letter/glyph fallback in place.
func (p *Panel) ensureFavicon(tabID string) {
	p.mu.Lock()
	index := p.tabIndexLocked(tabID)
	if index < 0 || p.closed || p.route != RouteConnected || p.gateway == nil || p.cfg.Icons == nil {
		p.mu.Unlock()
		return
	}
	pageURL := p.tabs[index].URL
	origin, err := domainbrowser.Origin(pageURL)
	if err != nil {
		p.mu.Unlock()
		return
	}
	key := string(p.cfg.HostID) + "\n" + origin
	if p.iconTried[key] || p.iconActive[key] || p.tabs[index].IconDataURL != "" {
		p.mu.Unlock()
		return
	}
	p.iconActive[key] = true
	gateway := p.gateway
	p.mu.Unlock()

	go func() {
		dataURL, err := gateway.fetchFavicon(p.life, pageURL, p.cookieHeader(pageURL))
		p.mu.Lock()
		delete(p.iconActive, key)
		p.iconTried[key] = true
		if err == nil && p.route == RouteConnected && p.gateway == gateway {
			p.cfg.Icons.SetAuto(p.cfg.HostID, pageURL, dataURL)
			for i := range p.tabs {
				if sameOrigin(p.tabs[i].URL, pageURL) {
					p.tabs[i].IconDataURL = dataURL
				}
			}
		}
		p.mu.Unlock()
		p.changed()
	}()
}

func sameOrigin(a, b string) bool {
	left, err := domainbrowser.Origin(a)
	if err != nil {
		return false
	}
	right, err := domainbrowser.Origin(b)
	return err == nil && left == right
}

func (p *Panel) cookieHeader(pageURL string) string {
	if p.cfg.Cookies == nil {
		return ""
	}
	p.mu.Lock()
	gateway := p.gateway
	p.mu.Unlock()
	if gateway == nil {
		return ""
	}
	gatewayURL, err := gateway.URLFor(pageURL)
	if err != nil {
		return ""
	}
	return p.cfg.Cookies(gatewayURL)
}

// SetPageFlags updates the navigation state after a same-document history
// command, where no load event fires.
func (p *Panel) SetPageFlags(tabID string, canBack, canForward, loading bool) {
	p.mu.Lock()
	index := p.tabIndexLocked(tabID)
	if index < 0 || p.closed {
		p.mu.Unlock()
		return
	}
	changed := p.tabs[index].CanGoBack != canBack ||
		p.tabs[index].CanGoForward != canForward ||
		p.tabs[index].Loading != loading
	p.tabs[index].CanGoBack = canBack
	p.tabs[index].CanGoForward = canForward
	p.tabs[index].Loading = loading
	p.mu.Unlock()
	if changed {
		p.changed()
	}
}

// GatewayURL returns the loopback URL for a tab's current address without
// changing its state. The UI uses it when it must recreate a page after a
// tab switch or a retry.
func (p *Panel) GatewayURL(tabID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.tabIndexLocked(tabID)
	if index < 0 || p.closed {
		return "", ErrNoTab
	}
	if p.route != RouteConnected || p.gateway == nil {
		return "", ErrNotConnected
	}
	if p.tabs[index].URL == "" {
		return "", ErrNoTab
	}
	return p.gateway.URLFor(p.tabs[index].URL)
}

// IsGatewayURL reports whether rawURL is one of this route's loopback
// gateway URLs. The UI allows those navigations and maps every other HTTP(S)
// address back through Navigate.
func (p *Panel) IsGatewayURL(rawURL string) bool {
	p.mu.Lock()
	gateway := p.gateway
	p.mu.Unlock()
	if gateway == nil {
		return false
	}
	_, ok := gateway.RemoteURL(rawURL)
	return ok
}

// StartDownload fetches a page download through this route's gateway. The
// caller has already prevented the WebView's own download.
func (p *Panel) StartDownload(gatewayURL, suggestedName string) (Download, error) {
	if p == nil || p.downloads == nil {
		return Download{}, ErrDownloadGone
	}
	return p.downloads.Start(gatewayURL, suggestedName)
}

// Downloads returns the download history, newest first.
func (p *Panel) Downloads() []Download { return p.downloads.Downloads() }

// CancelDownload aborts one active download.
func (p *Panel) CancelDownload(id string) bool { return p.downloads.Cancel(id) }

// RemoveDownload drops one finished record.
func (p *Panel) RemoveDownload(id string) bool { return p.downloads.Remove(id) }

// ClearDownloads removes every finished record.
func (p *Panel) ClearDownloads() bool { return p.downloads.ClearFinished() }

// BookmarkView adds display-only favicon data to a bookmark.
type BookmarkView struct {
	domainbrowser.Bookmark
	IconDataURL string
	IconGlyph   string
	IconColor   string
}

// VisibleBookmarks returns this Host's bookmarks with cached favicon data.
func (p *Panel) VisibleBookmarks() []BookmarkView {
	if p.cfg.Bookmarks == nil {
		return nil
	}
	items := p.cfg.Bookmarks.List(p.cfg.HostID)
	out := make([]BookmarkView, 0, len(items))
	for _, item := range items {
		view := BookmarkView{Bookmark: item, IconGlyph: item.Icon, IconColor: item.Color}
		if p.cfg.Icons != nil {
			if icon, ok := p.cfg.Icons.Get(p.cfg.HostID, item.URL); ok {
				view.IconDataURL = icon.DataURL
				if icon.Glyph != "" {
					view.IconGlyph = icon.Glyph
				}
				if icon.Color != "" {
					view.IconColor = icon.Color
				}
			}
		}
		out = append(out, view)
	}
	return out
}

// UpsertBookmark creates or edits a bookmark for this Host.
func (p *Panel) UpsertBookmark(input domainbrowser.BookmarkInput) (domainbrowser.Bookmark, error) {
	if p.cfg.Bookmarks == nil {
		return domainbrowser.Bookmark{}, errors.New("browser: bookmarks are unavailable")
	}
	return p.cfg.Bookmarks.Upsert(p.cfg.HostID, input)
}

// RemoveBookmark deletes a bookmark on behalf of this Host.
func (p *Panel) RemoveBookmark(id string) (bool, error) {
	if p.cfg.Bookmarks == nil {
		return false, errors.New("browser: bookmarks are unavailable")
	}
	return p.cfg.Bookmarks.Remove(p.cfg.HostID, id)
}

// hostLabel is the compact tab label used until the page reports a title.
func hostLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	return parsed.Host
}
