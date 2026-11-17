package session

import (
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/session/autologin"
)

// count reports how many bounded fallbacks the Session has scheduled.
func (m *manualStartupScheduler) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.fires)
}

// TestStartupPromptGrammarRejectsNonPromptLines pins every prose, markup,
// boot-progress, and bracketed-login line that must not count as a prompt.
func TestStartupPromptGrammarRejectsNonPromptLines(t *testing.T) {
	cases := []struct {
		name string
		tail string
	}{
		// Spaced prose carrying a URL, an email, or a colon-space label.
		{"url angle", "error: see https://x/y>"},
		{"email angle", "mail bob@example.com>"},
		{"progress label angle", "Progress: 3/10>"},
		{"url dollar", "see https://x/y$"},
		{"url hash", "see https://x/y#"},
		{"email prose angle", "Contact support@example.com>"},
		// Whitespace-free markup and angle lines.
		{"html open", "<html>"},
		{"html close", "</html>"},
		{"status markup", "<status>ok</status>"},
		{"ok markup", "<ok>"},
		{"dashes angle", "---->System>"},
		{"return markup", "<RETURN>"},
		{"enter markup", "<ENTER>"},
		{"make label", "make: Entering directory '/src'>"},
		// Boot/progress lines that happen to carry a path or a bracket.
		{"fsck progress", "fsck /dev/sda1: 100% "},
		{"rebuild progress", "Rebuilding /dev/md0: 45% "},
		{"progress bar", "Progress: [##########] 50% "},
		{"checking progress", "Checking /dev/sda1 ... 100% "},
		// Bracketed login defaults.
		{"login bracketed default", "Login [admin]> "},
		{"username bracketed default", "Username [admin]> "},
		{"user bracketed default", "User [admin]> "},
		// The pre-existing rejects must stay rejected.
		{"100 percent", "100%"},
		{"87.5 percent", "87.5%"},
		{"arrow", "->"},
		{"fat arrow", "==>"},
		{"hashes", "###"},
		{"currency US", "US$"},
		{"currency HK", "HK$"},
		{"filesystem check", "Filesystem check 100%"},
		{"total cost", "Total cost: US$"},
		{"dquote", "dquote>"},
		{"cmdand", "cmdand>"},
		{"pipe", "pipe>"},
		{"password angle", "Password> "},
		{"pin angle", "PIN> "},
		{"passphrase angle", "Enter passphrase for /home/deploy/key>"},
		{"last login", "Last login: Mon Nov 16 09:00:00 2026"},
		{"welcome banner", "Welcome to the lab switch"},
		// Shell metacharacters and unbalanced brackets never form a prompt.
		{"equals prefix", "cmd=run>"},
		{"backtick prefix", "echo `date`>"},
		{"brace open", "{ready>"},
		{"brace close", "ready}>"},
		{"powershell wrong terminator", `PS C:\foo$`},
		{"opening bracket only", "[user@host ~"},
		{"overlong", "deploy@web:" + strings.Repeat("a", 256) + "$"},
		{"prose angle", "device ready>"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if startupPromptReady(tc.tail) {
				t.Fatalf("startupPromptReady(%q) = true; a non-prompt line released the command", tc.tail)
			}
		})
	}
}

