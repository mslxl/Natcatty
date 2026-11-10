// Package mcp is the netcatty-mcp stdio process's MCP protocol layer. It owns
// the progressive tool surface and the JSON-RPC 2.0 protocol MCP clients speak;
// every tool call is forwarded to the running app through a Caller, which the
// command wires to the authenticated loopback transport. It imports no
// application package and never opens the Vault or database.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

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

	// maxQueuedRequests bounds the handoff between the stdin pump and the
	// sequential request loop. The pump never blocks on the handoff, so it can
	// always keep reading and observe EOF; once the bound is reached further
	// requests are answered with a JSON-RPC error instead of growing memory.
	maxQueuedRequests = 16

	// maxPendingCancellations bounds cancellation notifications remembered for
	// requests the request loop has not started yet. A client that sends
	// cancellations for ids it never used cannot grow the table without bound.
	maxPendingCancellations = 256
)

// JSON-RPC protocol error codes.
const (
	codeInvalidParams  = -32602
	codeMethodNotFound = -32601
	codeInternal       = -32603
	// codeRequestCancelled is the MCP/JSON-RPC error for a request the client
	// cancelled with notifications/cancelled.
	codeRequestCancelled = -32800
)

// Caller forwards one catalog RPC to the running app.
type Caller interface {
	Call(ctx context.Context, method string, params map[string]any) (any, error)
}

// Server is one stdio MCP session. Requests are handled in arrival order, but
// stdin is pumped concurrently so EOF (the client going away) is observed
// while a request is still running.
type Server struct {
	caller     Caller
	visibility *catalog.Visibility
	// clientID is the sanitized clientInfo from initialize. It is forwarded as
	// the reserved _mcpClient argument so the app can show which client is
	// asking for approval. It is only written and read by the request loop.
	clientID string

	// mu guards inflight and pendingCancels. The stdin pump cancels a request
	// while the request loop is handling it, so the tables are shared between
	// the two goroutines.
	mu       sync.Mutex
	inflight map[string]*inflightRequest
	// pendingCancels holds cancellations the pump read before the request
	// loop registered the request, which happens when the client writes a
	// request and its cancellation in one stdin write.
	pendingCancels map[string]struct{}
}

// NewServer starts a session with the original 14 core tools plus the loader.
func NewServer(caller Caller) *Server {
	return &Server{
		caller:         caller,
		visibility:     catalog.NewVisibility(),
		inflight:       make(map[string]*inflightRequest),
		pendingCancels: make(map[string]struct{}),
	}
}

// inflightRequest is one request the request loop is handling. cancelled is set
// by a notifications/cancelled message; the reply is then the JSON-RPC
// cancellation error rather than a possibly late result.
type inflightRequest struct {
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

// requestKey canonicalizes a JSON-RPC id so a cancellation notification's
// requestId (the same JSON value) matches the request being handled.
func requestKey(id json.RawMessage) string {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	return string(trimmed)
}

// Serve reads newline-delimited JSON-RPC from in until EOF and writes responses
// and notifications to out. A dedicated pump reads stdin, so EOF is observed
// while a request is still running: on EOF the whole session is cancelled and
// the caller's loopback transport is closed instead of waiting for the
// outstanding call. Requests themselves are still handled one at a time, so
// arrival order and the single discovery/loopback design are preserved.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	// If Serve returns before the pump reaches EOF (a stdout write failure or
	// parent cancellation), closing a closable stdin unblocks the pump's
	// pending ReadLine so the pump goroutine cannot leak.
	defer closeInput(in)

	output := &serializedWriter{writer: out}
	lines := make(chan []byte, maxQueuedRequests)
	readDone := make(chan error, 1)
	go s.pump(sessionCtx, cancelSession, in, lines, output, readDone)

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				// The pump closed the handoff after its terminal read. Any
				// request it queued before that has already been handled
				// above; EOF cancelled the session, so an in-flight call has
				// already unwound rather than delaying the disconnect.
				return readResult(<-readDone)
			}
			if err := s.handleLine(sessionCtx, line, output); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// readResult keeps the historical read-error contract: an oversized line is a
