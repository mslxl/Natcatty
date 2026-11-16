package session

import (
	"context"
	"testing"

	"github.com/mslxl/natcatty/application/session/autologin"
)

func openAutoLoginSession(t *testing.T, transport *scriptedTransport, spec StartSpec) *Session {
	t.Helper()
	factory := &scriptedFactory{transports: []*scriptedTransport{transport}}
	session, err := reconnectTestService(factory).Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestSerialAutoLoginAnswersTheDevicePrompts is the fake-serial contract: a
// serial console's login and password prompts are answered from the saved
// credentials, and the detector stops once the shell prompts.
func TestSerialAutoLoginAnswersTheDevicePrompts(t *testing.T) {
	transport := newScriptedTransport()
	session := openAutoLoginSession(t, transport, StartSpec{
		Kind:      KindSerial,
		AutoLogin: &autologin.Options{Username: "admin", Password: "secret"},
	})

	transport.output <- []byte("login: ")
	waitForCondition(t, "the saved username", func() bool {
		return transport.input() == "admin\r"
	})

	transport.output <- []byte("\r\nPassword: ")
	waitForCondition(t, "the saved password", func() bool {
		return transport.input() == "admin\rsecret\r"
	})

	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\n$ ")
	waitForCondition(t, "the shell prompt", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "admin\rsecret\r" {
		t.Fatalf("input = %q, want only the two credential lines", got)
	}
}

// TestSerialAutoLoginStaysArmedAfterAWakeEnter covers a silent console: the
// naked Enter that wakes it is forwarded, and the detector still answers the
// first prompt instead of treating the Enter as a takeover.
func TestSerialAutoLoginStaysArmedAfterAWakeEnter(t *testing.T) {
	transport := newScriptedTransport()
	session := openAutoLoginSession(t, transport, StartSpec{
		Kind:      KindSerial,
		AutoLogin: &autologin.Options{Username: "admin", Password: "secret"},
	})

	if _, err := session.TerminalConn().Write([]byte("\r")); err != nil {
		t.Fatalf("wake Enter: %v", err)
	}
	waitForCondition(t, "the wake Enter to reach the console", func() bool {
		return transport.input() == "\r"
	})

	transport.output <- []byte("login: ")
	waitForCondition(t, "the username after the wake Enter", func() bool {
		return transport.input() == "\radmin\r"
	})
}

// TestTypedInputBeforeThePromptDisarmsAutoLogin keeps a user's manual login
// from being answered with the saved credentials.
func TestTypedInputBeforeThePromptDisarmsAutoLogin(t *testing.T) {
	transport := newScriptedTransport()
	session := openAutoLoginSession(t, transport, StartSpec{
		Kind:      KindSerial,
		AutoLogin: &autologin.Options{Username: "admin", Password: "secret"},
	})

	if _, err := session.TerminalConn().Write([]byte("guest\r")); err != nil {
		t.Fatalf("typed login: %v", err)
	}
	waitForCondition(t, "the typed login to reach the console", func() bool {
		return transport.input() == "guest\r"
	})

	before := session.Snapshot().LastActivity
	transport.output <- []byte("login: ")
	waitForCondition(t, "the login prompt", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "guest\r" {
		t.Fatalf("input = %q, want no automated answer after the takeover", got)
	}
}

// TestAutoLoginStopsAfterTheAttemptLimit leaves a device that keeps rejecting
// the saved credentials at its own prompt instead of answering forever.
func TestAutoLoginStopsAfterTheAttemptLimit(t *testing.T) {
	transport := newScriptedTransport()
	session := openAutoLoginSession(t, transport, StartSpec{
		Kind:      KindSerial,
		AutoLogin: &autologin.Options{Username: "admin", Password: "bad", MaxAttempts: 2},
	})

	transport.output <- []byte("login: ")
	waitForCondition(t, "the first username", func() bool {
		return transport.input() == "admin\r"
	})
	transport.output <- []byte("\r\nPassword: ")
	waitForCondition(t, "the first password", func() bool {
		return transport.input() == "admin\rbad\r"
	})
	transport.output <- []byte("\r\nLogin incorrect\r\nlogin: ")
	waitForCondition(t, "the second attempt", func() bool {
		return transport.input() == "admin\rbad\radmin\r"
	})
	transport.output <- []byte("\r\nPassword: ")
	waitForCondition(t, "the second password", func() bool {
		return transport.input() == "admin\rbad\radmin\rbad\r"
	})

	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\nLogin incorrect\r\nlogin: ")
	waitForCondition(t, "the rejected prompt", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "admin\rbad\radmin\rbad\r" {
		t.Fatalf("input = %q, want no answer after the attempt limit", got)
	}
}

// TestStartupCommandWaitsForAutoLogin keeps the Host's first command out of
// the login exchange: it runs once the device reaches its shell prompt.
func TestStartupCommandWaitsForAutoLogin(t *testing.T) {
	transport := newScriptedTransport()
	openAutoLoginSession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "secret"},
	})

	transport.output <- []byte("login: ")
	waitForCondition(t, "the username", func() bool {
		return transport.input() == "admin\r"
	})
	transport.output <- []byte("\r\nPassword: ")
	waitForCondition(t, "the password", func() bool {
		return transport.input() == "admin\rsecret\r"
	})

	transport.output <- []byte("\r\n# ")
	waitForCondition(t, "the startup command after login", func() bool {
		return transport.input() == "admin\rsecret\rshow status\r"
	})
}

// TestFailedAutoLoginLeavesTheStartupCommandUnsent keeps a rejected login
// from having the Host startup command typed into the device's login prompt.
func TestFailedAutoLoginLeavesTheStartupCommandUnsent(t *testing.T) {
	transport := newScriptedTransport()
	session := openAutoLoginSession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "bad", MaxAttempts: 1},
	})

	transport.output <- []byte("login: ")
	waitForCondition(t, "the username", func() bool {
		return transport.input() == "admin\r"
	})
	transport.output <- []byte("\r\nPassword: ")
	waitForCondition(t, "the password", func() bool {
		return transport.input() == "admin\rbad\r"
	})

	before := session.Snapshot().LastActivity
	transport.output <- []byte("\r\nLogin incorrect\r\nlogin: ")
	waitForCondition(t, "the rejected login prompt", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	before = session.Snapshot().LastActivity
	transport.output <- []byte("login: ")
	waitForCondition(t, "the next login prompt", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := transport.input(); got != "admin\rbad\r" {
		t.Fatalf("input = %q, want no startup command at a failed login", got)
	}
}

// TestTelnetAutoLoginUsesTheSameAutomationPath runs the detector on the
// telnet transport kind, which shares the console automation.
func TestTelnetAutoLoginUsesTheSameAutomationPath(t *testing.T) {
	transport := newScriptedTransport()
	openAutoLoginSession(t, transport, StartSpec{
		Kind:      KindTelnet,
		AutoLogin: &autologin.Options{Username: "operator", Password: "hunter2"},
	})

	transport.output <- []byte("Username: ")
	waitForCondition(t, "the telnet username", func() bool {
		return transport.input() == "operator\r"
	})
	transport.output <- []byte("\r\nPassword: ")
	waitForCondition(t, "the telnet password", func() bool {
		return transport.input() == "operator\rhunter2\r"
	})
}