// TestStartupPromptGrammarAcceptsRealShapes pins the strict grammar against the
// real prompts it must keep accepting.
func TestStartupPromptGrammarAcceptsRealShapes(t *testing.T) {
	cases := []struct {
		name string
		tail string
	}{
		// Bracket prompts, optionally with each terminator.
		{"rhel bracket hash", "[user@host ~]# "},
		{"bracket path dollar", "[root@host /var/log]$ "},
		{"bracket percent", "[root@host /var/log]% "},
		{"bracket angle", "[user@host ~]> "},
		{"rhel bracket after last login", "Last login: Mon Nov 16 09:00:00 2026\r\n[root@host ~]# "},
		// PowerShell.
		{"powershell root drive", `PS C:\> `},
		{"powershell path", `PS C:\Users\deploy> `},
		{"powershell after welcome", "Welcome to Windows\r\n" + `PS C:\Users\deploy> `},
		// user@host prompts.
		{"user host", "user@host$ "},
		{"user host home", "user@host:~$ "},
		{"user host path", "user@host:/var/log$ "},
		{"user host spaced home", "user@host ~/src> "},
		{"user host spaced abs", "user@host /var/log% "},
		{"spaced dollar", "guest@host:~ $ "},
		{"spaced percent", "deploy@web /var/log % "},
		{"fish path", "deploy@web ~/src> "},
		{"debian bracket", "user@host ~/src]$ "},
		// Path-only prompts.
		{"home path", "~/src$ "},
		{"abs path", "/var/log# "},
		{"windows path", `C:\Users\deploy> `},
		// Single-token prompts (the old rule).
		{"root", "root# "},
		{"router", "Router# "},
		{"mysql", "mysql> "},
		{"deploy at web", "deploy@web> "},
		{"vendor percent", "workstation% "},
		// Bare terminators.
		{"bare angle", "> "},
		{"question angle", ">? "},
		{"bare dollar", "$ "},
		{"bare hash", "# "},
		{"bare percent", "% "},
		// ANSI styling, OSC titles, and trailing whitespace.
		{"ansi prompt", "\x1b[32mdeploy@web:~$\x1b[0m "},
		{"ansi rhel bracket", "\x1b[32m[root@host ~]#\x1b[0m "},
		{"osc title then prompt", "\x1b]0;title\x07deploy@web:~$ "},
		{"trailing whitespace", "deploy@web:~$   "},
		{"literal user host angle", "user@host> "},
		{"prompt after banner", "Last login: Mon Nov 16 09:00:00 2026\r\ndeploy@web:~$ "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !startupPromptReady(tc.tail) {
				t.Fatalf("startupPromptReady(%q) = false; a genuine prompt did not release the command", tc.tail)
			}
		})
	}
}

