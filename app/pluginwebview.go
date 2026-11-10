package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// mygoWebviewWindow is the native window capability a plugin page needs. The
// main window's *mygo.Window implements it; tests never create a real page.
type mygoWebviewWindow interface {
	NewWebView(mygo.WebViewOptions) (*mygo.WebView, error)
}

// pluginWebviewPageOptions is the only page configuration a plugin page
// receives: it is untrusted, so it calls no Go method (bound or MyGo's own),
// hears no broadcast, gets no permission, and the page bridge is a
// same-origin HTTP endpoint instead. The page gets its plugin's ephemeral
// session, never the host browser's session, and the inspector only when the
// host is in development mode.
func pluginWebviewPageOptions(devTools bool, session *mygo.Session) mygo.PageOptions {
	options := mygo.PageOptions{
		DevTools:  mygo.DevToolsDisabled,
		Untrusted: true,
		Session:   session,
	}
	if devTools {
		options.DevTools = mygo.DevToolsAuto
	}
	return options
}

// pluginWebviewRoute is one live page's bridge. The router registers a page
// only after the sandbox surface created it, so a host browser page or another
// window can never resolve a route.
type pluginWebviewRoute struct {
	origin string
	// allowPage checks that the caller's current URL is still a packaged
	// HTML page of this route's origin.
	allowPage func(rawURL string) bool
	bridge    func(ctx context.Context, caller any, origin, method, payload string) (string, error)
}

type pluginWebviewRouter struct {
	mu     sync.RWMutex
	routes map[any]pluginWebviewRoute
}

func newPluginWebviewRouter() *pluginWebviewRouter {
	return &pluginWebviewRouter{routes: make(map[any]pluginWebviewRoute)}
}

func (r *pluginWebviewRouter) add(identity any, route pluginWebviewRoute) {
	if r == nil || identity == nil {
		return
	}
	r.mu.Lock()
	r.routes[identity] = route
	r.mu.Unlock()
}

func (r *pluginWebviewRouter) remove(identity any) {
	if r == nil || identity == nil {
		return
	}
	r.mu.Lock()
	delete(r.routes, identity)
	r.mu.Unlock()
}

func (r *pluginWebviewRouter) lookup(identity any) (pluginWebviewRoute, bool) {
	if r == nil || identity == nil {
		return pluginWebviewRoute{}, false
	}
	r.mu.RLock()
	route, ok := r.routes[identity]
	r.mu.RUnlock()
	return route, ok
}

// pluginWebviewBridge verifies the host-observed caller identity, its current
// page URL, and its packaged page before the application sees a request. A
// page cannot name itself: caller and callerURL come from the platform, never
// from JavaScript.
type pluginWebviewBridge struct {
	router *pluginWebviewRouter
}

func (b *pluginWebviewBridge) call(ctx context.Context, caller any, callerURL, requestHost, method, payload string) (string, error) {
	if caller == nil {
		return "", errors.New("plugin bridge: no calling page")
	}
	route, ok := b.router.lookup(caller)
	if !ok {
		return "", errors.New("plugin bridge: the calling page is not a plugin sandbox")
	}
	origin := appplugin.WebviewPageOrigin(callerURL)
	if origin == "" || !strings.EqualFold(origin, route.origin) {
		return "", errors.New("plugin bridge: the calling page left its origin")
	}
	// The request must be served from the caller's own origin: a page of
	// another plugin cannot use this plugin's endpoint as its transport.
	pluginID, ok := domainplugin.ParseWebviewOrigin(origin)
	if !ok || !strings.EqualFold(pluginID, requestHost) {
		return "", errors.New("plugin bridge: the request host is another plugin")
	}
	if route.allowPage == nil || !route.allowPage(callerURL) {
		return "", errors.New("plugin bridge: the calling page is not a packaged plugin page")
	}
	return route.bridge(ctx, caller, origin, method, payload)
}

// webviewPluginSessions owns one ephemeral MyGo session per plugin, shared by
// every live page of that plugin and by no other plugin. An ephemeral session
// writes nothing to disk and frees its data when its last page closes; the
// host browser's default session is never involved.
type webviewPluginSessions struct {
	newSession func() *mygo.Session
	clearData  func(*mygo.Session, mygo.SiteData) error

	mu       sync.Mutex
	sessions map[string]*webviewPluginSession
}

type webviewPluginSession struct {
	session *mygo.Session
	pages   int
}

