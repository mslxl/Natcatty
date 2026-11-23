package vnc

import (
	"errors"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestDisplayAndPortDescribeTheSameEndpoint(t *testing.T) {
	for display := 0; display <= DisplayMax; display++ {
		port, ok := PortForDisplay(display)
		if !ok {
			t.Fatalf("display %d was rejected", display)
		}
		back, ok := DisplayForPort(port)
		if !ok || back != display {
			t.Fatalf("port %d mapped back to display %d, want %d", port, back, display)
		}
	}
	if _, ok := PortForDisplay(DisplayMax + 1); ok {
		t.Fatal("an out-of-range display was accepted")
	}
	if _, ok := DisplayForPort(DisplayBasePort - 1); ok {
		t.Fatal("a port below the display range was accepted")
	}
}

func TestNormalizeConfigFillsTheViewerDefaults(t *testing.T) {
	config := DefaultConfig()
	config.DisplayMode = "nonsense"
	config.FitMode = "nonsense"
	config.CursorMode = "nonsense"
	normalized := NormalizeConfig(&config)
	if normalized.DisplayMode != DisplayNumber || normalized.FitMode != Fit ||
		normalized.CursorMode != CursorLocal || !normalized.Shared {
		t.Fatalf("normalized config = %+v, want the viewer defaults", normalized)
	}
}

func TestSessionConfigNormalizesTheEndpointAndConfiguration(t *testing.T) {
	config := SessionConfig{
		Hostname: "  desk.local ",
		Username: " viewer ",
		Route:    Route{Jumps: []model.HostID{"jump-1"}},
	}
	normalized := config.Normalized()
	if normalized.Hostname != "desk.local" || normalized.Username != "viewer" {
		t.Fatalf("normalized endpoint = %+v, want trimmed values", normalized)
	}
	if normalized.Port != DefaultPort {
		t.Fatalf("port = %d, want %d", normalized.Port, DefaultPort)
	}
	if normalized.Configuration.FitMode != Fit || !normalized.Configuration.Shared {
		t.Fatalf("configuration = %+v, want defaults", normalized.Configuration)
	}
	normalized.Route.Jumps[0] = "changed"
	if config.Route.Jumps[0] != "jump-1" {
		t.Fatal("Normalized aliased the caller's jump slice")
	}
}

func TestSessionConfigValidation(t *testing.T) {
	if err := (SessionConfig{Hostname: "desk.local", Port: 5900}).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := (SessionConfig{Hostname: "", Port: 5900}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing hostname = %v, want ErrInvalidConfig", err)
	}
	if err := (SessionConfig{Hostname: "desk.local", Port: 70000}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("out-of-range port = %v, want ErrInvalidConfig", err)
	}
}

func TestWebSocketPathNormalization(t *testing.T) {
	tests := map[string]string{
		"":                 DefaultWebSocketPath,
		"   ":              DefaultWebSocketPath,
		"websockify":       "/websockify",
		"/vnc":             "/vnc",
		" /vnc?token=abc ": "/vnc?token=abc",
	}
	for input, want := range tests {
		if got := NormalizeWebSocketPath(input); got != want {
			t.Errorf("NormalizeWebSocketPath(%q) = %q, want %q", input, got, want)
		}
	}
	for _, valid := range []string{"/websockify", "/vnc?token=a=b", "/a/b", "/vnc%20x"} {
		if !ValidWebSocketPath(valid) {
			t.Errorf("ValidWebSocketPath(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{
		"", "websockify", "/web socket", "/vnc#frag", "/a\nb",
		// The HTTP client parses the target, so a malformed escape cannot be
		// sent even though it contains no forbidden character.
		"/vnc%zz", "/vnc%2",
	} {
		if ValidWebSocketPath(invalid) {
			t.Errorf("ValidWebSocketPath(%q) = true, want false", invalid)
		}
	}
}

func TestWebSocketConfigurationDefaultsAndClears(t *testing.T) {
	enabled := NormalizeConfig(&Config{WebSocket: true})
	if !enabled.WebSocket || enabled.WebSocketPath != DefaultWebSocketPath {
		t.Fatalf("enabled config = %+v, want the websockify path", enabled)
	}
	custom := NormalizeConfig(&Config{WebSocket: true, WebSocketPath: "vnc", WebSocketSecure: true})
	if custom.WebSocketPath != "/vnc" || !custom.WebSocketSecure {
		t.Fatalf("custom config = %+v, want the trimmed secure path", custom)
	}
	disabled := NormalizeConfig(&Config{WebSocketPath: "/vnc", WebSocketSecure: true})
	if disabled.WebSocket || disabled.WebSocketPath != "" || disabled.WebSocketSecure {
		t.Fatalf("disabled config = %+v, want the WebSocket settings cleared", disabled)
	}
}

func TestWebSocketSessionConfigDefaultsTheWebSocketPort(t *testing.T) {
	normalized := SessionConfig{
		Hostname:      "desk.local",
		Configuration: Config{WebSocket: true},
	}.Normalized()
	if normalized.Port != DefaultWebSocketPort {
		t.Fatalf("WebSocket port = %d, want %d", normalized.Port, DefaultWebSocketPort)
	}
	if normalized.Configuration.WebSocketPath != DefaultWebSocketPath {
		t.Fatalf("path = %q, want %q", normalized.Configuration.WebSocketPath, DefaultWebSocketPath)
	}
	plain := SessionConfig{Hostname: "desk.local"}.Normalized()
	if plain.Port != DefaultPort {
		t.Fatalf("plain port = %d, want %d", plain.Port, DefaultPort)
	}
}

func TestSessionConfigRejectsAnInvalidWebSocketPath(t *testing.T) {
	config := SessionConfig{
		Hostname: "desk.local",
		Port:     DefaultWebSocketPort,
		Configuration: Config{
			WebSocket:     true,
			WebSocketPath: "/bad path",
		},
	}.Normalized()
	if err := config.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid path = %v, want ErrInvalidConfig", err)
	}
}
