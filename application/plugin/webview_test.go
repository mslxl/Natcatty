package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

type fakeWebviewPage struct {
	identity  any
	request   appplugin.WebviewOpenRequest
	loaded    []string
	locale    string
	dir       appplugin.UIDirection
	crashed   []string
	reloads   int
	destroyed bool
}

func (p *fakeWebviewPage) Identity() any { return p.identity }
func (p *fakeWebviewPage) Native() any   { return nil }
func (p *fakeWebviewPage) LoadURL(rawURL string) error {
	p.loaded = append(p.loaded, rawURL)
	return nil
}
func (p *fakeWebviewPage) Reload()  { p.reloads++ }
func (p *fakeWebviewPage) Destroy() { p.destroyed = true }
func (p *fakeWebviewPage) SetLocale(locale string, dir appplugin.UIDirection) {
	p.locale, p.dir = locale, dir
}

type fakeWebviewPort struct {
	mu       sync.Mutex
	requests []appplugin.WebviewOpenRequest
	pages    []*fakeWebviewPage
	cleared  []string
	failOpen error
	clearErr error
}

func (p *fakeWebviewPort) Open(_ context.Context, request appplugin.WebviewOpenRequest) (appplugin.WebviewPage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failOpen != nil {
		return nil, p.failOpen
	}
	page := &fakeWebviewPage{identity: new(int), request: request, locale: request.Locale, dir: request.Direction}
	p.requests = append(p.requests, request)
	p.pages = append(p.pages, page)
	return page, nil
}

func (p *fakeWebviewPort) ClearStorage(origin string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleared = append(p.cleared, origin)
	return p.clearErr
}

func (p *fakeWebviewPort) lastPage(t *testing.T) *fakeWebviewPage {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pages) == 0 {
		t.Fatal("the fake port opened no page")
	}
	return p.pages[len(p.pages)-1]
}

func (p *fakeWebviewPort) opened(t *testing.T) []appplugin.WebviewOpenRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]appplugin.WebviewOpenRequest(nil), p.requests...)
}

type webviewFixture struct {
	host      *appplugin.HostServices
	session   *appplugin.ServiceSession
	webviews  *appplugin.WebviewSession
	port      *fakeWebviewPort
	authority *appplugin.Authority
	audit     *recordingAudit
	ref       appplugin.PluginRef
	fiber     domainplugin.FiberID
	now       *time.Time
}

func newWebviewFixture(t *testing.T, devtools bool) *webviewFixture {
	t.Helper()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	audit := &recordingAudit{}
	authority := appplugin.NewAuthority()
	port := &fakeWebviewPort{}
	ref := appplugin.PluginRef{
		ID:      "acme.webview",
		Name:    "Acme WebView",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
	}
	fiber := domainplugin.NewFiberID("installed", ref.ID, 1)
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority:       authority,
		Audit:           audit,
		Now:             func() time.Time { return now },
		DataDir:         func(id string) string { return filepath.Join(t.TempDir(), id) },
		Locale:          "en",
		Webview:         port,
		WebviewDevTools: devtools,
	})
	session := host.Open(ref, fiber, nil)
	session.AttachWebview(fstest.MapFS{
		"ui/index.html": &fstest.MapFile{Data: []byte("<!doctype html><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'self'; connect-src 'none'\"><script src=\"app.js\"></script>")},
		"ui/app.js":     &fstest.MapFile{Data: []byte("document.body.textContent='ok'")},
		"ui/embed.html": &fstest.MapFile{Data: []byte("<!doctype html><p>embed</p>")},
		"ui/style.css":  &fstest.MapFile{Data: []byte("body{}")},
		"ui/logo.png":   &fstest.MapFile{Data: []byte{0x89, 'P', 'N', 'G'}},
		"plugin.wasm":   &fstest.MapFile{Data: []byte("wasm")},
		"i18n/en.toml":  &fstest.MapFile{Data: []byte("x")},
	})
	authority.Grant(appplugin.SubjectForRef(ref), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionUINative, GrantedAt: now,
	})
	authority.Grant(appplugin.SubjectForRef(ref), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionUIWebview, GrantedAt: now,
	})
	t.Cleanup(func() { host.CloseAll() })
	return &webviewFixture{
		host: host, session: session, webviews: session.Webview(), port: port,
		authority: authority, audit: audit, ref: ref, fiber: fiber, now: &now,
	}
}