// protocol failure, while EOF and a broken pipe are an orderly disconnect.
func readResult(err error) error {
	if errors.Is(err, mcprpc.ErrLineTooLong) {
		return err
	}
	return nil
}

// closeInput unblocks a pump still inside ReadLine when Serve returns for a
// reason other than EOF. Readers that cannot be closed are already at EOF in
// the ordinary disconnect path.
func closeInput(in io.Reader) {
	if closer, ok := in.(io.Closer); ok {
		_ = closer.Close()
	}
}

// pump reads stdin until EOF or a terminal read error. Cancellation
// notifications are applied inline so they are not stuck behind a running
// request; every other notification has no response and is ignored, exactly as
// the request loop did before. Requests are only enqueued, never handed off
// synchronously, so a queued request can never stop the pump from reading the
// EOF that follows a client disconnect.
func (s *Server) pump(ctx context.Context, cancelSession context.CancelFunc,
	in io.Reader, lines chan<- []byte, out io.Writer, done chan<- error) {
	reader := bufio.NewReader(in)
	for {
		line, err := mcprpc.ReadLine(reader, MaxInputLineBytes)
		if len(line) > 0 && !s.handleNotification(line) {
			if enqueueErr := s.enqueueRequest(ctx, lines, out, line); enqueueErr != nil {
				// A full queue whose protocol error could not be written, or a
				// concurrently cancelled session, ends the pump just like a
				// terminal read error.
				err = enqueueErr
			}
		}
		if err != nil {
			// EOF (or a broken pipe) means the client is gone. Cancel every
			// in-flight request and close the loopback connection now: a
			// running tool call must not delay the disconnect. Closing the
			// handoff lets the request loop finish any already-queued work and
			// then return.
			cancelSession()
			s.closeCaller()
			close(lines)
			done <- err
			return
		}
	}
}

