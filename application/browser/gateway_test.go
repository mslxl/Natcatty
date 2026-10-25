package browser

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// routedTunnel records every address the gateway asks the Host to reach and
// connects it to the fixture server instead. The recording is the proof that
// page requests travel through the panel's tunnel, not a local direct call.
type routedTunnel struct {
	origin string
	dialer net.Dialer

	mu        sync.Mutex
	addresses []string
	done      chan struct{}
}

func newRoutedTunnel(origin string) *routedTunnel {
	return &routedTunnel{origin: origin, done: make(chan struct{})}
}

func (t *routedTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	t.mu.Lock()
	t.addresses = append(t.addresses, address)
	t.mu.Unlock()
	return t.dialer.DialContext(ctx, network, t.origin)
}

func (t *routedTunnel) Done() <-chan struct{} { return t.done }
func (t *routedTunnel) Discard()              { close(t.done) }

func (t *routedTunnel) dialed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.addresses...)
}

type gatewayFixture struct {
	server *httptest.Server
	tunnel *routedTunnel
	gw     *gateway
	origin string
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src https://evil.example")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Set-Cookie", "sid=abc; Domain=app.internal; Secure; SameSite=None; Path=/; HttpOnly")
		_, _ = io.WriteString(w, `<html><head><title>Console</title>`+
			`<meta http-equiv="Content-Security-Policy" content="default-src https://evil.example">`+
			`<meta http-equiv="refresh" content="0;url=https://evil.example">`+
			`</head><body>`+
			`<a href="http://app.internal:8080/next">next</a>`+
			`<img src="//app.internal:8080/logo.png">`+
			`<link rel="stylesheet" href="/style.css" integrity="sha256-bad">`+
			`<script src="/app.js" integrity="sha256-bad"></script>`+
			`<form action="http://app.internal:8080/echo"><input name="q"></form>`+
			`</body></html>`)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "final page")
	})
	mux.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.internal:9090/landing", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/landing", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "landing page")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "host="+r.Host+"\n"+
			"origin="+r.Header.Get("Origin")+"\n"+
			"referer="+r.Header.Get("Referer")+"\n"+
			"encoding="+r.Header.Get("Accept-Encoding")+"\n")
	})
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = io.WriteString(w, "body{background:url(http://app.internal:8080/bg.png)}")
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = io.WriteString(w, `fetch("http://app.internal:8080/data")`)
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data")
	})
	mux.HandleFunc("/vector.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg">`+
			`<script>fetch("http://127.0.0.1:1/")</script><rect width="1" height="1"/></svg>`)
	})
	mux.HandleFunc("/image.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	origin := strings.TrimPrefix(server.URL, "http://")
	tunnel := newRoutedTunnel(origin)
	gw := newGateway(tunnel)
	t.Cleanup(gw.Close)
	return &gatewayFixture{server: server, tunnel: tunnel, gw: gw, origin: origin}
}

func TestGatewayRoutesPageTrafficThroughTheTunnelAndRewritesThePage(t *testing.T) {
	fixture := newGatewayFixture(t)
	gatewayURL, err := fixture.gw.URLFor("http://app.internal:8080/")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(gatewayURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		t.Fatalf("gateway URL = %q", gatewayURL)
	}

	request, _ := http.NewRequest(http.MethodGet, gatewayURL, nil)
	request.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	request.Header.Set("Referer", gatewayURL+"previous")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	text := string(body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, text)
	}
	// The injected mapper names the remote origin once as its constant; every
	// page URL must have moved to the loopback gateway.
	if count := strings.Count(text, "http://app.internal:8080"); count != 1 {
		t.Fatalf("page holds %d direct remote URLs, want only the mapper constant:\n%s", count, text)
	}
	if !strings.Contains(text, `href="http://127.0.0.1:`) {
		t.Fatalf("absolute link was not rewritten:\n%s", text)
	}
	if !strings.Contains(text, `src="//127.0.0.1:`) {
		t.Fatalf("protocol-relative URL was not rewritten:\n%s", text)
	}
	if !strings.Contains(text, `action="http://127.0.0.1:`) {
		t.Fatalf("form action was not rewritten:\n%s", text)
	}
	if !strings.Contains(text, "new URL(u,document.baseURI)") {
		t.Fatalf("runtime URL mapping was not injected:\n%s", text)
	}
	if strings.Contains(text, "integrity=") {
		t.Fatalf("integrity attribute survived rewriting:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), "http-equiv=\"content-security-policy\"") ||
		strings.Contains(strings.ToLower(text), "http-equiv=\"refresh\"") {
		t.Fatalf("a remote policy survived rewriting:\n%s", text)
	}
	csp := response.Header.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'", "connect-src 'self'", "frame-src 'self'",
		"object-src 'none'", "frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("gateway CSP = %q, missing %q", csp, want)
		}
	}
	for _, forbidden := range []string{"127.0.0.1", "unsafe-eval", "ws://"} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("gateway CSP = %q, must not contain %q", csp, forbidden)
		}
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := response.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if got := response.Header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	cookie := response.Header.Get("Set-Cookie")
	if strings.Contains(cookie, "Domain=") || strings.Contains(cookie, "Secure") ||
		strings.Contains(cookie, "SameSite=None") {
		t.Fatalf("cookie attributes were not relaxed for loopback: %q", cookie)
	}
	if !strings.Contains(cookie, "sid=abc") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("cookie value changed: %q", cookie)
	}

	addresses := fixture.tunnel.dialed()
	if len(addresses) == 0 {
		t.Fatal("the gateway made no tunnel dial")
	}
	for _, address := range addresses {
		if address != "app.internal:8080" {
			t.Fatalf("tunnel dial = %q, want app.internal:8080", address)
		}
	}
}

