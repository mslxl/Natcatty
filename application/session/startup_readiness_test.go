package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// startupReadinessService builds a Service whose startup-command fallback is
// chosen by the test. The prompt heuristic itself is time-independent; only
// the bounded no-prompt fallback uses the timeout.
func startupReadinessService(factory Factory, timeout time.Duration) *Service {
	return NewService(factory,
		WithIDGenerator(func() model.SessionID { return "session-startup-readiness" }),
		WithStartupReadinessTimeout(timeout))
}

// TestTelnetStartupCommandWaitsForThePromptNotTheLoginBanner is the ticket 50
// telnet defect: a device banner and its login prompt are not readiness, so the
// Host startup command is never typed into them. The device prompt releases it
// exactly once.
func TestTelnetStartupCommandWaitsForThePromptNotTheLoginBanner(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	// A long fallback keeps the banner phase of this test inside the prompt
	// path; the bounded fallback has its own test.
	session, err := startupReadinessService(factory, 5*time.Second).Open(
		context.Background(), StartSpec{
			Kind:           KindTelnet,
			StartupCommand: "show status",
		})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// The Telnet greeting and login prompt are pre-shell output. "login: "
	// ends in a prompt-like punctuation but is not a shell prompt.
	before := session.Snapshot().LastActivity
	transport.output <- []byte("Welcome to the lab switch\r\n\r\nlogin: ")
	waitForCondition(t, "the telnet login banner", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "" {
		t.Fatalf("the startup command was typed at the telnet login banner: %q", got)
	}

	// A "press Enter" notice from the device is not a shell prompt either.
	before = session.Snapshot().LastActivity
	transport.output <- []byte("\r\nPress Enter to continue...")
	waitForCondition(t, "the press-enter notice", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "" {
		t.Fatalf("the startup command was typed at a press-enter notice: %q", got)
	}

	// The device prompt is readiness: exactly one command, with the Enter that
	// the user would have typed.
	transport.output <- []byte("\r\nRouter# ")
	waitForCondition(t, "the startup command at the device prompt", func() bool {
		return transport.input() == "show status\r"
	})

	// Device output after the prompt does not replay it within the connection.
	before = session.Snapshot().LastActivity
	transport.output <- []byte("\r\nshow status\r\nrunning\r\n")
	waitForCondition(t, "the command output", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(transport.input(), "show status\r"); got != 1 {
		t.Fatalf("the startup command ran %d times on one telnet connection", got)
	}
}

// TestSSHStartupCommandWaitsPastTheLoginBanner covers a second protocol with
// pre-shell output: an SSH login banner and MOTD must not release the command;
// the shell prompt must.
func TestSSHStartupCommandWaitsPastTheLoginBanner(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	session, err := startupReadinessService(factory, 5*time.Second).Open(
		context.Background(), StartSpec{
			Kind:           KindSSH,
			StartupCommand: "tail -f /var/log/app.log",
		})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	before := session.Snapshot().LastActivity
	transport.output <- []byte(
		"Authorized users only.\r\nLast login: Mon Nov 16 09:00:00 2026\r\n\r\n")
	waitForCondition(t, "the SSH login banner", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "" {
		t.Fatalf("the startup command was typed at the SSH banner: %q", got)
	}

	transport.output <- []byte("deploy@web:~$ ")
	waitForCondition(t, "the startup command at the shell prompt", func() bool {
		return transport.input() == "tail -f /var/log/app.log\r"
	})
}

// TestStartupCommandFallsBackWhenNoPromptIsRecognized proves a device that
// never prints a recognizable prompt does not hang: the command is typed after
// the bounded fallback, once.
func TestStartupCommandFallsBackWhenNoPromptIsRecognized(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	session, err := startupReadinessService(factory, 80*time.Millisecond).Open(
		context.Background(), StartSpec{
			Kind:           KindTelnet,
			StartupCommand: "show status",
		})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.output <- []byte("booting...\r\n")
	waitForCondition(t, "the bounded startup-command fallback", func() bool {
		return transport.input() == "show status\r"
	})

	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\nstill booting\r\n")
	waitForCondition(t, "output after the fallback", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(transport.input(), "show status\r"); got != 1 {
		t.Fatalf("the fallback typed the startup command %d times", got)
	}
}

// TestStaleStartupReadinessNeverTypesIntoTheNextConnection pins the generation
// guard. A readiness decision or timer callback from an abandoned transport
// must not type into the replacement, even when the replacement has a prompt.
func TestStaleStartupReadinessNeverTypesIntoTheNextConnection(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, second}}
	session, err := startupReadinessService(factory, 5*time.Second).Open(
		context.Background(), StartSpec{
			Kind:           KindSSH,
			StartupCommand: "echo ready",
		})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// A reconnect disarms the old generation and arms the replacement.
	oldGeneration := session.generation
	session.armStartupCommand(oldGeneration+1, second)
	session.observeStartupOutput(oldGeneration+1, []byte("$ "))

	// A late read or timer callback from the abandoned generation arrives with
	// the replacement's prompt retained.
	session.startupReadinessElapsed(oldGeneration)
	session.maybeRunStartupCommand(oldGeneration, first)

	// The synchronous state is the discriminator: a stale callback must leave
	// the live generation's pending command and transport untouched. (The write
	// itself happens on a goroutine, so checking the transport immediately
	// would race.)
	session.startupMu.Lock()
	pending, generation, armedTransport := session.startupPending,
		session.startupGeneration, session.startupTransport
	session.startupMu.Unlock()
	if !pending || generation != oldGeneration+1 || armedTransport != second {
		t.Fatalf("a stale readiness callback disarmed the live generation: "+
			"pending=%v generation=%d", pending, generation)
	}
	if got := first.input(); got != "" {
		t.Fatalf("a stale generation typed into the old transport: %q", got)
	}
	if got := second.input(); got != "" {
		t.Fatalf("a stale generation typed into the replacement transport: %q", got)
	}

	// The live generation still releases normally.
	session.maybeRunStartupCommand(oldGeneration+1, second)
	waitForCondition(t, "the replacement connection's startup command", func() bool {
		return second.input() == "echo ready\r"
	})
}

// TestStartupReadinessTimerIsStoppedWhenTheSessionEndsFirst covers the
// end-before-readiness half of the once contract: teardown cancels the pending
// wait, so a fallback that would have fired later never types into the closed
// connection.
func TestStartupReadinessTimerIsStoppedWhenTheSessionEndsFirst(t *testing.T) {
	transport := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	timeout := 60 * time.Millisecond
	session, err := startupReadinessService(factory, timeout).Open(
		context.Background(), StartSpec{
			Kind:           KindSSH,
			StartupCommand: "echo ready",
		})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.fail(End{Reason: EndReasonExited, ExitCode: 1, HasExitCode: true})
	waitForDone(t, session)

	session.startupMu.Lock()
	timer, pending := session.startupTimer, session.startupPending
	session.startupMu.Unlock()
	if timer != nil || pending {
		t.Fatalf("teardown left the startup readiness armed: timer=%v pending=%v", timer, pending)
	}

	// Wait past the fallback bound and prove nothing reached the transport.
	deadline := time.Now().Add(4 * timeout)
	for time.Now().Before(deadline) {
		if got := transport.input(); got != "" {
			t.Fatalf("a Session that ended before readiness typed %q", got)
		}
		time.Sleep(time.Millisecond)
	}
}
