//go:build !ios && !android

// External MCP runtime ownership. The loopback discovery bridge and the
// optional Streamable HTTP endpoint are opened only while the preferences'
// master switch is on, so the app can follow a Settings change without a
// restart. Every other package still addresses the two listeners the same way
// they did at launch.

package app

import (
	"context"
	"sync"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

// externalMCPEndpoints owns the two External MCP listeners. Apply is idempotent,
// so the preferences change hook can call it for any update.
type externalMCPEndpoints struct {
	mu      sync.Mutex
	ctx     context.Context
	bridge  *mcpReadBridge
	handler mcprpc.Handler
	mode    *mcpModeState
	// discoveryPath is where the loopback bridge publishes its pointer. It is
	// removed when the listener stops.
	discoveryPath string
	logf          func(format string, args ...any)
	server        *externalMCP
	http          *externalMCPHTTP
}

func newExternalMCPEndpoints(ctx context.Context, bridge *mcpReadBridge,
	handler mcprpc.Handler, mode *mcpModeState, discoveryPath string,
	logf func(format string, args ...any)) *externalMCPEndpoints {
	return &externalMCPEndpoints{
		ctx: ctx, bridge: bridge, handler: handler, mode: mode,
		discoveryPath: discoveryPath, logf: logf,
	}
}

// SetHTTP attaches the Streamable HTTP endpoint. It is nil when the build has
// no HTTP surface.
func (r *externalMCPEndpoints) SetHTTP(endpoint *externalMCPHTTP) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.http = endpoint
	r.mu.Unlock()
}

// Apply starts, stops, or reconfigures both listeners to match settings.
// Disabling stops the HTTP endpoint first: it shares the loopback handler.
func (r *externalMCPEndpoints) Apply(settings domainpreferences.ExternalMCP) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !settings.Enabled {
		httpEndpoint := r.http
		server := r.server
		r.server = nil
		r.mu.Unlock()
		httpEndpoint.Close()
		server.Close()
		return nil
	}
	if r.server == nil {
		server, err := startExternalMCP(r.ctx, r.bridge, r.discoveryPath, r.logf,
			withMCPHandler(r.handler), withMCPModeState(r.mode))
		if err != nil {
			r.mu.Unlock()
			return err
		}
		r.server = server
	}
	httpEndpoint := r.http
	r.mu.Unlock()
	if httpEndpoint == nil {
		return nil
	}
	return httpEndpoint.Apply(settings)
}

// Token returns the current loopback bearer token, or "" while the listener
// is off. The HTTP handler calls it on every request, so a stopped and
// restarted listener never serves a stale token.
func (r *externalMCPEndpoints) Token() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server == nil {
		return ""
	}
	return r.server.token
}

// Close stops both listeners in the order the app quits in.
func (r *externalMCPEndpoints) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	httpEndpoint := r.http
	server := r.server
	r.http, r.server = nil, nil
	r.mu.Unlock()
	httpEndpoint.Close()
	server.Close()
}