// enqueueRequest hands one request line to the request loop without ever
// blocking the pump. The queue has a hard bound; a request that finds it full
// is answered immediately with a JSON-RPC error rather than being dropped or
// growing memory while the pump keeps reading.
func (s *Server) enqueueRequest(ctx context.Context, lines chan<- []byte,
	out io.Writer, line []byte) error {
	select {
	case lines <- line:
		return nil
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return rejectRequest(out, line)
}

// rejectRequest answers a request the bounded queue could not accept. The
// request loop answers queued requests; this is the only path that writes from
// the pump, and it goes through the same serialized writer.
func rejectRequest(out io.Writer, line []byte) error {
	var message incoming
	if err := json.Unmarshal(line, &message); err != nil {
		return writeError(out, nil, codeInvalidParams, "Parse error")
	}
	if len(message.ID) == 0 || string(message.ID) == "null" {
		// handleNotification consumes every well-formed notification; this is
		// a defensive no-reply branch for a malformed one.
		return nil
	}
	return writeError(out, message.ID, codeInternal, "Server is busy: request queue is full")
}

// handleNotification consumes one client notification. Cancellation has an
// effect; everything else is fire-and-forget. It reports whether line was a
// notification and therefore must not reach the request loop.
func (s *Server) handleNotification(line []byte) bool {
	var message incoming
	if err := json.Unmarshal(line, &message); err != nil {
		return false
	}
	if len(message.ID) != 0 && string(message.ID) != "null" {
		return false
	}
	if message.Method == "notifications/cancelled" {
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(message.Params, &params) == nil {
			s.cancelRequest(params.RequestID)
		}
	}
	return true
}

// cancelRequest cancels the request whose id matches id. The pump can read a
// cancellation before the request loop has registered the request, notably
// when the client writes a request and its cancellation in one stdin write, so
// an unmatched cancellation is remembered for beginRequest instead of being
// dropped. The request loop then replies with the cancellation error.
func (s *Server) cancelRequest(id json.RawMessage) {
	key := requestKey(id)
	if key == "" {
		return
	}
	s.mu.Lock()
	request := s.inflight[key]
	if request == nil {
		if s.pendingCancels == nil {
			s.pendingCancels = make(map[string]struct{})
		}
		if len(s.pendingCancels) < maxPendingCancellations {
			s.pendingCancels[key] = struct{}{}
		}
		s.mu.Unlock()
		return
	}
	request.cancelled.Store(true)
	s.mu.Unlock()
	request.cancel()
}

// beginRequest gives one request its own context and registers it for
// cancellation notifications. A cancellation the pump observed before this
// point is applied now, so a request and its cancellation in one stdin write
// always cancel the call.
func (s *Server) beginRequest(ctx context.Context, id json.RawMessage) (context.Context, *inflightRequest) {
	requestCtx, cancel := context.WithCancel(ctx)
	request := &inflightRequest{cancel: cancel}
	key := requestKey(id)
	if key == "" {
		return requestCtx, request
	}
	s.mu.Lock()
	s.inflight[key] = request
	_, wasCancelled := s.pendingCancels[key]
	if wasCancelled {
		delete(s.pendingCancels, key)
		request.cancelled.Store(true)
	}
	s.mu.Unlock()
	if wasCancelled {
		cancel()
	}
	return requestCtx, request
}

// endRequest unregisters and releases one request context.
func (s *Server) endRequest(id json.RawMessage, request *inflightRequest) {
	if key := requestKey(id); key != "" {
		s.mu.Lock()
		if s.inflight[key] == request {
			delete(s.inflight, key)
		}
		s.mu.Unlock()
	}
	request.cancel()
}

// closeCaller closes the loopback transport when the caller owns one, so the
// app observes the disconnect immediately even while a call is unwinding.
func (s *Server) closeCaller() {
	if closer, ok := s.caller.(io.Closer); ok {
		_ = closer.Close()
	}
}

// serializedWriter keeps newline-delimited JSON-RPC messages intact when a
// notification handler and the request loop share one stdout.
type serializedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *serializedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
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
		// Notifications are consumed by the stdin pump; this is a defensive
		// no-reply branch for a line the pump forwarded.
		return nil
	}

	requestCtx, request := s.beginRequest(ctx, message.ID)
	defer s.endRequest(message.ID, request)
	reply := responder{out: out, id: message.ID, request: request}

	switch message.Method {
	case "initialize":
		result, rpcErr := s.initialize(message.Params)
		return reply.respond(result, rpcErr)
	case "ping":
		return reply.success(map[string]any{})
	case "tools/list":
		return reply.success(map[string]any{"tools": s.toolEntries(requestCtx)})
	case "tools/call":
		result, notification, rpcErr := s.callTool(requestCtx, message.Params)
		if notification != nil {
			if reply.cancelled() {
				return reply.failure(codeRequestCancelled, "Request cancelled")
			}
			if err := mcprpc.WriteMessage(out, notification); err != nil {
				return err
			}
		}
		return reply.respond(result, rpcErr)
	case "resources/list":
		return reply.success(map[string]any{
			"resources": []any{map[string]any{
				"uri":         ContextResourceURI,
				"name":        "environment",
				"description": "Current Netcatty workspace context: connected hosts, session IDs, and environment description.",
				"mimeType":    "application/json",
			}},
		})
	case "resources/read":
		return s.readResource(requestCtx, reply, message.Params)
	default:
		return reply.failure(codeMethodNotFound, "Method not found: "+message.Method)
	}
}

// responder writes one request's reply. A request the client cancelled gets the
// JSON-RPC cancellation error instead of a possibly late result.
type responder struct {
	out     io.Writer
	id      json.RawMessage
	request *inflightRequest
}

func (r responder) cancelled() bool {
	return r.request != nil && r.request.cancelled.Load()
}

func (r responder) success(result any) error {
	if r.cancelled() {
		return r.failure(codeRequestCancelled, "Request cancelled")
	}
	return writeSuccess(r.out, r.id, result)
}

func (r responder) failure(code int, message string) error {
	return writeError(r.out, r.id, code, message)
}

