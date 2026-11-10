package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func TestPluginWebviewPageOptionsAreUntrustedAndUseThePluginSession(t *testing.T) {
	session := mygo.NewEphemeralSession()
	for _, test := range []struct {
		name     string
		devtools bool
		want     mygo.DevTools
	}{
		{"production", false, mygo.DevToolsDisabled},
		{"development", true, mygo.DevToolsAuto},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := pluginWebviewPageOptions(test.devtools, session)
			if options.DevTools != test.want {
				t.Fatalf("devtools = %v, want %v", options.DevTools, test.want)
			}
			if !options.Untrusted {
				t.Fatal("a plugin page is not marked untrusted")
			}
			if options.Session != session {
				t.Fatalf("page session = %p, want the plugin's %p", options.Session, session)
			}
			if !options.Session.IsEphemeral() {
				t.Fatal("the plugin page does not use an ephemeral session")
			}
			if options.PreloadScript != "" {
				t.Fatalf("preload script = %q", options.PreloadScript)
			}
			if len(options.TrustedOrigins) != 0 {
				t.Fatalf("trusted origins = %v", options.TrustedOrigins)
			}
			if options.UserAgent != "" || options.ZoomFactor != 0 {
				t.Fatalf("unexpected page overrides: %+v", options)
			}
		})
	}
}

func TestPluginWebviewRouterOnlyKnowsRegisteredPages(t *testing.T) {
	router := newPluginWebviewRouter()
	page := new(int)
	browserPage := new(int)
	called := 0
	router.add(page, pluginWebviewRoute{
		origin:    "natcatty-plugin://acme.webview",
		allowPage: func(string) bool { return true },
		bridge: func(ctx context.Context, _ any, _, _, _ string) (string, error) {
			_ = ctx
			called++
			return "", nil
		},
	})
	if _, ok := router.lookup(browserPage); ok {
		t.Fatal("an unregistered host browser page resolved a plugin route")
	}
	route, ok := router.lookup(page)
	if !ok || route.origin != "natcatty-plugin://acme.webview" {
		t.Fatalf("route = %+v, %v", route, ok)
	}
	router.remove(page)
	if _, ok := router.lookup(page); ok {
		t.Fatal("a destroyed page still resolves a route")
	}
	if called != 0 {
		t.Fatalf("bridge ran %d times", called)
	}
}

func TestPluginWebviewBridgeChecksCallerOriginAndPackagedPage(t *testing.T) {
	router := newPluginWebviewRouter()
	page := new(int)
	other := new(int)
	var gotCaller any
	var gotOrigin, gotMethod, gotPayload string
	const origin = "natcatty-plugin://acme.webview"
	router.add(page, pluginWebviewRoute{
		origin: origin,
		allowPage: func(rawURL string) bool {
			return strings.HasPrefix(rawURL, origin+"/ui/") &&
				strings.HasSuffix(rawURL, ".html") && !strings.Contains(rawURL, "missing")
		},
		bridge: func(_ context.Context, caller any, origin, method, payload string) (string, error) {
			gotCaller, gotOrigin, gotMethod, gotPayload = caller, origin, method, payload
			return "answer", nil
		},
	})
	bridge := &pluginWebviewBridge{router: router}

	answer, err := bridge.call(context.Background(), page, origin+"/ui/index.html", "acme.webview", "greet", "Ada")
	if err != nil || answer != "answer" {
		t.Fatalf("bridge = %q, %v", answer, err)
	}
	if gotCaller != page || gotOrigin != origin || gotMethod != "greet" || gotPayload != "Ada" {
		t.Fatalf("bridge saw caller=%v origin=%q method=%q payload=%q",
			gotCaller, gotOrigin, gotMethod, gotPayload)
	}

	for _, test := range []struct {
		name string
		call func() (string, error)
	}{
		{"unregistered caller", func() (string, error) {
			return bridge.call(context.Background(), other, origin+"/ui/index.html", "acme.webview", "greet", "")
		}},
		{"foreign plugin origin", func() (string, error) {
			return bridge.call(context.Background(), page, "natcatty-plugin://other.plugin/ui/index.html", "acme.webview", "greet", "")
		}},
		{"remote origin", func() (string, error) {
			return bridge.call(context.Background(), page, "https://example.com/ui/index.html", "acme.webview", "greet", "")
		}},
		{"old windows spelling", func() (string, error) {
			return bridge.call(context.Background(), page, "http://acme.webview.localhost/ui/index.html", "acme.webview", "greet", "")
		}},
		{"request host is another plugin", func() (string, error) {
			return bridge.call(context.Background(), page, origin+"/ui/index.html", "other.plugin", "greet", "")
		}},
		{"outside ui", func() (string, error) {
			return bridge.call(context.Background(), page, origin+"/plugin.wasm", "acme.webview", "greet", "")
		}},
		{"missing page", func() (string, error) {
			return bridge.call(context.Background(), page, origin+"/ui/missing.html", "acme.webview", "greet", "")
		}},
		{"no caller", func() (string, error) {
			return bridge.call(context.Background(), nil, origin+"/ui/index.html", "acme.webview", "greet", "")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.call(); err == nil {
				t.Fatal("the bridge accepted a forged caller")
			}
		})
	}

	// The Windows spelling is the same page only where MyGo serves it that
	// way; the canonical custom-scheme URL is the page everywhere.
	windowsURL := "http://acme.webview.natcatty-plugin.localhost/ui/index.html"
	_, err = bridge.call(context.Background(), page, windowsURL, "acme.webview", "greet", "")
	if runtime.GOOS == "windows" && err != nil {
		t.Fatalf("the Windows spelling was denied on Windows: %v", err)
	}
	if runtime.GOOS != "windows" && err == nil {
		t.Fatal("the Windows spelling was accepted away from Windows")
	}
}

