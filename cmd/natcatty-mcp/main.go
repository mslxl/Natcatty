// Command natcatty-mcp is the stdio MCP server an external client launches. It
// finds the running app through the discovery file, authenticates one loopback
// JSON-RPC connection, and then speaks MCP on stdin/stdout. Secrets and errors
// go to stderr only, because stdout carries the protocol.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp"
)

// connectTimeout bounds discovery, dial, and authentication. A missing or
// unresponsive app fails the process instead of hanging an MCP client.
const connectTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "natcatty-mcp: %v\n", err)
		os.Exit(1)
	}
}

// run is main's testable body.
func run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	// The external bridge is the one transport that reconnects: a long-lived
	// MCP client outlives an app restart, so every lost socket re-reads the
	// discovery file and authenticates again before the call is retried.
	client := mcprpc.NewReconnectingClient(func(ctx context.Context) (*mcprpc.Client, error) {
		return connect(ctx, stderr)
	})

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := client.Connect(connectCtx); err != nil {
		return err
	}
	defer client.Close()

	return mcp.NewServer(bridgeCaller{client: client}).Serve(ctx, stdin, stdout)
}

// connect resolves the current app target and authenticates one connection. It
// resolves on every call so a restarted app's new port and token are picked up.
// Each attempt is bounded by connectTimeout when the caller's context has no
// deadline of its own.
func connect(ctx context.Context, stderr io.Writer) (*mcprpc.Client, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, connectTimeout)
		defer cancel()
	}
	discovery, err := resolveTarget()
	if err != nil {
		return nil, err
	}
	client, err := mcprpc.Dial(ctx, discovery.Host, discovery.Port)
	if err != nil {
		return nil, err
	}
	if err := client.Verify(ctx, discovery.Token); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("external MCP: authenticate with the app: %w", err)
	}
	fmt.Fprintf(stderr, "natcatty-mcp: connected to Netcatty on %s:%d\n", discovery.Host, discovery.Port)
	return client, nil
}

// resolveTarget prefers the explicit PORT/TOKEN launch overrides and otherwise
// reads the same discovery file the original stdio server did.
func resolveTarget() (mcprpc.Discovery, error) {
	if port, portOK := mcprpc.PortFromEnv(); portOK {
		if token, tokenOK := mcprpc.TokenFromEnv(); tokenOK {
			return mcprpc.Discovery{Host: "127.0.0.1", Port: port, Token: token}, nil
		}
	}
	path, err := mcprpc.ResolveDiscoveryPath()
	if err != nil {
		return mcprpc.Discovery{}, err
	}
	discovery, err := mcprpc.ReadDiscovery(path)
	if err != nil {
		return mcprpc.Discovery{}, fmt.Errorf("%w (is Netcatty running with External MCP enabled?)", err)
	}
	return discovery, nil
}

// rpcCaller is the transport surface the MCP layer needs. It accepts both the
// fail-fast Client and the reconnecting external bridge.
type rpcCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// bridgeCaller forwards one tool call to the app and decodes its JSON result.
type bridgeCaller struct {
	client rpcCaller
}

func (b bridgeCaller) Call(ctx context.Context, method string, params map[string]any) (any, error) {
	raw, err := b.client.Call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{"ok": true}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("external MCP: decode the app result: " + err.Error())
	}
	return value, nil
}
