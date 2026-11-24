// Phone Host Browser wiring. The phone shell owns the presentation; this file
// supplies it the shared application/browser panel over a per-Host MyGo website
// data store, a dedicated SSH channel lease from the shared Connection Manager,
// and an untrusted MyGo page for the web area.
//
// The lease, the loopback gateway (127.0.0.2 with the 127.0.0.1 fallback),
// destination policy, and the bounded download manager are the same
// application/browser code the desktop browser uses. Nothing about a page's
// routing is re-implemented for the phone: the phone page adapter only maps
// native WebView events onto the panel's port and refuses any navigation the
// panel did not gateway.
//
// Capability is a device fact, not a build guess. The probe makes one
// ephemeral session and round-trips a cookie through it. A WebView without
// multi-profile support (the Android host's MULTI_PROFILE gate) fails here, so
// the entry is hidden with MyGo's own reason instead of opening a browser that
// cannot keep a per-Host data store. The downloads directory is probed too,
// because a download whose callback or destination is unavailable must not be
// offered.

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneBrowserVault is the Vault read the phone browser needs. It is declared
// here rather than reusing the desktop forwarding seam, whose file is excluded
// from phone builds.
type phoneBrowserVault interface {
	Get(ctx context.Context, id model.HostID) (domainvault.Host, error)
}

// phoneBrowserCapabilityProbe is the device probe, replaceable in tests so a
// platform's missing profile support can be exercised without a device.
type phoneBrowserCapabilityProbe func() phoneshell.BrowserCapability

// phoneBrowserViewFactory makes the window's web view. A real phone uses
// Window.NewWebView; a test can inject a factory to prove the page options
// without a native surface.
type phoneBrowserViewFactory func(window *mygo.Window, opts mygo.WebViewOptions) (*mygo.WebView, error)

// phoneBrowserProvider is the phone shell's BrowserProvider. It opens one
// shared browser panel per Host, each with the Host's own isolated session.
type phoneBrowserProvider struct {
	vault       phoneBrowserVault
	routes      routeSource
	connections *appconnection.Manager
	// downloadsDir resolves the app's Downloads directory. The shared download
	// manager keeps every file inside it.
	downloadsDir func() (string, error)
	// redraw requests a repaint after a panel or download state change.
	redraw  func()
	probe   phoneBrowserCapabilityProbe
	newView phoneBrowserViewFactory

	once       sync.Once
	capability phoneshell.BrowserCapability
}

var _ phoneshell.BrowserProvider = (*phoneBrowserProvider)(nil)

// newPhoneBrowserProvider builds the provider from the same Vault, route
// resolver, and Connection Manager the phone Sessions use. Host keys, jump
// chains, proxies, and algorithm policy therefore resolve exactly as they do
// for a terminal.
func newPhoneBrowserProvider(vault phoneBrowserVault, routes routeSource,
	connections *appconnection.Manager, downloadsDir func() (string, error),
	redraw func()) *phoneBrowserProvider {
	if downloadsDir == nil {
		downloadsDir = func() (string, error) { return mygo.App.Path(mygo.PathDownloads) }
	}
	return &phoneBrowserProvider{
		vault:        vault,
		routes:       routes,
		connections:  connections,
		downloadsDir: downloadsDir,
		redraw:       redraw,
		probe:        probePhoneBrowserCapability,
		newView: func(window *mygo.Window, opts mygo.WebViewOptions) (*mygo.WebView, error) {
			if window == nil {
				return nil, errors.New("app: the Browser's window is not open")
			}
			return window.NewWebView(opts)
		},
	}
}

// Capability probes the device once and then reports the cached fact. The
// shell reads it on every frame, so the probe must not repeat native work.
func (p *phoneBrowserProvider) Capability() phoneshell.BrowserCapability {
	if p == nil {
		return phoneshell.BrowserCapability{Reason: "The Host Browser is not available in this build."}
	}
	p.once.Do(func() {
		probe := p.probe
		if probe == nil {
			probe = probePhoneBrowserCapability
		}
		p.capability = probe()
		if !p.capability.Supported && strings.TrimSpace(p.capability.Reason) == "" {
			p.capability.Reason = "This device cannot run the Host Browser."
		}
	})
	return p.capability
}

