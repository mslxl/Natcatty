//go:build ios || android

package phoneshell

// platformProxyCommandCapability keeps ProxyCommand execution closed on the
// phone builds: neither platform has passed real-device acceptance for
// starting a process, carrying bytes, cancelling, and reaping it. Only the
// configuration is available until that acceptance is recorded.
func platformProxyCommandCapability() ProxyCommandCapability {
	return ProxyCommandCapability{Supported: false}
}
