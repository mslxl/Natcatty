package session

import (
	"strings"
	"testing"
	"time"
)

// startupPromptShapeCases are genuine console prompts whose shape the prompt
// heuristic used to miss (spaces before the terminator, brackets, PowerShell,
// fish/nushell paths). Each must release the startup command on the prompt
// path; the fallback is deliberately long in the test, so only prompt
// recognition can satisfy the condition.
func startupPromptShapeCases() []struct {
	name string
	tail string
} {
	return []struct {
		name string
		tail string
	}{
		{"rhel bracket after last login", "Last login: Mon Nov 16 09:00:00 2026\r\n[root@host ~]# "},
		{"debian bracket", "user@host ~/src]$ "},
		{"powershell after welcome", "Welcome to Windows\r\n" + `PS C:\Users\deploy> `},
		{"fish after motd", "Welcome to the fish shell\r\n/usr/share/fish\r\ndeploy@web ~/src> "},
		{"spaced dollar", "guest@host:~ $ "},
		{"spaced percent", "deploy@web /var/log % "},
		{"spaced angle", "user@host ~/src> "},
		{"bare angle", "> "},
		{"question angle", ">? "},
		// U-Boot's "U-Boot>" is a single-token prompt, not boot progress.
		{"uboot", "U-Boot> "},
		// Paths with spaces and PowerShell-on-Linux.
		{"user host colon spaced path", "user@host:~/my dir$ "},
		{"rhel bracket spaced path", "[user@host /home/my dir]# "},
		{"powershell posix", "PS /home/deploy> "},
	}
}

// TestStartupPromptShapesReleaseOnThePromptPath feeds each real prompt shape
// through the read loop with a long fallback bound. Recognition, not the
// bounded fallback, must type the command.
func TestStartupPromptShapesReleaseOnThePromptPath(t *testing.T) {
	for _, tc := range startupPromptShapeCases() {
		t.Run(tc.name, func(t *testing.T) {
			transport := newWriteSignallingTransport()
			openStartupSafetySession(t, transport, StartSpec{
				Kind:           KindSSH,
				StartupCommand: "show status",
			}, WithStartupReadinessTimeout(5*time.Second))

			transport.output <- []byte(tc.tail)
			waitForCondition(t, "the startup command at the prompt shape", func() bool {
				return strings.Count(transport.input(), "show status\r") == 1
			})
		})
	}
}

// TestFallbackOverShellPrintedMOTDKeepsWaitingForThePrompt is the blocker 2
// regression: a MOTD printed by the shell itself is ordinary output, not proof
// that the console is still pre-shell. A fallback that fires over it must not
// permanently lose the one-shot command; a prompt that arrives later must still
// release it.
func TestFallbackOverShellPrintedMOTDKeepsWaitingForThePrompt(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSSH,
		StartupCommand: "show status",
	}, withStartupReadinessScheduler(scheduler.schedule))

	transport.output <- []byte("deploy@web:~$ cat /etc/motd\r\nWelcome to the team\r\n")
	waitForCondition(t, "the MOTD output in the readiness tail", func() bool {
		return startupTailHas(session, "Welcome to the team")
	})

	scheduler.fireFirst()

	// The fallback saw a banner line and must not type into it. The safe-wait
	// state (still pending, timer cleared) proves the write path completed.
	expectStartupSafeWait(t, session)
	expectNoStartupInput(t, transport, "show status")

	// The shell prompt arrives after the fallback; the command must still run,
	// exactly once.
	transport.output <- []byte("$ ")
	waitForCondition(t, "the startup command at the later prompt", func() bool {
		return strings.Count(transport.input(), "show status\r") == 1
	})

	before := session.Snapshot().LastActivity
	transport.output <- []byte("more output\r\n$ ")
	waitForCondition(t, "the next output burst", func() bool {
		return session.Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(transport.input(), "show status\r"); got != 1 {
		t.Fatalf("the startup command ran %d times after the fallback", got)
	}
}

// TestStartupTailBoundDropsOldPreShellMarkers pins the 2048-byte retained tail.
// A login marker that has scrolled out of the window must not keep the
// fallback's pre-shell scan armed; without the bound the marker survives and
// suppresses the command forever.
func TestStartupTailBoundDropsOldPreShellMarkers(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSSH,
		StartupCommand: "show status",
	}, withStartupReadinessScheduler(scheduler.schedule))

	payload := "login: \r\n" + strings.Repeat("xxxxxxxx\r\n", 400) + "END_OF_BURST"
	transport.output <- []byte(payload)
	waitForCondition(t, "the full burst to reach the retained tail", func() bool {
		return startupTailContains(session, "END_OF_BURST")
	})

	tail := startupTail(session)
	if len(tail) > startupTailLimit {
		t.Fatalf("the retained startup tail grew to %d bytes, bound is %d", len(tail), startupTailLimit)
	}
	if strings.Contains(tail, "login:") {
		t.Fatalf("the retained startup tail still holds a pre-shell marker from before the bound: %q", tail)
	}
	if startupPreShellVisible(tail) {
		t.Fatal("an old login marker outside the tail bound still reports pre-shell output")
	}
}

// TestArmingStartupCommandDropsThePreviousTail pins the arm-time tail reset. A
// re-arm (reconnect) must not carry a stale login/banner line from the previous
// connection into the new readiness decision.
func TestArmingStartupCommandDropsThePreviousTail(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSSH,
		StartupCommand: "show status",
	}, withStartupReadinessScheduler(scheduler.schedule))

	transport.output <- []byte("Welcome to the lab switch\r\n")
	waitForCondition(t, "the banner in the readiness tail", func() bool {
		return startupTailContains(session, "Welcome to the lab switch")
	})

	session.armStartupCommand(session.generation, transport)

	session.startupMu.Lock()
	tail := string(session.startupTail)
	pending := session.startupPending
	session.startupMu.Unlock()
	if tail != "" {
		t.Fatalf("re-arming kept %q from the previous readiness decision", tail)
	}
	if !pending {
		t.Fatal("re-arming did not leave the startup command pending")
	}
}

func startupTail(session *Session) string {
	session.startupMu.Lock()
	defer session.startupMu.Unlock()
	return string(session.startupTail)
}

func startupTailContains(session *Session, want string) bool {
	return strings.Contains(startupTail(session), want)
}
