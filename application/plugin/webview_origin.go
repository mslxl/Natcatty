package plugin

import (
	"net/url"
	"strings"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// WebviewPageOrigin returns the canonical per-plugin origin of a page URL or
// an Origin header, or "" when it belongs to no plugin sandbox origin.
//
// Pages run at natcatty-plugin://<plugin-id>/. Windows has no custom scheme
// in WebView2, so MyGo serves the same pages at
// http://<plugin-id>.natcatty-plugin.localhost/: that spelling is accepted
// only by a Windows build and maps back to the canonical origin. It is never
// accepted elsewhere, and the older http://<plugin-id>.localhost spelling is
// not a plugin page on any platform.
func WebviewPageOrigin(rawURL string) string {
	return webviewPageOrigin(rawURL, webviewAcceptWindowsSpelling)
}

// webviewPageOrigin is the platform-independent mapping. Accepting the
// Windows spelling is a parameter so tests can pin both platform behaviors on
// one host.
func webviewPageOrigin(rawURL string, windowsSpelling bool) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == domainplugin.WebviewScheme {
		if parsed.Host == "" {
			return ""
		}
		return domainplugin.WebviewOrigin(strings.ToLower(parsed.Host))
	}
	if !windowsSpelling || scheme != "http" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	pluginID, ok := strings.CutSuffix(host, "."+domainplugin.WebviewScheme+".localhost")
	if !ok || pluginID == "" {
		return ""
	}
	return domainplugin.WebviewOrigin(pluginID)
}

// sameWebviewOrigin reports whether origin is the sandbox origin of pluginID
// on this platform.
func sameWebviewOrigin(origin, pluginID string) bool {
	return webviewOriginsMatch(origin, pluginID, webviewAcceptWindowsSpelling)
}

func webviewOriginsMatch(origin, pluginID string, windowsSpelling bool) bool {
	if origin == "" || pluginID == "" {
		return false
	}
	return strings.EqualFold(webviewPageOrigin(origin, windowsSpelling),
		domainplugin.WebviewOrigin(pluginID))
}
