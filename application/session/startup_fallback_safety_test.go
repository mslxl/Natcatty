package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/session/autologin"
	"github.com/mslxl/natcatty/domain/model"
)

// startupSafetyFactory returns one scripted transport, so a test can observe
// every byte the Session writes.
type startupSafetyFactory struct {
	transport Transport
}

func (f startupSafetyFactory) Open(context.Context, StartSpec) (Transport, error) {
	return f.transport, nil
}

// writeSignallingTransport mirrors scriptedTransport but publishes each write,
// so a negative assertion can wait on a channel with a hard deadline instead of
// polling. Credential writes are published too; callers filter by substring.
type writeSignallingTransport struct {
	*scriptedTransport
	writes chan string
}

func newWriteSignallingTransport() *writeSignallingTransport {
	return &writeSignallingTransport{
		scriptedTransport: newScriptedTransport(),
		writes:            make(chan string, 16),
	}
}

func (t *writeSignallingTransport) Write(p []byte) (int, error) {
	copied := string(append([]byte(nil), p...))
	select {
	case t.writes <- copied:
	default:
	}
	return t.scriptedTransport.Write(p)
}

// manualStartupScheduler captures the readiness fallback instead of running it.
// It returns an inert timer; tests fire the captured callback explicitly. The
// callback must never run under the scheduler's lock: the Session holds
// startupMu while scheduling and the callback locks it.
type manualStartupScheduler struct {
	mu        sync.Mutex
	durations []time.Duration
	fires     []func()
}

func (m *manualStartupScheduler) schedule(duration time.Duration, fire func()) *time.Timer {
	m.mu.Lock()
	m.durations = append(m.durations, duration)
	m.fires = append(m.fires, fire)
	m.mu.Unlock()
	return time.NewTimer(time.Hour)
}

func (m *manualStartupScheduler) firstDuration(t *testing.T) time.Duration {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.durations) == 0 {
		t.Fatal("the startup readiness fallback was never scheduled")
	}
	return m.durations[0]
}

func (m *manualStartupScheduler) fireFirst() {
	m.mu.Lock()
	fire := m.fires[0]
	m.mu.Unlock()
	fire()
}

func openStartupSafetySession(t *testing.T, transport Transport, spec StartSpec, options ...Option) *Session {
	t.Helper()
	all := []Option{WithIDGenerator(func() model.SessionID { return "session-startup-safety" })}
	all = append(all, options...)
	service := NewService(startupSafetyFactory{transport: transport}, all...)
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func startupTailHas(session *Session, want string) bool {
	session.startupMu.Lock()
	defer session.startupMu.Unlock()
	return strings.Contains(string(session.startupTail), want)
}

// startupState reports whether the one-shot command is still pending and
// whether the fallback timer has cleared (fired and returned in a safe wait).
func startupState(session *Session) (pending, timerCleared bool) {
	session.startupMu.Lock()
	defer session.startupMu.Unlock()
	return session.startupPending, session.startupTimer == nil
}

// expectStartupSafeWait waits on a real state transition, bounded by a hard
// timeout: the bounded fallback has run and returned without typing, leaving
// the command pending with no timer. startupReadinessElapsed disarms before it
// spawns a sender, so this state proves no fallback write is still in flight.
func expectStartupSafeWait(t *testing.T, session *Session) {
	t.Helper()
	waitForCondition(t, "the fallback to finish in a safe wait", func() bool {
		pending, timerCleared := startupState(session)
		return pending && timerCleared
	})
	if startupPromptReady(startupTail(session)) {
		t.Fatal("the retained tail is prompt-ready; a startup write may still be in flight")
	}
}

// expectStartupStillWaiting proves the prompt path has not released the
// command: the command is still pending and the retained tail is not
// prompt-ready. The fallback must be captured by a manual scheduler, so no
// write path remains after this check.
func expectStartupStillWaiting(t *testing.T, session *Session) {
	t.Helper()
	if startupPromptReady(startupTail(session)) {
		t.Fatal("the retained tail is prompt-ready; the prompt path may still write")
	}
	if pending, _ := startupState(session); !pending {
		t.Fatal("the startup command was disarmed before a real prompt")
	}
}

// expectStartupPending proves a command that auto-login or a manual takeover
// still owns was not disarmed. The tail may be prompt-ready, but neither the
// prompt path nor a manual-scheduler fallback can write in this state.
func expectStartupPending(t *testing.T, session *Session) {
	t.Helper()
	if pending, _ := startupState(session); !pending {
		t.Fatal("the startup command was disarmed while auto-login or a takeover owned the console")
	}
}

// expectNoStartupInput fails if the transport already received the startup
// command. Call it only after a deterministic state (a safe wait, a pending
// manual fallback, or teardown) proves the write path has completed.
func expectNoStartupInput(t *testing.T, transport *writeSignallingTransport, command string) {
	t.Helper()
	if got := transport.input(); strings.Contains(got, command) {
		t.Fatalf("the startup command was written where it must not be: %q", got)
	}
}

// preShellReadinessCases are the console lines the startup command must never
// be typed into, plus one genuine prompt as the control.
func preShellReadinessCases() []struct {
	name string
	tail string
} {
	return []struct {
		name string
		tail string
	}{
		{"login", "Welcome to the lab switch\r\n\r\nlogin: "},
		{"username", "Username: "},
		{"password", "Password: "},
		{"press enter", "\r\nPress Enter to continue..."},
		{"last login", "Last login: Mon Nov 16 09:00:00 2026\r\n"},
		{"motd", "Welcome to the lab switch\r\n\r\n"},
		{"passphrase angle prompt", "Enter passphrase for /home/deploy/key> "},
		{"pin angle prompt", "Enter PIN for /home/deploy/key> "},
	}
}

// TestPromptPathWaitsForEveryPreShellForm feeds each pre-shell line through the
// read loop; none releases the command, and a later genuine prompt does.
func TestPromptPathWaitsForEveryPreShellForm(t *testing.T) {
	for _, tc := range preShellReadinessCases() {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := &manualStartupScheduler{}
			transport := newWriteSignallingTransport()
			session := openStartupSafetySession(t, transport, StartSpec{
				Kind:           KindSSH,
				StartupCommand: "show status",
			}, withStartupReadinessScheduler(scheduler.schedule))

			before := session.Snapshot().LastActivity
			transport.output <- []byte(tc.tail)
			waitForCondition(t, "the pre-shell output", func() bool {
				return session.Snapshot().LastActivity.After(before)
			})
			expectStartupStillWaiting(t, session)

			transport.output <- []byte("\r\n$ ")
			waitForCondition(t, "the startup command at the real prompt", func() bool {
				return strings.Contains(transport.input(), "show status\r")
			})
		})
	}
}

