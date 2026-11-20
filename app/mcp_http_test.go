package app

// Ticket 41 at the composition root: the Streamable HTTP endpoint is off
// until the preference enables it, and when it is on every request goes
// through the same policy handler as the loopback stdio transport.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

const mcpHTTPTestToken = "app-http-token"

func TestExternalMCPHTTPOpensNoListenerWhenDisabled(t *testing.T) {
	endpoint := newExternalMCPHTTP(mcprpc.HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("a disabled endpoint dispatched a request")
		return nil, nil
	}), func() string { return mcpHTTPTestToken }, t.Logf)
	defer endpoint.Close()

	if err := endpoint.Apply(domainpreferences.Defaults().ExternalMCP); err != nil {
		t.Fatalf("apply the default preferences: %v", err)
	}
	state := endpoint.transport.State()
	if state.Listening || state.Port != 0 || state.URL != "" {
		t.Fatalf("disabled state = %+v, want no listener", state)
	}
}

func TestExternalMCPHTTPDispatchesThroughThePolicyHandler(t *testing.T) {
	var methods []string
	handler := mcprpc.HandlerFunc(func(_ context.Context, method string, params json.RawMessage) (any, error) {
		methods = append(methods, method)
		var arguments map[string]any
		_ = json.Unmarshal(params, &arguments)
		return map[string]any{
			"ok":          true,
			"environment": "http-policy-path",
			"client":      arguments["_mcpClient"],
		}, nil
	})
	endpoint := newExternalMCPHTTP(handler, func() string { return mcpHTTPTestToken }, t.Logf)
	defer endpoint.Close()

	settings := domainpreferences.Defaults().ExternalMCP
	settings.HTTPEnabled = true
	settings.HTTPPort = 0 // ephemeral for the test
	if err := endpoint.Apply(settings); err != nil {
		t.Fatalf("enable the endpoint: %v", err)
	}
	state := endpoint.transport.State()
	if !state.Listening || state.Port == 0 {
		t.Fatalf("enabled state = %+v, want a listener", state)
	}
	if state.URL != fmt.Sprintf("http://127.0.0.1:%d/mcp", state.Port) {
		t.Fatalf("client URL = %q", state.URL)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d/mcp", state.Port)
	post := func(body map[string]any, session, token string) (*http.Response, []byte) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		request, err := http.NewRequest(http.MethodPost, base, bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("build the request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if session != "" {
			request.Header.Set("Mcp-Session-Id", session)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("HTTP request: %v", err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read the response: %v", err)
		}
		return response, payload
	}

	response, payload := post(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "app-test", "version": "1"},
		},
	}, "", mcpHTTPTestToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", response.StatusCode, payload)
	}
	session := response.Header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("initialize returned no session")
	}

	response, payload = post(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "get_environment", "arguments": map[string]any{}},
	}, session, mcpHTTPTestToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tools/call status = %d, body = %s", response.StatusCode, payload)
	}
	if len(methods) != 1 || methods[0] != "netcatty/getContext" {
		t.Fatalf("policy methods = %v, want the environment read", methods)
	}
	if !strings.Contains(string(payload), "http-policy-path") {
		t.Fatalf("tool result = %s", payload)
	}
	if !strings.Contains(string(payload), "app-test/1") {
		t.Fatalf("the client identity did not reach the policy handler: %s", payload)
	}

	response, _ = post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"},
		session, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.StatusCode)
	}

	// Disabling the preference stops the listener without a restart.
	settings.HTTPEnabled = false
	if err := endpoint.Apply(settings); err != nil {
		t.Fatalf("disable the endpoint: %v", err)
	}
	if endpoint.transport.State().Listening {
		t.Fatal("the disabled endpoint still reports a listener")
	}
	if _, err := client.Get(base); err == nil {
		t.Fatal("the endpoint still accepts connections after being disabled")
	}
}
