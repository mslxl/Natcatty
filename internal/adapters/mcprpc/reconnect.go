package mcprpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Connector opens and authenticates one loopback connection. It is called
// again whenever the previous socket is lost, so it must re-read the discovery
// file: a restarted app can change the port and token.
type Connector func(ctx context.Context) (*Client, error)

// ReconnectingClient is the external bridge's connection to the app. Unlike
// Client, which reports a lost socket, it drops that socket and reconnects once
// per call when the failure is a closed connection.
//
// It is opt-in: the app's managed transports keep using Client directly and
// still fail fast.
type ReconnectingClient struct {
	mu      sync.Mutex
	connect Connector
	client  *Client
	closed  bool
}

// NewReconnectingClient builds a client that dials through connect.
func NewReconnectingClient(connect Connector) *ReconnectingClient {
	return &ReconnectingClient{connect: connect}
}

// Connect establishes the initial authenticated connection. A call made before
// this succeeds also connects lazily, so callers may use either style.
func (c *ReconnectingClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrConnectionClosed
	}
	if c.client != nil {
		return nil
	}
	client, err := c.connect(ctx)
	if err != nil {
		return err
	}
	c.client = client
	return nil
}

// Call sends one request. When the current socket is gone it re-reads the
// discovery target through the Connector, redials, re-verifies, and retries the
// call exactly once. Failures that may have executed the request on the app
// side, and protocol or authentication errors, are returned without a retry.
func (c *ReconnectingClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrConnectionClosed
	}
	if c.client == nil {
		client, err := c.connect(ctx)
		if err != nil {
			return nil, err
		}
		c.client = client
	}

	result, err := c.client.Call(ctx, method, params)
	if err == nil || !IsConnectionClosed(err) {
		return result, err
	}

	// The socket died. Drop it so a later call cannot reuse it, reconnect with a
	// freshly resolved target, and retry this call once.
	_ = c.client.Close()
	c.client = nil
	client, connectErr := c.connect(ctx)
	if connectErr != nil {
		return nil, fmt.Errorf("external MCP: reconnect to the app: %w", connectErr)
	}
	c.client = client
	return client.Call(ctx, method, params)
}

// Close ends the connection. It is safe to call more than once and prevents a
// later call from reconnecting.
func (c *ReconnectingClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.client == nil {
		return nil
	}
	client := c.client
	c.client = nil
	return client.Close()
}