// TestFallbackLeavesEveryPreShellFormUnsent fires the bounded fallback with
// each pre-shell line retained and proves it does not write. The command stays
// in the safe wait-for-prompt state instead of being lost, so a genuine prompt
// that arrives later still releases it. The genuine-prompt control releases the
// command without the fallback.
func TestFallbackLeavesEveryPreShellFormUnsent(t *testing.T) {
	for _, tc := range preShellReadinessCases() {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := &manualStartupScheduler{}
			transport := newWriteSignallingTransport()
			session := openStartupSafetySession(t, transport, StartSpec{
				Kind:           KindSSH,
				StartupCommand: "show status",
			}, withStartupReadinessScheduler(scheduler.schedule))

			transport.output <- []byte(tc.tail)
			waitForCondition(t, "the pre-shell output in the tail", func() bool {
				return startupTailHas(session, strings.TrimSpace(tc.tail))
			})

			scheduler.fireFirst()
			expectStartupSafeWait(t, session)
			expectNoStartupInput(t, transport, "show status")

			// A real prompt after the pre-shell output must still release the
			// command exactly once.
			transport.output <- []byte("\r\n$ ")
			waitForCondition(t, "the startup command at the later prompt", func() bool {
				return strings.Count(transport.input(), "show status\r") == 1
			})
		})
	}

	t.Run("genuine prompt", func(t *testing.T) {
		transport := newWriteSignallingTransport()
		openStartupSafetySession(t, transport, StartSpec{
			Kind:           KindSSH,
			StartupCommand: "show status",
		})

		transport.output <- []byte("deploy@web:~$ ")
		waitForCondition(t, "the startup command at the real prompt", func() bool {
			return strings.Contains(transport.input(), "show status\r")
		})
	})
}

// TestBoundedFallbackDoesNotTypeIntoAPersistentLoginPrompt is the ticket 50
// regression: a real (shortened) fallback bound must not type the Host startup
// command into a Telnet login prompt that persisted past the bound.
func TestBoundedFallbackDoesNotTypeIntoAPersistentLoginPrompt(t *testing.T) {
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindTelnet,
		StartupCommand: "show status",
	}, WithStartupReadinessTimeout(80*time.Millisecond))

	transport.output <- []byte("Welcome to the lab switch\r\n\r\nlogin: ")
	waitForCondition(t, "the login banner in the readiness tail", func() bool {
		return startupTailHas(session, "login: ")
	})

	// The bound elapses and must not type. The safe-wait state (still pending,
	// timer cleared) is the real completion signal: startupReadinessElapsed
	// disarms before spawning a sender, so no fallback write can be in flight.
	expectStartupSafeWait(t, session)
	expectNoStartupInput(t, transport, "show status")
}

