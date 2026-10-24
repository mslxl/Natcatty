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
	discovery, err := resolveTarget()
	if err != nil {
		return err
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	client, err := mcprpc.Dial(connectCtx, discovery.Host, discovery.Port)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Verify(connectCtx, discovery.Token); err != nil {
		return fmt.Errorf("external MCP: authenticate with the app: %w", err)
	}
	fmt.Fprintf(stderr, "natcatty-mcp: connected to Netcatty on %s:%d\n", discovery.Host, discovery.Port)

	return mcp.NewServer(bridgeCaller{client: client}).Serve(ctx, stdin, stdout)
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
		return mcprpc.Discovery{}, fmt.Errorf("%w (is the Netcatty app running?)", err)
	}
	return discovery, nil
}

// bridgeCaller forwards one tool call to the app and decodes its JSON result.
type bridgeCaller struct {
	client *mcprpc.Client
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
