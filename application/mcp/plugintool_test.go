package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func normalize(t *testing.T, payload string) PluginCallResult {
	t.Helper()
	result, err := NormalizePluginToolResult([]byte(payload))
	if err != nil {
		t.Fatalf("NormalizePluginToolResult(%q): %v", payload, err)
	}
	return result
}

func TestPluginToolResultEnvelopeBecomesMCPContent(t *testing.T) {
	result := normalize(t, `{
		"content":[
			{"type":"text","text":"hello"},
			{"type":"image","data":"cG5n","mimeType":"image/png"}
		],
		"structuredContent":{"tool":"echo","n":1},
		"isError":false,
		"code":"guest_failed",
		"message":"guest tool failed"
	}`)
	if result.IsError {
		t.Fatal("isError = true")
	}
	if !result.HasStructuredContent {
		t.Fatal("structured content missing")
	}
	structured, _ := result.StructuredContent.(map[string]any)
	if structured["tool"] != "echo" {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	if len(result.Content) != 2 {
		t.Fatalf("content blocks = %d", len(result.Content))
	}
	text, _ := result.Content[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "hello" {
		t.Fatalf("text block = %#v", text)
	}
	image, _ := result.Content[1].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" ||
		image["data"] != "cG5n" {
		t.Fatalf("image block = %#v", image)
	}
	if result.GuestCode != "guest_failed" || result.GuestMessage != "guest tool failed" {
		t.Fatalf("guest error fields = %q, %q", result.GuestCode, result.GuestMessage)
	}
	if result.ResultBytes <= 0 {
		t.Fatalf("ResultBytes = %d, want the normalized payload size", result.ResultBytes)
	}
	body := result.MCPResult()
	if _, present := body["content"].([]any); !present {
		t.Fatalf("MCP content = %#v", body["content"])
	}
	if body["structuredContent"] == nil {
		t.Fatalf("MCP structuredContent = %#v", body["structuredContent"])
	}
	if _, present := body["isError"]; present {
		t.Fatalf("MCP result invented isError: %#v", body)
	}
}

func TestPluginToolResultFallsBackToTextAndBinary(t *testing.T) {
	plain := normalize(t, "hello from a text tool")
	if len(plain.Content) != 1 {
		t.Fatalf("content = %#v", plain.Content)
	}
	block, _ := plain.Content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello from a text tool" {
		t.Fatalf("text fallback = %#v", block)
	}
	if plain.HasStructuredContent {
		t.Fatal("text fallback invented structured content")
	}

	binary := []byte{0xff, 0xfe, 0x00, 0x01}
	result, err := NormalizePluginToolResult(binary)
	if err != nil {
		t.Fatalf("binary result: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("binary content = %#v", result.Content)
	}
	image, _ := result.Content[0].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "application/octet-stream" {
		t.Fatalf("binary block = %#v", image)
	}
	decoded, err := base64.StdEncoding.DecodeString(image["data"].(string))
	if err != nil || !bytes.Equal(decoded, binary) {
		t.Fatalf("binary data = %#v, %v", image["data"], err)
	}
}

func TestPluginToolResultPlainJSONIsStructuredContent(t *testing.T) {
	result := normalize(t, `{"count":2,"tags":["a"]}`)
	if !result.HasStructuredContent {
		t.Fatal("plain object became content instead of structured content")
	}
	object, _ := result.StructuredContent.(map[string]any)
	number, _ := object["count"].(json.Number)
	if number.String() != "2" {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	array := normalize(t, `[1,2,3]`)
	if !array.HasStructuredContent {
		t.Fatal("array became content")
	}
	// A bare code field is ordinary data, not an envelope signal.
	coded := normalize(t, `{"code":"E42","value":1}`)
	if !coded.HasStructuredContent {
		t.Fatal("an object with a code field was misread as an envelope")
	}
	codedObject, _ := coded.StructuredContent.(map[string]any)
	if codedObject["code"] != "E42" || codedObject["value"].(json.Number).String() != "1" {
		t.Fatalf("coded structured content = %#v", coded.StructuredContent)
	}
}

func TestPluginToolResultEmptyIsExplicit(t *testing.T) {
	result, err := NormalizePluginToolResult(nil)
	if err != nil {
		t.Fatalf("empty result: %v", err)
	}
	if len(result.Content) != 0 || result.HasStructuredContent {
		t.Fatalf("empty result = %+v", result)
	}
	body := result.MCPResult()
	content, ok := body["content"].([]any)
	if !ok || len(content) != 0 {
		t.Fatalf("empty MCP content = %#v", body["content"])
	}
	value, present := body["structuredContent"]
	if !present || value != nil {
		t.Fatalf("empty MCP structuredContent = %#v, present=%v", value, present)
	}
}

func TestPluginToolResultRejectsMalformedEnvelopes(t *testing.T) {
	cases := map[string]struct {
		payload string
		code    ErrorCode
	}{
		"content not an array": {
			`{"structuredContent":{},"content":"text"}`, CodePluginToolInvalidResult,
		},
		"unsupported block": {
			`{"content":[{"type":"audio","data":"x"}]}`, CodePluginToolInvalidResult,
		},
		"bad base64": {
			`{"content":[{"type":"image","data":"!!!!"}]}`, CodePluginToolInvalidResult,
		},
		"image without data": {
			`{"content":[{"type":"image"}]}`, CodePluginToolInvalidResult,
		},
		"text without text": {
			`{"content":[{"type":"text"}]}`, CodePluginToolInvalidResult,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizePluginToolResult([]byte(test.payload))
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestPluginToolResultBounds(t *testing.T) {
	oversized := bytes.Repeat([]byte("a"), MaxToolResultBytes+1)
	_, err := NormalizePluginToolResult(oversized)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodePluginToolResultLimit {
		t.Fatalf("oversized error = %v", err)
	}

	blocks := make([]string, MaxPluginToolContentBlocks+1)
	for i := range blocks {
		blocks[i] = `{"type":"text","text":"x"}`
	}
	_, err = NormalizePluginToolResult(
		[]byte(`{"content":[` + strings.Join(blocks, ",") + `]}`))
	if !errors.As(err, &typed) || typed.Code != CodePluginToolResultLimit {
		t.Fatalf("too many blocks error = %v", err)
	}
}

func TestTruncatePluginTextKeepsUTF8BoundariesAndMarksTruncation(t *testing.T) {
	if got := truncatePluginText("hello", 10); got != "hello" {
		t.Fatalf("short text = %q", got)
	}
	if got := truncatePluginText("hello world", 5); got != "hello"+pluginTruncationMarker {
		t.Fatalf("truncated text = %q", got)
	}
	if got := truncatePluginText("ééé", 3); !strings.HasSuffix(got, pluginTruncationMarker) ||
		!strings.HasPrefix(got, "é") {
		t.Fatalf("UTF-8 truncation = %q", got)
	}
	if got := truncatePluginText("x", 0); got != pluginTruncationMarker {
		t.Fatalf("zero limit = %q", got)
	}
}

func TestPluginToolErrorFieldsAreSanitized(t *testing.T) {
	result := normalize(t, `{"isError":true,`+
		`"code":"Guest Failed!","message":"a\u0000b\u007fc\nok"}`)
	if !result.IsError {
		t.Fatal("isError not read")
	}
	if result.GuestCode != "uestailed" {
		t.Fatalf("guest code = %q", result.GuestCode)
	}
	if result.GuestMessage != "abc\nok" {
		t.Fatalf("guest message = %q", result.GuestMessage)
	}
	if body := result.MCPResult(); body["isError"] != true {
		t.Fatalf("MCP isError = %#v", body["isError"])
	}

	withContent := normalize(t, `{"isError":true,`+
		`"content":[{"type":"text","text":"partial"}],`+
		`"structuredContent":{"failed":true}}`)
	body := withContent.MCPResult()
	content, _ := body["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("MCP error content = %#v", body["content"])
	}
	structured, _ := body["structuredContent"].(map[string]any)
	if body["isError"] != true || structured["failed"] != true {
		t.Fatalf("MCP error body = %#v", body)
	}
}
