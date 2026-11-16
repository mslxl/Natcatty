package mcp

// Ticket 41: the opt-in Streamable HTTP transport. Every test drives the real
// listener on an ephemeral loopback port; the protocol layer is the same one
// the stdio process runs, so these tests also prove the tool surface and the
// caller forwarding cannot drift between the two transports.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const httpTestToken = "ticket-41-token"

type httpFixture struct {
	t         *testing.T
	transport *HTTPTransport
	state     HTTPState
	base      string
	token     string
	client    *http.Client
}

func startHTTPFixture(t *testing.T, caller Caller, config HTTPConfig) *httpFixture {
	t.Helper()
	if config.Token == "" && config.TokenProvider == nil {
		config.Token = httpTestToken
	}
	config.Enabled = true
	if config.ListenAddress == "" {
		config.ListenAddress = "127.0.0.1"
	}
	if config.Logf == nil {
		config.Logf = t.Logf
	}
	transport := NewHTTPTransport()
	state, err := transport.Apply(config, caller)
	if err != nil {
		t.Fatalf("start the Streamable HTTP transport: %v", err)
	}
	t.Cleanup(transport.Stop)
	if !state.Listening {
		t.Fatal("the enabled transport is not listening")
	}
	return &httpFixture{
		t:         t,
		transport: transport,
		state:     state,
		base:      fmt.Sprintf("http://127.0.0.1:%d%s", state.Port, HTTPPath),
		token:     config.currentToken(),
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

type httpReply struct {
	status int
	header http.Header
	body   []byte
}

func (f *httpFixture) post(body any, session, token, accept string) httpReply {
	f.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("encode the request: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, f.base, bytes.NewReader(encoded))
	if err != nil {
		f.t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if accept == "" {
		accept = "application/json, text/event-stream"
	}
	request.Header.Set("Accept", accept)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	return f.do(request)
}

func (f *httpFixture) do(request *http.Request) httpReply {
	f.t.Helper()
	response, err := f.client.Do(request)
	if err != nil {
		f.t.Fatalf("HTTP request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatalf("read the response: %v", err)
	}
	return httpReply{status: response.StatusCode, header: response.Header, body: body}
}

func (f *httpFixture) initialize() string {
	f.t.Helper()
	reply := f.post(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "http-client", "version": "2"},
		},
	}, "", f.token, "")
	if reply.status != http.StatusOK {
		f.t.Fatalf("initialize status = %d, body = %s", reply.status, reply.body)
	}
	session := reply.header.Get("Mcp-Session-Id")
	if session == "" {
		f.t.Fatal("initialize did not return Mcp-Session-Id")
	}
	return session
}

func decodeHTTPReply(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var message map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body), &message); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return message
}

func httpResult(t *testing.T, reply httpReply, id int) map[string]any {
	t.Helper()
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", reply.status, reply.body)
	}
	message := decodeHTTPReply(t, reply.body)
	if rpcErr, present := message["error"]; present {
		t.Fatalf("request %d failed: %v", id, rpcErr)
	}
	result, ok := message["result"].(map[string]any)
	if !ok {
		t.Fatalf("request %d has no result: %v", id, message)
	}
	return result
}

func hasName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func httpRPCErrorCode(t *testing.T, reply httpReply) int {
	t.Helper()
	message := decodeHTTPReply(t, reply.body)
	rpcErr, ok := message["error"].(map[string]any)
	if !ok {
		t.Fatalf("no JSON-RPC error in %s", reply.body)
	}
	code, ok := rpcErr["code"].(float64)
	if !ok {
		t.Fatalf("error has no numeric code: %v", rpcErr)
	}
	return int(code)
}

// TestStreamableHTTPOpensNoListenerUntilEnabled is acceptance bullet one: with
// the option off, no socket is bound, even when the configured port is free.
func TestStreamableHTTPOpensNoListenerUntilEnabled(t *testing.T) {
	// Hold a port so an accidental bind would fail loudly.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold an ephemeral port: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	transport := NewHTTPTransport()
	defer transport.Stop()
	state, err := transport.Apply(HTTPConfig{
		Enabled:       false,
		ListenAddress: "127.0.0.1",
		Port:          port,
		Token:         httpTestToken,
	}, &fakeCaller{})
	if err != nil {
		t.Fatalf("a disabled transport tried to open a listener: %v", err)
	}
	if state.Listening || state.URL != "" || state.Port != 0 {
		t.Fatalf("disabled state = %+v, want no listener", state)
	}
	if transport.State().Listening {
		t.Fatal("the disabled transport reports a live listener")
	}
}

