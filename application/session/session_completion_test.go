package session

import (
	"context"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/serial"
)

// A Session emits one completion when the command finishes, never at Enter,
// carrying its identity, scope, and captured fields. A later mark and a close
// after that do not emit again.
func TestSessionCompletionHookEmitsOnceOnD(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	events := make(chan CommandEvent, 4)
	service := newTestService(factory,
		WithIDGenerator(func() model.SessionID { return "session-completion" }),
		WithCommandCompletionHook(func(event CommandEvent) { events <- event }),
	)
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Label: "web-1",
		Hostname: "10.0.0.5", Username: "deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.TerminalConn().Write([]byte("git status\r")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed input", func() bool { return factory.transport.input() == "git status\r" })
	select {
	case event := <-events:
		t.Fatalf("a completion was emitted at Enter: %+v", event)
	case <-time.After(100 * time.Millisecond):
	}

	factory.transport.send("\x1b]133;D;0\x07")
	_ = readOutput(t, session)
	var event CommandEvent
	select {
	case event = <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("the completion hook was not called")
	}
	if event.SessionID != "session-completion" || event.Scope != "host:host-1" ||
		event.HostID != "host-1" || event.HostLabel != "web-1" ||
		event.Kind != KindSSH || event.Hostname != "10.0.0.5" || event.Username != "deploy" {
		t.Fatalf("event identity = %+v", event)
	}
	if event.Command != "git status" || event.StartedAt.IsZero() ||
		event.FinishedAt.IsZero() || !event.HasExitCode || event.ExitCode != 0 {
		t.Fatalf("event capture = %+v", event)
	}
	if event.CaptureTier != domainhistory.CaptureTierNativeOSC {
		t.Fatalf("capture tier = %d, want Tier 2", event.CaptureTier)
	}
	if event.Transient {
		t.Fatal("a Session without Host Atuin marked its completion transient")
	}

	// Later marks and the Session close must not produce a second event.
	factory.transport.send("\x1b]133;D;0\x07")
	_ = readOutput(t, session)
	select {
	case extra := <-events:
		t.Fatalf("a later mark emitted a second completion: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
	_ = session.Close()
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the Session did not end")
	}
	select {
	case extra := <-events:
		t.Fatalf("Session close emitted a second completion: %+v", extra)
	default:
	}
}

// A pending command is finalized once when the Session closes, with no
// invented finish time or exit code.
func TestSessionCompletionHookFinalizesPendingOnClose(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	events := make(chan CommandEvent, 4)
	service := newTestService(factory,
		WithCommandCompletionHook(func(event CommandEvent) { events <- event }),
	)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.TerminalConn().Write([]byte("sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed input", func() bool { return factory.transport.input() == "sleep 30\r" })

	_ = session.Close()
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the Session did not end")
	}
	select {
	case event := <-events:
		if event.Command != "sleep 30" || event.Scope != "local" ||
			event.HasExitCode || !event.FinishedAt.IsZero() ||
			event.CaptureTier != domainhistory.CaptureTierFallback {
			t.Fatalf("close completion = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Session close did not finalize the pending command")
	}
	select {
	case extra := <-events:
		t.Fatalf("Session close emitted twice: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// The scope vocabulary the Session resolves itself: a saved Host, the local
// shell, and a serial port whose name is not a host and is never local.
func TestCommandScopeVocabulary(t *testing.T) {
	tests := []struct {
		name string
		spec StartSpec
		want string
	}{
		{"local", StartSpec{Kind: KindLocal}, "local"},
		{"saved host", StartSpec{Kind: KindSSH, HostID: "host-1"}, "host:host-1"},
		{"telnet host", StartSpec{Kind: KindTelnet, HostID: "host-2"}, "host:host-2"},
		{"serial", StartSpec{Kind: KindSerial, Serial: &serial.Config{Path: "/dev/tty.usbserial-1"}}, "serial:tty.usbserial-1"},
		{"serial without config", StartSpec{Kind: KindSerial}, "serial"},
		{"unsaved host", StartSpec{Kind: KindSSH, Hostname: "typed.example"}, "host:unsaved:typed.example:unknown"},
		{"unsaved host with username", StartSpec{Kind: KindSSH, Hostname: "typed.example", Username: "deploy"}, "host:unsaved:typed.example:deploy"},
		{"unsaved host falls back to its label", StartSpec{Kind: KindTelnet, Label: "deploy@typed.example"}, "host:unsaved:deploy@typed.example:unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := commandScope(test.spec); got != test.want {
				t.Fatalf("commandScope = %q, want %q", got, test.want)
			}
			// Autocomplete reads the same scope the completion is recorded
			// under, including the unsaved Host's captured origin.
			session := &Session{spec: test.spec}
			if got := session.HistoryScope(); got != test.want {
				t.Fatalf("HistoryScope = %q, want %q", got, test.want)
			}
		})
	}
}
