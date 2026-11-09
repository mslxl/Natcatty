package state

import (
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func browserHost() domainvault.Host {
	return domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
}

func TestOpenHostBrowserOpensThePanelAfterTheTabAttaches(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.OpenHostBrowser(browserHost()); err != nil {
		t.Fatalf("OpenHostBrowser: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	if !graph.surface.browserOpened {
		t.Fatal("the browser panel was not opened on the new surface")
	}
	if graph.state.Problem() != "" {
		t.Fatalf("problem = %q after a successful open", graph.state.Problem())
	}
}

func TestOpenHostLeavesTheBrowserPanelClosed(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.OpenHost(browserHost()); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	if graph.surface.browserOpened {
		t.Fatal("a plain connection opened the browser panel")
	}
}
