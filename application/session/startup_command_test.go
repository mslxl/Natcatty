package session

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestHostStartupCommandRunsOncePerConnectionAndReplaysOnReconnect is the
// Host Details startup-command contract: wait for the shell's first output,
// type the command once, never on a later output burst, and type it again
// after every reconnect.
func TestHostStartupCommandRunsOncePerConnectionAndReplaysOnReconnect(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	third := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, second, third}}
	service := reconnectTestService(factory)
	session, err := service.Open(context.Background(), StartSpec{
		Kind:           KindSSH,
		StartupCommand: "echo ready",
		Reconnect:      &ReconnectPolicy{Delay: time.Hour},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// A Session that has not produced output is not ready.
	if got := first.input(); got != "" {
		t.Fatalf("the startup command ran before the shell spoke: %q", got)
	}

	first.output <- []byte("$ ")
	waitForCondition(t, "the first startup command", func() bool {
		return strings.Count(first.input(), "echo ready\r") == 1
	})

	// Later output on the same connection must not replay it.
	before := session.Snapshot().LastActivity
	first.output <- []byte("more output\r\n")
	waitForCondition(t, "the next output burst", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(first.input(), "echo ready\r"); got != 1 {
		t.Fatalf("the startup command ran %d times on one connection", got)
	}

	if !session.ReconnectNow() {
		t.Fatal("ReconnectNow was refused")
	}
	waitForCondition(t, "the reconnected transport", func() bool {
		return factory.callCount() == 2
	})
	if got := second.input(); got != "" {
		t.Fatalf("the startup command ran before the reconnected shell spoke: %q", got)
	}

	second.output <- []byte("$ ")
	waitForCondition(t, "the replayed startup command", func() bool {
		return strings.Count(second.input(), "echo ready\r") == 1
	})

	before = session.Snapshot().LastActivity
	second.output <- []byte("more after reconnect\r\n")
	waitForCondition(t, "the next output after reconnect", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(second.input(), "echo ready\r"); got != 1 {
		t.Fatalf("the startup command replayed %d times on one connection", got)
	}

	// A second reconnect replays it again, once.
	if !session.ReconnectNow() {
		t.Fatal("the second ReconnectNow was refused")
	}
	waitForCondition(t, "the twice-reconnected transport", func() bool {
		return factory.callCount() == 3
	})
	third.output <- []byte("$ ")
	waitForCondition(t, "the startup command after the second reconnect", func() bool {
		return strings.Count(third.input(), "echo ready\r") == 1
	})
	before = session.Snapshot().LastActivity
	third.output <- []byte("more after the second reconnect\r\n")
	waitForCondition(t, "the next output after the second reconnect", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(third.input(), "echo ready\r"); got != 1 {
		t.Fatalf("the startup command ran %d times on the third connection", got)
	}
}

func TestHostWithoutStartupCommandTypesNothing(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	session, err := reconnectTestService(factory).Open(context.Background(), StartSpec{Kind: KindSSH})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.output <- []byte("$ ")
	waitForCondition(t, "the shell prompt", func() bool {
		return !session.Snapshot().LastActivity.IsZero()
	})
	if got := transport.input(); got != "" {
		t.Fatalf("a Host without a startup command typed %q", got)
	}
}

func TestStartupCommandDoesNotRunWhenTheSessionEndsFirst(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	session, err := reconnectTestService(factory).Open(context.Background(), StartSpec{
		Kind:           KindSSH,
		StartupCommand: "echo ready",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.fail(End{Reason: EndReasonExited, ExitCode: 1, HasExitCode: true})
	waitForDone(t, session)
	if got := transport.input(); got != "" {
		t.Fatalf("a Session that ended before readiness typed %q", got)
	}
}
