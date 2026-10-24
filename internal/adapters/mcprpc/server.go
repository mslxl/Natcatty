package mcprpc

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
)

// Handler answers one authenticated request. The returned value is encoded as
// the JSON-RPC result; an error becomes -32000.
type Handler interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Handle implements Handler.
func (fn HandlerFunc) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return fn(ctx, method, params)
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
}

// NewServer builds a server that accepts the dedicated external MCP token.
func NewServer(token string, handler Handler) *Server {
	return &Server{token: token, handler: handler}
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

// ServeConn authenticates and serves one connection.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	authenticated := false

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		line, readErr := readLine(reader, s.MaxLineBytes)
		if len(line) > 0 {
			closeConnection, writeErr := s.handleLine(ctx, conn, line, &authenticated)
			if writeErr != nil {
				return
			}
			if closeConnection {
				return
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

// handleLine processes one message and reports whether the connection must
// close.
func (s *Server) handleLine(ctx context.Context, conn net.Conn, line []byte, authenticated *bool) (bool, error) {
	var message request
	if err := json.Unmarshal(line, &message); err != nil {
		// The original bridge ignored malformed lines rather than killing the
		// connection.
		s.logf("ignoring a malformed JSON-RPC line")
		return false, nil
	}
	// Notifications (no id) are never authenticated work.
	if len(message.ID) == 0 || string(message.ID) == "null" || message.Method == "" {
		return false, nil
	}

	if !*authenticated {
		if message.Method == "auth/verify" && s.validToken(message.Params) {
			*authenticated = true
			return false, writeMessage(conn, response{
				JSONRPC: "2.0",
				ID:      message.ID,
				Result:  map[string]any{"ok": true},
			})
		}
		writeErr := writeMessage(conn, response{
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

	if s.handler == nil {
		return false, writeMessage(conn, response{
			JSONRPC: "2.0",
			ID:      message.ID,
			Error:   &RPCError{Code: CodeDispatchFailed, Message: "no request handler is configured"},
		})
	}
	result, err := s.handler.Handle(ctx, message.Method, message.Params)
	if err != nil {
		return false, writeMessage(conn, response{
			JSONRPC: "2.0",
			ID:      message.ID,
			Error:   &RPCError{Code: CodeDispatchFailed, Message: err.Error()},
		})
	}
	return false, writeMessage(conn, response{
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