func newWebviewPluginSessions() *webviewPluginSessions {
	return &webviewPluginSessions{
		newSession: func() *mygo.Session { return mygo.NewEphemeralSession() },
		clearData:  func(s *mygo.Session, types mygo.SiteData) error { return s.ClearData(types) },
		sessions:   make(map[string]*webviewPluginSession),
	}
}

// acquire returns the plugin's session, creating it on first use, and counts
// one more live page.
func (s *webviewPluginSessions) acquire(pluginID string) *mygo.Session {
	if s == nil || pluginID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]*webviewPluginSession)
	}
	entry := s.sessions[pluginID]
	if entry == nil {
		entry = &webviewPluginSession{session: s.newSession()}
		s.sessions[pluginID] = entry
	}
	entry.pages++
	return entry.session
}

// release counts one page gone. With no page left, the entry is dropped: an
// ephemeral session frees its data with its last page, and the next page of
// the plugin starts clean.
func (s *webviewPluginSessions) release(pluginID string) {
	if s == nil || pluginID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.sessions[pluginID]
	if entry == nil {
		return
	}
	entry.pages--
	if entry.pages <= 0 {
		delete(s.sessions, pluginID)
	}
}

// clear removes the plugin's in-memory page data. With a live page, the
// session is cleared and kept; with none, the entry is dropped and the
// ephemeral data is already gone. It reports a failed clear instead of
// claiming success.
func (s *webviewPluginSessions) clear(pluginID string) error {
	if s == nil || pluginID == "" {
		return fmt.Errorf("%w: %q", appplugin.ErrWebviewStorage, pluginID)
	}
	s.mu.Lock()
	entry := s.sessions[pluginID]
	if entry == nil {
		s.mu.Unlock()
		return nil
	}
	if entry.pages == 0 {
		delete(s.sessions, pluginID)
		s.mu.Unlock()
		return nil
	}
	session := entry.session
	s.mu.Unlock()
	if err := s.clearData(session, mygo.SiteDataAll); err != nil {
		return fmt.Errorf("%w: %w", appplugin.ErrWebviewStorage, err)
	}
	return nil
}

// pages reports the live page count of a plugin's session; tests use it.
func (s *webviewPluginSessions) pages(pluginID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.sessions[pluginID]; entry != nil {
		return entry.pages
	}
	return 0
}

// session returns the plugin's session, or nil; tests use it.
func (s *webviewPluginSessions) session(pluginID string) *mygo.Session {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.sessions[pluginID]; entry != nil {
		return entry.session
	}
	return nil
}

// mygoWebviewPort creates and destroys the native page. It applies every
// application policy callback at the platform boundary: navigation, popups,
// permissions, downloads, crash notification, and locale. It adds no policy
// of its own.
//
// The page URL is the application's per-plugin origin,
// natcatty-plugin://<plugin-id>/. MyGo hands the custom-scheme handler that
// URL; on Windows MyGo itself serves it at
// http://<plugin-id>.natcatty-plugin.localhost/ and maps it back, so the
// application never rewrites the URL. Plugin pages are untrusted and use
// their plugin's ephemeral session.
type mygoWebviewPort struct {
	router *pluginWebviewRouter

	mu       sync.Mutex
	sessions *webviewPluginSessions
}

func newMygoWebviewPort(router *pluginWebviewRouter) *mygoWebviewPort {
	return &mygoWebviewPort{router: router, sessions: newWebviewPluginSessions()}
}

func (p *mygoWebviewPort) sessionStore() *webviewPluginSessions {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions == nil {
		p.sessions = newWebviewPluginSessions()
	}
	return p.sessions
}

