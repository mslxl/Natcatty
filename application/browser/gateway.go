package browser

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
)

// The gateway is how a host-routed page reaches the WebView without the
// WebView knowing anything about the SSH transport. MyGo v0.5.5 has no
// per-web-view proxy, so each remote origin gets a loopback HTTP listener on
// an OS-assigned port. The page loads that listener; the listener fetches
// every request through the panel's dedicated tunnel, rewrites the page's
// absolute same-origin URLs to the listener, and refuses to expose a direct
// route.
// GatewayLoopbackHost is the preferred loopback address every origin gateway
// binds. It is deliberately not 127.0.0.1: MyGo's development builds
// auto-trust localhost, 127.0.0.1, and ::1 for its typed Go bridge, while
// 127.0.0.2 is still loopback but outside that trust set. No product API is
// bound on the gateway; this address is defence in depth around MyGo's
// origin check.
//
// Not every platform assigns 127.0.0.2 to its loopback interface: macOS
// assigns only 127.0.0.1, so binding 127.0.0.2 there fails with
// "can't assign requested address". The gateway falls back to
// GatewayFallbackLoopbackHost on such a platform, because a browser that
// cannot open any gateway cannot browse at all. The fallback is inside the
// development trust set; release builds (mygo build sets MYGO_ENV=production)
// are not, and the app registers no MyGo bindings, so the bridge can only
// reach MyGo's own window controls.
const GatewayLoopbackHost = "127.0.0.2"

// GatewayFallbackLoopbackHost is the last loopback address a gateway tries:
// the one every supported OS assigns. It is inside the hosts MyGo's
// development builds auto-trust, so it is used only where the preferred
// address cannot be bound.
const GatewayFallbackLoopbackHost = "127.0.0.1"

// gatewayHosts lists the loopback addresses a gateway may bind, preferred
// first.
func gatewayHosts() []string {
	return []string{GatewayLoopbackHost, GatewayFallbackLoopbackHost}
}

// IsGatewayHost reports whether host is a loopback address a gateway may
// bind. Callers that see a gateway URL use it instead of assuming the
// preferred address: macOS only assigns 127.0.0.1, so the gateway there
// binds the fallback.
func IsGatewayHost(host string) bool {
	for _, candidate := range gatewayHosts() {
		if host == candidate {
			return true
		}
	}
	return false
}

// IsGatewayURL reports whether raw is an HTTP URL for a gateway address with
// an explicit port. Callers that need the components still parse the URL;
// this is the shape check shared by the self-tests and the UI tests, so they
// cannot drift from the address set.
func IsGatewayURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Port() == "" {
		return false
	}
	return IsGatewayHost(parsed.Hostname())
}

// GatewayHostIsDevTrusted reports whether MyGo development builds auto-trust
// pages at host for their typed Go bridge. The preferred gateway address is
// outside that set; the macOS fallback is inside it.
func GatewayHostIsDevTrusted(host string) bool {
	switch host {
	case "localhost", GatewayFallbackLoopbackHost, "::1":
		return true
	}
	return false
}

// listenGateway opens a loopback listener for one origin gateway. It tries
// the addresses in gatewayHosts order and reports the first that binds.
func listenGateway() (net.Listener, error) {
	return listenGatewayAt(gatewayHosts())
}

// listenGatewayAt opens a listener on the first host that the OS assigns to a
// local interface. It keeps the first failure for the error when none binds.
// Only an unavailable address permits trying the next host (see
// listenGatewayWith); any other failure is returned immediately.
func listenGatewayAt(hosts []string) (net.Listener, error) {
	return listenGatewayWith(hosts, func(host string) (net.Listener, error) {
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	})
}

// listenGatewayWith is listenGatewayAt with the bind operation injected so a
// test can prove which errors permit the next address. Only an address the OS
// does not assign, or an unsupported address family, may fall through: a
// permission or resource failure must surface rather than be masked by the
// fallback, which would silently drop the preferred address's defence in
// depth.
func listenGatewayWith(hosts []string, bind func(host string) (net.Listener, error)) (net.Listener, error) {
	var firstErr error
	for _, host := range hosts {
		listener, err := bind(host)
		if err == nil {
			return listener, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !gatewayAddressUnavailable(err) {
			return nil, err
		}
	}
	if firstErr == nil {
		firstErr = errors.New("no loopback address to bind")
	}
	return nil, firstErr
}

// gatewayAddressUnavailable reports whether err means the OS does not assign
// the address to a local interface, or does not support its address family.
// These are the only failures that justify trying the next loopback address.
func gatewayAddressUnavailable(err error) bool {
	if errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) {
		return true
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) || runtime.GOOS != "windows" {
		return false
	}
	// Go's Windows syscall package defines EADDRNOTAVAIL as its own
	// os.Errno sentinel, not the Winsock code net.Listen returns, so compare
	// the Winsock values directly.
	const (
		wsaEAFNOSUPPORT  = syscall.Errno(10047)
		wsaEADDRNOTAVAIL = syscall.Errno(10049)
	)
	return errno == wsaEADDRNOTAVAIL || errno == wsaEAFNOSUPPORT
}

