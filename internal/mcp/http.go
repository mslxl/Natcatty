// Streamable HTTP transport for the external MCP server.
//
// The transport is opt-in: Apply opens a listener only when HTTPConfig.Enabled
// is true, so a build or profile that never enables it opens no HTTP socket.
// Once enabled it serves one endpoint at HTTPPath, requires the same bearer
// token the loopback JSON-RPC transport publishes in the discovery file, and
// validates Host and Origin so a browser page from another origin cannot drive
// the app. The MCP protocol itself is the same Server the stdio process runs;
// each HTTP session gets its own instance so client identity and the
// progressive tool surface stay per session.
package mcp

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

const (
	// HTTPPath is the single endpoint a Streamable HTTP MCP client uses.
	HTTPPath = "/mcp"
	// DefaultHTTPPort matches the original External MCP HTTP default.
	DefaultHTTPPort = 47391
	// DefaultHTTPListenAddress keeps the listener on loopback unless the user
	// explicitly chooses a different bind address.
	DefaultHTTPListenAddress = "127.0.0.1"
	// DefaultHTTPSessionIdle bounds an abandoned session. JSON responses do
	// not keep a socket open, so a client that never sends DELETE would
	// otherwise pin a session until the listener stops.
	DefaultHTTPSessionIdle = 15 * time.Minute
	// maxHTTPBodyBytes bounds one POST body, matching the original 4 MiB cap.
	maxHTTPBodyBytes = 4 << 20
)

// HTTPConfig is the Streamable HTTP endpoint configuration. An enabled config
// needs a token; a provider is re-read for every request so a rotated token
// takes effect without rebinding.
type HTTPConfig struct {
	Enabled       bool
	ListenAddress string
	// Port 0 asks the operating system for an ephemeral port. The app passes
	// the configured default; tests use 0.
	Port int
	// Token is the static bearer token. TokenProvider, when set, wins so the
	// owning process can expose its live discovery token.
	Token         string
	TokenProvider func() string
	// ClientHost names the address clients reach when ListenAddress is a
	// wildcard bind. A wildcard bind without it has no usable client URL.
	ClientHost string
	// SessionIdleTimeout overrides DefaultHTTPSessionIdle.
	SessionIdleTimeout time.Duration
	// Logf receives the non-loopback bind warning and non-fatal diagnostics.
	// It may be nil.
	Logf func(format string, args ...any)
}

func (c HTTPConfig) listenAddress() string {
	address := strings.TrimSpace(c.ListenAddress)
	if address == "" {
		return DefaultHTTPListenAddress
	}
	return address
}

func (c HTTPConfig) idleTimeout() time.Duration {
	if c.SessionIdleTimeout > 0 {
		return c.SessionIdleTimeout
	}
	return DefaultHTTPSessionIdle
}

func (c HTTPConfig) currentToken() string {
	if c.TokenProvider != nil {
		return c.TokenProvider()
	}
	return c.Token
}

func (c HTTPConfig) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// HTTPState is the observable listener state. A disabled transport has no
// listener and no URL.
type HTTPState struct {
	Enabled       bool
	Listening     bool
	ListenAddress string
	Port          int
	URL           string
}

// HTTPTransport owns the optional HTTP listener. Apply is idempotent for an
// unchanged bind, so the app can call it from the preferences change hook.
type HTTPTransport struct {
	mu       sync.Mutex
	applied  HTTPConfig
	hasApply bool

	handler  *HTTPHandler
	server   *http.Server
	listener net.Listener
	done     chan struct{}
	state    HTTPState
}

// NewHTTPTransport builds a disabled transport.
func NewHTTPTransport() *HTTPTransport {
	return &HTTPTransport{}
}