// TestDefaultFallbackDoesNotTypeIntoALoginPrompt proves the production four
// second default, not an override: the injected scheduler observes the exact
// bound and fires it deterministically.
func TestDefaultFallbackDoesNotTypeIntoALoginPrompt(t *testing.T) {
	if defaultStartupReadinessTimeout != 4*time.Second {
		t.Fatalf("the production fallback bound changed to %v; update this "+
			"regression and its documentation together", defaultStartupReadinessTimeout)
	}
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindTelnet,
		StartupCommand: "show status",
	}, withStartupReadinessScheduler(scheduler.schedule))

	if got := scheduler.firstDuration(t); got != defaultStartupReadinessTimeout {
		t.Fatalf("the fallback was armed for %v, want the production default %v", got, defaultStartupReadinessTimeout)
	}

	transport.output <- []byte("Welcome to the lab switch\r\n\r\nlogin: ")
	waitForCondition(t, "the login banner in the readiness tail", func() bool {
		return startupTailHas(session, "login: ")
	})

	scheduler.fireFirst()
	expectStartupSafeWait(t, session)
	expectNoStartupInput(t, transport, "show status")
}

// TestAutoLoginAuthorityBlocksAShellPromptStartupCommand is mutation-sensitive
// for autoLoginAllowsStartup: a recognized shell prompt appears before the
// saved login completed, and the startup command must still wait. Removing the
// guard types the command immediately.
func TestAutoLoginAuthorityBlocksAShellPromptStartupCommand(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "secret"},
	}, withStartupReadinessScheduler(scheduler.schedule))

	// "$ " is a genuine shell prompt, but the credential exchange never ran.
	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\n$ ")
	waitForCondition(t, "the shell prompt output", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if session.autoLoginAllowsStartup(session.generation) {
		t.Fatal("the auto-login detector reported completion without answering a prompt")
	}
	expectStartupPending(t, session)
	expectNoStartupInput(t, transport, "show status")
}

// TestManualTakeoverLeavesTheStartupCommandUnsent pins the deliberate behavior
// when the user types over auto-login: the console belongs to the user, so the
// Host startup command never runs on that connection, even at a later shell
// prompt.
func TestManualTakeoverLeavesTheStartupCommandUnsent(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "secret"},
	}, withStartupReadinessScheduler(scheduler.schedule))

	if _, err := session.TerminalConn().Write([]byte("guest\r")); err != nil {
		t.Fatalf("typed takeover: %v", err)
	}
	waitForCondition(t, "the typed takeover to reach the console", func() bool {
		return strings.Contains(transport.input(), "guest\r")
	})

	transport.output <- []byte("\r\nlogin: ")
	waitForCondition(t, "the login prompt after the takeover", func() bool {
		return startupTailHas(session, "login: ")
	})
	if got := transport.input(); strings.Contains(got, "admin") {
		t.Fatalf("auto-login answered after the user took over: %q", got)
	}

	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\n$ ")
	waitForCondition(t, "the shell prompt after the manual login", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	expectStartupPending(t, session)
	expectNoStartupInput(t, transport, "show status")
}

// TestStartupCommandAfterTeardownNeverWrites exercises the late-write guard in
// sendStartupCommand: once the Session context is cancelled, concurrent senders
// must not type into the closed transport.
func TestStartupCommandAfterTeardownNeverWrites(t *testing.T) {
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSSH,
		StartupCommand: "echo ready",
	})

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForDone(t, session)
	if session.ctx.Err() == nil {
		t.Fatal("the Session context survived teardown")
	}

	const senders = 8
	done := make(chan struct{}, senders)
	for index := 0; index < senders; index++ {
		go func() {
			session.sendStartupCommand(transport)
			done <- struct{}{}
		}()
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for index := 0; index < senders; index++ {
		select {
		case <-done:
		case <-deadline.C:
			t.Fatal("sendStartupCommand did not return within the deadline")
		}
	}
	// Every sender has returned, so the late-write guard has completed; no
	// write can still be in flight.
	expectNoStartupInput(t, transport, "echo ready")
}
