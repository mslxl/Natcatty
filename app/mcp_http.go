//go:build !ios && !android

// Optional Streamable HTTP endpoint for External MCP. The endpoint is off
// until preferences enable it; when it is on, every request is authenticated
// with the same discovery token the loopback JSON-RPC transport publishes and
// is dispatched through the same policy handler as stdio. There is no second
// authorization path.

package app

import (
	"context"
	"encoding/json"
	"fmt"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp"
)

// mcpHTTPCaller adapts the authenticated policy handler to the MCP protocol
// layer's Caller. Params cross as JSON so an HTTP call and a stdio call are
// indistinguishable to policy evaluation and the read/write executors.
type mcpHTTPCaller struct {
	handler mcprpc.Handler
}

func (c mcpHTTPCaller) Call(ctx context.Context, method string, params map[string]any) (any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("external MCP HTTP: encode params: %w", err)
	}
	return c.handler.Handle(ctx, method, raw)
}

// externalMCPHTTP owns the Streamable HTTP listener and applies preference
// changes without restarting the app.
type externalMCPHTTP struct {
	transport *mcp.HTTPTransport
	caller    mcpHTTPCaller
	token     func() string
	logf      func(format string, args ...any)
}

func newExternalMCPHTTP(handler mcprpc.Handler, token func() string,
	logf func(format string, args ...any)) *externalMCPHTTP {
	return &externalMCPHTTP{
		transport: mcp.NewHTTPTransport(),
		caller:    mcpHTTPCaller{handler: handler},
		token:     token,
		logf:      logf,
	}
}

// Apply starts, stops, or reconfigures the endpoint to match settings. It is
// idempotent, so the preferences change hook can call it for any update. The
// master External MCP switch always wins: an endpoint cannot listen while the
// feature itself is off.
func (h *externalMCPHTTP) Apply(settings domainpreferences.ExternalMCP) error {
	if h == nil {
		return nil
	}
	if !settings.Enabled {
		settings.HTTPEnabled = false
	}
	before := h.transport.State()
	state, err := h.transport.Apply(mcp.HTTPConfig{
		Enabled:       settings.HTTPEnabled,
		ListenAddress: settings.EffectiveHTTPListenAddress(),
		Port:          settings.HTTPPort,
		ClientHost:    settings.HTTPClientHost,
		TokenProvider: h.token,
		Logf:          h.logf,
	}, h.caller)
	if err != nil {
		return fmt.Errorf("external MCP Streamable HTTP: %w", err)
	}
	if state.Listening && (!before.Listening || before.Port != state.Port) {
		endpoint := state.URL
		if endpoint == "" {
			endpoint = "no client URL until a client address is configured"
		}
		if h.logf != nil {
			h.logf("natcatty: external MCP Streamable HTTP listening on %s:%d (%s)",
				state.ListenAddress, state.Port, endpoint)
		}
	}
	return nil
}

// Close stops the listener. It is safe on a nil receiver.
func (h *externalMCPHTTP) Close() {
	if h == nil {
		return
	}
	h.transport.Stop()
}