func (p *mygoWebviewPort) Open(_ context.Context, request appplugin.WebviewOpenRequest) (appplugin.WebviewPage, error) {
	if request.PluginID == "" {
		return nil, errors.New("plugin webview: the request names no plugin")
	}
	window, ok := request.Target.(mygoWebviewWindow)
	if !ok || window == nil {
		return nil, errors.New("plugin webview: the host window cannot show web views")
	}
	sessions := p.sessionStore()
	session := sessions.acquire(request.PluginID)
	view, err := window.NewWebView(mygo.WebViewOptions{
		Page: pluginWebviewPageOptions(request.DevTools, session),
	})
	if err != nil {
		sessions.release(request.PluginID)
		return nil, fmt.Errorf("plugin webview: create page: %w", err)
	}
	page := view.Page()
	if page == nil {
		sessions.release(request.PluginID)
		view.Destroy()
		return nil, errors.New("plugin webview: the host created no page")
	}
	adapter := &mygoWebviewPage{
		view:     view,
		page:     page,
		router:   p.router,
		pluginID: request.PluginID,
		sessions: sessions,
		route: pluginWebviewRoute{
			origin:    request.Origin,
			allowPage: request.AllowBridgeCall,
			bridge:    request.Bridge,
		},
		locale: request.Locale,
		dir:    request.Direction,
	}
	if request.Bridge != nil {
		p.router.add(page, adapter.route)
	}
	page.SetPermissionHandler(func(event mygo.PermissionRequest) bool {
		if request.AllowPermission == nil || len(event.Permissions) == 0 {
			return false
		}
		for _, permission := range event.Permissions {
			if !request.AllowPermission(string(permission)) {
				return false
			}
		}
		return true
	})
	page.SetWindowOpenHandler(func(event mygo.WindowOpenRequest) *mygo.WindowOptions {
		if request.AllowPopup == nil || !request.AllowPopup(event.URL) {
			return nil
		}
		return &mygo.WindowOptions{URL: event.URL}
	})
	page.OnWillNavigate(func(event *mygo.NavigateEvent) {
		if request.AllowNavigation == nil || !request.AllowNavigation(event.URL) {
			event.PreventDefault()
		}
	})
	page.OnWillDownload(func(event *mygo.DownloadEvent) {
		if request.AllowDownload == nil || !request.AllowDownload(event.URL) {
			event.PreventDefault()
		}
	})
	page.OnDidFinishLoad(func() { adapter.applyLocale() })
	page.OnRenderProcessGone(func(reason string) {
		p.router.remove(page)
		if request.Crashed != nil {
			request.Crashed(reason)
		}
	})
	return adapter, nil
}

// ClearStorage removes the plugin's in-memory page data through its ephemeral
// session. A plugin that never opened a page has nothing stored, so clearing
// succeeds there; a live session that fails to clear reports the failure
// rather than a false success. The host browser's default session is never
// touched.
func (p *mygoWebviewPort) ClearStorage(origin string) error {
	pluginID, ok := domainplugin.ParseWebviewOrigin(origin)
	if !ok || pluginID == "" {
		return fmt.Errorf("%w: %q", appplugin.ErrWebviewStorage, origin)
	}
	return p.sessionStore().clear(pluginID)
}

type mygoWebviewPage struct {
	view     *mygo.WebView
	page     *mygo.Page
	router   *pluginWebviewRouter
	pluginID string
	sessions *webviewPluginSessions
	route    pluginWebviewRoute
	locale   string
	dir      appplugin.UIDirection

	destroyOnce sync.Once
}

func (p *mygoWebviewPage) Identity() any { return p.page }

func (p *mygoWebviewPage) Native() any { return p.view }

func (p *mygoWebviewPage) LoadURL(rawURL string) error { return p.page.LoadURL(rawURL) }

func (p *mygoWebviewPage) Reload() {
	// A crash removes the bridge route; a reload restores the same page's
	// route before the new document can call it.
	if p.route.bridge != nil {
		p.router.add(p.page, p.route)
	}
	p.page.Reload()
}

func (p *mygoWebviewPage) Destroy() {
	p.destroyOnce.Do(func() {
		p.router.remove(p.page)
		p.sessions.release(p.pluginID)
		if !p.view.IsDestroyed() {
			p.view.Destroy()
		}
	})
}

// SetLocale makes the document's language and direction follow the host. The
// page reads documentElement lang/dir, not MyGo's injected runtime.
func (p *mygoWebviewPage) SetLocale(locale string, direction appplugin.UIDirection) {
	p.locale, p.dir = locale, direction
	if p.view.IsDestroyed() {
		return
	}
	go p.applyLocale()
}

func (p *mygoWebviewPage) applyLocale() {
	if p.view.IsDestroyed() {
		return
	}
	script := fmt.Sprintf("document.documentElement.lang=%s;document.documentElement.dir=%s;",
		strconv.Quote(p.locale), strconv.Quote(string(p.dir)))
	if _, err := p.page.Eval(script); err != nil {
		log.Printf("natcatty: plugin webview locale: %v", err)
	}
}

// pluginWebviewScheme serves natcatty-plugin://<plugin-id>/ from the active
// fiber's package, and the reserved bridge endpoint on the same origin. The
// application transport validates the host, origin, and path; this adapter
// only writes its headers and body. CORS headers MyGo may have prefilled are
// removed, and no cookie is ever set.
type pluginWebviewScheme struct {
	manager func() *appplugin.Manager
	router  *pluginWebviewRouter
}

