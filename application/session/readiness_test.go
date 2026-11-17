package session

import "testing"

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupPreShellVisible(tc.tail); got != tc.want {
				t.Fatalf("startupPreShellVisible(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}
}
