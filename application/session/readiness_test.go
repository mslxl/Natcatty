package session

import (
	"strings"
	"testing"
)

// TestStartupPromptReadyRejectsPreShellOutput pins the prompt heuristic against
// the output a console prints before its shell exists. None of these lines may
// release the Host startup command; the bounded fallback has its own tests.
func TestStartupPromptReadyRejectsPreShellOutput(t *testing.T) {
	cases := []struct {
		name string
		tail string
	}{
		{"telnet login banner", "Welcome to the lab switch\r\n\r\nlogin: "},
		{"username", "Username: "},
		{"username lowercase", "username:"},
		{"password", "Password: "},
		{"press enter", "\r\nPress Enter to continue..."},
		{"press return", "Press RETURN to start"},
		{"last login with timestamp", "Last login: Mon Nov 16 09:00:00 2026\r\n"},
		{"last login without timestamp", "Last login:\r\n"},
		{"progress percent", "Loading firmware image\r\n100% "},
		{"progress decimal", "Copying files\r\n87.5% "},
		{"arrow", "Starting services\r\n-> "},
		{"fat arrow", "==> "},
		{"punctuation banner", "### "},
		{"currency US", "US$ "},
		{"currency HK", "HK$ "},
		{"MOTD token", "MOTD>"},
		// A login word immediately before a prompt-like ">" is the exact shape
		// the angle-prompt widening must not release on. These pin the
		// prompt-path login classifier.
		{"password angle prompt", "Password> "},
		{"login angle prompt", "login> "},
		{"user angle prompt", "user> "},
		{"pin angle prompt", "PIN> "},
		// A "press Enter" notice that also carries a path signal would satisfy
		// the widened angle shape; the prompt-path continue classifier rejects
		// it.
		{"continue angle prompt", "Press Enter to continue /dev/tty> "},
		// A last-login line carrying a path signal is likewise prompt-shaped.
		// The prompt-path last-login classifier rejects it.
		{"last login angle prompt", "Last login: /home/deploy> "},
		// A passphrase phrase with a path signal ends on the widened ">" shape.
		{"passphrase angle prompt", "Enter passphrase for /home/deploy/key> "},
		// A line longer than the bounded prompt line is never readiness.
		{"overlong prompt", "deploy@web:" + strings.Repeat("a", 256) + "$ "},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if startupPromptReady(tc.tail) {
				t.Fatalf("startupPromptReady(%q) = true; a pre-shell line released the command", tc.tail)
			}
		})
	}
}

// TestStartupPromptReadyAcceptsShellPrompts keeps the tightened heuristic from
// rejecting genuine prompts, including ANSI-styled and vendor prompts.
func TestStartupPromptReadyAcceptsShellPrompts(t *testing.T) {
	cases := []struct {
		name string
		tail string
	}{
		{"bash user", "deploy@web:~$ "},
		{"root", "root# "},
		{"bare dollar", "$ "},
		{"bare hash", "# "},
		{"bare percent", "% "},
		{"zsh host", "workstation% "},
		{"vendor router", "Router# "},
		{"vendor prompt", "switch> "},
		{"ansi prompt", "\x1b[32mdeploy@web:~$\x1b[0m "},
		{"mysql", "mysql> "},
		{"prompt after banner", "Last login: Mon Nov 16 09:00:00 2026\r\ndeploy@web:~$ "},
		// Genuine prompts that carry spaces or brackets. Each must release on
		// the prompt path, not only after the bounded fallback.
		{"rhel bracket after last login", "Last login: Mon Nov 16 09:00:00 2026\r\n[root@host ~]# "},
		{"debian bracket", "user@host ~/src]$ "},
		{"spaced dollar", "guest@host:~ $ "},
		{"spaced percent", "deploy@web /var/log % "},
		{"spaced angle", "user@host ~/src> "},
		{"powershell drive", `PS C:\> `},
		{"powershell after welcome", "Welcome to Windows\r\n" + `PS C:\Users\deploy> `},
		{"fish after motd", "Welcome to the fish shell\r\n/usr/share/fish\r\ndeploy@web ~/src> "},
		{"bare angle", "> "},
		{"question angle", ">? "},
		{"windows drive prompt", `C:\Users\deploy> `},
		{"ansi rhel bracket", "\x1b[32m[root@host ~]#\x1b[0m "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !startupPromptReady(tc.tail) {
				t.Fatalf("startupPromptReady(%q) = false; a genuine prompt did not release the command", tc.tail)
			}
		})
	}
}

// TestStartupPreShellVisible recognizes the retained-output states in which the
// bounded fallback must not type into the console.
func TestStartupPreShellVisible(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"telnet login", "Welcome to the lab switch\r\n\r\nlogin: ", true},
		{"username", "Username: ", true},
		{"password", "Password: ", true},
		{"press enter", "\r\nPress Enter to continue...", true},
		{"last login", "Last login: Mon Nov 16 09:00:00 2026\r\n", true},
		{"last login then MOTD", "Last login: Mon Nov 16 09:00:00 2026\r\nWelcome to Ubuntu 24.04 LTS\r\n\r\n", true},
		{"previous login", "Previous login: yesterday\r\n", true},
		{"welcome banner", "Welcome to the lab switch\r\n\r\n", true},
		{"authorized users notice", "Authorized users only.\r\n", true},
		{"MOTD token", "MOTD>\r\n", true},
		{"booting", "booting...\r\n", false},
		{"shell prompt", "deploy@web:~$ ", false},
		{"shell prompt after banner", "Last login: Mon Nov 16 09:00:00 2026\r\n\r\n$ ", false},
		{"empty", "", false},
		{"boot progress is not pre-shell", "100% ", false},
		// A login marker followed by boot progress still names a pre-shell
		// console: the progress shape must be rejected as a prompt so the scan
		// reaches the login line.
		{"login then progress", "login: \r\n100% ", true},
		// ANSI styling around a real prompt must be stripped before the scan
		// recognizes it; otherwise the earlier banner wins.
		{"ansi banner then styled prompt", "Welcome to the lab\r\n\x1b[32mdeploy@web:~$\x1b[0m ", false},
		{"passphrase angle prompt", "Enter passphrase for /home/deploy/key> ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupPreShellVisible(tc.tail); got != tc.want {
				t.Fatalf("startupPreShellVisible(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}
}
