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

func startupWaitDisarmed(session *Session) bool {
	session.startupMu.Lock()
	defer session.startupMu.Unlock()
	return !session.startupPending
}

// startupTimerCleared reports that the bounded fallback has fired and left the
// command in the safe wait-for-prompt state (no timer, still pending).
func startupTimerCleared(session *Session) bool {
	session.startupMu.Lock()
	defer session.startupMu.Unlock()
	return session.startupTimer == nil
}

// expectNoStartupWrite drains writes for one hard-deadline window and fails if
// any contains the Host startup command. Other input (credentials, a manual
// takeover) is allowed.
func expectNoStartupWrite(t *testing.T, writes <-chan string, command string, window time.Duration) {
	t.Helper()
	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case got := <-writes:
			if strings.Contains(got, command) {
				t.Fatalf("the startup command was written where it must not be: %q", got)
			}
		case <-timer.C:
			return
		}
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
			transport := newWriteSignallingTransport()
			session := openStartupSafetySession(t, transport, StartSpec{
				Kind:           KindSSH,
				StartupCommand: "show status",
			})

			before := session.Snapshot().LastActivity
			transport.output <- []byte(tc.tail)
			waitForCondition(t, "the pre-shell output", func() bool {
				return session.Snapshot().LastActivity.After(before)
			})
			expectNoStartupWrite(t, transport.writes, "show status", 150*time.Millisecond)

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
			if startupWaitDisarmed(session) {
				t.Fatal("the fallback permanently disarmed the one-shot command over pre-shell output")
			}
			expectNoStartupWrite(t, transport.writes, "show status", 150*time.Millisecond)

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

	// The bound elapses and must not type. Waiting for the timer to clear proves
	// the fallback actually ran; it leaves the one-shot command in the safe
	// wait-for-prompt state rather than typing at the login prompt. The channel
	// window proves the fallback goroutine did not write.
	waitForCondition(t, "the fallback to leave the login prompt alone", func() bool {
		return startupTimerCleared(session)
	})
	if startupWaitDisarmed(session) {
		t.Fatal("the fallback permanently disarmed the one-shot command over a login prompt")
	}
	expectNoStartupWrite(t, transport.writes, "show status", 250*time.Millisecond)
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
	if startupWaitDisarmed(session) {
		t.Fatal("the fallback permanently disarmed the one-shot command after a persistent login prompt")
	}
	expectNoStartupWrite(t, transport.writes, "show status", 250*time.Millisecond)
}

// TestAutoLoginAuthorityBlocksAShellPromptStartupCommand is mutation-sensitive
// for autoLoginAllowsStartup: a recognized shell prompt appears before the
// saved login completed, and the startup command must still wait. Removing the
// guard types the command immediately.
func TestAutoLoginAuthorityBlocksAShellPromptStartupCommand(t *testing.T) {
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "secret"},
	})

	// "$ " is a genuine shell prompt, but the credential exchange never ran.
	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\n$ ")
	waitForCondition(t, "the shell prompt output", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if session.autoLoginAllowsStartup(session.generation) {
		t.Fatal("the auto-login detector reported completion without answering a prompt")
	}
	expectNoStartupWrite(t, transport.writes, "show status", 250*time.Millisecond)
}

// TestManualTakeoverLeavesTheStartupCommandUnsent pins the deliberate behavior
// when the user types over auto-login: the console belongs to the user, so the
// Host startup command never runs on that connection, even at a later shell
// prompt.
func TestManualTakeoverLeavesTheStartupCommandUnsent(t *testing.T) {
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "secret"},
	})

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
	expectNoStartupWrite(t, transport.writes, "show status", 250*time.Millisecond)
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
	expectNoStartupWrite(t, transport.writes, "echo ready", 100*time.Millisecond)
}