const (
	maxRewrittenBody = 16 << 20
	// gatewayPageCSP keeps every fetch, frame, and form on the page's own
	// loopback gateway. Inline code is allowed because Host pages and the
	// runtime URL mapper use it; eval and data:/blob: scripts are not. A
	// wildcard loopback source would let an untrusted page reach unrelated
	// local services, so only 'self' (this page's gateway origin) is allowed.
	gatewayPageCSP = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; font-src 'self' data:; " +
		"media-src 'self' data: blob:; " +
		"connect-src 'self'; frame-src 'self'; worker-src 'self'; " +
		"object-src 'none'; base-uri 'self'; form-action 'self'; " +
		"frame-ancestors 'none'"

	// gatewayAssetCSP locks down a response that is not a page, such as an SVG
	// or XML document opened directly. Such a document can execute script, so
	// it must not become a route to loopback services or to the network.
	gatewayAssetCSP = "default-src 'none'; script-src 'none'; " +
		"style-src 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' data:; media-src 'self' data:; " +
		"connect-src 'none'; frame-src 'none'; object-src 'none'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

var (
	integrityAttribute = regexp.MustCompile(`(?i)\s+integrity\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
	metaCSP            = regexp.MustCompile(`(?is)<meta[^>]+http-equiv\s*=\s*["']?content-security-policy["']?[^>]*>`)
	metaRefresh        = regexp.MustCompile(`(?is)<meta[^>]+http-equiv\s*=\s*["']?refresh["']?[^>]*>`)
	headTag            = regexp.MustCompile(`(?is)<head(?:\s[^>]*)?>`)
)

// gateway owns every loopback origin listener of one connected panel.
type gateway struct {
	tunnel    Tunnel
	transport *http.Transport

	mu      sync.Mutex
	origins map[string]*originProxy
	closed  bool
}

// originProxy is one remote origin and its loopback listener. A page is
// served from this listener, so relative URLs already stay on the gateway;
// absolute same-origin URLs are rewritten to it as well.
type originProxy struct {
	gateway       *gateway
	remoteOrigin  string
	remote        *url.URL
	gatewayOrigin string
	listener      net.Listener
	server        *http.Server
}

func newGateway(tunnel Tunnel) *gateway {
	return &gateway{
		tunnel: tunnel,
		transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           tunnel.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DisableCompression:    true,
		},
		origins: make(map[string]*originProxy),
	}
}

// URLFor returns the loopback URL a page loads for remoteRaw. The remote
// origin's listener is created on first use and kept for the panel's route.
func (g *gateway) URLFor(remoteRaw string) (string, error) {
	parsed, err := url.Parse(remoteRaw)
	if err != nil {
		return "", domainbrowser.ErrAddressInvalid
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", domainbrowser.ErrAddressScheme
	}
	if parsed.Host == "" {
		return "", domainbrowser.ErrAddressInvalid
	}
	origin, err := domainbrowser.Origin(remoteRaw)
	if err != nil {
		return "", err
	}
	proxy, err := g.ensureOrigin(origin, parsed)
	if err != nil {
		return "", err
	}
	gatewayURL := *parsed
	gatewayURL.Scheme = "http"
	gatewayURL.Host = proxy.listener.Addr().String()
	gatewayURL.User = nil
	if gatewayURL.Path == "" {
		gatewayURL.Path = "/"
	}
	return gatewayURL.String(), nil
}

func (g *gateway) ensureOrigin(origin string, parsed *url.URL) (*originProxy, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, errors.New("browser: the browser connection is closed")
	}
	if existing := g.origins[origin]; existing != nil {
		return existing, nil
	}
	listener, err := listenGateway()
	if err != nil {
		return nil, fmt.Errorf("browser: open the loopback gateway: %w", err)
	}
	address := listener.Addr().String()
	host, _, splitErr := net.SplitHostPort(address)
	if splitErr != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		_ = listener.Close()
		return nil, errors.New("browser: the gateway did not bind loopback")
	}
	remote := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	proxy := &originProxy{
		gateway:       g,
		remoteOrigin:  origin,
		remote:        remote,
		gatewayOrigin: "http://" + address,
		listener:      listener,
	}
	proxy.server = &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	g.origins[origin] = proxy
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

// RemoteURL maps a gateway URL back to the real address. It reports false for
// a URL the gateway did not serve, so a foreign navigation never overwrites
// the address bar.
func (g *gateway) RemoteURL(gatewayRaw string) (string, bool) {
	parsed, err := url.Parse(gatewayRaw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return "", false
	}
	gatewayOrigin := "http://" + parsed.Host
	g.mu.Lock()
	proxy := g.proxyForGatewayLocked(gatewayOrigin)
	g.mu.Unlock()
	if proxy == nil {
		return "", false
	}
	out := *proxy.remote
	out.Path = parsed.Path
	out.RawPath = parsed.RawPath
	out.RawQuery = parsed.RawQuery
	out.ForceQuery = parsed.ForceQuery
	out.Fragment = parsed.Fragment
	return out.String(), true
}

func (g *gateway) proxyForGatewayLocked(gatewayOrigin string) *originProxy {
	for _, proxy := range g.origins {
		if proxy.gatewayOrigin == gatewayOrigin {
			return proxy
		}
	}
	return nil
}

func (g *gateway) remoteOriginOf(gatewayOrigin string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	proxy := g.proxyForGatewayLocked(gatewayOrigin)
	if proxy == nil {
		return "", false
	}
	return proxy.remoteOrigin, true
}

func (g *gateway) gatewayOriginOf(remoteOrigin string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	proxy := g.origins[remoteOrigin]
	if proxy == nil {
		return "", false
	}
	return proxy.gatewayOrigin, true
}

// fetchFavicon fetches the origin's /favicon.ico through the Host tunnel and
// returns a data URL. The page-declared icon list is not visible to this
// build, so the origin fallback is the only candidate; ICO is rejected
// because the MyGo bitmap decoder cannot display it.
func (g *gateway) fetchFavicon(ctx context.Context, pageURL, cookies string) (string, error) {
	origin, err := domainbrowser.Origin(pageURL)
	if err != nil {
		return "", err
	}
	gatewayURL, err := g.URLFor(origin + "/favicon.ico")
	if err != nil {
		return "", err
	}
	remoteRaw, ok := g.RemoteURL(gatewayURL)
	if !ok {
		return "", ErrDownloadURL
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteRaw, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp,image/x-icon;q=0.1")
	request.Header.Set("Accept-Encoding", "identity")
	if cookies != "" {
		request.Header.Set("Cookie", cookies)
	}
	response, err := g.transport.RoundTrip(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("browser: favicon status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, domainbrowser.MaxIconBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > domainbrowser.MaxIconBytes {
		return "", errors.New("browser: favicon exceeds the size limit")
	}
	mime := domainbrowser.SniffImageMIME(body)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body), nil
	default:
		return "", errors.New("browser: unsupported favicon format")
	}
}

// openDownload starts a download from one of this route's gateway URLs. It
// refuses every other URL, so a page-supplied string cannot turn the tunnel
// into an open proxy. Redirects stay on the route's remote origin and are
// bounded, so a redirect cannot reach another address the Host can dial.
func (g *gateway) openDownload(ctx context.Context, gatewayURL, cookies string) (*http.Response, error) {
	remoteRaw, ok := g.RemoteURL(gatewayURL)
	if !ok {
		return nil, ErrDownloadURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteRaw, nil)
	if err != nil {
		return nil, ErrDownloadURL
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Encoding", "identity")
	if cookies != "" {
		request.Header.Set("Cookie", cookies)
	}
	client := &http.Client{
		Transport: g.transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("browser: too many download redirects")
			}
			// The first URL was mapped from this route's gateway origin. A
			// redirect that changes scheme, host, or port would fetch an
			// address the page could never navigate to, so refuse it.
			if next.URL.Scheme != request.URL.Scheme ||
				!strings.EqualFold(next.URL.Host, request.URL.Host) {
				return ErrDownloadURL
			}
			return nil
		},
	}
	return client.Do(request)
}

