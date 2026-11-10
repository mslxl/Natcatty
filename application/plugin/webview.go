package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// WebviewCSP is the only script and network policy a plugin page receives.
// There is no remote content: scripts and styles come from the packaged
// origin, the page may connect only back to its own origin (the bridge
// endpoint), and it cannot frame, base-hijack, or submit a form.
const WebviewCSP = "default-src 'self'; script-src 'self'; object-src 'none'; " +
	"frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; " +
	"form-action 'none'; connect-src 'self'; worker-src 'none'"

// WebviewBridgeMethodHeader carries the fixed allowlist method name of a
// bridge request. The body is the bounded payload, and the page URL the host
// observed decides the caller, never a header.
const WebviewBridgeMethodHeader = "X-Natcatty-Method"

const (
	// MaxWebviews bounds the simultaneous sandboxed pages one plugin may
	// have, across standalone Views and embedded nodes.
	MaxWebviews = 8
	// WebviewBridgeCallsPerSecond bounds page-to-backend bridge requests in
	// one fixed one-second window per surface.
	WebviewBridgeCallsPerSecond = 16
	// MaxWebviewPendingRequests bounds the bridge requests one surface may
	// have in flight, so a page cannot grow the host's pending map without
	// limit.
	MaxWebviewPendingRequests = 8
	// MaxWebviewAssetBytes bounds one packaged asset served to a page.
	MaxWebviewAssetBytes = 8 << 20
)

// Webview sandbox errors. A denied bridge call never reaches the backend
// queue; a closed surface never serves another asset.
var (
	ErrWebviewClosed       = errors.New("plugin webview: the surface is closed")
	ErrWebviewCrossOrigin  = errors.New("plugin webview: cross-origin asset request")
	ErrWebviewOrigin       = errors.New("plugin webview: bridge call from a foreign origin")
	ErrWebviewPage         = errors.New("plugin webview: bridge call from a foreign page")
	ErrWebviewMethod       = errors.New("plugin webview: method is not in the view allowlist")
	ErrWebviewPayload      = errors.New("plugin webview: bridge payload exceeds the limit")
	ErrWebviewRate         = errors.New("plugin webview: bridge rate limit")
	ErrWebviewPending      = errors.New("plugin webview: too many pending bridge requests")
	ErrWebviewLimit        = errors.New("plugin webview: simultaneous webview limit")
	ErrWebviewAssetPath    = errors.New("plugin webview: asset is not a packaged ui asset")
	ErrWebviewAssetType    = errors.New("plugin webview: unsupported asset type")
	ErrWebviewAssetMissing = errors.New("plugin webview: asset not found")
	ErrWebviewBackend      = errors.New("plugin webview: the plugin backend is not listening")
	ErrWebviewRequest      = errors.New("plugin webview: unknown bridge request")
	ErrWebviewStorage      = errors.New("plugin webview: page storage could not be cleared")
	ErrWebviewUnavailable  = errors.New("plugin webview: the host webview port is not configured")
)

// WebviewPort is the consumer-owned boundary to the platform web engine. The
// application layer owns origin, transport, CSP, bridge verification, rate
// limits, and lifecycle; a host adapter implements only native page creation
// and destruction. Tests use an in-memory port, so every security decision is
// testable without a browser engine.
type WebviewPort interface {
	Open(ctx context.Context, request WebviewOpenRequest) (WebviewPage, error)
	ClearStorage(origin string) error
}

// WebviewPage is one native page created by a WebviewPort. Identity is the
// host adapter's opaque caller identity (for MyGo, the *mygo.Page). Every
// bridge call carries the identity observed by the host, never one supplied
// by JavaScript.
type WebviewPage interface {
	Identity() any
	// Native returns the platform widget the native UI places, or nil for a
	// headless port.
	Native() any
	LoadURL(rawURL string) error
	Reload()
	Destroy()
	SetLocale(locale string, direction UIDirection)
}