func TestPluginPageOriginMapsThePluginHostOnly(t *testing.T) {
	tests := map[string]string{
		"natcatty-plugin://acme.webview/ui/index.html": "natcatty-plugin://acme.webview",
		"natcatty-plugin://acme.webview":               "natcatty-plugin://acme.webview",
		"http://acme.webview.localhost/ui/index.html":  "",
		"https://acme.webview.localhost/ui/index.html": "",
		"https://example.com/":                         "",
		"javascript:alert(1)":                          "",
		"data:text/html,hi":                            "",
	}
	for rawURL, want := range tests {
		if got := appplugin.WebviewPageOrigin(rawURL); got != want {
			t.Fatalf("origin(%q) = %q, want %q", rawURL, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		if got := appplugin.WebviewPageOrigin("http://acme.webview.natcatty-plugin.localhost/ui/index.html"); got != "" {
			t.Fatalf("Windows spelling mapped away from Windows: %q", got)
		}
	}
}

func TestPluginWebviewSchemeRefusesToServeWithoutAnActivePlugin(t *testing.T) {
	handler := &pluginWebviewScheme{manager: func() *appplugin.Manager { return nil }}
	request := httptest.NewRequest(http.MethodGet,
		"http://acme.webview.natcatty-plugin.localhost/ui/index.html", nil)
	request.Header.Set("Origin", "natcatty-plugin://acme.webview")
	request.Header.Set("Cookie", "session=host-browser")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	for _, header := range []string{"Access-Control-Allow-Origin", "Set-Cookie"} {
		if response.Header().Get(header) != "" {
			t.Fatalf("response leaked %s", header)
		}
	}
}

func TestPluginWebviewBridgeEndpointRequiresAPostAndAPage(t *testing.T) {
	handler := &pluginWebviewScheme{manager: func() *appplugin.Manager { return nil }}
	get := httptest.NewRequest(http.MethodGet, "natcatty-plugin://acme.webview"+domainplugin.WebviewBridgePath, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET bridge status = %d, want 405", getResponse.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "natcatty-plugin://acme.webview"+domainplugin.WebviewBridgePath,
		strings.NewReader("{}"))
	post.Header.Set(appplugin.WebviewBridgeMethodHeader, "greet")
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	// httptest.NewRequest has no MyGo caller page in its context.
	if postResponse.Code != http.StatusForbidden {
		t.Fatalf("POST without a calling page = %d, want 403", postResponse.Code)
	}
	if postResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("the bridge answered with a CORS grant")
	}
}

// TestPluginWebviewPathRegistersNoGoBindings pins the new invariant: the
// webview path installs the custom-scheme handler only. Plugin pages are
// untrusted and use the same-origin HTTP bridge, so there is no process-wide
// MyGo binding through which another page could reach product state.
func TestPluginWebviewPathRegistersNoGoBindings(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "pluginwebview.go", nil, 0)
	if err != nil {
		t.Fatalf("parse pluginwebview.go: %v", err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "mygo" {
			return true
		}
		if selector.Sel.Name == "Bind" || selector.Sel.Name == "BindAs" {
			t.Fatalf("pluginwebview.go registers a MyGo binding: mygo.%s", selector.Sel.Name)
		}
		return true
	})
}