// TestStartupPreShellDetectsBootProgressAndMarkup pins the fallback's new
// pre-shell evidence. A percent sign, a progress bar, a boot/progress label, or
// markup must make the fallback safe-wait instead of typing.
func TestStartupPreShellDetectsBootProgressAndMarkup(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"percent", "Loading firmware\r\n100% ", true},
		{"decimal percent", "Copying files\r\n87.5% ", true},
		{"fsck", "fsck /dev/sda1: 100% ", true},
		{"rebuilding", "Rebuilding /dev/md0: 45% ", true},
		{"progress bar", "Progress: [##########] 50% ", true},
		{"checking", "Checking /dev/sda1 ... 100% ", true},
		{"loading label", "Loading kernel modules\r\n", true},
		{"booting", "booting...\r\n", true},
		{"starting label", "Starting network services\r\n", true},
		{"waiting label", "Waiting for device\r\n", true},
		{"copying label", "Copying image\r\n", true},
		{"html", "<html>\r\n", true},
		{"closing html", "</html>\r\n", true},
		{"markup", "<status>ok</status>\r\n", true},
		{"dashes angle", "---->System>\r\n", true},
		{"login bracketed default", "Login [admin]> ", true},
		{"username bracketed default", "Username [admin]> ", true},
		{"user bracketed default", "User [admin]> ", true},
		// A real prompt after the marker wins: the command must not stay
		// suppressed once the shell is ready.
		{"prompt after progress", "100% \r\nRouter# ", false},
		{"prompt after markup", "<html>\r\n$ ", false},
		{"markerless output", "device ready\r\n", false},
		// A lone ">" in ordinary output or an unrecognized prompt is not
		// markup; the fallback must still be able to type.
		{"arrow prose", "error -> retry\r\n", false},
		{"unrecognized prompt", "switch prompt>\r\n", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupPreShellVisible(tc.tail); got != tc.want {
				t.Fatalf("startupPreShellVisible(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}
}

// TestTelnetBannerProseDoesNotReleaseTheStartupCommand drives the production
// read loop: a Telnet banner containing a URL, email, progress label, or markup
// must not release the Host startup command. A later genuine device prompt
// releases it exactly once. The manual scheduler captures the fallback so no
// timer races the negative assertion.
func TestTelnetBannerProseDoesNotReleaseTheStartupCommand(t *testing.T) {
	banners := []struct {
		name string
		tail string
	}{
		{"url", "Welcome to the lab switch\r\nFor help see https://example.com/support>"},
		{"email", "Welcome to the lab switch\r\nContact support@example.com>"},
		{"progress label", "Welcome to the lab switch\r\nProgress: 3/10>"},
		{"markup", "Welcome to the lab switch\r\n<html>"},
		{"progress percent", "Welcome to the lab switch\r\nfsck /dev/sda1: 100% "},
	}
	for _, tc := range banners {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := &manualStartupScheduler{}
			transport := newWriteSignallingTransport()
			session := openStartupSafetySession(t, transport, StartSpec{
				Kind:           KindTelnet,
				StartupCommand: "show status",
			}, withStartupReadinessScheduler(scheduler.schedule),
				WithStartupReadinessTimeout(80*time.Millisecond))
			if got := scheduler.firstDuration(t); got != 80*time.Millisecond {
				t.Fatalf("the fallback was armed for %v, want the short test bound", got)
			}

			before := session.Snapshot().LastActivity
			transport.output <- []byte(tc.tail)
			waitForCondition(t, "the telnet banner", func() bool {
				return session.Snapshot().LastActivity.After(before)
			})
			expectNoStartupWrite(t, transport.writes, "show status", 150*time.Millisecond)

			transport.output <- []byte("\r\nRouter# ")
			waitForCondition(t, "the startup command at the device prompt", func() bool {
				return strings.Count(transport.input(), "show status\r") == 1
			})

			// A later burst with another prompt must not replay the command.
			before = session.Snapshot().LastActivity
			transport.output <- []byte("\r\nshow status\r\nrunning\r\nRouter# ")
			waitForCondition(t, "the command output", func() bool {
				return session.Snapshot().LastActivity.After(before)
			})
			if got := strings.Count(transport.input(), "show status\r"); got != 1 {
				t.Fatalf("the startup command ran %d times on one connection", got)
			}
		})
	}
}

// TestFallbackSafeWaitsOverBootProgressAndReleasesAtThePrompt pins the new
// pre-shell evidence in the bounded fallback: boot/progress/markup output is
// never typed into, the one-shot command stays pending, and a later prompt
// releases it exactly once.
func TestFallbackSafeWaitsOverBootProgressAndReleasesAtThePrompt(t *testing.T) {
	cases := []struct {
		name string
		tail string
	}{
		{"percent", "Loading firmware image\r\n100% "},
		{"fsck", "fsck /dev/sda1: 100% "},
		{"rebuilding", "Rebuilding /dev/md0: 45% "},
		{"progress bar", "Progress: [##########] 50% "},
		{"checking", "Checking /dev/sda1 ... 100% "},
		{"loading label", "Loading kernel modules\r\n"},
		{"booting", "booting...\r\n"},
		{"html", "<html>\r\n"},
		{"markup", "<status>ok</status>\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := &manualStartupScheduler{}
			transport := newWriteSignallingTransport()
			session := openStartupSafetySession(t, transport, StartSpec{
				Kind:           KindTelnet,
				StartupCommand: "show status",
			}, withStartupReadinessScheduler(scheduler.schedule),
				WithStartupReadinessTimeout(80*time.Millisecond))

			transport.output <- []byte(tc.tail)
			waitForCondition(t, "the boot/progress output in the tail", func() bool {
				return startupTailHas(session, strings.TrimSpace(tc.tail))
			})

			scheduler.fireFirst()
			if startupWaitDisarmed(session) {
				t.Fatal("the fallback permanently disarmed the one-shot command over boot/progress output")
			}
			expectNoStartupWrite(t, transport.writes, "show status", 150*time.Millisecond)

			// The next output rearms the fallback; a real prompt still releases
			// the command exactly once.
			transport.output <- []byte("\r\nRouter# ")
			waitForCondition(t, "the startup command at the later prompt", func() bool {
				return strings.Count(transport.input(), "show status\r") == 1
			})
		})
	}
}

