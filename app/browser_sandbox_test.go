package app

import (
	"net/url"
	"testing"

	"github.com/egoist/mygo"

	appbrowser "github.com/mslxl/natcatty/application/browser"
)

// sandboxPage records the two handlers hardenBrowserPage installs.
type sandboxPage struct {
	permission func(mygo.PermissionRequest) bool
	windowOpen func(mygo.WindowOpenRequest) *mygo.WindowOptions
}

func (p *sandboxPage) SetPermissionHandler(fn func(mygo.PermissionRequest) bool) {
	p.permission = fn
}
func (p *sandboxPage) SetWindowOpenHandler(fn func(mygo.WindowOpenRequest) *mygo.WindowOptions) {
	p.windowOpen = fn
}

// TestBrowserPageCannotCallProductAPIs pins every route an untrusted Host page
// could use to reach app state:
//   - the page gets no preload script and no TrustedOrigins entry, so MyGo's
//     typed bridge refuses it;
//   - the gateway prefers 127.0.0.2, which is loopback but outside the
//     localhost/127.0.0.1/::1 hosts MyGo's development bridge auto-trusts.
//     macOS assigns only 127.0.0.1, so there the gateway falls back to it
//     (GatewayFallbackLoopbackHost); the app registers no MyGo bindings, so
//     even a development-trusted origin can reach no product API;
//   - permissions are denied and window.open returns no window options;
//   - there are no app-level MyGo bindings for it to call.
func TestBrowserPageCannotCallProductAPIs(t *testing.T) {
	options := browserPageOptions()
	if options.PreloadScript != "" {
		t.Fatalf("browser page has a preload script: %q", options.PreloadScript)
	}
	if len(options.TrustedOrigins) != 0 {
		t.Fatalf("browser page trusts origins: %v", options.TrustedOrigins)
	}
	if options.DevTools != mygo.DevToolsDisabled {
		t.Fatalf("browser page DevTools = %v, want disabled", options.DevTools)
	}

	gatewayURL, err := url.Parse("http://" + appbrowser.GatewayLoopbackHost + ":41873/")
	if err != nil {
		t.Fatal(err)
	}
	if !appbrowser.IsGatewayHost(gatewayURL.Hostname()) {
		t.Fatalf("gateway host = %q, want a loopback address a gateway binds", gatewayURL.Hostname())
	}
	if appbrowser.GatewayHostIsDevTrusted(gatewayURL.Hostname()) {
		t.Fatalf("gateway origin %q is trusted by MyGo's development bridge", gatewayURL.Host)
	}
	if !appbrowser.GatewayHostIsDevTrusted(appbrowser.GatewayFallbackLoopbackHost) {
		t.Fatalf("fallback gateway host %q is not the documented dev-trusted fallback",
			appbrowser.GatewayFallbackLoopbackHost)
	}

	page := &sandboxPage{}
	hardenBrowserPage(page)
	if page.permission == nil || page.permission(mygo.PermissionRequest{}) {
		t.Fatal("browser page permissions are not denied")
	}
	if page.windowOpen == nil {
		t.Fatal("browser page has no window-open handler")
	}
	if page.windowOpen(mygo.WindowOpenRequest{URL: "http://127.0.0.1:9/"}) != nil {
		t.Fatal("window.open created a page outside the browser panel")
	}
}
