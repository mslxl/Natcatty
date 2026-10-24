package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

type fakeCaller struct {
	calls   []call
	results map[string]any
	errs    map[string]error
}

type call struct {
	method string
	params map[string]any
}

func (f *fakeCaller) Call(_ context.Context, method string, params map[string]any) (any, error) {
	f.calls = append(f.calls, call{method: method, params: params})
	if err := f.errs[method]; err != nil {
		return nil, err
	}
	if result, ok := f.results[method]; ok {
		return result, nil
	}
	return map[string]any{"ok": true}, nil
}

// runScript feeds newline-delimited JSON requests to a fresh stdio server and
// returns the decoded output lines.
func runScript(t *testing.T, caller Caller, requests ...string) []map[string]any {
	t.Helper()
	input := strings.Join(requests, "\n") + "\n"
	var output bytes.Buffer
	server := NewServer(caller)
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("decode output line %q: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

func request(id int, method string, params any) string {
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func resultOf(t *testing.T, messages []map[string]any, id int) map[string]any {
	t.Helper()
	for _, message := range messages {
		if number, ok := message["id"].(float64); ok && int(number) == id {
			if rpcErr, ok := message["error"]; ok {
				t.Fatalf("request %d failed: %v", id, rpcErr)
			}
			result, ok := message["result"].(map[string]any)
			if !ok {
				t.Fatalf("request %d has no result object: %v", id, message)
			}
			return result
		}
	}
	t.Fatalf("no response for request %d: %v", id, messages)
	return nil
}

func toolNames(t *testing.T, result map[string]any) []string {
	t.Helper()
	raw, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result = %v", result)
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		tool, _ := item.(map[string]any)
		name, _ := tool["name"].(string)
		names = append(names, name)
	}
	return names
}

func TestInitializeIdentifiesTheOriginalServer(t *testing.T) {
	messages := runScript(t, &fakeCaller{}, request(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	}))
	result := resultOf(t, messages, 1)
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "netcatty-remote-hosts" || info["version"] != "1.0.0" {
		t.Fatalf("serverInfo = %v", info)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	capabilities, _ := result["capabilities"].(map[string]any)
	tools, _ := capabilities["tools"].(map[string]any)
	if tools["listChanged"] != true {
		t.Fatalf("tools capability = %v", tools)
	}
	if !strings.Contains(fmt.Sprint(result["instructions"]), "15 core tools start loaded") {
		t.Fatalf("instructions = %v", result["instructions"])
	}
}

func TestToolsListStartsWithFourteenCoreToolsAndTheLoader(t *testing.T) {
	messages := runScript(t, &fakeCaller{}, request(1, "tools/list", nil))
	names := toolNames(t, resultOf(t, messages, 1))
	if len(names) != 15 {
		t.Fatalf("tools = %d, want 15: %v", len(names), names)
	}
	if names[0] != catalog.LoaderName {
		t.Fatalf("first tool = %q, want the loader", names[0])
	}
	for index, want := range catalog.CoreNames {
		if names[index+1] != want {
			t.Fatalf("tool %d = %q, want %q", index+1, names[index+1], want)
		}
	}
}

func TestLoaderExposesAToolsetAndNotifiesListChanged(t *testing.T) {
	messages := runScript(t, &fakeCaller{},
		request(1, "tools/call", map[string]any{
			"name":      catalog.LoaderName,
			"arguments": map[string]any{"toolset": "sftp_advanced"},
		}),
		request(2, "tools/list", nil),
	)

	// The list_changed notification is emitted before the loader response so a
	// client that processes linearly refreshes before it continues.
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3: %v", len(messages), messages)
	}
	notification := messages[0]
	if notification["method"] != "notifications/tools/list_changed" {
		t.Fatalf("first message = %v, want the list_changed notification", notification)
	}

	result := resultOf(t, messages, 1)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	text := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("loader text is not JSON: %v\n%s", err, text)
	}
	if payload["toolset"] != "sftp_advanced" {
		t.Fatalf("toolset = %v", payload["toolset"])
	}
	if got := payload["newlyLoaded"].([]any); len(got) != 6 {
		t.Fatalf("newlyLoaded = %v", got)
	}
	if payload["loadedCatalogToolCount"].(float64) != 20 || payload["visibleToolCount"].(float64) != 21 {
		t.Fatalf("loader counts = %v", payload)
	}
	if payload["remainingCatalogToolCount"].(float64) != 49 {
		t.Fatalf("remaining = %v", payload["remainingCatalogToolCount"])
	}

	names := toolNames(t, resultOf(t, messages, 2))
	if len(names) != 21 {
		t.Fatalf("visible tools after loading = %d, want 21", len(names))
	}
	if !contains(names, "sftp_stat") || !contains(names, "sftp_home") {
		t.Fatalf("sftp_advanced tools are not visible: %v", names)
	}
}