// Close stops every listener and request. In-flight requests fail rather
// than finishing on a released tunnel.
func (g *gateway) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	proxies := make([]*originProxy, 0, len(g.origins))
	for _, proxy := range g.origins {
		proxies = append(proxies, proxy)
	}
	g.origins = make(map[string]*originProxy)
	g.mu.Unlock()

	for _, proxy := range proxies {
		_ = proxy.server.Close()
	}
	g.transport.CloseIdleConnections()
}

// ServeHTTP proxies one page request through the panel's tunnel.
func (p *originProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		proxyError(w, http.StatusMethodNotAllowed)
		return
	}
	target := *p.remote
	target.Path = r.URL.Path
	target.RawPath = r.URL.RawPath
	target.RawQuery = r.URL.RawQuery
	target.ForceQuery = r.URL.ForceQuery

	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		proxyError(w, http.StatusBadRequest)
		return
	}
	outbound.ContentLength = r.ContentLength
	outbound.Header = requestHeaders(r.Header)
	outbound.Host = p.remote.Host
	p.mapRequestHeaders(outbound)

	response, err := p.gateway.transport.RoundTrip(outbound)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		proxyError(w, http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	if location := response.Header.Get("Location"); response.StatusCode >= 300 &&
		response.StatusCode < 400 && location != "" {
		p.redirect(w, response, target, location)
		return
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	html := strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml")
	textual := html ||
		strings.Contains(contentType, "text/css") ||
		strings.Contains(contentType, "javascript") ||
		strings.Contains(contentType, "ecmascript")

	p.copyResponseHeaders(w.Header(), response.Header)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	switch {
	case html:
		w.Header().Set("Content-Security-Policy", gatewayPageCSP)
	case !textual:
		w.Header().Set("Content-Security-Policy", gatewayAssetCSP)
	}

	if !textual {
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}

	if encoding := response.Header.Get("Content-Encoding"); encoding != "" &&
		!strings.EqualFold(encoding, "identity") {
		proxyError(w, http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRewrittenBody+1))
	if err != nil {
		proxyError(w, http.StatusBadGateway)
		return
	}
	if len(body) > maxRewrittenBody {
		proxyError(w, http.StatusBadGateway)
		return
	}
	rewritten := p.rewriteOrigin(body)
	if html {
		rewritten = p.rewriteHTML(rewritten)
	}
	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(rewritten)
}

func (p *originProxy) redirect(w http.ResponseWriter, response *http.Response, target url.URL, location string) {
	parsed, err := url.Parse(location)
	if err != nil {
		proxyError(w, http.StatusBadGateway)
		return
	}
	resolved := target.ResolveReference(parsed)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		proxyError(w, http.StatusBadGateway)
		return
	}
	gatewayURL, err := p.gateway.URLFor(resolved.String())
	if err != nil {
		proxyError(w, http.StatusBadGateway)
		return
	}
	p.copyResponseHeaders(w.Header(), response.Header)
	w.Header().Set("Location", gatewayURL)
	w.WriteHeader(response.StatusCode)
}