func TestPluginWebviewSessionsAreEphemeralAndPerPlugin(t *testing.T) {
	store := newWebviewPluginSessions()
	first := store.acquire("acme.webview")
	same := store.acquire("acme.webview")
	other := store.acquire("acme.other")
	if first == nil || same == nil || other == nil {
		t.Fatal("acquire returned no session")
	}
	if first != same {
		t.Fatal("one plugin got two sessions")
	}
	if first == other {
		t.Fatal("two plugins share one session")
	}
	if !first.IsEphemeral() || !other.IsEphemeral() {
		t.Fatal("a plugin session is not ephemeral")
	}
	if got := store.pages("acme.webview"); got != 2 {
		t.Fatalf("live pages = %d, want 2", got)
	}
	store.release("acme.webview")
	if store.session("acme.webview") != first {
		t.Fatal("the session went away while a page was live")
	}
	store.release("acme.webview")
	if store.session("acme.webview") != nil || store.pages("acme.webview") != 0 {
		t.Fatal("the session was kept after its last page closed")
	}
	if next := store.acquire("acme.webview"); next == first {
		t.Fatal("a new page reused the closed session")
	}
}

func TestPluginWebviewSessionStorageClearIsReal(t *testing.T) {
	var gotTypes mygo.SiteData
	clears := 0
	store := newWebviewPluginSessions()
	store.clearData = func(_ *mygo.Session, types mygo.SiteData) error {
		gotTypes = types
		clears++
		return nil
	}
	session := store.acquire("acme.webview")
	if err := store.clear("acme.webview"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if clears != 1 || gotTypes != mygo.SiteDataAll {
		t.Fatalf("clearData(%v) called %d times, want SiteDataAll once", gotTypes, clears)
	}
	if store.session("acme.webview") != session {
		t.Fatal("a live session was dropped by a clear")
	}

	failure := errors.New("store failed")
	store.clearData = func(*mygo.Session, mygo.SiteData) error { return failure }
	if err := store.clear("acme.webview"); !errors.Is(err, appplugin.ErrWebviewStorage) || !errors.Is(err, failure) {
		t.Fatalf("failed clear = %v, want ErrWebviewStorage wrapping the failure", err)
	}

	// With no live page the ephemeral entry is simply dropped.
	store.release("acme.webview")
	store.clearData = func(*mygo.Session, mygo.SiteData) error {
		t.Fatal("a dropped session must not be cleared")
		return nil
	}
	if err := store.clear("acme.webview"); err != nil {
		t.Fatalf("clear with no session: %v", err)
	}
	if err := store.clear("never.opened"); err != nil {
		t.Fatalf("clear of a plugin with no page: %v", err)
	}
}

func TestPluginWebviewPortClearStorageRejectsForeignOrigins(t *testing.T) {
	port := newMygoWebviewPort(nil)
	for _, origin := range []string{
		"https://example.com", "http://acme.webview.localhost",
		"http://acme.webview.natcatty-plugin.localhost", "",
	} {
		if err := port.ClearStorage(origin); !errors.Is(err, appplugin.ErrWebviewStorage) {
			t.Fatalf("ClearStorage(%q) = %v, want ErrWebviewStorage", origin, err)
		}
	}
	if err := port.ClearStorage("natcatty-plugin://never.opened"); err != nil {
		t.Fatalf("ClearStorage of nothing = %v, want success", err)
	}
}