// Open starts one Host's browser. The panel is handed a fresh connector; every
// panel acquires its own dedicated Channel lease, so two Browsers, or a
// Browser and a terminal, never share a transport.
func (p *phoneBrowserProvider) Open(host domainvault.Host, window *mygo.Window) (phoneshell.Browser, error) {
	if capability := p.Capability(); !capability.Supported {
		reason := strings.TrimSpace(capability.Reason)
		if reason == "" {
			reason = "This device cannot run the Host Browser."
		}
		return nil, errors.New(reason)
	}
	if !host.Protocol.SupportsForwarding() {
		return nil, fmt.Errorf("app: %s Hosts cannot carry a Browser", host.Protocol)
	}
	if window == nil {
		return nil, errors.New("app: the Browser's window is not open")
	}
	label := strings.TrimSpace(host.Label)
	if label == "" {
		label = host.Hostname
	}
	newView := p.newView
	if newView == nil {
		newView = func(window *mygo.Window, opts mygo.WebViewOptions) (*mygo.WebView, error) {
			return window.NewWebView(opts)
		}
	}
	redraw := p.redraw
	if redraw == nil {
		redraw = invalidatePhoneBrowserWindows
	}
	creator := func(opts mygo.WebViewOptions) (*mygo.WebView, error) {
		return newView(window, opts)
	}
	browser := &phoneBrowser{
		hostID:     host.ID,
		newView:    creator,
		newSession: mygo.NewSession,
		saveCopy:   mygo.Dialog.SaveCopy,
		sessions:   make(map[string]*mygo.Session),
	}
	core := appbrowser.New(appbrowser.Config{
		HostID: host.ID,
		Label:  label,
		Connector: phoneBrowserConnector{
			vault:       p.vault,
			routes:      p.routes,
			connections: p.connections,
		},
		Redraw: redraw,
		// Bookmarks and site icons are desktop stores; the phone Browser
		// deliberately keeps no cross-Host list of its own.
		Cookies: browser.cookieHeader,
		Downloads: appbrowser.DownloadOptions{
			Dir:     p.downloadsDir,
			Cookies: browser.cookieHeader,
			Redraw:  redraw,
		},
	})
	browser.core = core
	return browser, nil
}

// phoneBrowserSessionName gives one Host's page at one remote site its own
// persistent website data store. The identity is the Host plus the remote
// site's origin (scheme, host, and port), never the loopback gateway's port:
// cookies are not isolated by port, so two sites behind one Host would
// otherwise share one jar, and a reconnect rebinds the gateway on a new port
// while the site stays the same. Hashing keeps the on-disk name safe whatever
// characters a Vault record uses; the same pair always maps back to the same
// store.
func phoneBrowserSessionName(id model.HostID, siteOrigin string) string {
	sum := sha256.Sum256([]byte(string(id) + "\n" + phoneBrowserSiteOrigin(siteOrigin)))
	return "phone-browser-" + hex.EncodeToString(sum[:])
}

// phoneBrowserSiteOrigin canonicalizes a site origin so equivalent spellings
// (case, a default port) share a store and distinct sites never do. It is the
// same normalization the surface compares pages by.
func phoneBrowserSiteOrigin(raw string) string {
	origin, err := domainbrowser.CanonicalOrigin(strings.TrimSpace(raw))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	return origin
}

