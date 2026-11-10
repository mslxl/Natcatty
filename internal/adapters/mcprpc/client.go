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

// Notification is one server-to-client message received on the connection.
type Notification struct {
	Method string
	Params json.RawMessage
}

// Client is the stdio process's one loopback connection to the app. It does
// not reconnect: a lost app connection is reported, exactly as the original
// stdio server did. A background reader routes responses to their callers and
// notifications to an optional handler, so the app can push progress and
// tools/list_changed while a call is in flight.
type Client struct {
	conn   net.Conn
	reader *bufio.Reader

	writeMu sync.Mutex

	mu        sync.Mutex
	nextID    uint64
	pending   map[uint64]chan clientReply
	closed    bool
	closedErr error
	done      chan struct{}

	notificationMu sync.Mutex
	notification   func(Notification)

	// MaxLineBytes overrides the inbound buffer cap; tests use a small value.
	MaxLineBytes int
}

type clientReply struct {
	result json.RawMessage
	err    error
}

// Dial connects to an authenticated loopback bridge and starts its reader.
// The context bounds the TCP handshake only.
func Dial(ctx context.Context, host string, port int) (*Client, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("external MCP: connect to the app: %w", err)
	}
	c := &Client{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		pending: make(map[uint64]chan clientReply),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// SetNotificationHandler installs the receiver for server-to-client
// notifications. It is called on the reader goroutine, so the handler must not
// block; the stdio process writes the notification to its own output.
func (c *Client) SetNotificationHandler(fn func(Notification)) {
	c.notificationMu.Lock()
	c.notification = fn
	c.notificationMu.Unlock()
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

// Call sends one request and waits for its matching response. Concurrent calls
// are allowed; writes are serialized on the connection and responses are
// matched by request id.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.closed {
		err := c.closedErr
		c.mu.Unlock()
		if err == nil {
			err = errors.New("external MCP: the app connection is closed")
		}
		return nil, err
	}
	id := c.nextID
	c.nextID++
	reply := make(chan clientReply, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		message["params"] = params
	}
	if err := c.write(ctx, message); err != nil {
		c.shutdown(fmt.Errorf("external MCP: send %s: %w", method, err))
		return nil, fmt.Errorf("external MCP: send %s: %w", method, err)
	}

	select {
	case result := <-reply:
		return result.result, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		select {
		case result := <-reply:
			return result.result, result.err
		default:
		}
		c.mu.Lock()
		err := c.closedErr
		c.mu.Unlock()
		if err == nil {
			err = errors.New("external MCP: the app connection is closed")
		}
		return nil, err
	}
}

// write serializes one JSON-RPC message onto the connection under the
// caller's deadline.
func (c *Client) write(ctx context.Context, message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
		defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()
	}
	return writeMessage(c.conn, message)
}

// readLoop routes responses by id and notifications to the installed handler.
func (c *Client) readLoop() {
	limit := c.MaxLineBytes
	if limit <= 0 {
		limit = MaxClientLineBytes
	}
	for {
		line, err := readLine(c.reader, limit)
		if len(line) > 0 {
			c.dispatchLine(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.shutdown(errors.New("external MCP: the app closed the connection"))
			} else if errors.Is(err, ErrLineTooLong) {
				c.shutdown(err)
			} else {
				c.shutdown(fmt.Errorf("external MCP: read: %w", err))
			}
			return
		}
	}
}

func (c *Client) dispatchLine(line []byte) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(line, &message); err != nil {
		return
	}
	if message.Method != "" {
		c.notificationMu.Lock()
		handler := c.notification
		c.notificationMu.Unlock()
		if handler != nil {
			handler(Notification{Method: message.Method, Params: message.Params})
		}
		return
	}
	id, err := strconv.ParseUint(string(message.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	reply := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if reply == nil {
		return
	}
	if message.Error != nil {
		reply <- clientReply{err: message.Error}
		return
	}
	reply <- clientReply{result: message.Result}
}

// shutdown marks the connection closed and fails every waiting call.
func (c *Client) shutdown(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closedErr = cause
	pending := c.pending
	c.pending = make(map[uint64]chan clientReply)
	close(c.done)
	c.mu.Unlock()
	for _, reply := range pending {
		select {
		case reply <- clientReply{err: cause}:
		default:
		}
	}
	_ = c.conn.Close()
}

// Close ends the connection. It is safe to call more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	c.shutdown(errors.New("external MCP: the app connection is closed"))
	return nil
}