// TestFallbackRearmsOnOutputAfterASafeWait pins observeStartupOutput's rearm.
// Removing it leaves startupTimer nil after a safe wait, so a device that keeps
// printing ordinary output never gets another bounded fallback.
func TestFallbackRearmsOnOutputAfterASafeWait(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSSH,
		StartupCommand: "show status",
	}, withStartupReadinessScheduler(scheduler.schedule))

	transport.output <- []byte("login: ")
	waitForCondition(t, "the login prompt in the tail", func() bool {
		return startupTailHas(session, "login: ")
	})
	if got := scheduler.count(); got != 1 {
		t.Fatalf("the fallback was scheduled %d times before it fired, want 1", got)
	}

	scheduler.fireFirst()
	if startupWaitDisarmed(session) {
		t.Fatal("the fallback disarmed the one-shot command over a login prompt")
	}
	expectNoStartupWrite(t, transport.writes, "show status", 100*time.Millisecond)

	// Ordinary output after the safe wait must arm a fresh bounded fallback.
	transport.output <- []byte("still waiting\r\n")
	waitForCondition(t, "the fallback to rearm after later output", func() bool {
		return scheduler.count() >= 2
	})
}

// TestFallbackDoesNotTypeWhileAutoLoginIsUnfinished pins the fallback's
// auto-login branch. Without it the bounded fallback disarms and types the
// startup command into a console whose saved login has not completed.
func TestFallbackDoesNotTypeWhileAutoLoginIsUnfinished(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSerial,
		StartupCommand: "show status",
		AutoLogin:      &autologin.Options{Username: "admin", Password: "secret"},
	}, withStartupReadinessScheduler(scheduler.schedule))

	transport.output <- []byte("device ready\r\n")
	waitForCondition(t, "the markerless output in the tail", func() bool {
		return startupTailHas(session, "device ready")
	})
	if session.autoLoginAllowsStartup(session.generation) {
		t.Fatal("the auto-login detector reported completion without answering a prompt")
	}

	scheduler.fireFirst()
	if startupWaitDisarmed(session) {
		t.Fatal("the fallback disarmed the one-shot command while auto-login was unfinished")
	}
	expectNoStartupWrite(t, transport.writes, "show status", 250*time.Millisecond)
}

// TestStartupPromptSplitAcrossReadsReleasesOnce pins the retained tail: a
// prompt split across transport reads is recognized after the second chunk.
func TestStartupPromptSplitAcrossReadsReleasesOnce(t *testing.T) {
	scheduler := &manualStartupScheduler{}
	transport := newWriteSignallingTransport()
	session := openStartupSafetySession(t, transport, StartSpec{
		Kind:           KindSSH,
		StartupCommand: "show status",
	}, withStartupReadinessScheduler(scheduler.schedule))

	transport.output <- []byte("deploy@web:")
	waitForCondition(t, "the first prompt fragment", func() bool {
		return startupTailHas(session, "deploy@web:")
	})
	expectNoStartupWrite(t, transport.writes, "show status", 100*time.Millisecond)

	transport.output <- []byte("~$ ")
	waitForCondition(t, "the startup command at the split prompt", func() bool {
		return strings.Count(transport.input(), "show status\r") == 1
	})
}
