package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Plugin tool results use a small reserved envelope so a guest can return MCP
// content blocks plus structured content without the host having to guess what
// arbitrary bytes mean:
//
//	{"content":[{"type":"text","text":"..."},
//	            {"type":"image","data":"<base64>","mimeType":"image/png"}],
//	 "structuredContent": <any>,
//	 "isError": false,
//	 "code": "guest_code", "message": "..."}
//
// A payload that is valid JSON but is not an envelope object becomes
// structuredContent. Non-JSON UTF-8 becomes one text block; non-UTF-8 bytes
// become one base64 image block. An empty payload is an explicit empty result:
// content is an empty array and structuredContent is null.

const (
	// MaxPluginToolContentBlocks bounds one result's content array.
	MaxPluginToolContentBlocks = 128
	// MaxPluginToolErrorTextBytes bounds a guest error message before it is
	// shown to the client.
	MaxPluginToolErrorTextBytes = 2048
	// MaxPluginToolMIMETypeBytes bounds an image mime type.
	MaxPluginToolMIMETypeBytes = 128
)

// PluginCallResult is the host-normalized MCP result of one plugin tool call.
type PluginCallResult struct {
	Content []any
	// StructuredContent is meaningful when HasStructuredContent is true.
	StructuredContent    any
	HasStructuredContent bool
	IsError              bool
	// GuestCode and GuestMessage are sanitized envelope error fields.
	GuestCode    string
	GuestMessage string
	// ResultBytes is the bounded size of the normalized result.
	ResultBytes int
}

// NormalizePluginToolResult maps one guest payload to MCP content and
// structured content. It enforces the 16 MiB result bound, caps text blocks
// with an explicit truncation marker, validates base64 image blocks, and
// rejects malformed envelopes with a typed error. It never includes host
// paths or stack traces in an error message.
func NormalizePluginToolResult(payload []byte) (PluginCallResult, error) {
	result := PluginCallResult{Content: []any{}}
	if len(payload) == 0 {
		return result, nil
	}
	if len(payload) > MaxToolResultBytes {
		return PluginCallResult{}, &Error{
			Code:    CodePluginToolResultLimit,
			Message: fmt.Sprintf("Plugin tool result exceeds the %d byte limit.", MaxToolResultBytes),
		}
	}

	var value any
	if json.Valid(payload) {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return PluginCallResult{}, pluginInvalidResult(err)
		}
	} else if utf8.Valid(payload) {
		text := truncatePluginText(string(payload), MaxToolResultBytes)
		result.Content = append(result.Content, map[string]any{"type": "text", "text": text})
		result.ResultBytes = len(text)
		return result, nil
	} else {
		encoded := base64.StdEncoding.EncodeToString(payload)
		if len(encoded) > MaxToolResultBytes {
			return PluginCallResult{}, &Error{
				Code:    CodePluginToolResultLimit,
				Message: "Plugin tool binary result exceeds the base64 content limit.",
			}
		}
		result.Content = append(result.Content, map[string]any{
			"type": "image", "data": encoded, "mimeType": "application/octet-stream",
		})
		result.ResultBytes = len(encoded)
		return result, nil
	}

	object, isObject := value.(map[string]any)
	if !isObject || !looksLikePluginEnvelope(object) {
		result.StructuredContent = value
		result.HasStructuredContent = true
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > MaxToolResultBytes {
			return PluginCallResult{}, &Error{
				Code:    CodePluginToolResultLimit,
				Message: "Plugin tool structured content exceeds the result limit.",
			}
		}
		result.ResultBytes = len(encoded)
		return result, nil
	}

	used := 0
	if structured, present := object["structuredContent"]; present {
		encoded, err := json.Marshal(structured)
		if err != nil {
			return PluginCallResult{}, pluginInvalidResult(err)
		}
		if len(encoded) > MaxToolResultBytes {
			return PluginCallResult{}, &Error{
				Code:    CodePluginToolResultLimit,
				Message: "Plugin tool structured content exceeds the result limit.",
			}
		}
		result.StructuredContent = structured
		result.HasStructuredContent = true
		used += len(encoded)
	}

	if rawContent, present := object["content"]; present {
		blocks, ok := rawContent.([]any)
		if !ok {
			return PluginCallResult{}, pluginInvalidResult(fmt.Errorf("content must be an array"))
		}
		if len(blocks) > MaxPluginToolContentBlocks {
			return PluginCallResult{}, &Error{
				Code:    CodePluginToolResultLimit,
				Message: fmt.Sprintf("Plugin tool result has more than %d content blocks.", MaxPluginToolContentBlocks),
			}
		}
		for _, raw := range blocks {
			block, err := normalizePluginContentBlock(raw, &used)
			if err != nil {
				return PluginCallResult{}, err
			}
			result.Content = append(result.Content, block)
		}
	}
	if isError, ok := object["isError"].(bool); ok {
		result.IsError = isError
	}
	if code, ok := object["code"].(string); ok {
		result.GuestCode = sanitizePluginCode(code)
	}
	if message, ok := object["message"].(string); ok {
		result.GuestMessage = sanitizePluginText(message, MaxPluginToolErrorTextBytes)
	}
	result.ResultBytes = used
	return result, nil
}