// TestStreamableHTTPServesInitializeListAndCall is acceptance bullet two: a
// real MCP client initializes, lists tools, and calls one over Streamable HTTP.
func TestStreamableHTTPServesInitializeListAndCall(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		"netcatty/getContext": map[string]any{
			"ok": true, "environment": "netcatty-terminal",
		},
	}}
	fixture := startHTTPFixture(t, caller, HTTPConfig{})
	session := fixture.initialize()

	list := fixture.post(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	}, session, fixture.token, "")
	result := httpResult(t, list, 2)
	names := toolNames(t, result)
	if len(names) == 0 || names[0] != "load_netcatty_tools" {
		t.Fatalf("tools/list order = %v, want the loader first", names)
	}
	if !hasName(names, "get_environment") {
		t.Fatalf("tools/list = %v, want get_environment", names)
	}

	call := fixture.post(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "get_environment", "arguments": map[string]any{}},
	}, session, fixture.token, "")
	callResult := httpResult(t, call, 3)
	content, _ := callResult["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("tool result content = %v", callResult)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "netcatty-terminal") {
		t.Fatalf("tool result text = %q", text)
	}

	if len(caller.calls) != 1 || caller.calls[0].method != "netcatty/getContext" {
		t.Fatalf("caller calls = %+v, want one getContext", caller.calls)
	}
	if label, _ := caller.calls[0].params[reservedClientArgument].(string); label != "http-client/2" {
		t.Fatalf("forwarded client label = %q, want http-client/2", label)
	}

	// The loader emits notifications/tools/list_changed before its result.
	// The JSON reply must still be the result, and the next list must show
	// the newly loaded tool.
	loaded := fixture.post(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name":      "load_netcatty_tools",
			"arguments": map[string]any{"toolset": "attachments"},
		},
	}, session, fixture.token, "")
	loadedResult := httpResult(t, loaded, 4)
	loadedContent, _ := loadedResult["content"].([]any)
	if len(loadedContent) != 1 {
		t.Fatalf("loader result content = %v", loadedResult)
	}
	loaderText, _ := loadedContent[0].(map[string]any)["text"].(string)
	if !strings.Contains(loaderText, "newlyLoaded") {
		t.Fatalf("loader result = %q", loaderText)
	}
	after := fixture.post(map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/list",
	}, session, fixture.token, "")
	afterNames := toolNames(t, httpResult(t, after, 5))
	if !hasName(afterNames, "list_attachments") {
		t.Fatalf("tools/list after the loader = %v", afterNames)
	}
}

// TestStreamableHTTPRejectsUnauthenticatedRequests is acceptance bullet three.
func TestStreamableHTTPRejectsUnauthenticatedRequests(t *testing.T) {
	fixture := startHTTPFixture(t, &fakeCaller{}, HTTPConfig{})
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}},
	}

	for name, token := range map[string]string{
		"missing": "",
		"wrong":   "not-the-token",
	} {
		reply := fixture.post(initialize, "", token, "")
		if reply.status != http.StatusUnauthorized {
			t.Fatalf("%s token status = %d, want 401", name, reply.status)
		}
		if code := httpRPCErrorCode(t, reply); code != -32001 {
			t.Fatalf("%s token error code = %d, want -32001", name, code)
		}
	}
}

// TestStreamableHTTPRotatedTokenAppliesWithoutRebinding proves the live token
// provider is consulted for every request, not only when the listener starts.
func TestStreamableHTTPRotatedTokenAppliesWithoutRebinding(t *testing.T) {
	current := httpTestToken
	transport := NewHTTPTransport()
	defer transport.Stop()
	state, err := transport.Apply(HTTPConfig{
		Enabled:       true,
		ListenAddress: "127.0.0.1",
		Port:          0,
		TokenProvider: func() string { return current },
		Logf:          t.Logf,
	}, &fakeCaller{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	fixture := &httpFixture{
		t: t, transport: transport, state: state,
		base:  fmt.Sprintf("http://127.0.0.1:%d%s", state.Port, HTTPPath),
		token: current, client: &http.Client{Timeout: 5 * time.Second},
	}
	session := fixture.initialize()
	current = "rotated-token"
	stale := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		session, httpTestToken, "")
	if stale.status != http.StatusUnauthorized {
		t.Fatalf("stale token status = %d, want 401", stale.status)
	}
	fresh := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"},
		session, "rotated-token", "")
	httpResult(t, fresh, 3)
}

