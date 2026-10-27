// Package mcp is the netcatty-mcp stdio process's MCP protocol layer. It owns
// the progressive tool surface and the JSON-RPC 2.0 protocol MCP clients speak;
// every tool call is forwarded to the running app through a Caller, which the
// command wires to the authenticated loopback transport. It imports no
// application package and never opens the Vault or database.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

const (
	// ServerName and ServerVersion match the original MCP server identity.
	ServerName    = "netcatty-remote-hosts"
	ServerVersion = "1.0.0"

	// DefaultProtocolVersion is used when the client asks for a version this
	// server does not know.
	DefaultProtocolVersion = "2024-11-05"

	// MaxInputLineBytes bounds one inbound JSON-RPC line, matching the
	// original 10 MiB buffer cap.
	MaxInputLineBytes = 10 << 20
	// MaxToolResultBytes bounds one tool result's text. It sits above the
	// original 10 MiB in-memory SFTP read ceiling to leave room for JSON
	// escaping, so a read the original accepts is not cut by the transport.
	MaxToolResultBytes = 16 << 20

	// ContextResourceURI is the environment resource the original server
	// exposed beside the tools.
	ContextResourceURI = "netcatty://context"

	// reservedClientArgument carries the clientInfo identity to the app. It is
	// transport metadata, never a tool argument, and is excluded from approval
	// summaries.
	reservedClientArgument = "_mcpClient"
)

// JSON-RPC protocol error codes.
const (
	codeInvalidParams  = -32602
	codeMethodNotFound = -32601
	codeInternal       = -32603
)

// Caller forwards one catalog RPC to the running app.
type Caller interface {
	Call(ctx context.Context, method string, params map[string]any) (any, error)
}

// Server is one stdio MCP session. It is single-threaded: MCP stdio delivers
// requests in order and the app transport serializes calls.
type Server struct {
	caller     Caller
	visibility *catalog.Visibility
	// clientID is the sanitized clientInfo from initialize. It is forwarded as
	// the reserved _mcpClient argument so the app can show which client is
	// asking for approval.
	clientID string
}

// NewServer starts a session with the original 14 core tools plus the loader.
func NewServer(caller Caller) *Server {
	return &Server{caller: caller, visibility: catalog.NewVisibility()}
}

// Serve reads newline-delimited JSON-RPC from in until EOF and writes responses
// and notifications to out.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	for {
		line, err := mcprpc.ReadLine(reader, MaxInputLineBytes)
		if len(line) > 0 {
			if serveErr := s.handleLine(ctx, line, out); serveErr != nil {
				return serveErr
			}
		}
		if err != nil {
			if errors.Is(err, mcprpc.ErrLineTooLong) {
				return err
			}
			return nil
		}
	}
}

type incoming struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type successResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errorResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Error   *mcprpc.RPCError `json:"error"`
}

func (s *Server) handleLine(ctx context.Context, line []byte, out io.Writer) error {
	var message incoming
	if err := json.Unmarshal(line, &message); err != nil {
		return writeError(out, nil, codeInvalidParams, "Parse error")
	}
	if len(message.ID) == 0 || string(message.ID) == "null" {
		// A notification is fire-and-forget; MCP's initialized and cancelled
		// notifications need no reply.
		return nil
	}

	switch message.Method {
	case "initialize":
		result, rpcErr := s.initialize(message.Params)
		return s.respond(out, message.ID, result, rpcErr)
	case "ping":
		return writeSuccess(out, message.ID, map[string]any{})
	case "tools/list":
		return writeSuccess(out, message.ID, map[string]any{"tools": s.toolEntries()})
	case "tools/call":
		result, notification, rpcErr := s.callTool(ctx, message.Params)
		if notification != nil {
			if err := mcprpc.WriteMessage(out, notification); err != nil {
				return err
			}
		}
		return s.respond(out, message.ID, result, rpcErr)
	case "resources/list":
		return writeSuccess(out, message.ID, map[string]any{
			"resources": []any{map[string]any{
				"uri":         ContextResourceURI,
				"name":        "environment",
				"description": "Current Netcatty workspace context: connected hosts, session IDs, and environment description.",
				"mimeType":    "application/json",
			}},
		})
	case "resources/read":
		return s.readResource(ctx, message.ID, message.Params, out)
	default:
		return writeError(out, message.ID, codeMethodNotFound, "Method not found: "+message.Method)
	}
}

func (s *Server) initialize(params json.RawMessage) (any, *mcprpc.RPCError) {
	var body struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &body)
	s.clientID = clientLabel(body.ClientInfo.Name, body.ClientInfo.Version)
	version := supportedProtocolVersion(body.ProtocolVersion)
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": true},
			"resources": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": ServerVersion,
		},
		"instructions": Instructions,
	}, nil
}

// supportedProtocolVersion echoes a version this server implements and falls
// back to the original-era default otherwise.
func supportedProtocolVersion(requested string) string {
	switch requested {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return requested
	default:
		return DefaultProtocolVersion
	}
}

func (s *Server) toolEntries() []any {
	tools := make([]any, 0, s.visibility.VisibleCount())
	loader := catalog.LoaderTool()
	tools = append(tools, toolEntry(loader))
	for _, tool := range s.visibility.Visible() {
		tools = append(tools, toolEntry(tool))
	}
	return tools
}