func requestHeaders(source http.Header) http.Header {
	out := source.Clone()
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		out.Del(name)
	}
	out.Del("Accept-Encoding")
	out.Set("Accept-Encoding", "identity")
	return out
}

func (p *originProxy) mapRequestHeaders(request *http.Request) {
	if origin := request.Header.Get("Origin"); origin != "" {
		if remote, ok := p.gateway.remoteOriginOf(origin); ok {
			request.Header.Set("Origin", remote)
		}
	}
	if referer := request.Header.Get("Referer"); referer != "" {
		if parsed, err := url.Parse(referer); err == nil {
			if remote, ok := p.gateway.remoteOriginOf(parsed.Scheme + "://" + parsed.Host); ok {
				if origin, err := url.Parse(remote); err == nil {
					parsed.Scheme = origin.Scheme
					parsed.Host = origin.Host
					request.Header.Set("Referer", parsed.String())
				}
			}
		}
	}
}

func (p *originProxy) copyResponseHeaders(target, source http.Header) {
	for name, values := range source {
		switch http.CanonicalHeaderKey(name) {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
			"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
			"Content-Length", "Content-Encoding",
			"Content-Security-Policy", "Content-Security-Policy-Report-Only",
			"X-Frame-Options", "Strict-Transport-Security",
			"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy",
			"Cross-Origin-Resource-Policy", "Alt-Svc", "Link", "Refresh",
			"Location":
			continue
		}
		if http.CanonicalHeaderKey(name) == "Set-Cookie" {
			for _, value := range values {
				target.Add(name, rewriteSetCookie(value))
			}
			continue
		}
		if http.CanonicalHeaderKey(name) == "Access-Control-Allow-Origin" {
			for _, value := range values {
				if gateway, ok := p.gateway.gatewayOriginOf(value); ok {
					value = gateway
				}
				target.Add(name, value)
			}
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func rewriteSetCookie(value string) string {
	cookie, err := http.ParseSetCookie(value)
	if err != nil {
		return value
	}
	// A cookie must belong to the loopback gateway, not to the remote
	// hostname the WebView never visits. Secure and SameSite=None cookies
	// cannot be set over the loopback HTTP origin, so relax those attributes
	// while keeping the cookie's name, value, path, and expiry.
	cookie.Domain = ""
	cookie.Secure = false
	cookie.Partitioned = false
	if cookie.SameSite == http.SameSiteNoneMode {
		cookie.SameSite = http.SameSiteLaxMode
	}
	return cookie.String()
}

// rewriteOrigin replaces the remote origin with the loopback one throughout
// HTML, CSS, and JavaScript. This keeps absolute URLs on the Host route.
func (p *originProxy) rewriteOrigin(body []byte) []byte {
	text := string(body)
	text = strings.ReplaceAll(text, p.remoteOrigin, p.gatewayOrigin)
	if host := p.remote.Host; host != "" {
		text = strings.ReplaceAll(text, "//"+host, "//"+p.listener.Addr().String())
	}
	return []byte(text)
}

// rewriteHTML additionally removes policies that would let the page leave
// the gateway, strips integrity hashes invalidated by rewriting, and injects
// the runtime mapping for same-origin URLs the page builds at run time.
func (p *originProxy) rewriteHTML(body []byte) []byte {
	text := integrityAttribute.ReplaceAllString(string(body), "")
	text = metaCSP.ReplaceAllString(text, "")
	text = metaRefresh.ReplaceAllString(text, "")
	script := p.runtimeScript()
	if loc := headTag.FindStringIndex(text); loc != nil {
		at := loc[1]
		return []byte(text[:at] + script + text[at:])
	}
	return []byte(script + text)
}

// runtimeScript maps same-origin URLs the page constructs after parsing. The
// server rewrites attributes; this catches links, forms, fetch, XHR, and
// history calls that build an absolute URL from a string.
func (p *originProxy) runtimeScript() string {
	return `<script>(function(){var remote=` + strconv.Quote(p.remoteOrigin) +
		`,gateway=` + strconv.Quote(p.gatewayOrigin) + `;` +
		`function map(u){if(typeof u!=="string")return u;try{var a=new URL(u,document.baseURI);` +
		`if(a.origin===remote)return gateway+a.pathname+a.search+a.hash;}catch(e){}return u;}` +
		`document.addEventListener("click",function(e){var a=e.target&&e.target.closest?e.target.closest("a[href]"):null;` +
		`if(a){var m=map(a.href);if(m!==a.href)a.href=m;}},true);` +
		`document.addEventListener("submit",function(e){var f=e.target;if(f&&f.action){var m=map(f.action);if(m!==f.action)f.action=m;}},true);` +
		`var f=window.fetch;if(f)window.fetch=function(i,n){try{if(typeof i==="string")i=map(i);` +
		`else if(i&&i.url){var m=map(i.url);if(m!==i.url)i=new Request(m,i);}}catch(e){}` +
		`return f.call(this,i,n);};` +
		`var x=window.XMLHttpRequest&&XMLHttpRequest.prototype;if(x&&x.open){var o=x.open;` +
		`x.open=function(m,u){arguments[1]=map(u);return o.apply(this,arguments);};}` +
		`var hs=window.history;if(hs&&hs.pushState){var ps=hs.pushState,rs=hs.replaceState;` +
		`hs.pushState=function(s,t,u){if(u!==undefined)u=map(String(u));return ps.call(this,s,t,u);};` +
		`hs.replaceState=function(s,t,u){if(u!==undefined)u=map(String(u));return rs.call(this,s,t,u);};}` +
		`})();</script>`
}

// proxyError ends a gateway request without product copy. The application
// layer does not own display strings; a page failure is localized by the UI.
// An empty body also means a failed request cannot run script at the loopback
// origin.
func proxyError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", gatewayAssetCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
}