// TestStreamableHTTPSessionContract covers the session lifecycle, Host and
// Origin validation, content negotiation, and DELETE.
func TestStreamableHTTPSessionContract(t *testing.T) {
	fixture := startHTTPFixture(t, &fakeCaller{}, HTTPConfig{})
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}},
	}

	missing := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		"", fixture.token, "")
	if missing.status != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", missing.status)
	}

	session := fixture.initialize()
	unknown := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"},
		"does-not-exist", fixture.token, "")
	if unknown.status != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", unknown.status)
	}

	// A browser page on another origin, and a DNS-rebinding Host, are refused.
	evilOrigin, err := http.NewRequest(http.MethodPost, fixture.base, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build the Origin request: %v", err)
	}
	evilOrigin.Header.Set("Content-Type", "application/json")
	evilOrigin.Header.Set("Authorization", "Bearer "+fixture.token)
	evilOrigin.Header.Set("Origin", "http://evil.example")
	if reply := fixture.do(evilOrigin); reply.status != http.StatusForbidden {
		t.Fatalf("foreign Origin status = %d, want 403", reply.status)
	}
	rebinding, err := http.NewRequest(http.MethodPost, fixture.base, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build the Host request: %v", err)
	}
	rebinding.Host = fmt.Sprintf("evil.example:%d", fixture.state.Port)
	rebinding.Header.Set("Content-Type", "application/json")
	rebinding.Header.Set("Authorization", "Bearer "+fixture.token)
	if reply := fixture.do(rebinding); reply.status != http.StatusMisdirectedRequest {
		t.Fatalf("foreign Host status = %d, want 421", reply.status)
	}

	wrongType, err := http.NewRequest(http.MethodPost, fixture.base, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build the content-type request: %v", err)
	}
	wrongType.Header.Set("Content-Type", "text/plain")
	wrongType.Header.Set("Authorization", "Bearer "+fixture.token)
	if reply := fixture.do(wrongType); reply.status != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong Content-Type status = %d, want 415", reply.status)
	}
	if reply := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/list"},
		session, fixture.token, "text/plain"); reply.status != http.StatusNotAcceptable {
		t.Fatalf("wrong Accept status = %d, want 406", reply.status)
	}
	if reply := fixture.post([]any{initialize}, "", fixture.token, ""); reply.status != http.StatusBadRequest {
		t.Fatalf("batch status = %d, want 400", reply.status)
	}

	get, err := http.NewRequest(http.MethodGet, fixture.base, nil)
	if err != nil {
		t.Fatalf("build the GET request: %v", err)
	}
	get.Header.Set("Authorization", "Bearer "+fixture.token)
	get.Header.Set("Mcp-Session-Id", session)
	if reply := fixture.do(get); reply.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", reply.status)
	}

	remove, err := http.NewRequest(http.MethodDelete, fixture.base, nil)
	if err != nil {
		t.Fatalf("build the DELETE request: %v", err)
	}
	remove.Header.Set("Authorization", "Bearer "+fixture.token)
	remove.Header.Set("Mcp-Session-Id", session)
	if reply := fixture.do(remove); reply.status != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", reply.status)
	}
	gone := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/list"},
		session, fixture.token, "")
	if gone.status != http.StatusNotFound {
		t.Fatalf("deleted session status = %d, want 404", gone.status)
	}
}

// TestStreamableHTTPExpiresAnIdleSession covers the abandoned-session sweep.
func TestStreamableHTTPExpiresAnIdleSession(t *testing.T) {
	fixture := startHTTPFixture(t, &fakeCaller{}, HTTPConfig{
		SessionIdleTimeout: 30 * time.Millisecond,
	})
	session := fixture.initialize()

	// Advance the handler's clock instead of sleeping or polling the session
	// (a poll would itself refresh its last-activity time).
	fixture.transport.mu.Lock()
	handler := fixture.transport.handler
	fixture.transport.mu.Unlock()
	if handler == nil {
		t.Fatal("the fixture has no handler")
	}
	base := handler.now()
	handler.now = func() time.Time { return base.Add(time.Hour) }

	reply := fixture.post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		session, fixture.token, "")
	if reply.status != http.StatusNotFound {
		t.Fatalf("expired session status = %d, want 404 (body %s)", reply.status, reply.body)
	}
}

// TestStreamableHTTPNegotiatesSSE proves the text/event-stream response shape
// a client that does not accept JSON uses.
func TestStreamableHTTPNegotiatesSSE(t *testing.T) {
	fixture := startHTTPFixture(t, &fakeCaller{}, HTTPConfig{})
	reply := fixture.post(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}},
	}, "", fixture.token, "text/event-stream")
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", reply.status, reply.body)
	}
	if contentType := reply.header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}
	if reply.header.Get("Mcp-Session-Id") == "" {
		t.Fatal("the SSE initialize response has no session id")
	}
	text := string(reply.body)
	if !strings.Contains(text, "data: ") {
		t.Fatalf("SSE body = %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		message := decodeHTTPReply(t, []byte(strings.TrimPrefix(line, "data: ")))
		if _, ok := message["result"].(map[string]any); !ok {
			t.Fatalf("SSE message = %v", message)
		}
		return
	}
	t.Fatalf("no SSE data line in %q", text)
}

// TestHTTPClientURLKeepsWildcardBindsUnreachable is the client-URL contract for
// a custom bind: 0.0.0.0 is not a reachable address without a client host.
func TestHTTPClientURLKeepsWildcardBindsUnreachable(t *testing.T) {
	if got := clientURL("127.0.0.1", 47391, ""); got != "http://127.0.0.1:47391/mcp" {
		t.Fatalf("loopback URL = %q", got)
	}
	if got := clientURL("0.0.0.0", 47391, ""); got != "" {
		t.Fatalf("wildcard URL without a client host = %q, want empty", got)
	}
	if got := clientURL("0.0.0.0", 47391, "192.168.1.5"); got != "http://192.168.1.5:47391/mcp" {
		t.Fatalf("wildcard URL with a client host = %q", got)
	}
	if got := clientURL("::1", 47391, ""); got != "http://[::1]:47391/mcp" {
		t.Fatalf("IPv6 URL = %q", got)
	}
}
