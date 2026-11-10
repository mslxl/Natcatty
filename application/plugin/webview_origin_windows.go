//go:build windows

package plugin

// webviewAcceptWindowsSpelling is true on Windows, where WebView2 serves the
// custom scheme at http://<plugin-id>.natcatty-plugin.localhost/.
const webviewAcceptWindowsSpelling = true