// phoneBrowserCookieHeader reads the Host's own session cookies for a gateway
// URL, so a favicon or download request through the tunnel carries the same
// sign-in as the page. The page never sees these values.
func phoneBrowserCookieHeader(session *mygo.Session, gatewayURL string) string {
	if session == nil {
		return ""
	}
	cookies, err := session.Cookies.List(mygo.CookieFilter{URL: gatewayURL})
	if err != nil {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

// invalidatePhoneBrowserWindows repaints every phone window. A panel calls it
// from a tunnel or download goroutine; MyGo's invalidation is safe there.
func invalidatePhoneBrowserWindows() {
	for _, window := range mygo.Windows() {
		if window != nil {
			window.Invalidate()
		}
	}
}

// phoneBrowserKeepalive detects a dead dedicated tunnel in about fifteen
// seconds, as the desktop browser does: a browser must show the disconnect and
// offer Retry promptly instead of leaving a page that silently stopped.
var phoneBrowserKeepalive = appconnection.KeepalivePolicy{
	Interval: 5 * time.Second,
	CountMax: 3,
}

// phoneBrowserConnector is the shared Connection Manager adapted to the
// browser's Connector port. It mirrors the desktop connector: one fresh,
// dedicated Channel lease per panel, with the Host's resolved jumps, proxy,
// network, and algorithms.
type phoneBrowserConnector struct {
	vault       phoneBrowserVault
	routes      routeSource
	connections *appconnection.Manager
}

var _ appbrowser.Connector = phoneBrowserConnector{}

func (c phoneBrowserConnector) Connect(ctx context.Context, hostID model.HostID) (appbrowser.Tunnel, error) {
	if c.vault == nil {
		return nil, errors.New("app: no Vault")
	}
	host, err := c.vault.Get(ctx, hostID)
	if err != nil {
		return nil, err
	}
	// A browser tunnel opens an SSH direct-tcpip channel. A Telnet or Local
	// Host has no channel to dedicate, so refuse it before touching the
	// Connection Manager.
	if !host.Protocol.SupportsForwarding() {
		return nil, fmt.Errorf("app: %s Hosts cannot host a browser tunnel", host.Protocol)
	}
	if c.connections == nil {
		return nil, errors.New("app: no connection manager")
	}
	var route appconnection.Route
	if c.routes != nil {
		route, err = c.routes.Route(ctx, hostID)
		if err != nil {
			return nil, err
		}
	}
	lease, err := c.connections.Acquire(ctx, appconnection.Request{
		HostID:            host.ID,
		Hostname:          host.Hostname,
		Port:              host.Port,
		Username:          host.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Network:           route.Network,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		Dedicated:         true,
		LeaseKind:         appconnection.LeaseChannel,
		Keepalive:         &phoneBrowserKeepalive,
	})
	if err != nil {
		return nil, err
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, err
	}
	return &phoneBrowserTunnel{lease: lease, ForwardingClient: forwarding}, nil
}

// phoneBrowserTunnel ties the browser's dialer to its dedicated lease. The
// panel always discards it; a browser connection is never reusable.
type phoneBrowserTunnel struct {
	lease *appconnection.Lease
	appconnection.ForwardingClient
}

func (t *phoneBrowserTunnel) Done() <-chan struct{} { return t.lease.Done() }
func (t *phoneBrowserTunnel) Discard()              { _ = t.lease.Discard() }

// phoneBrowser is the shared panel as the phone shell sees it. It also owns
// the per-site website data stores, the window's web-view factory, and the
// system save path a finished download is exported through.
type phoneBrowser struct {
	core    *appbrowser.Panel
	hostID  model.HostID
	newView func(mygo.WebViewOptions) (*mygo.WebView, error)
	// newSession makes the persistent store of a site. A test injects one
	// that records the names without a native host.
	newSession func(string) *mygo.Session
	// saveCopy is the system save/share path (MyGo's Dialog.SaveCopy on a
	// phone). An empty path with a nil error means the user cancelled.
	saveCopy func(path, name string) (string, error)

	mu       sync.Mutex
	sessions map[string]*mygo.Session
}

// sessionForSite returns the store of one canonical site origin, making it on
// first use. An empty origin (a blank tab) has no store: nothing has been
// written anywhere yet.
func (b *phoneBrowser) sessionForSite(siteOrigin string) *mygo.Session {
	site := phoneBrowserSiteOrigin(siteOrigin)
	if site == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if session := b.sessions[site]; session != nil {
		return session
	}
	newSession := b.newSession
	if newSession == nil {
		newSession = mygo.NewSession
	}
	session := newSession(phoneBrowserSessionName(b.hostID, site))
	if b.sessions == nil {
		b.sessions = make(map[string]*mygo.Session)
	}
	b.sessions[site] = session
	return session
}

// sessionForGateway returns the store the page at a gateway URL belongs to.
// The panel maps the loopback URL back to the real remote address, so a
// download or favicon reads the cookies of the site whose page started it and
// never another Host's or site's jar.
func (b *phoneBrowser) sessionForGateway(gatewayURL string) *mygo.Session {
	if b == nil || b.core == nil {
		return nil
	}
	remote, ok := b.core.RemoteURL(gatewayURL)
	if !ok {
		return nil
	}
	origin, err := domainbrowser.Origin(remote)
	if err != nil {
		return nil
	}
	return b.sessionForSite(origin)
}

// cookieHeader reads the cookies of the site the gateway URL serves. The page
// never sees these values; they go on the tunnel request for a favicon or a
// download.
func (b *phoneBrowser) cookieHeader(gatewayURL string) string {
	return phoneBrowserCookieHeader(b.sessionForGateway(gatewayURL), gatewayURL)
}

// Downloads is the shared manager's history.
func (b *phoneBrowser) Downloads() []appbrowser.Download {
	if b == nil || b.core == nil {
		return nil
	}
	return b.core.Downloads()
}

// CancelDownload aborts one active download.
func (b *phoneBrowser) CancelDownload(id string) bool {
	if b == nil || b.core == nil {
		return false
	}
	return b.core.CancelDownload(id)
}

// ExportDownload copies a finished file through the system save picker. The
// downloaded file stays in the app's directory; the picker receives a copy.
func (b *phoneBrowser) ExportDownload(download appbrowser.Download) (string, error) {
	if download.Status != appbrowser.DownloadCompleted {
		return "", errors.New("app: only a completed download can be exported")
	}
	if strings.TrimSpace(download.SavePath) == "" {
		return "", errors.New("app: the download has no file")
	}
	if b.saveCopy == nil {
		return "", errors.New("app: this build has no system save path")
	}
	return b.saveCopy(download.SavePath, download.Filename)
}

var _ phoneshell.Browser = (*phoneBrowser)(nil)

func (b *phoneBrowser) Start()                  { b.core.Start() }
func (b *phoneBrowser) Retry() bool             { return b.core.Retry() }
func (b *phoneBrowser) Close()                  { b.core.Close() }
func (b *phoneBrowser) State() appbrowser.State { return b.core.State() }
func (b *phoneBrowser) Subscribe(fn func(appbrowser.State)) func() {
	return b.core.Subscribe(fn)
}
func (b *phoneBrowser) Navigate(tabID, address string) (string, error) {
	return b.core.Navigate(tabID, address)
}
func (b *phoneBrowser) NewTab() (string, error) { return b.core.NewTab() }
func (b *phoneBrowser) CloseTab(id string) bool { return b.core.CloseTab(id) }
func (b *phoneBrowser) Activate(id string) bool { return b.core.Activate(id) }
func (b *phoneBrowser) GatewayURL(tabID string) (string, error) {
	return b.core.GatewayURL(tabID)
}
func (b *phoneBrowser) IsGatewayURL(rawURL string) bool { return b.core.IsGatewayURL(rawURL) }
func (b *phoneBrowser) PageChanged(tabID string, page appbrowser.PageState) {
	b.core.PageChanged(tabID, page)
}
func (b *phoneBrowser) PageFailed(tabID, detail string) { b.core.PageFailed(tabID, detail) }
func (b *phoneBrowser) PageStopped(tabID string) bool   { return b.core.PageStopped(tabID) }
func (b *phoneBrowser) SetPageFlags(tabID string, canBack, canForward, loading bool) {
	b.core.SetPageFlags(tabID, canBack, canForward, loading)
}
func (b *phoneBrowser) StartDownload(gatewayURL, suggestedName string) (appbrowser.Download, error) {
	return b.core.StartDownload(gatewayURL, suggestedName)
}

// NewPage makes one untrusted web view in the store of the page's own site.
// Pages of one Host and site share that store, as tabs of one browser do;
// another site gets another store, and another Host never shares either.
func (b *phoneBrowser) NewPage(siteOrigin string) (phoneshell.BrowserPage, error) {
	if b.newView == nil {
		return nil, errors.New("app: no Browser window")
	}
	session := b.sessionForSite(siteOrigin)
	if session == nil {
		return nil, errors.New("app: a Browser page needs a site")
	}
	view, err := b.newView(mygo.WebViewOptions{
		Page: phoneBrowserPageOptions(session),
	})
	if err != nil {
		return nil, fmt.Errorf("create browser page: %w", err)
	}
	hardenPhoneBrowserPage(view.Page())
	return newPhoneBrowserPage(view), nil
}

// phoneBrowserPageOptions configures the web area. There is deliberately no
// PreloadScript, no TrustedOrigins entry, and Untrusted is set: the page has
// no Go bridge, no MyGo-window bridge, no permissions, and no popups. Only the
// loopback gateway URL is loaded, and every other navigation is decided by the
// phone surface's guard.
func phoneBrowserPageOptions(session *mygo.Session) mygo.PageOptions {
	return mygo.PageOptions{
		Session:   session,
		Untrusted: true,
		DevTools:  mygo.DevToolsDisabled,
	}
}

// phoneBrowserSandboxPage is the part of a MyGo page the sandbox hardens.
// Keeping it an interface lets a test prove both handlers are installed
// without a native web view.
type phoneBrowserSandboxPage interface {
	SetPermissionHandler(func(mygo.PermissionRequest) bool)
	SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions)
}

// hardenPhoneBrowserPage removes the native capabilities a Host page must never
// have: permission requests are denied, and window.open cannot create a page
// the panel does not own.
func hardenPhoneBrowserPage(page phoneBrowserSandboxPage) {
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
}

// phoneBrowserPage adapts one MyGo web view to the phone shell's page port.
type phoneBrowserPage struct {
	view *mygo.WebView
	page *mygo.Page
	offs []func()

	mu        sync.Mutex
	listeners []func(phoneshell.BrowserPageEvent)
	guard     func(string) (string, bool)
	destroyed bool
}

var _ phoneshell.BrowserPage = (*phoneBrowserPage)(nil)

func newPhoneBrowserPage(view *mygo.WebView) *phoneBrowserPage {
	p := &phoneBrowserPage{view: view, page: view.Page()}
	p.offs = append(p.offs,
		p.page.OnWillNavigate(func(event *mygo.NavigateEvent) {
			if event == nil {
				return
			}
			p.mu.Lock()
			guard := p.guard
			p.mu.Unlock()
			if guard == nil {
				return
			}
			replacement, handled := guard(event.URL)
			if !handled {
				return
			}
			event.PreventDefault()
			if replacement != "" {
				_ = p.page.LoadURL(replacement)
			}
		}),
		p.page.OnDidNavigate(func(rawURL string) {
			p.emit(phoneshell.BrowserPageEvent{Kind: phoneshell.BrowserPageNavigated, URL: rawURL})
		}),
		p.page.OnPageTitleUpdated(func(event *mygo.TitleEvent) {
			if event == nil {
				return
			}
			p.emit(phoneshell.BrowserPageEvent{Kind: phoneshell.BrowserPageTitle, Title: event.Title})
		}),
		p.page.OnWillDownload(func(event *mygo.DownloadEvent) {
			if event == nil {
				return
			}
			// The WebView's own download would write outside the panel's
			// bounded, gateway-verified path. Cancel it and hand the same
			// gateway URL to the shared manager.
			event.PreventDefault()
			suggested := event.SuggestedName
			if suggested == "" {
				suggested = phoneBrowserDownloadName(event.URL)
			}
			p.emit(phoneshell.BrowserPageEvent{
				Kind:          phoneshell.BrowserPageDownload,
				URL:           event.URL,
				SuggestedName: suggested,
			})
		}),
		p.page.OnDidFinishLoad(func() {
			p.emit(phoneshell.BrowserPageEvent{Kind: phoneshell.BrowserPageLoaded})
		}),
		p.page.OnDidFailLoad(func(loadError *mygo.LoadError) {
			if loadError == nil || phoneBrowserLoadIsAborted(loadError.Code) {
				return
			}
			p.emit(phoneshell.BrowserPageEvent{
				Kind:  phoneshell.BrowserPageFailed,
				URL:   loadError.URL,
				Error: loadError.Description,
			})
		}),
		p.page.OnRenderProcessGone(func(reason string) {
			p.emit(phoneshell.BrowserPageEvent{Kind: phoneshell.BrowserPageFailed, Error: reason})
		}),
	)
	return p
}

func (p *phoneBrowserPage) Native() ui.NativeWebView {
	if p.view == nil || p.view.IsDestroyed() {
		return nil
	}
	return p.view
}

func (p *phoneBrowserPage) Load(rawURL string) error { return p.page.LoadURL(rawURL) }
func (p *phoneBrowserPage) Reload()                  { p.page.Reload() }
func (p *phoneBrowserPage) Stop()                    { p.page.Stop() }
func (p *phoneBrowserPage) Back()                    { p.page.GoBack() }
func (p *phoneBrowserPage) Forward()                 { p.page.GoForward() }
func (p *phoneBrowserPage) CanGoBack() bool          { return p.page.CanGoBack() }
func (p *phoneBrowserPage) CanGoForward() bool       { return p.page.CanGoForward() }
func (p *phoneBrowserPage) Loading() bool            { return p.page.IsLoading() }
func (p *phoneBrowserPage) URL() string              { return p.page.URL() }

func (p *phoneBrowserPage) Destroy() {
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return
	}
	p.destroyed = true
	offs := p.offs
	p.offs = nil
	p.listeners = nil
	p.mu.Unlock()
	for _, off := range offs {
		if off != nil {
			off()
		}
	}
	if p.view != nil {
		p.view.Destroy()
	}
}

