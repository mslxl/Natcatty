package mcprpc

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// notificationWriteTimeout bounds one push. A client that stops reading must
// not stall a plugin lifecycle change or a tool call waiting on progress.
const notificationWriteTimeout = time.Second

// Handler answers one authenticated request. The returned value is encoded as
// the JSON-RPC result; an error becomes -32000. The handler's context carries
// the connection lifetime: when the client disconnects, the context is
// cancelled and any in-flight work must stop.
type Handler interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Handle implements Handler.
func (fn HandlerFunc) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return fn(ctx, method, params)
}

// Notifier pushes one server-to-client notification on the connection that
// carried a request. Progress forwarding and tools/list_changed use it; the
// stdio process relays what it receives to its MCP client.
type Notifier interface {
	Notify(method string, params any) error
}

type notifierKey struct{}

// NotifierFrom returns the connection notifier attached to ctx. A request
// served outside a connection (a direct handler test) has none.
func NotifierFrom(ctx context.Context) (Notifier, bool) {
	notifier, ok := ctx.Value(notifierKey{}).(Notifier)
	return notifier, ok
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Server is the app-side authenticated loopback listener.
type Server struct {
	token   string
	handler Handler

	// MaxLineBytes overrides the inbound buffer cap; tests use a small value.
	MaxLineBytes int
	// Logf receives non-fatal diagnostics such as malformed lines. It may be
	// nil.
	Logf func(format string, args ...any)

	mu    sync.Mutex
	conns map[*serverConn]struct{}
}

// NewServer builds a server that accepts the dedicated external MCP token.
func NewServer(token string, handler Handler) *Server {
	return &Server{token: token, handler: handler, conns: make(map[*serverConn]struct{})}
}

// serverConn is one authenticated socket with serialized writes, so a push
// notification can never interleave with a response body.
type serverConn struct {
	conn    net.Conn
	writeMu sync.Mutex
}

func (c *serverConn) write(message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeMessage(c.conn, message)
}

// Notify implements Notifier. The write is bounded so a stalled client cannot
// hold up a lifecycle notification burst or a guest's tool call.
func (c *serverConn) Notify(method string, params any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(notificationWriteTimeout))
	defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()
	return writeMessage(c.conn, notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) register(conn *serverConn) {
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) unregister(conn *serverConn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// Broadcast pushes one notification to every live connection. Failed writes
// are ignored: the read side will notice a dead connection and cancel its
// context.
func (s *Server) Broadcast(method string, params any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	conns := make([]*serverConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Notify(method, params)
	}
}

// Serve accepts connections until the context is cancelled or the listener
// fails. Each connection is served on its own goroutine.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("external MCP: accept: %w", err)
		}
		go s.ServeConn(ctx, conn)
	}
}

// ServeConn authenticates and serves one connection. Requests after auth run
// concurrently so the read loop keeps observing the socket; a disconnect
// cancels the connection context, which cancels every in-flight request.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	connCtx, cancel := context.WithCancel(ctx)
	connection := &serverConn{conn: conn}
	s.register(connection)
	var pending sync.WaitGroup
	defer func() {
		cancel()
		pending.Wait()
		s.unregister(connection)
		_ = conn.Close()
	}()

	reader := bufio.NewReader(conn)
	authenticated := false

	for {
		if connCtx.Err() != nil {
			return
		}
		line, readErr := readLine(reader, s.MaxLineBytes)
		if len(line) > 0 {
			var message request
			if err := json.Unmarshal(line, &message); err != nil {
				// The original bridge ignored malformed lines rather than
				// killing the connection.
				s.logf("ignoring a malformed JSON-RPC line")
			} else if len(message.ID) > 0 && string(message.ID) != "null" && message.Method != "" {
				if !authenticated {
					closeConnection, writeErr := s.authenticate(connCtx, connection, message, &authenticated)
					if writeErr != nil {
						return
					}
					if closeConnection {
						return
					}
				} else {
					pending.Add(1)
					go func(message request) {
						defer pending.Done()
						s.serveRequest(connCtx, connection, message)
					}(message)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, ErrLineTooLong) {
				s.logf("dropping a connection whose message exceeded the buffer cap")
			}
			return
		}
	}
}

// authenticate handles the mandatory first message. It reports whether the
// connection must close.
func (s *Server) authenticate(ctx context.Context, conn *serverConn,
	message request, authenticated *bool) (bool, error) {
	if message.Method == "auth/verify" && s.validToken(message.Params) {
		*authenticated = true
		return false, conn.write(response{
			JSONRPC: "2.0",
			ID:      message.ID,
			Result:  map[string]any{"ok": true},
		})
	}
	writeErr := conn.write(response{
		JSONRPC: "2.0",
		ID:      message.ID,
		Error: &RPCError{
			Code:    CodeAuthRequired,
			Message: "Authentication required. Send auth/verify with valid token first.",
		},
	})
	if writeErr != nil {
		return true, writeErr
	}
	return true, nil
}

func (s *Server) serveRequest(ctx context.Context, conn *serverConn, message request) {
	if s.handler == nil {
		_ = conn.write(response{
			JSONRPC: "2.0",
			ID:      message.ID,
			Error:   &RPCError{Code: CodeDispatchFailed, Message: "no request handler is configured"},
		})
		return
	}
	requestCtx := context.WithValue(ctx, notifierKey{}, Notifier(conn))
	result, err := s.handler.Handle(requestCtx, message.Method, message.Params)
	if err != nil {
		_ = conn.write(response{
			JSONRPC: "2.0",
			ID:      message.ID,
			Error:   &RPCError{Code: CodeDispatchFailed, Message: err.Error()},
		})
		return
	}
	_ = conn.write(response{
		JSONRPC: "2.0",
		ID:      message.ID,
		Result:  result,
	})
}

// validToken compares the presented token in constant time.
func (s *Server) validToken(params json.RawMessage) bool {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return false
	}
	presented := []byte(body.Token)
	expected := []byte(s.token)
	if len(presented) != len(expected) {
		// Compare against the expected length anyway so a wrong-length token
		// is not faster than a wrong value.
		subtle.ConstantTimeCompare(expected, expected)
		return false
	}
	return subtle.ConstantTimeCompare(presented, expected) == 1
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}