// WebviewOpenRequest is what the application hands a port. The bridge,
// navigation, permission, popup, and download callbacks are the only policy a
// port may apply; it must not add trusted origins, preload scripts, host bindings
// of its own, or a host browser session.
type WebviewOpenRequest struct {
	PluginID  string
	Origin    string
	URL       string
	Entry     string
	Title     string
	Target    any
	Embedded  bool
	DevTools  bool
	Locale    string
	Direction UIDirection
	Methods   []string

	Bridge          func(ctx context.Context, caller any, origin, method, payload string) (string, error)
	AllowNavigation func(rawURL string) bool
	AllowPermission func(permission string) bool
	AllowPopup      func(rawURL string) bool
	AllowDownload   func(rawURL string) bool
	// AllowBridgeCall reports whether the page at rawURL may use the bridge
	// endpoint. The application requires a packaged HTML page of the same
	// plugin origin; the port must call it with the page URL the platform
	// observed, never with anything the page supplied.
	AllowBridgeCall func(rawURL string) bool
	Crashed         func(reason string)
	// OnRequest tells the host that a bridge request is queued for the guest.
	// The host schedules a frame, whose poll drains the event and lets the
	// backend answer. It runs on the bridge caller's goroutine.
	OnRequest func()
}

// WebviewSurfaceRequest asks the session for one sandboxed page. Methods is
// the copy of the View Contribution's fixed allowlist; the page cannot extend
// it.
type WebviewSurfaceRequest struct {
	Entry    string
	Methods  []string
	Target   any
	Embedded bool
	Title    string
	// OnCrash observes an unrecoverable page crash. The bridge is already
	// closed when it runs; the host may show an error surface and reload.
	OnCrash func(reason string)
	// OnRequest wakes the host when a page bridge request is queued, so the
	// guest poll can answer without waiting for an unrelated frame.
	OnRequest func()
}

// WebviewAsset is one validated packaged UI file and the headers it must be
// served with. Headers never include CORS grants or cookies.
type WebviewAsset struct {
	ContentType string
	Body        []byte
	Headers     map[string]string
}

// WebviewSession owns every sandboxed page of one Plugin Instance: the
// package asset transport, the fixed origin, the bridge handles, and the
// bounded pending requests. It closes with the fiber.
type WebviewSession struct {
	owner *ServiceSession
	ref   PluginRef
	port  WebviewPort

	mu        sync.Mutex
	closed    bool
	files     fs.FS
	devTools  bool
	surfaces  map[Handle]*WebviewSurface
	handles   *HandleTable
	locale    string
	direction UIDirection
}

// newWebviewSession builds the webview half of a service session.
func newWebviewSession(owner *ServiceSession, port WebviewPort, files fs.FS,
	devTools bool, locale string, direction UIDirection) *WebviewSession {
	return &WebviewSession{
		owner:     owner,
		ref:       owner.ref,
		port:      port,
		files:     files,
		devTools:  devTools,
		surfaces:  make(map[Handle]*WebviewSurface),
		handles:   NewHandleTable(MaxWebviews),
		locale:    locale,
		direction: direction,
	}
}

// Closed reports whether the fiber closed every sandboxed page.
func (s *WebviewSession) Closed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Origin returns the per-plugin origin every page of this session uses.
func (s *WebviewSession) Origin() string {
	if s == nil {
		return ""
	}
	return domainplugin.WebviewOrigin(s.ref.ID)
}