func (p *phoneBrowserPage) OnEvent(fn func(phoneshell.BrowserPageEvent)) func() {
	if fn == nil {
		return func() {}
	}
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

func (p *phoneBrowserPage) SetNavigationGuard(fn func(string) (string, bool)) {
	p.mu.Lock()
	p.guard = fn
	p.mu.Unlock()
}

func (p *phoneBrowserPage) emit(event phoneshell.BrowserPageEvent) {
	p.mu.Lock()
	destroyed := p.destroyed
	listeners := append([]func(phoneshell.BrowserPageEvent){}, p.listeners...)
	p.mu.Unlock()
	if destroyed {
		return
	}
	for _, fn := range listeners {
		if fn != nil {
			fn(event)
		}
	}
}

// phoneBrowserLoadIsAborted reports navigation statuses that only mean a load
// was canceled. Linux's engine reports ERR_ABORTED as -3; WebView2 reports a
// download navigation's connection abort as 9 and a stopped load as 14.
func phoneBrowserLoadIsAborted(code int) bool {
	switch code {
	case -3, 9, 14:
		return true
	default:
		return false
	}
}

// phoneBrowserDownloadName derives a display name from the real page URL when
// the engine reports none.
func phoneBrowserDownloadName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path == "" {
		return "download"
	}
	name := filepath.Base(parsed.Path)
	if name == "/" || name == "." || name == "" {
		return "download"
	}
	return name
}

