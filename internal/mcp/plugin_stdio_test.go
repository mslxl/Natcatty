package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

func pluginInitialize() string {
	return request(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "plugin-test", "version": "2"},
	})
}

func TestToolsListAddsTheAppPluginToolsAfterTheBuiltIns(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		mcprpc.MethodPluginToolList: map[string]any{
			"ok": true,
			"tools": []any{
				map[string]any{
					"name":        "plugin.acme.tools.echo",
					"description": "Echo.",
					"inputSchema": map[string]any{"type": "object"},
				},
				map[string]any{"name": "acme.tools.unqualified"},
				map[string]any{"name": "plugin.acme.tools.echo", "description": "duplicate"},
				map[string]any{"name": "plugin.acme.tools.write"},
			},
		},
	}}
	messages := runScript(t, caller, pluginInitialize(), request(2, "tools/list", nil))
	names := toolNames(t, resultOf(t, messages, 2))
	if len(names) != 17 {
		t.Fatalf("tools = %d, want 15 built-ins plus 2 plugin tools: %v", len(names), names)
	}
	counts := map[string]int{}
	for _, name := range names {
		counts[name]++
	}
	if counts["plugin.acme.tools.echo"] != 1 || counts["plugin.acme.tools.write"] != 1 {
		t.Fatalf("plugin tools = %v", names)
	}
	if counts["acme.tools.unqualified"] != 0 {
		t.Fatalf("an unqualified tool was advertised: %v", names)
	}

	if len(caller.calls) != 1 || caller.calls[0].method != mcprpc.MethodPluginToolList {
		t.Fatalf("calls = %+v", caller.calls)
	}
	if caller.calls[0].params[reservedClientArgument] == "" {
		t.Fatalf("plugin list params = %#v, want the client identity", caller.calls[0].params)
	}
}

func TestToolsListSurvivesAPluginRegistryFailure(t *testing.T) {
	caller := &fakeCaller{errs: map[string]error{
		mcprpc.MethodPluginToolList: errPluginUnavailable,
	}}
	messages := runScript(t, caller, request(1, "tools/list", nil))
	names := toolNames(t, resultOf(t, messages, 1))
	if len(names) != 15 {
		t.Fatalf("tools = %d, want the 15 built-ins: %v", len(names), names)
	}
}

var errPluginUnavailable = errors.New("plugin registry unavailable")

func TestPluginToolCallIsForwardedWithItsIdentityAndProgressToken(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		mcprpc.MethodPluginToolList: map[string]any{"ok": true, "tools": []any{}},
		mcprpc.MethodPluginToolCall: map[string]any{
			"ok":                true,
			"tool":              "plugin.acme.tools.echo",
			"content":           []any{map[string]any{"type": "text", "text": "echo: hi"}},
			"structuredContent": map[string]any{"tool": "echo"},
		},
	}}
	messages := runScript(t, caller, pluginInitialize(), request(2, "tools/call", map[string]any{
		"name":      "plugin.acme.tools.echo",
		"arguments": map[string]any{"text": "hi"},
		"_meta":     map[string]any{"progressToken": "pt-1"},
	}))
	result := resultOf(t, messages, 2)
	content, _ := result["content"].([]any)
	block, _ := content[0].(map[string]any)
	if block["text"] != "echo: hi" {
		t.Fatalf("content = %#v", content)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if structured["tool"] != "echo" {
		t.Fatalf("structuredContent = %#v", result["structuredContent"])
	}
	if _, present := result["isError"]; present {
		t.Fatalf("isError = %#v", result["isError"])
	}

	var forwarded map[string]any
	for _, call := range caller.calls {
		if call.method == mcprpc.MethodPluginToolCall {
			forwarded = call.params
		}
	}
	if forwarded == nil {
		t.Fatalf("calls = %+v", caller.calls)
	}
	if forwarded["name"] != "plugin.acme.tools.echo" ||
		forwarded["_progressToken"] != "pt-1" ||
		forwarded[reservedClientArgument] == "" {
		t.Fatalf("forwarded params = %#v", forwarded)
	}
	arguments, _ := forwarded["arguments"].(map[string]any)
	if arguments["text"] != "hi" {
		t.Fatalf("forwarded arguments = %#v", forwarded["arguments"])
	}
}

func TestPluginToolDenialBecomesAToolError(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		mcprpc.MethodPluginToolCall: map[string]any{
			"ok":    false,
			"code":  "mcp_plugin_tool_denied",
			"error": "Plugin \"acme.tools\" has not been granted \"mcp.tools\".",
		},
	}}
	messages := runScript(t, caller, request(1, "tools/call", map[string]any{
		"name": "plugin.acme.tools.echo",
	}))
	result := resultOf(t, messages, 1)
	content, _ := result["content"].([]any)
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	if !strings.Contains(text, "mcp_plugin_tool_denied") ||
		!strings.Contains(text, "has not been granted") {
		t.Fatalf("denial text = %q", text)
	}
	if result["isError"] != true {
		t.Fatalf("isError = %#v", result["isError"])
	}
	if _, present := result["structuredContent"]; present {
		t.Fatalf("denial invented structuredContent: %#v", result["structuredContent"])
	}
}

func TestPluginToolGuestErrorKeepsStructuredContent(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		mcprpc.MethodPluginToolCall: map[string]any{
			"ok":                true,
			"content":           []any{map[string]any{"type": "text", "text": "partial"}},
			"structuredContent": map[string]any{"failed": true},
			"isError":           true,
		},
	}}
	messages := runScript(t, caller, request(1, "tools/call", map[string]any{
		"name": "plugin.acme.tools.fail",
	}))
	result := resultOf(t, messages, 1)
	if result["isError"] != true {
		t.Fatalf("isError = %#v", result["isError"])
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if structured["failed"] != true {
		t.Fatalf("structuredContent = %#v", result["structuredContent"])
	}
}
