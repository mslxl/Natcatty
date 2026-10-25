package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	browserui "github.com/mslxl/natcatty/ui/browser"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// errBrowserSessionUnavailable is returned when a Session has no saved Host to
// route a browser through. The Session menu keeps Open Browser disabled in
// that case, so this is a last line of defence rather than a normal path.
var errBrowserSessionUnavailable = errors.New("app: the browser needs a saved SSH Host")

// errBrowserWindowUnavailable is returned when the native window that owns the
// Session cannot make a web view. A real window always can; a fake host in a
// test that never opens a browser cannot.
var errBrowserWindowUnavailable = errors.New("app: the browser window has no web view surface")

// browserConnector adapts the shared Connection Manager to the browser
// application's tunnel port. Every panel gets a fresh, dedicated Connection:
// Dedicated keeps the pool from lending it to a Session or another browser, so
// closing one panel cannot disturb another route. Jump chains, proxies, host
// keys, and algorithm policy come from the same resolver a Session uses.
type browserConnector struct {
	vault       forwardingVault
	routes      routeSource
	connections *appconnection.Manager
}

var _ appbrowser.Connector = browserConnector{}

// browserKeepalive detects a dead dedicated tunnel in about fifteen seconds.
// The global terminal schedule (30 s x 10) is tuned for an interactive shell
// that can tolerate a slow network; a browser panel that loses its Host must
// show the disconnect and offer Retry promptly instead of leaving a page that
// silently stopped loading.
var browserKeepalive = appconnection.KeepalivePolicy{
	Interval: 5 * time.Second,
	CountMax: 3,
}

func (c browserConnector) Connect(ctx context.Context, hostID model.HostID) (appbrowser.Tunnel, error) {
	if c.connections == nil {
		return nil, errors.New("app: no connection manager")
	}
	if c.vault == nil {
		return nil, errors.New("app: no Vault")
	}
	host, err := c.vault.Get(ctx, hostID)
	if err != nil {
		return nil, err
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
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		// A browser never shares a transport: its own failure, idle timeout,
		// or close must not touch the terminal that opened it.
		Dedicated: true,
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &browserKeepalive,
	})
	if err != nil {
		return nil, err
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, err
	}
	return &browserTunnel{lease: lease, ForwardingClient: forwarding}, nil
}

// browserTunnel ties the browser's dialer to its dedicated Connection lease.
type browserTunnel struct {
	lease *appconnection.Lease
	appconnection.ForwardingClient
}

func (t *browserTunnel) Done() <-chan struct{} { return t.lease.Done() }
func (t *browserTunnel) Discard()              { _ = t.lease.Discard() }

// browserWindow is the native window capability a page factory needs.
type browserWindow interface {
	NewWebView(mygo.WebViewOptions) (*mygo.WebView, error)
}

// windowForSession returns the window that currently shows the Session. A
// detached Pane must use its own window: a web view belongs to the window that
// made it.
func (m *windowManager) windowForSession(id model.SessionID) hostWindow {
	if m.state != nil {
		if tabID, ok := m.state.DetachedTabForSession(id); ok {
			if window := m.detached[tabID]; window != nil && !window.IsDestroyed() {
				return window
			}
			return nil
		}
	}
	if m.main != nil && !m.main.IsDestroyed() {
		return m.main
	}
	return nil
}

// openSessionBrowser builds one Session's browser panel. The opener is called
// from the UI thread when the Session menu item is chosen; the app supplies
// the connector, and the owning window supplies the page views.
func openSessionBrowser(session *appsession.Session, windows *windowManager,
	connector appbrowser.Connector, redraw func(), onClose func()) (sessionui.BrowserPanel, error) {
	if session == nil || session.HostID() == "" {
		return nil, errBrowserSessionUnavailable
	}
	if windows == nil {
		return nil, errBrowserWindowUnavailable
	}
	window := windows.windowForSession(session.ID())
	creator, ok := window.(browserWindow)
	if !ok {
		return nil, errBrowserWindowUnavailable
	}
	core := appbrowser.New(appbrowser.Config{
		HostID:    session.HostID(),
		Label:     session.Label(),
		Connector: connector,
		Redraw:    redraw,
	})
	return browserui.New(core, mygoPageFactory{window: creator},
		browserui.WithRedraw(redraw),
		browserui.WithCloseRequest(onClose),
	), nil
}