// Apply starts, reconfigures, or stops the listener to match config. Disabled
// always stops it and opens nothing. An enabled config without a token is an
// error: the endpoint must never be reachable anonymously.
func (t *HTTPTransport) Apply(config HTTPConfig, caller Caller) (HTTPState, error) {
	if t == nil {
		return HTTPState{}, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if !config.Enabled {
		t.stopLocked()
		t.applied = config
		t.hasApply = true
		return t.state, nil
	}
	if caller == nil {
		return t.state, errors.New("external MCP HTTP: no request handler is configured")
	}
	if strings.TrimSpace(config.currentToken()) == "" {
		return t.state, errors.New("external MCP HTTP: a bearer token is required")
	}
	config.ListenAddress = config.listenAddress()
	if config.Port < 0 || config.Port > 65535 {
		return t.state, fmt.Errorf("external MCP HTTP: port %d is out of range", config.Port)
	}

	// A live listener with the same bind only needs the new token provider,
	// client address, and idle timeout.
	if t.listener != nil && t.hasApply && t.applied.ListenAddress == config.ListenAddress &&
		t.applied.Port == config.Port {
		effective := config
		effective.ListenAddress = t.state.ListenAddress
		effective.Port = t.state.Port
		t.handler.configure(effective)
		t.applied = config
		t.state.URL = clientURL(t.state.ListenAddress, t.state.Port, config.ClientHost)
		return t.state, nil
	}

	t.stopLocked()
	listener, err := net.Listen("tcp", net.JoinHostPort(config.ListenAddress, strconv.Itoa(config.Port)))
	if err != nil {
		t.applied = config
		t.hasApply = true
		t.state = HTTPState{Enabled: true}
		return t.state, fmt.Errorf("external MCP HTTP: listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	handler := newHTTPHandler(caller, config)
	handler.setListen(config.ListenAddress, port)
	server := &http.Server{
		Handler: handler,
		// A tool call may legitimately take minutes; only the header read is
		// bounded. No ReadTimeout/WriteTimeout is set on purpose.
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()

	t.handler = handler
	t.server = server
	t.listener = listener
	t.done = done
	t.applied = config
	t.hasApply = true
	t.state = HTTPState{
		Enabled:       true,
		Listening:     true,
		ListenAddress: config.ListenAddress,
		Port:          port,
		URL:           clientURL(config.ListenAddress, port, config.ClientHost),
	}
	if !isLoopbackHost(config.ListenAddress) {
		config.logf("natcatty: external MCP Streamable HTTP is bound to non-loopback address %s; "+
			"anyone who obtains the bearer token can control exposed Sessions", config.ListenAddress)
	}
	return t.state, nil
}

// Stop closes the listener and every live request. It is safe to call more
// than once and safe on a nil transport.
func (t *HTTPTransport) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
	t.state = HTTPState{}
}

// State reports the current listener state.
func (t *HTTPTransport) State() HTTPState {
	if t == nil {
		return HTTPState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// stopLocked closes the listener and its connections and waits briefly for
// Serve to return.
func (t *HTTPTransport) stopLocked() {
	server := t.server
	listener := t.listener
	done := t.done
	t.server = nil
	t.listener = nil
	t.done = nil
	t.handler = nil
	if listener != nil {
		_ = listener.Close()
	}
	if server != nil {
		// Close destroys live connections, so a long tool call cannot pin the
		// app's shutdown the way a graceful drain would.
		_ = server.Close()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	t.state = HTTPState{}
}

// HTTPHandler serves the Streamable HTTP endpoint. It is exported so a caller
// that already owns an HTTP server can mount the endpoint directly.
type HTTPHandler struct {
	mu       sync.Mutex
	caller   Caller
	config   HTTPConfig
	sessions map[string]*httpSession
	now      func() time.Time
}

type httpSession struct {
	id           string
	server       *Server
	lastActivity time.Time
	inFlight     int
	mu           sync.Mutex
}

func newHTTPHandler(caller Caller, config HTTPConfig) *HTTPHandler {
	return &HTTPHandler{
		caller:   caller,
		config:   config,
		sessions: map[string]*httpSession{},
		now:      time.Now,
	}
}

// configure updates the values a live listener reads per request.
func (h *HTTPHandler) configure(config HTTPConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.config = config
}

// setListen records the actual bound endpoint for Host validation and URLs.
func (h *HTTPHandler) setListen(address string, port int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.config.ListenAddress = address
	h.config.Port = port
}

// ServeHTTP implements http.Handler.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != HTTPPath {
		writeHTTPRPCError(w, http.StatusNotFound, mcprpc.CodeDispatchFailed, "Not found", nil)
		return
	}
	if !h.validHost(r.Host) {
		writeHTTPRPCError(w, http.StatusMisdirectedRequest, mcprpc.CodeDispatchFailed, "Invalid Host header", nil)
		return
	}
	if !h.validOrigin(r.Header.Get("Origin")) {
		writeHTTPRPCError(w, http.StatusForbidden, mcprpc.CodeDispatchFailed, "Invalid Origin header", nil)
		return
	}
	presented := bearerToken(r.Header.Get("Authorization"))
	if !h.tokenMatches(presented) {
		writeHTTPRPCError(w, http.StatusUnauthorized, mcprpc.CodeAuthRequired, "Unauthorized", nil)
		return
	}
	h.expireIdleSessions()

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r, presented)
	case http.MethodDelete:
		h.handleDelete(w, r)
	case http.MethodGet, http.MethodHead:
		// This server answers every request inline; it does not offer a
		// server-initiated SSE stream. Streamable HTTP allows 405 here.
		writeHTTPRPCError(w, http.StatusMethodNotAllowed, mcprpc.CodeDispatchFailed,
			"Method not allowed", map[string]string{"Allow": "POST, DELETE"})
	default:
		writeHTTPRPCError(w, http.StatusMethodNotAllowed, mcprpc.CodeDispatchFailed,
			"Method not allowed", map[string]string{"Allow": "POST, DELETE"})
	}
}

func (h *HTTPHandler) handlePost(w http.ResponseWriter, r *http.Request, presented string) {
	mediaType := requestMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeHTTPRPCError(w, http.StatusUnsupportedMediaType, mcprpc.CodeDispatchFailed,
			"Content-Type must be application/json", nil)
		return
	}
	jsonOK, sseOK := acceptedContentTypes(r.Header.Get("Accept"))
	if !jsonOK && !sseOK {
		writeHTTPRPCError(w, http.StatusNotAcceptable, mcprpc.CodeDispatchFailed,
			"Accept must allow application/json or text/event-stream", nil)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHTTPBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeHTTPRPCError(w, http.StatusRequestEntityTooLarge, mcprpc.CodeDispatchFailed,
				"Payload too large", nil)
			return
		}
		writeHTTPRPCError(w, http.StatusBadRequest, mcprpc.CodeDispatchFailed,
			"Invalid request body", nil)
		return
	}
	// A half-open POST passed the header check before its body arrived. Re-read
	// the live token so a rotation during that wait cannot still dispatch.
	if !h.tokenMatches(presented) {
		writeHTTPRPCError(w, http.StatusUnauthorized, mcprpc.CodeAuthRequired, "Unauthorized", nil)
		return
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		writeHTTPRPCError(w, http.StatusBadRequest, codeInvalidParams, "Invalid JSON", nil)
		return
	}
	if bytes.HasPrefix(trimmed, []byte("[")) {
		writeHTTPRPCError(w, http.StatusBadRequest, mcprpc.CodeDispatchFailed,
			"Batch requests are not supported", nil)
		return
	}
	var message incoming
	if err := json.Unmarshal(trimmed, &message); err != nil {
		writeHTTPRPCError(w, http.StatusBadRequest, codeInvalidParams, "Invalid JSON", nil)
		return
	}

	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID != "" {
		session := h.session(sessionID)
		if session == nil {
			writeHTTPRPCError(w, http.StatusNotFound, mcprpc.CodeDispatchFailed, "Session not found", nil)
			return
		}
		h.serveMessage(w, r, session, body, message, jsonOK)
		return
	}
	if message.Method != "initialize" || isNotification(message) {
		writeHTTPRPCError(w, http.StatusBadRequest, mcprpc.CodeDispatchFailed,
			"Bad Request: missing session", nil)
		return
	}
	session, err := h.openSession()
	if err != nil {
		writeHTTPRPCError(w, http.StatusInternalServerError, mcprpc.CodeDispatchFailed,
			err.Error(), nil)
		return
	}
	w.Header().Set("Mcp-Session-Id", session.id)
	h.serveMessage(w, r, session, body, message, jsonOK)
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		writeHTTPRPCError(w, http.StatusBadRequest, mcprpc.CodeDispatchFailed,
			"Bad Request: missing session", nil)
		return
	}
	h.mu.Lock()
	_, known := h.sessions[sessionID]
	if known {
		delete(h.sessions, sessionID)
	}
	h.mu.Unlock()
	if !known {
		writeHTTPRPCError(w, http.StatusNotFound, mcprpc.CodeDispatchFailed, "Session not found", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveMessage runs one message in its session and writes the protocol reply.
func (h *HTTPHandler) serveMessage(w http.ResponseWriter, r *http.Request, session *httpSession,
	body []byte, message incoming, jsonOK bool) {
	h.mu.Lock()
	session.inFlight++
	session.lastActivity = h.now()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		session.inFlight--
		session.lastActivity = h.now()
		h.mu.Unlock()
	}()

	// The stdio Server is single-threaded by contract; serializing one HTTP
	// session keeps the progressive tool surface and client identity stable.
	session.mu.Lock()
	var output bytes.Buffer
	_ = session.server.handleLine(r.Context(), body, &output)
	session.mu.Unlock()

	if isNotification(message) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	responses := splitMessages(output.Bytes())
	if len(responses) == 0 {
		writeHTTPRPCError(w, http.StatusInternalServerError, mcprpc.CodeDispatchFailed,
			"the MCP session produced no response", nil)
		return
	}
	if jsonOK {
		// A tools/call may emit notifications/tools/list_changed before its
		// result. The result is the last message; a JSON response carries it
		// alone, while the SSE branch delivers the notification too.
		writeHTTPJSON(w, http.StatusOK, responses[len(responses)-1])
		return
	}
	writeHTTPStream(w, responses)
}

func (h *HTTPHandler) openSession() (*httpSession, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return nil, fmt.Errorf("external MCP HTTP: create a session id: %w", err)
	}
	session := &httpSession{
		id:           hex.EncodeToString(identifier),
		server:       NewServer(h.caller),
		lastActivity: h.now(),
	}
	h.mu.Lock()
	h.sessions[session.id] = session
	h.mu.Unlock()
	return session, nil
}

