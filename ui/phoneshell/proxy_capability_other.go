//go:build !ios && !android

package phoneshell

// platformProxyCommandCapability reports the host build. The phone pages are
// also driven by desktop tests, where ProxyCommand is executable; real phone
// builds use the mobile file.
func platformProxyCommandCapability() ProxyCommandCapability {
	return ProxyCommandCapability{Supported: true}
}