// OpenWebview creates one sandboxed page. The ui.webview grant is checked on
// every open, so a revoked grant cannot create another page even while an old
// surface is still tearing down.
func (s *WebviewSession) OpenWebview(request WebviewSurfaceRequest) (*WebviewSurface, error) {
	if s == nil {
		return nil, ErrWebviewUnavailable
	}
	if s.owner == nil {
		return nil, ErrWebviewUnavailable
	}
	if err := s.owner.check(ServiceUI, "webview", domainplugin.PermissionUIWebview, domainplugin.Scope{}); err != nil {
		return nil, err
	}
	entry := request.Entry
	if entry == "" {
		entry = domainplugin.DefaultWebviewEntry
	}
	if !domainplugin.ValidWebviewEntry(entry) {
		return nil, fmt.Errorf("%w: %q", ErrWebviewAssetPath, entry)
	}
	if len(request.Methods) > domainplugin.MaxWebviewBridgeMethods {
		return nil, fmt.Errorf("%w: %d methods", ErrWebviewLimit, len(request.Methods))
	}
	methods := make(map[string]bool, len(request.Methods))
	for _, method := range request.Methods {
		if !domainplugin.ValidWebviewBridgeMethod(method) {
			return nil, fmt.Errorf("%w: %q", ErrWebviewMethod, method)
		}
		if methods[method] {
			return nil, fmt.Errorf("%w: duplicate %q", ErrWebviewMethod, method)
		}
		methods[method] = true
	}
	if err := s.fileExists(entry); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrWebviewClosed
	}
	if len(s.surfaces) >= MaxWebviews {
		s.mu.Unlock()
		return nil, ErrWebviewLimit
	}
	locale, direction := s.locale, s.direction
	s.mu.Unlock()

	origin := s.Origin()
	surface := &WebviewSurface{
		session:   s,
		origin:    origin,
		entry:     entry,
		url:       domainplugin.WebviewURL(s.ref.ID, entry),
		methods:   methods,
		embedded:  request.Embedded,
		onCrash:   request.OnCrash,
		onRequest: request.OnRequest,
		pending:   make(map[uint64]chan webviewReply),
	}
	handle, err := s.handles.Issue(surface)
	if err != nil {
		var limit *LimitError
		if errors.As(err, &limit) {
			return nil, fmt.Errorf("%w: %v", ErrWebviewLimit, err)
		}
		return nil, err
	}
	surface.handle = handle
	openRequest := WebviewOpenRequest{
		PluginID:        s.ref.ID,
		Origin:          origin,
		URL:             surface.url,
		Entry:           entry,
		Title:           request.Title,
		Target:          request.Target,
		Embedded:        request.Embedded,
		DevTools:        s.devTools,
		Locale:          locale,
		Direction:       direction,
		Methods:         append([]string(nil), request.Methods...),
		Bridge:          surface.Bridge,
		AllowNavigation: surface.allowNavigation,
		AllowPermission: func(string) bool { return false },
		AllowPopup:      func(string) bool { return false },
		AllowDownload:   func(string) bool { return false },
		AllowBridgeCall: surface.allowBridgeCall,
		Crashed:         surface.crash,
	}
	page, err := s.port.Open(context.Background(), openRequest)
	if err != nil {
		_ = s.handles.Release(handle)
		return nil, err
	}
	surface.page = page

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = s.handles.Release(handle)
		page.Destroy()
		return nil, ErrWebviewClosed
	}
	s.surfaces[handle] = surface
	s.mu.Unlock()
	if err := page.LoadURL(surface.url); err != nil {
		_ = surface.Close()
		return nil, err
	}
	return surface, nil
}

// fileExists verifies that the entry is a packaged ui file before a native
// page is created for it.
func (s *WebviewSession) fileExists(entry string) error {
	s.mu.Lock()
	files := s.files
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrWebviewClosed
	}
	if files == nil {
		return fmt.Errorf("%w: the package has no files", ErrWebviewAssetMissing)
	}
	info, err := fs.Stat(files, entry)
	if err != nil || info.IsDir() {
		return fmt.Errorf("%w: %s", ErrWebviewAssetMissing, entry)
	}
	return nil
}

// Surfaces returns the live surfaces in handle order. It is a snapshot for
// lifecycle code and tests.
func (s *WebviewSession) Surfaces() []*WebviewSurface {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*WebviewSurface, 0, len(s.surfaces))
	for _, surface := range s.surfaces {
		out = append(out, surface)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].handle < out[j].handle })
	return out
}