func (f *webviewFixture) open(t *testing.T, methods ...string) *appplugin.WebviewSurface {
	t.Helper()
	surface, err := f.webviews.OpenWebview(appplugin.WebviewSurfaceRequest{
		Entry:   "ui/index.html",
		Methods: methods,
		Target:  struct{}{},
	})
	if err != nil {
		t.Fatalf("OpenWebview: %v", err)
	}
	return surface
}

func (f *webviewFixture) listenWebview(t *testing.T) {
	t.Helper()
	if _, err := f.session.NativeUI().Listen(0, domainnativeui.EventWebview); err != nil {
		t.Fatalf("listen webview: %v", err)
	}
}

type bridgeResult struct {
	value string
	err   error
}

func startBridge(t *testing.T, surface *appplugin.WebviewSurface, page *fakeWebviewPage,
	origin, method, payload string) <-chan bridgeResult {
	t.Helper()
	out := make(chan bridgeResult, 1)
	go func() {
		value, err := surface.Bridge(context.Background(), page.Identity(), origin, method, payload)
		out <- bridgeResult{value: value, err: err}
	}()
	return out
}

type webviewEnvelope struct {
	Type    string `json:"type"`
	Request uint64 `json:"request"`
	Method  string `json:"method"`
	Payload string `json:"payload"`
	Origin  string `json:"origin"`
	Reason  string `json:"reason"`
}

func pollEnvelope(t *testing.T, session *appplugin.ServiceSession) webviewEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session.NativeUI().PendingEvents() > 0 {
			events := session.NativeUI().Poll(8)
			for _, event := range events {
				if event.Kind != domainnativeui.EventWebview {
					continue
				}
				var envelope webviewEnvelope
				if err := json.Unmarshal([]byte(event.Text), &envelope); err != nil {
					t.Fatalf("event payload %q: %v", event.Text, err)
				}
				if event.Node != 0 {
					t.Fatalf("webview event node = %#x, want 0", uint64(event.Node))
				}
				return envelope
			}
			t.Fatalf("poll returned no webview event: %+v", events)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for a webview event")
	return webviewEnvelope{}
}

func reply(t *testing.T, session *appplugin.ServiceSession, reply domainnativeui.WebviewReply) domainnativeui.StatusResponse {
	t.Helper()
	response := session.Dispatcher().Call(context.Background(), appplugin.AbiServiceCall(
		appplugin.ServiceUI, domainpluginabi.UiMethodWebviewRespond,
		domainnativeui.EncodeWebviewReply(reply)))
	status, err := domainnativeui.DecodeStatus(response)
	if err != nil {
		t.Fatalf("DecodeStatus: %v", err)
	}
	return status
}

func awaitBridge(t *testing.T, result <-chan bridgeResult) bridgeResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the bridge result")
		return bridgeResult{}
	}
}

