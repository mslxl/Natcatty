package state

import (
	"context"
	"errors"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// gatedFactory holds a Host dial open until the test releases it, so the
// off-thread MCP path can be observed while the UI thread flushes a frame.
type gatedFactory struct {
	started   chan struct{}
	release   chan struct{}
	transport *fakeTransport
}

func (f *gatedFactory) Open(ctx context.Context, _ appsession.StartSpec) (appsession.Transport, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return f.transport, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newMCPHostState(t *testing.T, factory appsession.Factory) *State {
	t.Helper()
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	return NewState(WithSessions(service, func(*appsession.Session) (Surface, error) {
		return &fakeSurface{}, nil
	}))
}

func TestOpenHostForMCPQueuesTheConnectingLabelForFlush(t *testing.T) {
	factory := &gatedFactory{
		started: make(chan struct{}, 1), release: make(chan struct{}),
		transport: newFakeTransport(),
	}
	state := newMCPHostState(t, factory)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}
	done := make(chan error, 1)
	go func() {
		_, err := state.OpenHostForMCP(context.Background(), host)
		done <- err
	}()
	select {
	case <-factory.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the Host dial never started")
	}
	// The MCP handler goroutine must not touch pending directly: the label is
	// invisible until Flush applies the queue on the UI thread.
	if pending := state.PendingConnections(); len(pending) != 0 {
		t.Fatalf("off-thread open mutated pending before Flush: %v", pending)
	}
	state.Flush()
	if pending := state.PendingConnections(); len(pending) != 1 || pending[0] != "prod" {
		t.Fatalf("pending after Flush = %v, want prod", pending)
	}
	close(factory.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OpenHostForMCP: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenHostForMCP did not return after the dial")
	}
	state.Flush()
	if pending := state.PendingConnections(); len(pending) != 0 {
		t.Fatalf("pending after the outcome Flush = %v, want none", pending)
	}
	if state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want 1", state.TabCount())
	}
}

func TestOpenHostForMCPFailureIsQueuedForFlush(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport(), err: errors.New("dial failed")}
	state := newMCPHostState(t, factory)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}
	if _, err := state.OpenHostForMCP(context.Background(), host); err == nil {
		t.Fatal("OpenHostForMCP succeeded without a transport")
	}
	if state.Problem() != "" {
		t.Fatal("the failed open wrote the problem off the UI thread")
	}
	state.Flush()
	if state.Problem() == "" {
		t.Fatal("Flush did not apply the failed open's problem")
	}
	if pending := state.PendingConnections(); len(pending) != 0 {
		t.Fatalf("failed open left a pending label: %v", pending)
	}
}