func toolEntry(tool catalog.Tool) map[string]any {
	return map[string]any{
		"name":        tool.Name,
		"description": tool.Description,
		"inputSchema": tool.InputSchema(),
	}
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, any, *mcprpc.RPCError) {
	var body struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return nil, nil, &mcprpc.RPCError{Code: codeInvalidParams, Message: "tools/call needs a name"}
	}
	if strings.TrimSpace(body.Name) == "" {
		return nil, nil, &mcprpc.RPCError{Code: codeInvalidParams, Message: "tools/call needs a name"}
	}
	if body.Arguments == nil {
		body.Arguments = map[string]any{}
	}
	if s.clientID != "" {
		body.Arguments[reservedClientArgument] = s.clientID
	}
	if body.Name == catalog.LoaderName {
		return s.loadTools(body.Arguments)
	}

	tool, known := catalog.ByName(body.Name)
	if !known || !s.visibility.IsVisible(body.Name) {
		return nil, nil, &mcprpc.RPCError{
			Code:    codeInvalidParams,
			Message: "Unknown tool: " + body.Name,
		}
	}

	result, err := s.caller.Call(ctx, tool.RPCMethod, body.Arguments)
	if err != nil {
		return toolError("Error: " + err.Error()), nil, nil
	}
	text, isError := formatToolResult(result)
	response := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	}
	if isError {
		response["isError"] = true
	}
	return response, nil, nil
}

// loadTools runs the transport-control loader. It returns the tool result, an
// optional list_changed notification, and a protocol error.
func (s *Server) loadTools(arguments map[string]any) (any, any, *mcprpc.RPCError) {
	toolset, _ := arguments["toolset"].(string)
	if strings.TrimSpace(toolset) == "" {
		return nil, nil, &mcprpc.RPCError{Code: codeInvalidParams, Message: "toolset is required"}
	}
	newly, already, err := s.visibility.Load(toolset)
	if err != nil {
		return nil, nil, &mcprpc.RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if newly == nil {
		newly = []string{}
	}
	if already == nil {
		already = []string{}
	}
	payload := map[string]any{
		"toolset":                   toolset,
		"newlyLoaded":               newly,
		"alreadyLoaded":             already,
		"availableToolsets":         catalog.LoadableToolsetNames,
		"loadedCatalogToolCount":    s.visibility.LoadedCatalogCount(),
		"visibleToolCount":          s.visibility.VisibleCount(),
		"remainingCatalogToolCount": s.visibility.RemainingCatalogCount(),
	}
	text, err := prettyJSON(payload)
	if err != nil {
		return nil, nil, &mcprpc.RPCError{Code: codeInternal, Message: err.Error()}
	}
	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": boundText(text)}},
	}
	var notification any
	if len(newly) > 0 {
		notification = map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/tools/list_changed",
		}
	}
	return result, notification, nil
}

func (s *Server) readResource(ctx context.Context, id json.RawMessage, params json.RawMessage, out io.Writer) error {
	var body struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &body); err != nil || body.URI != ContextResourceURI {
		return writeError(out, id, codeInvalidParams, "Unknown resource")
	}
	value, err := s.caller.Call(ctx, "netcatty/getContext", map[string]any{
		reservedClientArgument: s.clientID,
	})
	if err != nil {
		return writeError(out, id, codeInternal, err.Error())
	}
	text, err := prettyJSON(value)
	if err != nil {
		return writeError(out, id, codeInternal, err.Error())
	}
	return writeSuccess(out, id, map[string]any{
		"contents": []any{map[string]any{
			"uri":      ContextResourceURI,
			"mimeType": "application/json",
			"text":     boundText(text),
		}},
	})
}

func (s *Server) respond(out io.Writer, id json.RawMessage, result any, rpcErr *mcprpc.RPCError) error {
	if rpcErr != nil {
		return writeError(out, id, rpcErr.Code, rpcErr.Message)
	}
	return writeSuccess(out, id, result)
}

func writeSuccess(out io.Writer, id json.RawMessage, result any) error {
	return mcprpc.WriteMessage(out, successResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(out io.Writer, id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return mcprpc.WriteMessage(out, errorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcprpc.RPCError{Code: code, Message: message},
	})
}

// formatToolResult mirrors the original generic MCP handler: an ok:false
// result is a tool error with only the error text; everything else is
// pretty-printed JSON.
func formatToolResult(value any) (string, bool) {
	if result, ok := value.(map[string]any); ok {
		if success, present := result["ok"].(bool); present && !success {
			return "Error: " + rpcErrorText(result), true
		}
	}
	text, err := prettyJSON(value)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	return boundText(text), false
}

func rpcErrorText(result map[string]any) string {
	message := ""
	if text, ok := result["error"].(string); ok && text != "" {
		message = text
	}
	if message == "" {
		if code, ok := result["code"]; ok {
			message = fmt.Sprintf("Operation failed (%v)", code)
		} else {
			message = "Operation failed"
		}
	}
	// A typed policy denial carries a stable protocol code. Keep the message
	// first so clients that only display text remain readable, and append the
	// code so an automated client and the end-to-end tests can branch on it.
	if code, ok := result["code"].(string); ok && code != "" {
		return message + " (code: " + code + ")"
	}
	return message
}

// clientLabel sanitizes the MCP clientInfo identity. The app shows it in an
// approval prompt, so control characters and an unbounded name never cross the
// process boundary.
func clientLabel(name, version string) string {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" {
		return ""
	}
	label := name
	if version != "" {
		label += "/" + version
	}
	var builder strings.Builder
	for _, character := range label {
		if builder.Len() >= 120 {
			break
		}
		if character < 0x20 || character == 0x7f {
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func toolError(text string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": boundText(text)}},
		"isError": true,
	}
}

func prettyJSON(value any) (string, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("external MCP: encode result: %w", err)
	}
	return string(encoded), nil
}

const truncationMarker = "\n…[truncated]"

// boundText caps a tool result so one runaway read cannot flood the client.
func boundText(text string) string {
	if len(text) <= MaxToolResultBytes {
		return text
	}
	cut := MaxToolResultBytes
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut] + truncationMarker
}

// utf8Start reports whether b begins a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