// Serve returns one packaged UI asset for the plugin origin. The request path
// is validated here, not in the app adapter: only files under ui/ with a
// known static type are readable, traversal and encoded separators are
// rejected, and the response never carries CORS or cookies.
func (s *WebviewSession) Serve(origin, requestPath string) (WebviewAsset, error) {
	if s == nil {
		return WebviewAsset{}, ErrWebviewUnavailable
	}
	s.mu.Lock()
	closed, files := s.closed, s.files
	s.mu.Unlock()
	if closed {
		return WebviewAsset{}, ErrWebviewClosed
	}
	if files == nil {
		return WebviewAsset{}, fmt.Errorf("%w: the package has no files", ErrWebviewAssetMissing)
	}
	if origin != "" && !sameWebviewOrigin(origin, s.ref.ID) {
		return WebviewAsset{}, ErrWebviewCrossOrigin
	}
	entry, err := webviewAssetPath(requestPath)
	if err != nil {
		return WebviewAsset{}, err
	}
	contentType, ok := webviewContentType(entry)
	if !ok {
		return WebviewAsset{}, fmt.Errorf("%w: %s", ErrWebviewAssetType, entry)
	}
	info, err := fs.Stat(files, entry)
	if err != nil || info.IsDir() {
		return WebviewAsset{}, fmt.Errorf("%w: %s", ErrWebviewAssetMissing, entry)
	}
	if info.Size() > MaxWebviewAssetBytes {
		return WebviewAsset{}, fmt.Errorf("%w: %s is %d bytes", ErrWebviewPayload, entry, info.Size())
	}
	body, err := fs.ReadFile(files, entry)
	if err != nil {
		return WebviewAsset{}, fmt.Errorf("%w: %v", ErrWebviewAssetMissing, err)
	}
	return WebviewAsset{
		ContentType: contentType,
		Body:        body,
		Headers: map[string]string{
			"Content-Security-Policy":      WebviewCSP,
			"X-Content-Type-Options":       "nosniff",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Cache-Control":                "no-store",
		},
	}, nil
}

// webviewAssetPath turns a request path into a package-relative UI file.
// "/" maps to the default entry; every other path must already be clean.
func webviewAssetPath(requestPath string) (string, error) {
	if requestPath == "" || requestPath == "/" {
		return domainplugin.DefaultWebviewEntry, nil
	}
	if strings.ContainsAny(requestPath, "\\:#?%") || strings.ContainsRune(requestPath, 0) {
		return "", fmt.Errorf("%w: %q", ErrWebviewAssetPath, requestPath)
	}
	cleaned := path.Clean(requestPath)
	if cleaned != requestPath || !strings.HasPrefix(cleaned, "/ui/") || strings.HasSuffix(cleaned, "/") {
		return "", fmt.Errorf("%w: %q", ErrWebviewAssetPath, requestPath)
	}
	entry := strings.TrimPrefix(cleaned, "/")
	for _, segment := range strings.Split(entry, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: %q", ErrWebviewAssetPath, requestPath)
		}
	}
	return entry, nil
}

func webviewContentType(entry string) (string, bool) {
	switch strings.ToLower(path.Ext(entry)) {
	case ".html":
		return "text/html; charset=utf-8", true
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8", true
	case ".css":
		return "text/css; charset=utf-8", true
	case ".json":
		return "application/json", true
	case ".svg":
		return "image/svg+xml", true
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	case ".woff":
		return "font/woff", true
	case ".woff2":
		return "font/woff2", true
	case ".ttf":
		return "font/ttf", true
	case ".otf":
		return "font/otf", true
	}
	return "", false
}

