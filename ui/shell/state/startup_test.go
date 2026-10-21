package state

import (
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestOpenHostWithStartupSendsTheExactChunksAfterAttaching(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	startup := StartupInput{
		Chunks: [][]byte{[]byte("echo one\r"), []byte("echo two\r")},
		Delay:  time.Millisecond,
	}
	if err := graph.state.OpenHostWithStartup(host, startup); err != nil {
		t.Fatalf("OpenHostWithStartup: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	waitFor(t, "the startup input", func() bool {
		graph.factory.transport.mu.Lock()
		defer graph.factory.transport.mu.Unlock()
		return string(graph.factory.transport.input) == "echo one\recho two\r"
	})
	if graph.state.Problem() != "" {
		t.Fatalf("problem = %q after a successful startup delivery", graph.state.Problem())
	}
}

func TestStartupInputEmpty(t *testing.T) {
	if !(StartupInput{}).Empty() {
		t.Fatal("an input with no chunks is not empty")
	}
	if (StartupInput{Chunks: [][]byte{{'x'}}}).Empty() {
		t.Fatal("an input with a chunk reports empty")
	}
}

func TestFocusedHostIDReportsOnlySSHSessions(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if hostID, ok := graph.state.FocusedHostID(); ok || hostID != "" {
		t.Fatalf("local Session reported Host %q, %v", hostID, ok)
	}
	if err := graph.state.OpenHost(domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 2 && graph.state.ActiveTab() == 1
	})
	if hostID, ok := graph.state.FocusedHostID(); !ok || hostID != "host-1" {
		t.Fatalf("FocusedHostID = %q, %v; want host-1", hostID, ok)
	}
}
