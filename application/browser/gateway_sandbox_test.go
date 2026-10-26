package browser

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestGatewayHostilePageCannotLeaveTheRoute serves a page that tries every
// escape route the original sandbox was expected to stop: cross-origin fetch,
// an image beacon, a foreign iframe and object, window.open, eval, an external
// form, and a typed-bridge call. The gateway keeps the page on its own origin
// and replaces remote CSP with a policy whose only network source is 'self'.
// Go's client does not execute the page script, so the zero foreign-request
// count here proves the gateway never fetches those URLs itself; the real
// WebView run is recorded in the ticket.
func TestGatewayHostilePageCannotLeaveTheRoute(t *testing.T) {
	var foreignHits atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignHits.Add(1)
	}))
	defer foreign.Close()

	hostile := `<!doctype html><html><head><title>Hostile page</title>
<meta http-equiv="Content-Security-Policy" content="default-src * 'unsafe-eval'">
</head><body>
<a href="` + foreign.URL + `/navigate">leave</a>
<form action="` + foreign.URL + `/form"><input name="q"></form>
<img src="` + foreign.URL + `/pixel.png">
<iframe src="` + foreign.URL + `/frame"></iframe>
<object data="` + foreign.URL + `/object"></object>
<script>
for (const target of ["` + foreign.URL + `/fetch"]) {
  try { fetch(target); } catch (error) {}
  try { new Image().src = target; } catch (error) {}
  try { document.body.appendChild(Object.assign(document.createElement("iframe"), {src: target})); } catch (error) {}
  try { window.open(target); } catch (error) {}
  try { eval("fetch('" + target + "')"); } catch (error) {}
  try { window.mygo.call("Vault.List"); } catch (error) {}
}
</script>
</body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/hostile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, hostile)
	})
	originServer := httptest.NewServer(mux)
	defer originServer.Close()
	tunnel := newRoutedTunnel(strings.TrimPrefix(originServer.URL, "http://"))
	gw := newGateway(tunnel)
	defer gw.Close()

	gatewayURL, err := gw.URLFor(originServer.URL + "/hostile")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(gatewayURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, body)
	}

	csp := response.Header.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"connect-src 'self'",
		"frame-src 'self'",
		"worker-src 'self'",
		"object-src 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("hostile page CSP = %q, missing %q", csp, want)
		}
	}
	for _, forbidden := range []string{
		"unsafe-eval", foreign.URL, "http://", "https://", "ws:",
	} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("hostile page CSP = %q, must not allow %q", csp, forbidden)
		}
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "http-equiv=\"content-security-policy\"") {
		t.Fatalf("the page's own permissive CSP survived:\n%s", text)
	}
	for _, productAPI := range []string{
		"window.netcatty", "window.hostBrowser", "host-browser:", "preload.js",
	} {
		if strings.Contains(text, productAPI) {
			t.Fatalf("the gateway injected a product bridge marker %q:\n%s", productAPI, text)
		}
	}
	if hits := foreignHits.Load(); hits != 0 {
		t.Fatalf("the gateway made %d foreign requests", hits)
	}
	addresses := tunnel.dialed()
	for _, address := range addresses {
		if address != strings.TrimPrefix(originServer.URL, "http://") {
			t.Fatalf("tunnel dial = %q, want only the origin", address)
		}
	}
	if len(addresses) == 0 {
		t.Fatal("the gateway served the page without a tunnel dial")
	}
	if got := len(addresses); got != 1 {
		t.Fatalf("tunnel dials = %d, want 1", got)
	}
}
