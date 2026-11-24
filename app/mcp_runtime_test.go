package app

// The External MCP listeners are opt-in: a default profile has no discovery
// file, no loopback bridge, and no HTTP endpoint. Turning the master switch on
// and off at runtime starts and stops both, the same way a restart would.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

func newMCPRuntimeForTest(t *testing.T) (*externalMCPEndpoints, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "external-mcp", "discovery.json")
	runtime := newExternalMCPEndpoints(context.Background(), &mcpReadBridge{},
		nil, newMCPModeState(path), path, nil)
	return runtime, path
}

func TestExternalMCPRuntimeOpensNothingUntilEnabled(t *testing.T) {
	runtime, path := newMCPRuntimeForTest(t)
	defer runtime.Close()

	if err := runtime.Apply(domainpreferences.Defaults().ExternalMCP); err != nil {
		t.Fatalf("apply the default preferences: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a disabled server published a discovery file: %v", err)
	}
	if token := runtime.Token(); token != "" {
		t.Fatalf("a disabled server has token %q", token)
	}
}

func TestExternalMCPRuntimeFollowsTheMasterSwitch(t *testing.T) {
	runtime, path := newMCPRuntimeForTest(t)
	defer runtime.Close()

	settings := domainpreferences.Defaults().ExternalMCP
	settings.Enabled = true
	if err := runtime.Apply(settings); err != nil {
		t.Fatalf("enable the server: %v", err)
	}
	discovery, err := mcprpc.ReadDiscovery(path)
	if err != nil {
		t.Fatalf("read the discovery file: %v", err)
	}
	if discovery.Port == 0 || discovery.Token == "" {
		t.Fatalf("discovery = %+v, want a live listener", discovery)
	}
	if runtime.Token() != discovery.Token {
		t.Fatalf("runtime token = %q, discovery token = %q", runtime.Token(), discovery.Token)
	}

	settings.Enabled = false
	if err := runtime.Apply(settings); err != nil {
		t.Fatalf("disable the server: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a disabled server kept its discovery file: %v", err)
	}
	if token := runtime.Token(); token != "" {
		t.Fatalf("a disabled server has token %q", token)
	}
}

func TestExternalMCPRuntimeGatesTheHTTPEndpointByTheMasterSwitch(t *testing.T) {
	runtime, _ := newMCPRuntimeForTest(t)
	runtime.SetHTTP(newExternalMCPHTTP(mcprpc.HandlerFunc(
		func(context.Context, string, json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		}), runtime.Token, nil))
	defer runtime.Close()

	settings := domainpreferences.Defaults().ExternalMCP
	settings.HTTPEnabled = true
	settings.HTTPPort = 0
	if err := runtime.Apply(settings); err != nil {
		t.Fatalf("apply an HTTP opt-in under a disabled server: %v", err)
	}
	if state := runtime.http.transport.State(); state.Listening {
		t.Fatalf("the HTTP endpoint listened while the server was off: %+v", state)
	}

	settings.Enabled = true
	if err := runtime.Apply(settings); err != nil {
		t.Fatalf("enable the server and endpoint: %v", err)
	}
	if state := runtime.http.transport.State(); !state.Listening || state.Port == 0 {
		t.Fatalf("the enabled endpoint did not listen: %+v", state)
	}

	settings.Enabled = false
	if err := runtime.Apply(settings); err != nil {
		t.Fatalf("disable the server: %v", err)
	}
	if state := runtime.http.transport.State(); state.Listening {
		t.Fatalf("the endpoint kept listening after the switch went off: %+v", state)
	}
}
