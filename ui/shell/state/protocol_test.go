package state

import (
	"errors"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestOpenHostFailsClosedForAnUnavailableProviderProtocol(t *testing.T) {
	// No provider is registered in this build. A Host saved with a provider
	// protocol must not map onto the SSH default branch.
	graph := newTestGraph(t)
	err := graph.state.OpenHost(domainvault.Host{
		ID: "host-plugin", Label: "plugin", Hostname: "plugin.example.com",
		Protocol: "x-acme-loop",
	})
	if !errors.Is(err, appsession.ErrProviderUnavailable) {
		t.Fatalf("OpenHost error = %v, want ErrProviderUnavailable", err)
	}
	if graph.state.TabCount() != 0 {
		t.Fatalf("TabCount = %d, want no Tab for an unavailable provider", graph.state.TabCount())
	}
	if graph.factory.spec.Kind != "" {
		t.Fatalf("the fake transport opened a Session: %+v", graph.factory.spec)
	}
}

func TestOpenHostRejectsAMalformedProtocol(t *testing.T) {
	graph := newTestGraph(t)
	err := graph.state.OpenHost(domainvault.Host{
		ID: "host-bad", Label: "bad", Hostname: "bad.example.com", Protocol: "gopher",
	})
	if !errors.Is(err, protocolmodel.ErrInvalidProtocol) {
		t.Fatalf("OpenHost error = %v, want ErrInvalidProtocol", err)
	}
	if graph.state.TabCount() != 0 {
		t.Fatalf("TabCount = %d, want no Tab for an invalid protocol", graph.state.TabCount())
	}
}
