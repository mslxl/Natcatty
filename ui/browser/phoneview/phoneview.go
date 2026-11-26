// Package phoneview places a native web view for the phone's Host Browser.
//
// The Host Browser surface itself lives in ui/phoneshell. The architecture
// test allows ui.WebView only under ui/browser, the desktop Host-routed
// browser panel's home, and the phone dependency gate forbids that exact path
// because the panel is desktop-only. This package is the phone-safe seam: it
// sits under ui/browser, so the single-WebView-home rule still holds, but it
// imports only github.com/egoist/mygo/ui, so the phone graph never reaches
// the desktop panel. The gate matches exact import paths, which is why
// ui/browser/phoneview is admissible while ui/browser is not.
package phoneview

import "github.com/egoist/mygo/ui"

// WebArea shows native in the element's content box and fills it. It is the
// only renderer the phone needs from ui.WebView: the address bar, tabs, and
// navigation controls around the page are native phone controls that
// ui/phoneshell draws itself.
func WebArea(c *ui.Context, native ui.NativeWebView) ui.Element {
	return ui.WebView(c, native).Fill()
}