// webviewHTMLPath turns a navigation or page path into a packaged HTML file.
// Unlike an asset request ("/" maps to the default entry), a page must name
// its packaged entry explicitly: "/", non-HTML assets, traversal, encoded
// separators, and anything outside ui/ are denied.
func webviewHTMLPath(requestPath string) (string, error) {
	if requestPath == "" || strings.ContainsRune(requestPath, '\x00') ||
		strings.ContainsAny(requestPath, `\:#?%`) {
		return "", fmt.Errorf("%w: %q", ErrWebviewAssetPath, requestPath)
	}
	cleaned := path.Clean(requestPath)
	if cleaned != requestPath || !strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("%w: %q", ErrWebviewAssetPath, requestPath)
	}
	entry := strings.TrimPrefix(cleaned, "/")
	if !domainplugin.ValidWebviewEntry(entry) {
		return "", fmt.Errorf("%w: %q", ErrWebviewAssetPath, requestPath)
	}
	return entry, nil
}

// setLocale propagates a host locale change to every live page. The port sets
// the document's lang and dir explicitly, so the page does not depend on
// MyGo's injected runtime, which an untrusted page cannot call anyway.
func (s *WebviewSession) setLocale(locale string, direction UIDirection) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.locale, s.direction = locale, direction
	surfaces := make([]*WebviewSurface, 0, len(s.surfaces))
	for _, surface := range s.surfaces {
		surfaces = append(surfaces, surface)
	}
	s.mu.Unlock()
	for _, surface := range surfaces {
		surface.setLocale(locale, direction)
	}
}

// Reply completes one bridge request. The dispatcher resolves the handle and
// calls this; the page can never call it directly.
func (s *WebviewSession) Reply(reply domainnativeui.WebviewReply) error {
	if s == nil {
		return ErrWebviewUnavailable
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrWebviewClosed
	}
	value, err := s.handles.Resolve(Handle(reply.Handle))
	if err != nil {
		var invalid *InvalidHandleError
		if errors.As(err, &invalid) {
			return fmt.Errorf("%w: %v", ErrWebviewRequest, err)
		}
		return err
	}
	surface, ok := value.(*WebviewSurface)
	if !ok {
		return ErrWebviewRequest
	}
	return surface.reply(reply)
}

// ClearStorage asks the host to remove the origin's session-only page data.
// Uninstall calls it after the fiber unloads; plugin pages use ephemeral
// sessions, so no page data is durable in the first place.
func (s *WebviewSession) ClearStorage() error {
	if s == nil {
		return ErrWebviewUnavailable
	}
	s.mu.Lock()
	port, closed := s.port, s.closed
	s.mu.Unlock()
	if closed {
		return ErrWebviewClosed
	}
	if port == nil {
		return ErrWebviewUnavailable
	}
	if err := port.ClearStorage(s.Origin()); err != nil {
		return fmt.Errorf("%w: %w", ErrWebviewStorage, err)
	}
	return nil
}

// close destroys every page and closes pending bridge requests. The host
// calls it on unload, revocation, and trap teardown.
func (s *WebviewSession) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	surfaces := make([]*WebviewSurface, 0, len(s.surfaces))
	for _, surface := range s.surfaces {
		surfaces = append(surfaces, surface)
	}
	s.surfaces = make(map[Handle]*WebviewSurface)
	for _, surface := range surfaces {
		_ = s.handles.Release(surface.handle)
	}
	s.mu.Unlock()
	for _, surface := range surfaces {
		surface.closeFromSession()
	}
	// Page data lives only in the plugin's ephemeral session. A session close
	// is the end of the plugin's live webview state, so the origin's data goes
	// with it; a port that cannot clear one origin reports that and keeps the
	// host browser's session untouched.
	if s.port != nil {
		_ = s.port.ClearStorage(domainplugin.WebviewOrigin(s.ref.ID))
	}
}

