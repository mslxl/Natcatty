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
