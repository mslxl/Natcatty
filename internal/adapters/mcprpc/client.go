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
	"time"
)

// Client is the stdio process's one loopback connection to the app. It does
// not reconnect: a lost app connection is reported, exactly as the original
// stdio server did.
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
		return nil, errors.New("external MCP: the app connection is closed")
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
		return nil, fmt.Errorf("external MCP: send %s: %w", method, err)
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
				return nil, errors.New("external MCP: the app closed the connection")
			}
			if errors.Is(err, ErrLineTooLong) {
				return nil, err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("external MCP: read %s: %w", method, err)
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