// detach removes one surface from the session and releases its handle.
func (s *WebviewSession) detach(surface *WebviewSurface) {
	if s == nil || surface == nil {
		return
	}
	s.mu.Lock()
	if current, ok := s.surfaces[surface.handle]; ok && current == surface {
		delete(s.surfaces, surface.handle)
	}
	s.mu.Unlock()
	_ = s.handles.Release(surface.handle)
}

// WebviewSurface is one sandboxed page. Its origin is the plugin origin, its
// bridge only accepts calls from its own page identity, and its method set is
// the View Contribution's fixed allowlist.
type WebviewSurface struct {
	session   *WebviewSession
	handle    Handle
	page      WebviewPage
	origin    string
	entry     string
	url       string
	methods   map[string]bool
	embedded  bool
	onCrash   func(string)
	onRequest func()

	mu         sync.Mutex
	closed     bool
	crashed    bool
	pending    map[uint64]chan webviewReply
	next       uint64
	rateSecond int64
	rateCount  int
}

type webviewReply struct {
	payload string
	err     error
}

// Handle returns the generation-tagged webview handle the backend sees in
// every webview event.
func (s *WebviewSurface) Handle() Handle {
	if s == nil {
		return 0
	}
	return s.handle
}

// Origin returns the per-plugin origin.
func (s *WebviewSurface) Origin() string {
	if s == nil {
		return ""
	}
	return s.origin
}

// URL returns the initial page URL.
func (s *WebviewSurface) URL() string {
	if s == nil {
		return ""
	}
	return s.url
}

// Entry returns the packaged entry the surface serves.
func (s *WebviewSurface) Entry() string {
	if s == nil {
		return ""
	}
	return s.entry
}

// Native returns the platform page widget, or nil for a headless port.
func (s *WebviewSurface) Native() any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return nil
	}
	return page.Native()
}

// Embedded reports whether the page is an embedded node rather than a
// standalone View.
func (s *WebviewSurface) Embedded() bool {
	if s == nil {
		return false
	}
	return s.embedded
}

// Close destroys the native page and closes the bridge. The plugin backend
// keeps its state; the host recreates a surface on the next attach.
func (s *WebviewSurface) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pending := s.takePendingLocked()
	page := s.page
	s.mu.Unlock()
	if page != nil {
		page.Destroy()
	}
	s.session.detach(s)
	for _, channel := range pending {
		channel <- webviewReply{err: ErrWebviewClosed}
	}
	return nil
}

func (s *WebviewSurface) closeFromSession() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	pending := s.takePendingLocked()
	page := s.page
	s.mu.Unlock()
	if page != nil {
		page.Destroy()
	}
	for _, channel := range pending {
		channel <- webviewReply{err: ErrWebviewClosed}
	}
}

func (s *WebviewSurface) takePendingLocked() []chan webviewReply {
	pending := make([]chan webviewReply, 0, len(s.pending))
	for request, channel := range s.pending {
		delete(s.pending, request)
		pending = append(pending, channel)
	}
	return pending
}

// Reload restarts a crashed page and reopens its bridge. Backend state is
// untouched.
func (s *WebviewSurface) Reload() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.crashed = false
	page := s.page
	s.mu.Unlock()
	if page != nil {
		page.Reload()
	}
}

func (s *WebviewSurface) setLocale(locale string, direction UIDirection) {
	if s == nil {
		return
	}
	s.mu.Lock()
	page, closed := s.page, s.closed
	s.mu.Unlock()
	if page != nil && !closed {
		page.SetLocale(locale, direction)
	}
}