func TestWebviewSurfaceUsesItsOriginAndServesOnlyPackagedUI(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	surface := fixture.open(t, "greet")
	origin := "natcatty-plugin://acme.webview"
	if surface.Origin() != origin || surface.URL() != origin+"/ui/index.html" {
		t.Fatalf("surface origin/url = %q %q", surface.Origin(), surface.URL())
	}
	request := fixture.port.opened(t)[0]
	if request.Origin != origin || request.URL != origin+"/ui/index.html" || request.PluginID != "acme.webview" {
		t.Fatalf("port request = %+v", request)
	}
	if request.DevTools {
		t.Fatal("devtools enabled outside development mode")
	}
	if len(request.Methods) != 1 || request.Methods[0] != "greet" {
		t.Fatalf("methods = %v", request.Methods)
	}

	asset, err := fixture.webviews.Serve(origin, "/ui/index.html")
	if err != nil {
		t.Fatalf("Serve html: %v", err)
	}
	if asset.ContentType != "text/html; charset=utf-8" || !strings.Contains(string(asset.Body), "app.js") {
		t.Fatalf("asset = %q %q", asset.ContentType, asset.Body)
	}
	if got := asset.Headers["Content-Security-Policy"]; got != appplugin.WebviewCSP {
		t.Fatalf("CSP = %q", got)
	}
	for _, want := range []string{"connect-src 'self'", "script-src 'self'", "object-src 'none'",
		"frame-src 'none'", "base-uri 'none'", "form-action 'none'", "worker-src 'none'"} {
		if !strings.Contains(appplugin.WebviewCSP, want) {
			t.Fatalf("CSP %q misses %q", appplugin.WebviewCSP, want)
		}
	}
	if asset.Headers["X-Content-Type-Options"] != "nosniff" {
		t.Fatalf("nosniff = %q", asset.Headers["X-Content-Type-Options"])
	}
	for _, forbidden := range []string{"Access-Control-Allow-Origin", "Set-Cookie"} {
		if _, ok := asset.Headers[forbidden]; ok {
			t.Fatalf("asset leaks %s", forbidden)
		}
	}

	for _, test := range []struct {
		name   string
		origin string
		path   string
	}{
		{"other plugin origin", "natcatty-plugin://other.plugin", "/ui/index.html"},
		{"https origin", "https://example.com", "/ui/index.html"},
		{"windows localhost spelling off windows", "http://acme.webview.natcatty-plugin.localhost", "/ui/index.html"},
		{"old windows localhost spelling", "http://acme.webview.localhost", "/ui/index.html"},
		{"path traversal", origin, "/ui/../plugin.wasm"},
		{"outside the ui subtree", origin, "/plugin.wasm"},
		{"unknown type", origin, "/ui/tool.exe"},
		{"missing", origin, "/ui/missing.html"},
		{"encoded traversal", origin, "/ui/%2e%2e/plugin.wasm"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fixture.webviews.Serve(test.origin, test.path); err == nil {
				t.Fatal("Serve accepted a forbidden request")
			}
		})
	}
	if _, err := fixture.webviews.Serve("", "/ui/app.js"); err != nil {
		t.Fatalf("same-origin fetch: %v", err)
	}
}

func TestWebviewOpenRequiresTheWebviewGrantAndPackagedEntry(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref), domainplugin.PermissionUIWebview)
	if _, err := fixture.webviews.OpenWebview(appplugin.WebviewSurfaceRequest{Entry: "ui/index.html"}); err == nil {
		t.Fatal("OpenWebview without ui.webview succeeded")
	} else if !appplugin.IsDenied(err) {
		t.Fatalf("error = %v, want a typed denial", err)
	}
	if len(fixture.port.opened(t)) != 0 {
		t.Fatal("a denied open reached the port")
	}

	for _, entry := range []string{"plugin.wasm", "ui/../plugin.wasm", "/ui/index.html", "ui/app.js", "ui/missing.html"} {
		if _, err := fixture.webviews.OpenWebview(appplugin.WebviewSurfaceRequest{Entry: entry}); err == nil {
			t.Fatalf("OpenWebview accepted entry %q", entry)
		}
	}
	if len(fixture.port.opened(t)) != 0 {
		t.Fatal("an invalid entry reached the port")
	}
}

