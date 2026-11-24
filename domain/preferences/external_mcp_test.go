package preferences

import (
	"encoding/json"
	"testing"
)

// TestExternalMCPIsOffByDefault is the opt-in contract for the external MCP
// server and its Streamable HTTP endpoint.
func TestExternalMCPIsOffByDefault(t *testing.T) {
	group := Defaults().ExternalMCP
	if group.Enabled {
		t.Fatal("the External MCP server is enabled by default")
	}
	if group.HTTPEnabled {
		t.Fatal("the Streamable HTTP endpoint is enabled by default")
	}
	if group.HTTPPort != DefaultExternalMCPHTTPPort {
		t.Fatalf("default HTTP port = %d, want %d", group.HTTPPort, DefaultExternalMCPHTTPPort)
	}
	if got := group.EffectiveHTTPListenAddress(); got != DefaultExternalMCPHTTPListenAddress {
		t.Fatalf("default listen address = %q, want loopback", got)
	}
	if group.HTTPAllowCustomBind {
		t.Fatal("a non-loopback bind is allowed by default")
	}
}

// TestExternalMCPHTTPNormalizesUnusableValues keeps a malformed document from
// producing a privileged port or a bind address the transport cannot use.
func TestExternalMCPHTTPNormalizesUnusableValues(t *testing.T) {
	normalized := NormalizeExternalMCP(ExternalMCP{
		HTTPEnabled:       true,
		HTTPPort:          80,
		HTTPListenAddress: "not-an-address",
		HTTPClientHost:    "0.0.0.0",
	})
	if normalized.HTTPPort != DefaultExternalMCPHTTPPort {
		t.Fatalf("port = %d, want the default", normalized.HTTPPort)
	}
	if normalized.HTTPListenAddress != DefaultExternalMCPHTTPListenAddress {
		t.Fatalf("listen address = %q, want the loopback default", normalized.HTTPListenAddress)
	}
	if normalized.HTTPClientHost != "" {
		t.Fatalf("client host = %q, want the wildcard to be dropped", normalized.HTTPClientHost)
	}
	if !normalized.HTTPEnabled {
		t.Fatal("normalization changed the opt-in flag")
	}

	custom := NormalizeExternalMCP(ExternalMCP{
		HTTPAllowCustomBind: true,
		HTTPPort:            5000,
		HTTPListenAddress:   "192.168.1.5",
		HTTPClientHost:      "10.0.0.2:47391",
	})
	if custom.HTTPPort != 5000 || custom.EffectiveHTTPListenAddress() != "192.168.1.5" {
		t.Fatalf("custom bind = %+v", custom)
	}
	if custom.HTTPClientHost != "" {
		t.Fatalf("client host with a port = %q, want it dropped", custom.HTTPClientHost)
	}
}

// TestExternalMCPHTTPIgnoresACustomAddressWithoutTheOptIn proves the address
// only applies after the explicit non-loopback switch is on.
func TestExternalMCPHTTPIgnoresACustomAddressWithoutTheOptIn(t *testing.T) {
	group := ExternalMCP{HTTPListenAddress: "0.0.0.0"}
	if got := group.EffectiveHTTPListenAddress(); got != DefaultExternalMCPHTTPListenAddress {
		t.Fatalf("listen address = %q, want loopback until the opt-in", got)
	}
	group.HTTPAllowCustomBind = true
	if got := group.EffectiveHTTPListenAddress(); got != "0.0.0.0" {
		t.Fatalf("listen address after the opt-in = %q", got)
	}
}

// TestExternalMCPHTTPSurvivesJSONAndStaysDeviceLocal checks the persisted
// shape and that cloud sync never carries the machine's bind settings.
func TestExternalMCPHTTPSurvivesJSONAndStaysDeviceLocal(t *testing.T) {
	settings := Defaults()
	settings.ExternalMCP = ExternalMCP{
		Enabled:             true,
		HTTPEnabled:         true,
		HTTPPort:            51234,
		HTTPAllowCustomBind: true,
		HTTPListenAddress:   "192.168.1.5",
		HTTPClientHost:      "192.168.1.5",
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("encode settings: %v", err)
	}
	var decoded Settings
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	decoded = Normalize(decoded)
	if decoded.ExternalMCP != settings.ExternalMCP {
		t.Fatalf("round trip = %+v, want %+v", decoded.ExternalMCP, settings.ExternalMCP)
	}
	portable, err := json.Marshal(Portable(settings))
	if err != nil {
		t.Fatalf("encode portable settings: %v", err)
	}
	if containsJSONKey(portable, "externalMcp") {
		t.Fatalf("portable settings carry the endpoint: %s", portable)
	}
}

func containsJSONKey(document []byte, key string) bool {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(document, &decoded); err != nil {
		return false
	}
	_, present := decoded[key]
	return present
}
