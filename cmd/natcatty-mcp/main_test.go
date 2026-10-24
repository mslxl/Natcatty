package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

// startAppBridge serves the app side of the authenticated loopback transport on
// an already-bound listener, so a client dial cannot race readiness.
func startAppBridge(t *testing.T, token string) (port int, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	handler := mcprpc.HandlerFunc(func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "netcatty/getContext":
			return map[string]any{
				"ok":          true,
				"environment": "netcatty-terminal",
				"hosts":       []any{},
				"hostCount":   0,
			}, nil
		default:
			return nil, &mcprpc.RPCError{Code: mcprpc.CodeDispatchFailed, Message: "unknown method " + method}
		}
	})
	server := mcprpc.NewServer(token, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	stop = func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("app bridge did not stop")
		}
	}
	t.Cleanup(stop)
	return listener.Addr().(*net.TCPAddr).Port, stop
}

func requestLine(id int, method string, params any) string {
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	encoded, _ := json.Marshal(message)
	return string(encoded)
}

func decodeStdout(t *testing.T, output string) []map[string]any {
	t.Helper()
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("stdout is not JSON-RPC: %v\n%s", err, line)
		}
		messages = append(messages, message)
	}
	return messages
}

// TestRunAuthenticatesThenServesMCPOverStdio is acceptance bullet one without a
// GUI: a real loopback listener, the real discovery file, the real stdio
// protocol, and a read tool call.
func TestRunAuthenticatesThenServesMCPOverStdio(t *testing.T) {
	port, _ := startAppBridge(t, "secret-token")
	discoveryPath := filepath.Join(t.TempDir(), "external-mcp", "discovery.json")
	if _, err := mcprpc.WriteDiscovery(discoveryPath, mcprpc.Discovery{
		Host:  "127.0.0.1",
		Port:  port,
		Token: "secret-token",
	}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	t.Setenv(mcprpc.EnvDiscoveryFile, discoveryPath)
	t.Setenv(mcprpc.EnvPort, "")
	t.Setenv(mcprpc.EnvToken, "")

	input := strings.Join([]string{
		requestLine(1, "initialize", map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		}),
		requestLine(2, "tools/list", nil),
		requestLine(3, "tools/call", map[string]any{
			"name":      "get_environment",
			"arguments": map[string]any{},
		}),
	}, "\n") + "\n"

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), strings.NewReader(input), &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "connected to Netcatty") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	messages := decodeStdout(t, stdout.String())
	if len(messages) != 3 {
		t.Fatalf("responses = %d, want 3: %v", len(messages), messages)
	}

	tools := messages[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(catalog.CoreNames)+1 {
		t.Fatalf("visible tools = %d, want %d", len(tools), len(catalog.CoreNames)+1)
	}

	content := messages[2]["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "netcatty-terminal") {
		t.Fatalf("tool text = %s", text)
	}
}

// TestRunUsesTheExplicitPortAndTokenOverrides proves the original launch
// override still bypasses the discovery file.
func TestRunUsesTheExplicitPortAndTokenOverrides(t *testing.T) {
	port, _ := startAppBridge(t, "env-token")
	t.Setenv(mcprpc.EnvDiscoveryFile, filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv(mcprpc.EnvPort, strconv.Itoa(port))
	t.Setenv(mcprpc.EnvToken, "env-token")

	var stdout, stderr bytes.Buffer
	input := requestLine(1, "ping", nil) + "\n"
	if err := run(context.Background(), strings.NewReader(input), &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	messages := decodeStdout(t, stdout.String())
	if len(messages) != 1 || messages[0]["id"].(float64) != 1 {
		t.Fatalf("responses = %v", messages)
	}
}

// TestRunRejectsAWrongToken proves authentication is required before any MCP
// traffic is served.
func TestRunRejectsAWrongToken(t *testing.T) {
	port, _ := startAppBridge(t, "secret-token")
	discoveryPath := filepath.Join(t.TempDir(), "discovery.json")
	if _, err := mcprpc.WriteDiscovery(discoveryPath, mcprpc.Discovery{
		Host:  "127.0.0.1",
		Port:  port,
		Token: "wrong-token",
	}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	t.Setenv(mcprpc.EnvDiscoveryFile, discoveryPath)
	t.Setenv(mcprpc.EnvPort, "")
	t.Setenv(mcprpc.EnvToken, "")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), strings.NewReader(requestLine(1, "ping", nil)+"\n"), &stdout, &stderr)
	if err == nil {
		t.Fatal("a wrong token was accepted")
	}
	if !strings.Contains(err.Error(), "authenticate") {
		t.Fatalf("error = %v", err)
	}
	if code := mcprpc.ErrorCode(err); code != mcprpc.CodeAuthRequired {
		t.Fatalf("code = %d, want %d", code, mcprpc.CodeAuthRequired)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout leaked before authentication: %q", stdout.String())
	}
}

func TestRunExplainsAMissingDiscoveryFile(t *testing.T) {
	t.Setenv(mcprpc.EnvDiscoveryFile, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(mcprpc.EnvPort, "")
	t.Setenv(mcprpc.EnvToken, "")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run without discovery succeeded")
	}
	if !strings.Contains(err.Error(), "is the Netcatty app running?") {
		t.Fatalf("error = %v", err)
	}
}

// TestBridgeCallerDecodesAppResults covers the adapter between the transport's
// raw JSON and the protocol layer's values.
func TestBridgeCallerDecodesAppResults(t *testing.T) {
	port, _ := startAppBridge(t, "token")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mcprpc.Dial(ctx, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if err := client.Verify(ctx, "token"); err != nil {
		t.Fatalf("verify: %v", err)
	}

	value, err := (bridgeCaller{client: client}).Call(ctx, "netcatty/getContext", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	result, ok := value.(map[string]any)
	if !ok || result["environment"] != "netcatty-terminal" {
		t.Fatalf("value = %#v", value)
	}
}
