package pluginregistry

import "testing"

// TestOpenHTTPUsesABoundedClient guards the registry HTTP timeout: a stalled
// server must not hang install, update, or automatic-update startup work.
func TestOpenHTTPUsesABoundedClient(t *testing.T) {
	registry := OpenHTTP("https://plugins.example/")
	if registry.client == nil {
		t.Fatal("HTTP registry has no client")
	}
	if registry.client.Timeout != HTTPTimeout {
		t.Fatalf("HTTP client timeout = %v, want %v", registry.client.Timeout, HTTPTimeout)
	}
	if HTTPTimeout <= 0 {
		t.Fatalf("HTTPTimeout = %v, want a positive bound", HTTPTimeout)
	}
}