// Bridge is the host bridge entry point behind the same-origin HTTP
// endpoint. caller is the identity the port observed, never one supplied by
// the page; origin is derived by the port from the page's current URL. The
// request is delivered to the backend as a webview event and waits for
// ui.webview_respond.
func (s *WebviewSurface) Bridge(ctx context.Context, caller any, origin, method, payload string) (string, error) {
	if s == nil {
		return "", ErrWebviewClosed
	}
	s.mu.Lock()
	closed, crashed, page := s.closed, s.crashed, s.page
	s.mu.Unlock()
	if closed || crashed || page == nil {
		return "", ErrWebviewClosed
	}
	if !sameIdentity(caller, page.Identity()) {
		s.audit(ServiceCodeDenied)
		return "", ErrWebviewPage
	}
	if !sameWebviewOrigin(origin, s.session.ref.ID) {
		s.audit(ServiceCodeDenied)
		return "", ErrWebviewOrigin
	}
	if !s.methods[method] {
		s.audit(ServiceCodeDenied)
		return "", fmt.Errorf("%w: %q", ErrWebviewMethod, method)
	}
	if len(payload) > domainnativeui.MaxWebviewBridgeBytes {
		s.audit(ServiceCodeLimit)
		return "", fmt.Errorf("%w: %d bytes", ErrWebviewPayload, len(payload))
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.closed || s.crashed {
		s.mu.Unlock()
		return "", ErrWebviewClosed
	}
	second := s.session.now().Unix()
	if second != s.rateSecond {
		s.rateSecond = second
		s.rateCount = 0
	}
	if s.rateCount >= WebviewBridgeCallsPerSecond {
		s.mu.Unlock()
		s.audit(ServiceCodeLimit)
		return "", ErrWebviewRate
	}
	if len(s.pending) >= MaxWebviewPendingRequests {
		s.mu.Unlock()
		s.audit(ServiceCodeLimit)
		return "", ErrWebviewPending
	}
	s.rateCount++
	s.next++
	request := s.next
	channel := make(chan webviewReply, 1)
	s.pending[request] = channel
	s.mu.Unlock()

	envelope, err := json.Marshal(webviewRequestEnvelope{
		Type: "request", Request: request, Method: method, Payload: payload, Origin: origin,
	})
	if err != nil {
		s.dropPending(request)
		return "", err
	}
	if !s.session.enqueue(s, envelope) {
		s.dropPending(request)
		s.audit(ServiceCodeFault)
		return "", ErrWebviewBackend
	}
	if s.onRequest != nil {
		s.onRequest()
	}

	select {
	case reply := <-channel:
		return reply.payload, reply.err
	case <-ctx.Done():
		s.dropPending(request)
		return "", ctx.Err()
	}
}

func (s *WebviewSurface) dropPending(request uint64) {
	s.mu.Lock()
	delete(s.pending, request)
	s.mu.Unlock()
}

func (s *WebviewSurface) reply(reply domainnativeui.WebviewReply) error {
	s.mu.Lock()
	channel, ok := s.pending[reply.Request]
	if ok {
		delete(s.pending, reply.Request)
	}
	closed := s.closed || s.crashed
	s.mu.Unlock()
	if closed {
		return ErrWebviewClosed
	}
	if !ok {
		return ErrWebviewRequest
	}
	if len(reply.Payload) > domainnativeui.MaxWebviewBridgeBytes {
		channel <- webviewReply{err: ErrWebviewPayload}
		return ErrWebviewPayload
	}
	if reply.Status != domainnativeui.StatusOK {
		channel <- webviewReply{err: fmt.Errorf("%w: %s", ErrWebviewBackend, reply.Payload)}
		return nil
	}
	channel <- webviewReply{payload: reply.Payload}
	return nil
}

func (s *WebviewSurface) crash(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed || s.crashed {
		s.mu.Unlock()
		return
	}
	s.crashed = true
	pending := s.takePendingLocked()
	s.mu.Unlock()
	for _, channel := range pending {
		channel <- webviewReply{err: ErrWebviewClosed}
	}
	envelope, err := json.Marshal(webviewEventEnvelope{Type: "crash", Reason: reason})
	if err == nil {
		s.session.enqueue(s, envelope)
	}
	s.audit(ServiceCodeFault)
	if s.onCrash != nil {
		s.onCrash(reason)
	}
}

func (s *WebviewSurface) audit(result string) {
	if s == nil || s.session == nil || s.session.owner == nil {
		return
	}
	s.session.owner.record(ServiceUI, "webview", domainplugin.PermissionUIWebview,
		domainplugin.Scope{}, result)
}

// allowNavigation keeps a page on its own origin's packaged HTML entries.
// External links, "/", traversal, non-HTML assets, and any other page are
// denied; MyGo's untrusted-page default never sees a navigation to another
// origin, and this handler narrows that further.
func (s *WebviewSurface) allowNavigation(rawURL string) bool {
	return s.allowPackagedPage(rawURL)
}

// allowBridgeCall is the bridge's page check: the calling page must still be
// a packaged HTML entry of this plugin's origin. A page cannot become another
// page and keep its bridge.
func (s *WebviewSurface) allowBridgeCall(rawURL string) bool {
	return s.allowPackagedPage(rawURL)
}

func (s *WebviewSurface) allowPackagedPage(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if !sameWebviewOrigin(origin, s.session.ref.ID) {
		return false
	}
	entry, err := webviewHTMLPath(parsed.EscapedPath())
	if err != nil {
		return false
	}
	return s.session.fileExists(entry) == nil
}

type webviewRequestEnvelope struct {
	Type    string `json:"type"`
	Request uint64 `json:"request"`
	Method  string `json:"method"`
	Payload string `json:"payload"`
	Origin  string `json:"origin"`
}

type webviewEventEnvelope struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

func (s *WebviewSession) enqueue(surface *WebviewSurface, payload []byte) bool {
	if s == nil || s.owner == nil || surface == nil {
		return false
	}
	ui := s.owner.NativeUI()
	if ui == nil {
		return false
	}
	return ui.Enqueue(domainnativeui.Event{
		Kind:    domainnativeui.EventWebview,
		Related: domainnativeui.NodeRef(uint64(surface.handle)),
		Text:    string(payload),
	})
}

// now returns the host clock used by the bridge rate window.
func (s *WebviewSession) now() time.Time {
	if s != nil && s.owner != nil && s.owner.host != nil {
		return s.owner.host.now()
	}
	return time.Now()
}

// sameIdentity compares the port's caller identity with the page identity. A
// non-comparable identity never matches.
func sameIdentity(caller, page any) bool {
	if caller == nil || page == nil {
		return false
	}
	callerType, pageType := reflect.TypeOf(caller), reflect.TypeOf(page)
	if callerType != pageType || !callerType.Comparable() {
		return false
	}
	return reflect.ValueOf(caller).Interface() == reflect.ValueOf(page).Interface()
}

// ServeWebview serves one packaged asset for the active fiber of pluginID.
// The app's custom-scheme handler calls it; the host id in the URL is the
// plugin id, so a request can never name another plugin's session.
func (h *HostServices) ServeWebview(pluginID, origin, requestPath string) (WebviewAsset, error) {
	if h == nil {
		return WebviewAsset{}, ErrWebviewUnavailable
	}
	h.mu.Lock()
	sessions := make([]*ServiceSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		if session.ref.ID == pluginID {
			sessions = append(sessions, session)
		}
	}
	h.mu.Unlock()
	for _, session := range sessions {
		if webview := session.Webview(); webview != nil {
			return webview.Serve(origin, requestPath)
		}
	}
	return WebviewAsset{}, fmt.Errorf("%w: %s", ErrWebviewUnavailable, pluginID)
}

// ClearWebviewStorage removes the plugin's session-only page data after an
// uninstall. A plugin without an active webview session or a port reports the
// typed unavailable error; uninstall treats it as best effort.
func (h *HostServices) ClearWebviewStorage(pluginID string) error {
	if h == nil {
		return ErrWebviewUnavailable
	}
	if h.webview == nil {
		return ErrWebviewUnavailable
	}
	return h.webview.ClearStorage(domainplugin.WebviewOrigin(pluginID))
}
