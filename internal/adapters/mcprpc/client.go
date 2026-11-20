package mcprpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// ErrConnectionClosed reports that the loopback socket is gone: the app closed
// it, the peer reset it, or the client wrote to a closed connection. It marks
// the only class of failure a caller may retry on a fresh connection, because a
// request that failed this way is not known to have reached the app.
var ErrConnectionClosed = errors.New("external MCP: the app connection is closed")

// IsConnectionClosed reports whether err came from a lost or closed loopback
// socket rather than a protocol, authentication, or dispatch failure.
func IsConnectionClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnectionClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	// Windows reports socket loss as winsock numbers that do not compare equal
	// to the Unix-style syscall constants above. Any non-timeout failure of an
	// established read or write is a dead socket, whatever the platform calls it.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "read" || opErr.Op == "write"
	}
	return false
}

// closedError wraps a transport failure that means the socket is gone. A
// deadline or context failure is deliberately not wrapped: retrying it would
// replay work the app may already have done.
func closedError(operation string, err error) error {
	if !IsConnectionClosed(err) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%w: %s: %v", ErrConnectionClosed, operation, err)
}

// Client is the stdio process's one loopback connection to the app. It never
// redials by itself: a lost connection is reported to its caller. The external
// bridge wraps it in ReconnectingClient when it opts into reconnection; managed
// callers keep this fail-fast behavior.
type Client struct {
	conn   net.Conn
	reader *bufio.Reader

	mu     sync.Mutex
	nextID uint64
	closed bool

	// MaxLineBytes overrides the inbound buffer cap; tests use a small value.
	MaxLineBytes int
}

// Dial connects to an authenticated loopback bridge. The context bounds the
// TCP handshake only.
func Dial(ctx context.Context, host string, port int) (*Client, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("external MCP: connect to the app: %w", err)
	}
	return &Client{conn: conn, reader: bufio.NewReader(conn)}, nil
}

// Verify sends the mandatory first auth/verify request.
func (c *Client) Verify(ctx context.Context, token string) error {
	result, err := c.Call(ctx, "auth/verify", map[string]any{"token": token})
	if err != nil {
		return err
	}
	var verified struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(result, &verified); err != nil {
		return fmt.Errorf("external MCP: decode auth/verify: %w", err)
	}
	if !verified.OK {
		return &RPCError{Code: CodeAuthRequired, Message: "external MCP: authentication failed"}
	}
	return nil
}

// Call sends one request and waits for its matching response. It is safe for
// concurrent use; requests are serialized on the connection.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrConnectionClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id := c.nextID
	c.nextID++
	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		message["params"] = params
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
		_ = c.conn.SetReadDeadline(deadline)
		defer func() {
			_ = c.conn.SetWriteDeadline(time.Time{})
			_ = c.conn.SetReadDeadline(time.Time{})
		}()
	}
	if err := writeMessage(c.conn, message); err != nil {
		c.closed = true
		return nil, closedError("external MCP: send "+method, err)
	}

	wantID := strconv.FormatUint(id, 10)
	for {
		line, err := readLine(c.reader, c.MaxLineBytes)
		if len(line) > 0 {
			var message struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *RPCError       `json:"error"`
			}
			if jsonErr := json.Unmarshal(line, &message); jsonErr == nil &&
				string(message.ID) == wantID {
				if message.Error != nil {
					return nil, message.Error
				}
				return message.Result, nil
			}
		}
		if err != nil {
			c.closed = true
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w: the app closed the connection", ErrConnectionClosed)
			}
			if errors.Is(err, ErrLineTooLong) {
				return nil, err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, closedError("external MCP: read "+method, err)
		}
	}
}

// Close ends the connection. It is safe to call more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