func TestLoaderAllExposesSeventyTools(t *testing.T) {
	messages := runScript(t, &fakeCaller{},
		request(1, "tools/call", map[string]any{
			"name":      catalog.LoaderName,
			"arguments": map[string]any{"toolset": "all"},
		}),
		request(2, "tools/list", nil),
	)
	names := toolNames(t, resultOf(t, messages, 2))
	if len(names) != 70 {
		t.Fatalf("visible tools = %d, want 70", len(names))
	}
	for _, tool := range catalog.All() {
		if !contains(names, tool.Name) {
			t.Fatalf("tool %q is missing after load all", tool.Name)
		}
	}
}

func TestLoaderRejectsAnUnknownToolset(t *testing.T) {
	messages := runScript(t, &fakeCaller{}, request(1, "tools/call", map[string]any{
		"name":      catalog.LoaderName,
		"arguments": map[string]any{"toolset": "everything"},
	}))
	response := findMessage(messages, 1)
	if _, ok := response["error"]; !ok {
		t.Fatalf("unknown toolset did not fail: %v", response)
	}
}

func TestReadToolCallMapsTheRPCAndBoundsTheText(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		"netcatty/getContext": map[string]any{
			"environment": "netcatty-terminal",
			"hosts":       []any{},
			"hostCount":   0,
		},
	}}
	messages := runScript(t, caller, request(1, "tools/call", map[string]any{
		"name":      "get_environment",
		"arguments": map[string]any{},
	}))
	result := resultOf(t, messages, 1)
	if result["isError"] == true {
		t.Fatalf("get_environment failed: %v", result)
	}
	if len(caller.calls) != 1 || caller.calls[0].method != "netcatty/getContext" {
		t.Fatalf("calls = %+v", caller.calls)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"environment": "netcatty-terminal"`) {
		t.Fatalf("text = %s", text)
	}
}

func TestCallerResultsWithOKFalseBecomeToolErrors(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		"public/vault/host/get": map[string]any{"ok": false, "error": `Host "x" was not found.`},
	}}
	messages := runScript(t, caller,
		request(1, "tools/call", map[string]any{
			"name":      catalog.LoaderName,
			"arguments": map[string]any{"toolset": "vault_hosts"},
		}),
		request(2, "tools/call", map[string]any{
			"name":      "host_get",
			"arguments": map[string]any{"hostId": "x"},
		}),
	)
	result := resultOf(t, messages, 2)
	if result["isError"] != true {
		t.Fatalf("ok:false result was not an error: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != `Error: Host "x" was not found.` {
		t.Fatalf("text = %q", text)
	}
}

func TestOversizedResultsAreBounded(t *testing.T) {
	huge := strings.Repeat("x", MaxToolResultBytes*2)
	caller := &fakeCaller{results: map[string]any{"netcatty/getContext": huge}}
	messages := runScript(t, caller, request(1, "tools/call", map[string]any{
		"name":      "get_environment",
		"arguments": map[string]any{},
	}))
	result := resultOf(t, messages, 1)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if len(text) > MaxToolResultBytes+128 {
		t.Fatalf("text = %d bytes, want at most %d plus the marker", len(text), MaxToolResultBytes)
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("bounded text has no truncation marker: %q", text[len(text)-80:])
	}
}

func TestCallerFailureBecomesAnIsErrorResult(t *testing.T) {
	caller := &fakeCaller{errs: map[string]error{"netcatty/getContext": errors.New("app went away")}}
	messages := runScript(t, caller, request(1, "tools/call", map[string]any{
		"name":      "get_environment",
		"arguments": map[string]any{},
	}))
	result := resultOf(t, messages, 1)
	if result["isError"] != true {
		t.Fatalf("result = %v", result)
	}
	if !strings.Contains(fmt.Sprint(result["content"]), "app went away") {
		t.Fatalf("content = %v", result["content"])
	}
}

func TestUnknownToolAndMethodAreProtocolErrors(t *testing.T) {
	messages := runScript(t, &fakeCaller{},
		request(1, "tools/call", map[string]any{"name": "not_a_tool"}),
		request(2, "not/a/method", nil),
	)
	if _, ok := findMessage(messages, 1)["error"]; !ok {
		t.Fatalf("unknown tool did not fail: %v", messages)
	}
	errorBody, _ := findMessage(messages, 2)["error"].(map[string]any)
	if errorBody["code"].(float64) != -32601 {
		t.Fatalf("unknown method error = %v", errorBody)
	}
}

func TestPingResourcesAndNotifications(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		"netcatty/getContext": map[string]any{"environment": "netcatty-terminal"},
	}}
	messages := runScript(t, caller,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		request(1, "ping", nil),
		request(2, "resources/list", nil),
		request(3, "resources/read", map[string]any{"uri": "netcatty://context"}),
	)
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3 (the notification has no response): %v", len(messages), messages)
	}
	if _, ok := resultOf(t, messages, 1)["result"]; ok {
		t.Fatalf("ping result = %v", messages)
	}
	resources := resultOf(t, messages, 2)["resources"].([]any)
	if len(resources) != 1 {
		t.Fatalf("resources = %v", resources)
	}
	read := resultOf(t, messages, 3)
	contents := read["contents"].([]any)
	if len(contents) != 1 || contents[0].(map[string]any)["uri"] != "netcatty://context" {
		t.Fatalf("contents = %v", contents)
	}
	if !strings.Contains(contents[0].(map[string]any)["text"].(string), "netcatty-terminal") {
		t.Fatalf("context text = %v", contents[0])
	}
}

func TestHiddenToolCannotBeCalledBeforeLoading(t *testing.T) {
	caller := &fakeCaller{}
	messages := runScript(t, caller, request(1, "tools/call", map[string]any{
		"name":      "sftp_stat",
		"arguments": map[string]any{"sessionId": "s", "path": "/"},
	}))
	if _, ok := findMessage(messages, 1)["error"]; !ok {
		t.Fatalf("a deferred tool was callable before loading: %v", messages)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("the caller ran a hidden tool: %+v", caller.calls)
	}
}

func TestToolEntriesCarryTheOriginalSchemas(t *testing.T) {
	messages := runScript(t, &fakeCaller{}, request(1, "tools/list", nil))
	raw := resultOf(t, messages, 1)["tools"].([]any)
	var sftpList map[string]any
	for _, item := range raw {
		tool := item.(map[string]any)
		if tool["name"] == "sftp_list" {
			sftpList = tool
		}
	}
	if sftpList == nil {
		t.Fatal("sftp_list is missing")
	}
	schema := sftpList["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("schema = %v", schema)
	}
	properties := schema["properties"].(map[string]any)
	if properties["sessionId"].(map[string]any)["description"] != "SFTP session ID." {
		t.Fatalf("sessionId property = %v", properties["sessionId"])
	}
	if !containsAny(schema["required"], "sessionId", "path") {
		t.Fatalf("required = %v", schema["required"])
	}

	loader := raw[0].(map[string]any)
	loaderSchema := loader["inputSchema"].(map[string]any)
	enum := loaderSchema["properties"].(map[string]any)["toolset"].(map[string]any)["enum"].([]any)
	if len(enum) != 8 || enum[7] != "all" {
		t.Fatalf("loader enum = %v", enum)
	}
}

func findMessage(messages []map[string]any, id int) map[string]any {
	for _, message := range messages {
		if number, ok := message["id"].(float64); ok && int(number) == id {
			return message
		}
	}
	return map[string]any{}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsAny(value any, wants ...string) bool {
	items, _ := value.([]any)
	for _, item := range items {
		for _, want := range wants {
			if item == want {
				return true
			}
		}
	}
	return false
}