// looksLikePluginEnvelope distinguishes the reserved envelope from an
// arbitrary JSON object a tool may return as structured content. Only keys
// that carry the envelope's distinctive shape count: a content key must
// already be an array, isError must be a boolean, and structuredContent is
// unambiguous. A bare string code is ordinary data, not a signal, so a tool
// returning {"code":"E42","value":1} keeps its payload.
func looksLikePluginEnvelope(object map[string]any) bool {
	if _, present := object["structuredContent"]; present {
		return true
	}
	if content, present := object["content"]; present {
		if _, ok := content.([]any); ok {
			return true
		}
	}
	if flag, present := object["isError"]; present {
		if _, ok := flag.(bool); ok {
			return true
		}
	}
	return false
}

func normalizePluginContentBlock(raw any, used *int) (map[string]any, error) {
	block, ok := raw.(map[string]any)
	if !ok {
		return nil, pluginInvalidResult(fmt.Errorf("a content block must be an object"))
	}
	kind, _ := block["type"].(string)
	switch kind {
	case "text":
		text, ok := block["text"].(string)
		if !ok {
			return nil, pluginInvalidResult(fmt.Errorf("a text block needs a string text"))
		}
		remaining := MaxToolResultBytes - *used
		if remaining < 0 {
			remaining = 0
		}
		text = truncatePluginText(text, remaining)
		*used += len(text)
		return map[string]any{"type": "text", "text": text}, nil
	case "image":
		data, ok := block["data"].(string)
		if !ok || data == "" {
			return nil, pluginInvalidResult(fmt.Errorf("an image block needs base64 data"))
		}
		if _, err := decodePluginBase64(data); err != nil {
			return nil, pluginInvalidResult(fmt.Errorf("an image block has invalid base64 data"))
		}
		if len(data) > MaxToolResultBytes-*used {
			return nil, &Error{
				Code:    CodePluginToolResultLimit,
				Message: "Plugin tool image content exceeds the result limit.",
			}
		}
		mimeType, _ := block["mimeType"].(string)
		mimeType = sanitizePluginText(mimeType, MaxPluginToolMIMETypeBytes)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		*used += len(data)
		return map[string]any{"type": "image", "data": data, "mimeType": mimeType}, nil
	default:
		return nil, pluginInvalidResult(fmt.Errorf("unsupported content block type %q", kind))
	}
}

func decodePluginBase64(data string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(data)
}

func pluginInvalidResult(err error) *Error {
	return &Error{
		Code:    CodePluginToolInvalidResult,
		Message: "Plugin returned an invalid tool result: " + err.Error(),
	}
}

// sanitizePluginCode keeps only a short lowercase token so a guest cannot
// smuggle display text or impersonate a host code.
func sanitizePluginCode(code string) string {
	var builder bytes.Buffer
	for _, character := range code {
		if builder.Len() >= 64 {
			break
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// sanitizePluginText strips hidden control characters except newline and tab,
// bounds the byte length, and keeps the result valid UTF-8. It shares the
// domain's MCP display-text rule so client-visible text has one policy.
func sanitizePluginText(text string, limit int) string {
	return domainplugin.SanitizeMCPDisplayText(text, limit)
}

const pluginTruncationMarker = "\n…[truncated]"

// truncatePluginText keeps at most limit bytes, backs up to a UTF-8 boundary,
// and appends the explicit truncation marker.
func truncatePluginText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	if limit <= 0 {
		return pluginTruncationMarker
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + pluginTruncationMarker
}

// MCPResult renders the normalized result as the MCP tools/call result body.
// Structured content is always present (null for an empty result) and content
// is always an array, so an empty result has an explicit representation.
func (r PluginCallResult) MCPResult() map[string]any {
	content := r.Content
	if content == nil {
		content = []any{}
	}
	result := map[string]any{
		"content":           content,
		"structuredContent": nil,
	}
	if r.HasStructuredContent {
		result["structuredContent"] = r.StructuredContent
	}
	if r.IsError {
		result["isError"] = true
	}
	return result
}