func (r responder) respond(result any, rpcErr *mcprpc.RPCError) error {
	if rpcErr != nil {
		return r.failure(rpcErr.Code, rpcErr.Message)
	}
	return r.success(result)
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

func (s *Server) toolEntries(ctx context.Context) []any {
	tools := make([]any, 0, s.visibility.VisibleCount()+1)
	loader := catalog.LoaderTool()
	tools = append(tools, toolEntry(loader))
	for _, tool := range s.visibility.Visible() {
		tools = append(tools, toolEntry(tool))
	}
	// Plugin tools are served by the app's single registry after its
	// two-gate check. An app failure removes them rather than failing the
	// built-in list, so a broken plugin never hides the core tools.
	tools = append(tools, s.pluginToolEntries(ctx)...)
	return tools
}

// pluginToolEntries asks the app for the currently authorized plugin tools.
// Only names inside the plugin namespace are accepted, and a duplicate name is
// dropped: the host never advertises a tool it did not receive from the one
// registry.
func (s *Server) pluginToolEntries(ctx context.Context) []any {
	params := map[string]any{}
	if s.clientID != "" {
		params[reservedClientArgument] = s.clientID
	}
	value, err := s.caller.Call(ctx, mcprpc.MethodPluginToolList, params)
	if err != nil {
		return nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if success, present := result["ok"].(bool); present && !success {
		return nil
	}
	raw, _ := result["tools"].([]any)
	entries := make([]any, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if !strings.HasPrefix(name, "plugin.") || seen[name] {
			continue
		}
		seen[name] = true
		entries = append(entries, entry)
	}
	return entries
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
		Meta      map[string]any `json:"_meta"`
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
	// A plugin-namespaced name always goes to the app, even when it is not in
	// the current list: the app answers with the typed unavailable/reloading
	// failure instead of the host inventing a tool.
	if strings.HasPrefix(body.Name, "plugin.") {
		return s.callPluginTool(ctx, body.Name, body.Arguments, body.Meta)
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

// callPluginTool forwards one plugin tool call to the app. The app owns the
// two-gate authorization, dispatch, result normalization, and audit; this
// layer only carries the client identity, an optional progress token, and the
// normalized MCP result.
func (s *Server) callPluginTool(ctx context.Context, name string,
	arguments map[string]any, meta map[string]any) (any, any, *mcprpc.RPCError) {
	params := map[string]any{
		"name":      name,
		"arguments": arguments,
	}
	if s.clientID != "" {
		params[reservedClientArgument] = s.clientID
	}
	if token, ok := meta["progressToken"]; ok && token != nil {
		params["_progressToken"] = token
	}
	value, err := s.caller.Call(ctx, mcprpc.MethodPluginToolCall, params)
	if err != nil {
		return toolError("Error: " + err.Error()), nil, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return toolError("Error: plugin tool returned no result"), nil, nil
	}
	if success, present := result["ok"].(bool); present && !success {
		return toolError("Error: " + rpcErrorText(result)), nil, nil
	}
	content, _ := result["content"].([]any)
	if content == nil {
		content = []any{}
	}
	response := map[string]any{"content": boundPluginContent(content)}
	if structured, present := result["structuredContent"]; present {
		response["structuredContent"] = structured
	}
	if isError, _ := result["isError"].(bool); isError {
		response["isError"] = true
	}
	return response, nil, nil
}

// boundPluginContent caps text blocks with the same marker the stdio layer
// uses for built-in results. Image data was already validated and bounded by
// the app.
func boundPluginContent(content []any) []any {
	out := make([]any, 0, len(content))
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "text" {
			if text, ok := block["text"].(string); ok {
				copied := make(map[string]any, len(block))
				for key, value := range block {
					copied[key] = value
				}
				copied["text"] = boundText(text)
				out = append(out, copied)
				continue
			}
		}
		out = append(out, block)
	}
	return out
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

func (s *Server) readResource(ctx context.Context, reply responder, params json.RawMessage) error {
	var body struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &body); err != nil || body.URI != ContextResourceURI {
		return reply.failure(codeInvalidParams, "Unknown resource")
	}
	value, err := s.caller.Call(ctx, "netcatty/getContext", map[string]any{
		reservedClientArgument: s.clientID,
	})
	if err != nil {
		return reply.failure(codeInternal, err.Error())
	}
	text, err := prettyJSON(value)
	if err != nil {
		return reply.failure(codeInternal, err.Error())
	}
	return reply.success(map[string]any{
		"contents": []any{map[string]any{
			"uri":      ContextResourceURI,
			"mimeType": "application/json",
			"text":     boundText(text),
		}},
	})
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