func (h *HTTPHandler) session(id string) *httpSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

// expireIdleSessions drops sessions that have been idle past the timeout. A
// session with a request in flight is never expired.
func (h *HTTPHandler) expireIdleSessions() {
	h.mu.Lock()
	defer h.mu.Unlock()
	idle := h.config.idleTimeout()
	now := h.now()
	for id, session := range h.sessions {
		if session.inFlight > 0 {
			continue
		}
		if now.Sub(session.lastActivity) >= idle {
			delete(h.sessions, id)
		}
	}
}

func (h *HTTPHandler) tokenMatches(presented string) bool {
	h.mu.Lock()
	expected := h.config.currentToken()
	h.mu.Unlock()
	if strings.TrimSpace(expected) == "" {
		// Fail closed: an endpoint without a token must not be usable.
		return false
	}
	presentedHash := sha256.Sum256([]byte(presented))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(presentedHash[:], expectedHash[:]) == 1
}

// validHost requires the Host header to name the bound port and an allowed
// hostname, which defeats DNS-rebinding requests.
func (h *HTTPHandler) validHost(raw string) bool {
	h.mu.Lock()
	listenAddress := h.config.ListenAddress
	port := h.config.Port
	h.mu.Unlock()
	hostname, rawPort, ok := splitHTTPHost(raw)
	if !ok || rawPort == "" || rawPort != strconv.Itoa(port) {
		return false
	}
	return allowedHostname(hostname, listenAddress)
}