// mygoPageFactory makes one isolated page per browser tab. Untrusted content
// gets no preload, no trusted origin, no developer tools, and no bridge: only
// the loopback gateway URL is loaded, and the page cannot call the app.
type mygoPageFactory struct {
	window browserWindow
}

var _ browserui.PageFactory = mygoPageFactory{}

func (f mygoPageFactory) NewPage() (browserui.Page, error) {
	view, err := f.window.NewWebView(mygo.WebViewOptions{
		Page: mygo.PageOptions{DevTools: mygo.DevToolsDisabled},
	})
	if err != nil {
		return nil, fmt.Errorf("create browser page: %w", err)
	}
	page := view.Page()
	// The page never gets a capability that could bypass the Host route:
	// permission prompts are denied, and window.open cannot create a second
	// page the panel does not own.
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	return newMygoPage(view), nil
}

// mygoPage adapts one MyGo web view to the browser UI's page port.
type mygoPage struct {
	view *mygo.WebView
	page *mygo.Page
	offs []func()

	mu        sync.Mutex
	listeners []func(browserui.PageEvent)
	guard     func(string) (string, bool)
	destroyed bool
}

var _ browserui.Page = (*mygoPage)(nil)

func newMygoPage(view *mygo.WebView) *mygoPage {
	p := &mygoPage{view: view, page: view.Page()}
	p.offs = append(p.offs,
		p.page.OnWillNavigate(func(event *mygo.NavigateEvent) {
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
			p.emit(browserui.PageEvent{Kind: browserui.PageNavigated, URL: rawURL})
		}),
		p.page.OnPageTitleUpdated(func(event *mygo.TitleEvent) {
			p.emit(browserui.PageEvent{Kind: browserui.PageTitle, Title: event.Title})
		}),
		p.page.OnDidFinishLoad(func() {
			p.emit(browserui.PageEvent{Kind: browserui.PageLoaded})
		}),
		p.page.OnDidFailLoad(func(loadError *mygo.LoadError) {
			// ERR_ABORTED is what Stop and a superseded navigation report;
			// neither is a page failure.
			if loadError == nil || loadError.Code == -3 {
				return
			}
			p.emit(browserui.PageEvent{
				Kind:  browserui.PageFailed,
				URL:   loadError.URL,
				Error: loadError.Description,
			})
		}),
		p.page.OnRenderProcessGone(func(reason string) {
			p.emit(browserui.PageEvent{Kind: browserui.PageFailed, Error: reason})
		}),
	)
	return p
}

func (p *mygoPage) Native() ui.NativeWebView {
	if p.view == nil || p.view.IsDestroyed() {
		return nil
	}
	return p.view
}

func (p *mygoPage) Load(rawURL string) error { return p.page.LoadURL(rawURL) }
func (p *mygoPage) Reload()                  { p.page.Reload() }
func (p *mygoPage) Stop()                    { p.page.Stop() }
func (p *mygoPage) Back()                    { p.page.GoBack() }
func (p *mygoPage) Forward()                 { p.page.GoForward() }
func (p *mygoPage) CanGoBack() bool          { return p.page.CanGoBack() }
func (p *mygoPage) CanGoForward() bool       { return p.page.CanGoForward() }
func (p *mygoPage) Loading() bool            { return p.page.IsLoading() }
func (p *mygoPage) URL() string              { return p.page.URL() }

func (p *mygoPage) Destroy() {
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

func (p *mygoPage) OnEvent(fn func(browserui.PageEvent)) func() {
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

func (p *mygoPage) SetNavigationGuard(fn func(string) (string, bool)) {
	p.mu.Lock()
	p.guard = fn
	p.mu.Unlock()
}

func (p *mygoPage) emit(event browserui.PageEvent) {
	p.mu.Lock()
	destroyed := p.destroyed
	listeners := append([]func(browserui.PageEvent){}, p.listeners...)
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
