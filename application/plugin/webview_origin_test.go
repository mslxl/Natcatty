package plugin

import "testing"

// TestWebviewPageOriginMapsBothPlatformSpellings pins the platform-parameterized
// URL/origin mapping: the canonical custom-scheme origin everywhere, and the
// http://<plugin-id>.natcatty-plugin.localhost spelling only where MyGo serves
// custom schemes that way (Windows). The old http://<plugin-id>.localhost
// spelling is never a plugin origin.
func TestWebviewPageOriginMapsBothPlatformSpellings(t *testing.T) {
	canonical := "natcatty-plugin://acme.webview"
	tests := []struct {
		name    string
		raw     string
		windows bool
		want    string
	}{
		{"canonical page", "natcatty-plugin://acme.webview/ui/index.html", false, canonical},
		{"canonical origin header", "natcatty-plugin://acme.webview", false, canonical},
		{"windows spelling on windows", "http://acme.webview.natcatty-plugin.localhost/ui/index.html", true, canonical},
		{"windows origin header on windows", "http://acme.webview.natcatty-plugin.localhost", true, canonical},
		{"windows spelling off windows", "http://acme.webview.natcatty-plugin.localhost/ui/index.html", false, ""},
		{"old spelling on windows", "http://acme.webview.localhost/ui/index.html", true, ""},
		{"old spelling off windows", "http://acme.webview.localhost/ui/index.html", false, ""},
		{"https on windows", "https://acme.webview.natcatty-plugin.localhost/ui/index.html", true, ""},
		{"port on windows", "http://acme.webview.natcatty-plugin.localhost:8080/ui/index.html", true, ""},
		{"suffix confusion on windows", "http://evil.natcatty-plugin.localhost.evil/ui/index.html", true, ""},
		{"wrong host position on windows", "http://natcatty-plugin.localhost.evil/ui/index.html", true, ""},
		{"empty windows host", "http://natcatty-plugin.localhost/ui/index.html", true, ""},
		{"scheme host only", "natcatty-plugin:///ui/index.html", false, ""},
		{"user info", "natcatty-plugin://user@acme.webview/ui/index.html", false, ""},
		{"opaque", "natcatty-plugin:acme.webview", false, ""},
		{"remote", "https://example.com/ui/index.html", false, ""},
		{"javascript", "javascript:alert(1)", false, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := webviewPageOrigin(test.raw, test.windows); got != test.want {
				t.Fatalf("webviewPageOrigin(%q, windows=%v) = %q, want %q",
					test.raw, test.windows, got, test.want)
			}
		})
	}
}

func TestWebviewOriginsMatchOnlyTheSamePlugin(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		plugin  string
		windows bool
		want    bool
	}{
		{"canonical same plugin", "natcatty-plugin://acme.webview", "acme.webview", false, true},
		{"canonical case insensitive", "natcatty-plugin://ACME.WebView", "acme.webview", false, true},
		{"canonical other plugin", "natcatty-plugin://acme.other", "acme.webview", false, false},
		{"windows same plugin", "http://acme.webview.natcatty-plugin.localhost", "acme.webview", true, true},
		{"windows other plugin", "http://acme.other.natcatty-plugin.localhost", "acme.webview", true, false},
		{"windows spelling rejected elsewhere", "http://acme.webview.natcatty-plugin.localhost", "acme.webview", false, false},
		{"old localhost spelling", "http://acme.webview.localhost", "acme.webview", true, false},
		{"empty", "", "acme.webview", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := webviewOriginsMatch(test.origin, test.plugin, test.windows); got != test.want {
				t.Fatalf("webviewOriginsMatch(%q, %q, windows=%v) = %v, want %v",
					test.origin, test.plugin, test.windows, got, test.want)
			}
		})
	}
}

func TestWebviewHTMLPathAcceptsOnlyPackagedEntries(t *testing.T) {
	for _, allowed := range []string{"/ui/index.html", "/ui/embed.html"} {
		if _, err := webviewHTMLPath(allowed); err != nil {
			t.Fatalf("webviewHTMLPath(%q): %v", allowed, err)
		}
	}
	for _, denied := range []string{
		"", "/", "/index.html", "/plugin.wasm", "/ui/app.js", "/ui/style.css",
		"/ui/index.html/", "/ui/../plugin.wasm", "/ui/%2e%2e/plugin.wasm",
		"/ui/index.html?x=1", "/ui/index.html#x", "/ui//index.html", "ui/index.html",
	} {
		if _, err := webviewHTMLPath(denied); err == nil {
			t.Fatalf("webviewHTMLPath accepted %q", denied)
		}
	}
}