// validOrigin rejects a cross-origin browser request. A request without an
// Origin header is a native MCP client and is allowed.
func (h *HTTPHandler) validOrigin(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	h.mu.Lock()
	listenAddress := h.config.ListenAddress
	h.mu.Unlock()
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	return allowedHostname(parsed.Hostname(), listenAddress)
}

func allowedHostname(hostname, listenAddress string) bool {
	host := strings.ToLower(unbracketHost(hostname))
	if host == "" {
		return false
	}
	if isLoopbackHost(host) {
		return true
	}
	if isWildcardHost(listenAddress) {
		return net.ParseIP(host) != nil
	}
	return host == strings.ToLower(unbracketHost(listenAddress))
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(unbracketHost(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func isWildcardHost(host string) bool {
	switch unbracketHost(host) {
	case "0.0.0.0", "::":
		return true
	default:
		return false
	}
}

func unbracketHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) > 2 {
		return host[1 : len(host)-1]
	}
	return host
}

// splitHTTPHost parses a Host header into hostname and optional port.
func splitHTTPHost(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if strings.HasPrefix(raw, "[") {
		end := strings.Index(raw, "]")
		if end <= 1 {
			return "", "", false
		}
		hostname := raw[1:end]
		rest := raw[end+1:]
		if rest == "" {
			return hostname, "", true
		}
		if !strings.HasPrefix(rest, ":") || !digitsOnly(rest[1:]) {
			return "", "", false
		}
		return hostname, rest[1:], true
	}
	if index := strings.LastIndex(raw, ":"); index >= 0 {
		if !digitsOnly(raw[index+1:]) {
			return "", "", false
		}
		return raw[:index], raw[index+1:], true
	}
	return raw, "", true
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func requestMediaType(header string) string {
	value := strings.TrimSpace(strings.Split(header, ";")[0])
	return strings.ToLower(value)
}

// acceptedContentTypes reports which response encodings the client accepts.
func acceptedContentTypes(header string) (jsonOK, sseOK bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return true, false
	}
	for _, part := range strings.Split(header, ",") {
		mediaType := requestMediaType(part)
		switch mediaType {
		case "*/*":
			return true, true
		case "application/json":
			jsonOK = true
		case "text/event-stream":
			sseOK = true
		}
	}
	return jsonOK, sseOK
}

func isNotification(message incoming) bool {
	return len(message.ID) == 0 || string(message.ID) == "null"
}

// splitMessages splits the newline-delimited JSON the protocol layer wrote.
// JSON encoding escapes newlines, so a line boundary is a message boundary.
func splitMessages(output []byte) [][]byte {
	lines := bytes.Split(output, []byte{'\n'})
	messages := make([][]byte, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			messages = append(messages, line)
		}
	}
	return messages
}

func writeHTTPJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte{'\n'})
}

// writeHTTPStream answers with one SSE event per protocol message. The stream
// closes after the response, which a Streamable HTTP client accepts for a POST.
func writeHTTPStream(w http.ResponseWriter, responses [][]byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	for _, response := range responses {
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", response)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeHTTPRPCError(w http.ResponseWriter, status, code int, message string, headers map[string]string) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error":   &mcprpc.RPCError{Code: code, Message: message},
	})
	if err != nil {
		body = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"Internal error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	for name, value := range headers {
		w.Header().Set(name, value)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte{'\n'})
}

// clientURL is the endpoint clients use. A wildcard bind has no reachable
// address of its own, so it yields a URL only when ClientHost names one.
func clientURL(listenAddress string, port int, clientHost string) string {
	host := listenAddress
	if isWildcardHost(host) {
		host = strings.TrimSpace(clientHost)
	}
	if host == "" || isWildcardHost(host) {
		return ""
	}
	host = unbracketHost(host)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + strconv.Itoa(port) + HTTPPath
}

var _ http.Handler = (*HTTPHandler)(nil)