func TestGatewayMapsHeadersAndFollowsRedirectsInsideTheRoute(t *testing.T) {
	fixture := newGatewayFixture(t)
	gatewayURL, err := fixture.gw.URLFor("http://app.internal:8080/echo")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(gatewayURL)
	request, _ := http.NewRequest(http.MethodGet, gatewayURL, nil)
	request.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	request.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host+"/page")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	text := string(body)
	for _, want := range []string{
		"host=app.internal:8080",
		"origin=http://app.internal:8080",
		"referer=http://app.internal:8080/page",
		"encoding=identity",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("echo missing %q:\n%s", want, text)
		}
	}

	redirectURL, err := fixture.gw.URLFor("http://app.internal:8080/next")
	if err != nil {
		t.Fatal(err)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	redirect, err := noFollow.Get(redirectURL)
	if err != nil {
		t.Fatal(err)
	}
	redirect.Body.Close()
	if redirect.StatusCode != http.StatusFound {
		t.Fatalf("redirect status = %d", redirect.StatusCode)
	}
	location := redirect.Header.Get("Location")
	locationURL, err := url.Parse(location)
	if err != nil || locationURL.Hostname() != "127.0.0.1" {
		t.Fatalf("redirect Location = %q", location)
	}
	final, err := http.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	finalBody, _ := io.ReadAll(final.Body)
	final.Body.Close()
	if string(finalBody) != "final page" {
		t.Fatalf("redirected body = %q", finalBody)
	}

	crossURL, err := fixture.gw.URLFor("http://app.internal:8080/cross")
	if err != nil {
		t.Fatal(err)
	}
	cross, err := noFollow.Get(crossURL)
	if err != nil {
		t.Fatal(err)
	}
	cross.Body.Close()
	crossLocation := cross.Header.Get("Location")
	crossParsed, err := url.Parse(crossLocation)
	if err != nil || crossParsed.Hostname() != "127.0.0.1" ||
		crossParsed.Port() == locationURL.Port() {
		t.Fatalf("cross-origin redirect Location = %q", crossLocation)
	}
	landing, err := http.Get(crossLocation)
	if err != nil {
		t.Fatal(err)
	}
	landingBody, _ := io.ReadAll(landing.Body)
	landing.Body.Close()
	if string(landingBody) != "landing page" {
		t.Fatalf("cross-origin body = %q", landingBody)
	}

	remote, ok := fixture.gw.RemoteURL(location)
	if !ok || remote != "http://app.internal:8080/final" {
		t.Fatalf("RemoteURL(%q) = %q, %v", location, remote, ok)
	}
}

func TestGatewayRewritesStyleSheetsAndScriptsThroughTheSameOrigin(t *testing.T) {
	fixture := newGatewayFixture(t)
	cssURL, err := fixture.gw.URLFor("http://app.internal:8080/style.css")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(cssURL)
	if err != nil {
		t.Fatal(err)
	}
	css, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(css), "http://app.internal:8080") ||
		!strings.Contains(string(css), "127.0.0.1") {
		t.Fatalf("CSS body = %s", css)
	}

	jsURL, _ := fixture.gw.URLFor("http://app.internal:8080/app.js")
	response, err = http.Get(jsURL)
	if err != nil {
		t.Fatal(err)
	}
	script, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(script), "http://app.internal:8080") ||
		!strings.Contains(string(script), "127.0.0.1") {
		t.Fatalf("JS body = %s", script)
	}
}

func TestGatewayCloseStopsItsLoopbackListeners(t *testing.T) {
	fixture := newGatewayFixture(t)
	gatewayURL, err := fixture.gw.URLFor("http://app.internal:8080/final")
	if err != nil {
		t.Fatal(err)
	}
	fixture.gw.Close()
	if _, err := http.Get(gatewayURL); err == nil {
		t.Fatal("a closed gateway still served a request")
	}
}

func TestGatewayLocksDownNonPageDocuments(t *testing.T) {
	fixture := newGatewayFixture(t)
	svgURL, err := fixture.gw.URLFor("http://app.internal:8080/vector.svg")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(svgURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SVG status = %d", response.StatusCode)
	}
	if !strings.Contains(string(body), "<svg") {
		t.Fatalf("SVG body = %q", body)
	}
	csp := response.Header.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'", "script-src 'none'", "connect-src 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("asset CSP = %q, missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "127.0.0.1") {
		t.Fatalf("asset CSP = %q reaches loopback beyond its own origin", csp)
	}
	imageURL, err := fixture.gw.URLFor("http://app.internal:8080/image.png")
	if err != nil {
		t.Fatal(err)
	}
	image, err := http.Get(imageURL)
	if err != nil {
		t.Fatal(err)
	}
	image.Body.Close()
	if got := image.Header.Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'none'") {
		t.Fatalf("image CSP = %q", got)
	}
}

func TestGatewayErrorResponsesCarryNoProductCopyAndCannotRunScript(t *testing.T) {
	fixture := newGatewayFixture(t)
	if _, err := fixture.gw.URLFor("http://app.internal:8080/"); err != nil {
		t.Fatal(err)
	}
	fixture.gw.mu.Lock()
	proxy := fixture.gw.origins["http://app.internal:8080"]
	fixture.gw.mu.Unlock()
	if proxy == nil {
		t.Fatal("the gateway did not create an origin proxy")
	}
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodConnect, "/", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("CONNECT status = %d", recorder.Code)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("gateway error body = %q, want no display copy", body)
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'none'") {
		t.Fatalf("error CSP = %q", got)
	}
}