// probePhoneBrowserCapability makes an ephemeral session and round-trips one
// cookie through it. That is the smallest native path the Host Browser needs:
// a per-Host data store, a cookie module, and the download directory. Android's
// MULTI_PROFILE gate fails the first step, and its error becomes the reason the
// entry is hidden. A panic before App.Run is also a fail-closed answer.
func probePhoneBrowserCapability() (capability phoneshell.BrowserCapability) {
	defer func() {
		if recovered := recover(); recovered != nil {
			capability = phoneshell.BrowserCapability{
				Reason: "The Host Browser needs the app's WebView to be ready.",
			}
		}
	}()
	if _, err := mygo.App.Path(mygo.PathDownloads); err != nil {
		return phoneshell.BrowserCapability{
			Reason: "The Host Browser needs a downloads directory: " + err.Error(),
		}
	}
	session := mygo.NewEphemeralSession()
	probeCookie := mygo.Cookie{
		Name:   "natcatty-browser-probe",
		Value:  "1",
		Domain: "natcatty.invalid",
		Path:   "/",
	}
	if err := session.Cookies.Set(probeCookie); err != nil {
		return phoneshell.BrowserCapability{
			Reason: "This device's WebView has no per-Host data stores: " + err.Error(),
		}
	}
	if _, err := session.Cookies.List(mygo.CookieFilter{
		Name: probeCookie.Name, Domain: probeCookie.Domain,
	}); err != nil {
		return phoneshell.BrowserCapability{
			Reason: "This device's WebView cannot read its cookies: " + err.Error(),
		}
	}
	if err := session.Cookies.Delete(probeCookie); err != nil {
		return phoneshell.BrowserCapability{
			Reason: "This device's WebView cannot clear its cookies: " + err.Error(),
		}
	}
	return phoneshell.BrowserCapability{Supported: true}
}