func TestWebviewBridgeVerifiesTheCallingPageOriginAndAllowlist(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	surface := fixture.open(t, "greet")
	fixture.listenWebview(t)
	page := fixture.port.lastPage(t)
	origin := surface.Origin()

	result := startBridge(t, surface, page, origin, "greet", `{"name":"Ada"}`)
	envelope := pollEnvelope(t, fixture.session)
	if envelope.Type != "request" || envelope.Method != "greet" || envelope.Payload != `{"name":"Ada"}` || envelope.Origin != origin {
		t.Fatalf("envelope = %+v", envelope)
	}
	events := fixture.session.NativeUI().Poll(8)
	_ = events
	status := reply(t, fixture.session, domainnativeui.WebviewReply{
		Handle: uint64(surface.Handle()), Request: envelope.Request,
		Status: domainnativeui.StatusOK, Payload: "hello Ada",
	})
	if status.Status != domainnativeui.StatusOK {
		t.Fatalf("reply status = %v", status.Status)
	}
	if got := awaitBridge(t, result); got.err != nil || got.value != "hello Ada" {
		t.Fatalf("bridge = %q, %v", got.value, got.err)
	}

	// Every rejected call is refused before it reaches the backend queue.
	before := fixture.session.NativeUI().PendingEvents()
	for _, test := range []struct {
		name    string
		caller  any
		origin  string
		method  string
		payload string
	}{
		{"wrong page", new(int), origin, "greet", "{}"},
		{"wrong origin", page.Identity(), "natcatty-plugin://other.plugin", "greet", "{}"},
		{"https origin", page.Identity(), "https://example.com", "greet", "{}"},
		{"method outside allowlist", page.Identity(), origin, "exec", "{}"},
		{"payload too large", page.Identity(), origin, "greet", string(make([]byte, domainnativeui.MaxWebviewBridgeBytes+1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := surface.Bridge(context.Background(), test.caller, test.origin, test.method, test.payload); err == nil {
				t.Fatal("Bridge accepted a forbidden call")
			}
		})
	}
	if got := fixture.session.NativeUI().PendingEvents(); got != before {
		t.Fatalf("rejected bridge calls queued %d events", got-before)
	}
}

func TestWebviewBridgeRateLimitsAndStaleReplies(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	surface := fixture.open(t, "ping")
	fixture.listenWebview(t)
	page := fixture.port.lastPage(t)
	for i := 0; i < appplugin.WebviewBridgeCallsPerSecond; i++ {
		result := startBridge(t, surface, page, surface.Origin(), "ping", "{}")
		envelope := pollEnvelope(t, fixture.session)
		reply(t, fixture.session, domainnativeui.WebviewReply{
			Handle: uint64(surface.Handle()), Request: envelope.Request,
			Status: domainnativeui.StatusOK, Payload: "pong",
		})
		if got := awaitBridge(t, result); got.err != nil || got.value != "pong" {
			t.Fatalf("bridge %d = %q, %v", i, got.value, got.err)
		}
	}
	if _, err := surface.Bridge(context.Background(), page.Identity(), surface.Origin(), "ping", "{}"); !errors.Is(err, appplugin.ErrWebviewRate) {
		t.Fatalf("over-rate error = %v, want ErrWebviewRate", err)
	}

	status := reply(t, fixture.session, domainnativeui.WebviewReply{
		Handle: uint64(surface.Handle()), Request: 9999,
		Status: domainnativeui.StatusOK, Payload: "ghost",
	})
	if status.Status != domainnativeui.StatusStaleHandle && status.Status != domainnativeui.StatusInvalid {
		t.Fatalf("unknown reply status = %v", status.Status)
	}
}

func TestWebviewCrashClosesOnlyTheCrashedBridge(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	first := fixture.open(t, "ping")
	second := fixture.open(t, "ping")
	fixture.listenWebview(t)
	firstPage := fixture.port.pages[0]
	secondPage := fixture.port.pages[1]

	firstPage.request.Crashed("renderer gone")
	envelope := pollEnvelope(t, fixture.session)
	if envelope.Type != "crash" || envelope.Reason != "renderer gone" {
		t.Fatalf("crash envelope = %+v", envelope)
	}
	if _, err := first.Bridge(context.Background(), firstPage.Identity(), first.Origin(), "ping", "{}"); !errors.Is(err, appplugin.ErrWebviewClosed) {
		t.Fatalf("crashed surface bridge error = %v", err)
	}
	result := startBridge(t, second, secondPage, second.Origin(), "ping", "{}")
	envelope = pollEnvelope(t, fixture.session)
	reply(t, fixture.session, domainnativeui.WebviewReply{
		Handle: uint64(second.Handle()), Request: envelope.Request,
		Status: domainnativeui.StatusOK, Payload: "still alive",
	})
	if got := awaitBridge(t, result); got.err != nil || got.value != "still alive" {
		t.Fatalf("surviving surface = %q, %v", got.value, got.err)
	}

	first.Reload()
	fixture.listenWebview(t)
	result = startBridge(t, first, firstPage, first.Origin(), "ping", "{}")
	envelope = pollEnvelope(t, fixture.session)
	reply(t, fixture.session, domainnativeui.WebviewReply{
		Handle: uint64(first.Handle()), Request: envelope.Request,
		Status: domainnativeui.StatusOK, Payload: "reloaded",
	})
	if got := awaitBridge(t, result); got.err != nil || got.value != "reloaded" {
		t.Fatalf("reloaded surface = %q, %v", got.value, got.err)
	}
}

func TestWebviewDeniesNavigationPopupsPermissionsAndDownloads(t *testing.T) {
	fixture := newWebviewFixture(t, true)
	surface := fixture.open(t, "greet")
	request := fixture.port.lastPage(t).request
	if !request.DevTools {
		t.Fatal("development mode did not enable devtools")
	}
	origin := surface.Origin()
	for _, allowed := range []string{origin + "/ui/embed.html", origin + "/ui/index.html"} {
		if !request.AllowNavigation(allowed) {
			t.Fatalf("packaged same-origin navigation to %q denied", allowed)
		}
	}
	for _, rawURL := range []string{
		"https://example.com/", "http://example.com/", "natcatty-plugin://other.plugin/x",
		"javascript:alert(1)", "data:text/html,hi", "file:///etc/passwd",
		origin + "/", origin + "/index.html",
		origin + "/plugin.wasm", origin + "/ui/../plugin.wasm",
		origin + "/ui/app.js", origin + "/ui/style.css", origin + "/ui/missing.html",
		origin + "/ui/%2e%2e/plugin.wasm", origin + "/ui/index.html/",
		origin + ":8080/ui/index.html",
	} {
		if request.AllowNavigation(rawURL) {
			t.Fatalf("navigation to %q was allowed", rawURL)
		}
	}
	if !request.AllowBridgeCall(origin + "/ui/index.html") {
		t.Fatal("the packaged entry was denied the bridge")
	}
	for _, rawURL := range []string{
		origin + "/", origin + "/ui/app.js", origin + "/ui/missing.html",
		"https://example.com/ui/index.html", "natcatty-plugin://other.plugin/ui/index.html",
	} {
		if request.AllowBridgeCall(rawURL) {
			t.Fatalf("bridge page check accepted %q", rawURL)
		}
	}
	if request.AllowPermission("camera") || request.AllowPermission("microphone") ||
		request.AllowPermission("geolocation") || request.AllowPermission("clipboard-read") {
		t.Fatal("a permission request was allowed")
	}
	if request.AllowPopup(origin + "/ui/index.html") {
		t.Fatal("a popup was allowed")
	}
	if request.AllowDownload(origin + "/ui/index.html") {
		t.Fatal("a download was allowed")
	}
}

func TestWebviewLocaleFollowsTheHostAndUnloadClosesEverySurface(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	surface := fixture.open(t, "greet")
	page := fixture.port.lastPage(t)
	if page.locale != "en" || page.dir != appplugin.UIDirectionLTR {
		t.Fatalf("initial locale = %q %q", page.locale, page.dir)
	}
	if !fixture.host.SetLocale("ar") {
		t.Fatal("SetLocale reported no change")
	}
	if page.locale != "ar" || page.dir != appplugin.UIDirectionRTL {
		t.Fatalf("locale after host change = %q %q", page.locale, page.dir)
	}
	fixture.listenWebview(t)

	fixture.host.CloseAll()
	if !page.destroyed {
		t.Fatal("unload did not destroy the page")
	}
	fixture.port.mu.Lock()
	cleared := append([]string(nil), fixture.port.cleared...)
	fixture.port.mu.Unlock()
	if len(cleared) != 1 || cleared[0] != fixture.webviews.Origin() {
		t.Fatalf("unload cleared page storage = %v, want %q", cleared, fixture.webviews.Origin())
	}
	if _, err := surface.Bridge(context.Background(), page.Identity(), surface.Origin(), "greet", "{}"); !errors.Is(err, appplugin.ErrWebviewClosed) {
		t.Fatalf("bridge after unload = %v", err)
	}
	if _, err := fixture.webviews.Serve(surface.Origin(), "/ui/index.html"); !errors.Is(err, appplugin.ErrWebviewClosed) {
		t.Fatalf("serve after unload = %v", err)
	}
}

func TestWebviewCapsSimultaneousSurfaces(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	for i := 0; i < appplugin.MaxWebviews; i++ {
		fixture.open(t, "greet")
	}
	if _, err := fixture.webviews.OpenWebview(appplugin.WebviewSurfaceRequest{Entry: "ui/index.html"}); !errors.Is(err, appplugin.ErrWebviewLimit) {
		t.Fatalf("open above the cap = %v, want ErrWebviewLimit", err)
	}
	if err := fixture.webviews.Surfaces()[0].Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fixture.open(t, "greet")
}

var _ fs.FS = fstest.MapFS{}

func TestWebviewBridgeCapsPendingRequests(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	surface := fixture.open(t, "ping")
	fixture.listenWebview(t)
	page := fixture.port.lastPage(t)

	results := make(chan bridgeResult, appplugin.MaxWebviewPendingRequests)
	for i := 0; i < appplugin.MaxWebviewPendingRequests; i++ {
		go func() {
			value, err := surface.Bridge(context.Background(), page.Identity(), surface.Origin(), "ping", "{}")
			results <- bridgeResult{value: value, err: err}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for fixture.session.NativeUI().PendingEvents() < appplugin.MaxWebviewPendingRequests {
		if time.Now().After(deadline) {
			t.Fatal("the bridge requests never reached the queue")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := surface.Bridge(context.Background(), page.Identity(), surface.Origin(), "ping", "{}"); !errors.Is(err, appplugin.ErrWebviewPending) {
		t.Fatalf("above the pending cap = %v, want ErrWebviewPending", err)
	}
	_ = surface.Close()
	for i := 0; i < appplugin.MaxWebviewPendingRequests; i++ {
		if got := awaitBridge(t, results); !errors.Is(got.err, appplugin.ErrWebviewClosed) {
			t.Fatalf("pending bridge after close = %v, want ErrWebviewClosed", got.err)
		}
	}
}

func TestWebviewStorageClearReportsRealResults(t *testing.T) {
	fixture := newWebviewFixture(t, false)
	fixture.open(t, "greet")
	if err := fixture.webviews.ClearStorage(); err != nil {
		t.Fatalf("ClearStorage: %v", err)
	}
	fixture.port.mu.Lock()
	cleared := append([]string(nil), fixture.port.cleared...)
	fixture.port.mu.Unlock()
	if len(cleared) != 1 || cleared[0] != fixture.webviews.Origin() {
		t.Fatalf("cleared origins = %v", cleared)
	}

	failure := errors.New("renderer store is gone")
	fixture.port.mu.Lock()
	fixture.port.clearErr = failure
	fixture.port.mu.Unlock()
	if err := fixture.webviews.ClearStorage(); !errors.Is(err, appplugin.ErrWebviewStorage) || !errors.Is(err, failure) {
		t.Fatalf("failed ClearStorage = %v, want ErrWebviewStorage wrapping the port error", err)
	}

	empty := appplugin.NewHostServices(appplugin.ServiceDeps{})
	if err := empty.ClearWebviewStorage("acme.webview"); !errors.Is(err, appplugin.ErrWebviewUnavailable) {
		t.Fatalf("ClearWebviewStorage without a port = %v, want ErrWebviewUnavailable", err)
	}
}
