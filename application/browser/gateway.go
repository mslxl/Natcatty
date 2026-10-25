package browser

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
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
