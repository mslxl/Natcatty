package session

import (
	"context"
	"testing"
	"time"

	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func telnetSpec() StartSpec {
	cfg := domaintelnet.Config{Hostname: "console.local", Port: 2323}
	return StartSpec{
		Kind:   KindTelnet,
		HostID: "host-telnet",
		Telnet: &cfg,
		Cols:   100,
		Rows:   30,
	}
}

func TestOpenTelnetCarriesTheEndpointAndLabel(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	spec := telnetSpec()
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	got := factory.spec
	if got.Kind != KindTelnet || got.Telnet == nil || *got.Telnet != *spec.Telnet {
		t.Fatalf("factory spec = %+v, want the Telnet endpoint", got)
	}
	if got.EncodingScope != domainterminal.EncodingScopeHost("host-telnet") {
		t.Fatalf("encoding scope = %q, want the Host scope", got.EncodingScope)
	}
	if label := session.Label(); label != "console.local" {
		t.Fatalf("label = %q, want console.local", label)
	}
	if snapshot := session.Snapshot(); snapshot.Kind != KindTelnet {
		t.Fatalf("snapshot kind = %q, want %q", snapshot.Kind, KindTelnet)
	}
}

func TestTelnetSessionReadsWritesAndCloses(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.Open(context.Background(), telnetSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	conn := session.TerminalConn()
	factory.transport.send("router> ")
	got := make([]byte, 8)
	if _, err := readWithTimeout(t, conn, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "router> " {
		t.Fatalf("output = %q, want the banner", got)
	}

	if _, err := conn.Write([]byte("show ip\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	eventually(t, "input to reach the Telnet transport", func() bool {
		return factory.transport.input() == "show ip\r"
	})

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	eventually(t, "the Telnet Session to report closed", func() bool {
		return session.Snapshot().Status == StatusClosed
	})
}

func TestTelnetManualReconnectDialsTheSameEndpoint(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, second}}
	service := reconnectTestService(factory)
	spec := telnetSpec()
	spec.Reconnect = &ReconnectPolicy{
		Delay:       time.Hour,
		ShouldRetry: func(End) bool { return false },
	}
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !session.CanReconnect() {
		t.Fatal("CanReconnect = false for a running Telnet Session with a policy")
	}
	if !session.ReconnectNow() {
		t.Fatal("ReconnectNow was refused")
	}
	eventually(t, "the manual Telnet reconnect", func() bool { return factory.callCount() == 2 })
	if factory.specs[1].Kind != KindTelnet || factory.specs[1].Telnet == nil ||
		*factory.specs[1].Telnet != *spec.Telnet {
		t.Fatalf("reconnect spec = %+v, want the same Telnet endpoint", factory.specs[1])
	}
	if snapshot := session.Snapshot(); snapshot.Status != StatusRunning {
		t.Fatalf("status after reconnect = %q, want running", snapshot.Status)
	}
}

func TestTelnetDropDoesNotReconnectAutomatically(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	service := reconnectTestService(factory)
	spec := telnetSpec()
	spec.Reconnect = &ReconnectPolicy{ShouldRetry: func(End) bool { return false }}
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.fail(End{Reason: EndReasonError, ExitCode: 1, HasExitCode: true})
	eventually(t, "the Telnet Session to end", func() bool {
		return session.Snapshot().Status == StatusExited
	})
	if calls := factory.callCount(); calls != 1 {
		t.Fatalf("factory calls = %d, want no automatic reconnect", calls)
	}
}