func (h *pluginWebviewScheme) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	stripPluginWebviewCORS(w)
	if r.URL.Path == domainplugin.WebviewBridgePath {
		h.serveBridge(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	manager := h.manager()
	if manager == nil {
		http.NotFound(w, r)
		return
	}
	pluginID := r.URL.Hostname()
	if pluginID == "" {
		http.NotFound(w, r)
		return
	}
	asset, err := manager.Container().ServeWebview(pluginID, r.Header.Get("Origin"), r.URL.Path)
	if err != nil {
		switch {
		case errors.Is(err, appplugin.ErrWebviewCrossOrigin):
			w.WriteHeader(http.StatusForbidden)
		case errors.Is(err, appplugin.ErrWebviewAssetType):
			w.WriteHeader(http.StatusUnsupportedMediaType)
		default:
			http.NotFound(w, r)
		}
		return
	}
	for key, value := range asset.Headers {
		w.Header().Set(key, value)
	}
	w.Header().Set("Content-Type", asset.ContentType)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(asset.Body)
}

// serveBridge serves POST /__bridge. The calling page and its current URL
// come from the platform (CallerPage), so a fetch cannot name another page,
// plugin, or origin. The application enforces the fixed method allowlist,
// payload cap, rate, and pending-request cap.
func (h *pluginWebviewScheme) serveBridge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "plugin bridge: POST required", http.StatusMethodNotAllowed)
		return
	}
	page := mygo.CallerPage(r.Context())
	if page == nil {
		http.Error(w, "plugin bridge: no calling page", http.StatusForbidden)
		return
	}
	method := r.Header.Get(appplugin.WebviewBridgeMethodHeader)
	if method == "" || len(method) > 64 {
		http.Error(w, "plugin bridge: missing or invalid method", http.StatusBadRequest)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, domainnativeui.MaxWebviewBridgeBytes+1))
	if err != nil {
		http.Error(w, "plugin bridge: cannot read the payload", http.StatusBadRequest)
		return
	}
	if len(payload) > domainnativeui.MaxWebviewBridgeBytes {
		http.Error(w, "plugin bridge: payload exceeds the limit", http.StatusRequestEntityTooLarge)
		return
	}
	bridge := &pluginWebviewBridge{router: h.router}
	answer, err := bridge.call(r.Context(), page, page.URL(), r.URL.Hostname(), method, string(payload))
	if err != nil {
		status := pluginBridgeStatus(err)
		log.Printf("natcatty: plugin webview bridge: %v", err)
		http.Error(w, "plugin bridge: "+err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, answer)
}

func pluginBridgeStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, appplugin.ErrWebviewPayload):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, appplugin.ErrWebviewRate), errors.Is(err, appplugin.ErrWebviewPending):
		return http.StatusTooManyRequests
	case errors.Is(err, appplugin.ErrWebviewClosed):
		return http.StatusGone
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout
	case errors.Is(err, appplugin.ErrWebviewBackend):
		return http.StatusBadGateway
	case errors.Is(err, appplugin.ErrWebviewRequest):
		return http.StatusConflict
	}
	return http.StatusForbidden
}

// stripPluginWebviewCORS removes the CORS grants MyGo may add for trusted
// pages. A plugin page is same-origin with its assets and bridge, and no
// response may hand another origin a read grant.
func stripPluginWebviewCORS(w http.ResponseWriter) {
	for key := range w.Header() {
		if strings.HasPrefix(http.CanonicalHeaderKey(key), "Access-Control-") {
			w.Header().Del(key)
		}
	}
}

// installPluginWebviewScheme registers the one custom scheme. MyGo snapshots
// registered schemes when a window is created, so this must run in the
// composition root before the first window. It is idempotent for process-wide
// test runs. Plugin pages are untrusted, so the path registers no MyGo
// binding at all; their bridge is the same-origin HTTP endpoint.
func installPluginWebviewScheme(manager func() *appplugin.Manager,
	router *pluginWebviewRouter) error {
	pluginWebviewInstallOnce.Do(func() {
		if err := mygo.Protocol.Handle(domainplugin.WebviewScheme,
			&pluginWebviewScheme{manager: manager, router: router}); err != nil {
			pluginWebviewInstallErr = fmt.Errorf("plugin webview: register %s: %w",
				domainplugin.WebviewScheme, err)
		}
	})
	return pluginWebviewInstallErr
}

var (
	pluginWebviewInstallOnce sync.Once
	pluginWebviewInstallErr  error
)
